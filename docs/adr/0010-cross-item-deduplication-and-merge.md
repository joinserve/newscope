# ADR 0010: Beat-level aggregation

- Status: Proposed
- Date: 2026-04-22
- Deciders: caspar

## Context

The inbox shows one card per source article. Three problems fall out:

1. **Noise** — wire pickups and translated reposts produce 3–5 near-identical
   cards per story.
2. **No "what's new"** — a returning user sees the whole inbox, not the delta
   since the last visit to a given story.
3. **Per-article classification** scores duplicates individually, so ranking
   is per-source, not per-story.

The goal is **read less, understand more**. The user cares about *what is
happening*, not about the shape of the storage.

Multi-day event timelines ("stories") are a related but distinct concern:
they chain beats by entity overlap, not by text similarity. They are out of
scope here and will be addressed in a future ADR when the need is concrete.

## Decision

### One new layer: beat

| Layer | What it represents | How it's formed |
|---|---|---|
| **fragment** (`items`, existing) | a single article from one source | RSS parse |
| **beat** (`beats`) | several articles reporting the same update | 48h window + embedding cosine > threshold |

Beats collapse short-window duplicates. One item belongs to at most one beat
(hard membership). Past the 48h window, a beat is considered closed by the
worker logic — not by a stored flag, but by comparing `first_seen_at` to now.

### Primary UX target: since-last-visit

The feature's user-visible win is "**this beat has 3 new updates since you
last looked**". Newscope is single-tenant self-hosted (no auth, no user
table), so view state lives as one column on `beats`:
`last_viewed_at DATETIME`. When multi-user support arrives, this moves to a
dedicated `user_beat_views` table — that is a future migration, not a
present design constraint.

### Hard requirement: fully toggleable

If the operator does not configure an embedding provider, **the entire
feature is off** and nothing else misbehaves:

- `config.embedding.provider == ""` → no workers start, no beats routes are
  mounted, no beats tables are queried.
- Schema DDL still runs (idempotent, empty tables cost nothing).
- UI templates render the existing items inbox with no branches on
  feature flags — there's simply no beats section to include.

The gate is `features.BeatsEnabled(cfg)`. Every worker constructor, every
route registration, every template include checks it.

## Data model

```
┌────────────┐       ┌────────────┐       ┌──────────────────┐
│   feeds    │ 1──N  │   items    │ 1──1  │ item_embeddings  │
│            │───────│ (fragment) │───────│                  │
│ id         │       │ id         │       │ item_id (PK,FK)  │
│ url        │       │ feed_id    │       │ model            │
│ title      │       │ title      │       │ vector (BLOB)    │
│ ...        │       │ summary    │       └──────────────────┘
└────────────┘       │ topics     │
                     │ published  │
                     └─────┬──────┘
                           │ 1
                           │
                           │ N
                     ┌─────┴──────────┐
                     │ beat_members   │
                     │                │         ┌────────────────────┐
                     │ beat_id (FK)   │ N    1  │      beats         │
                     │ item_id (FK)   │─────────│                    │
                     │ added_at       │         │ id                 │
                     │ UNIQUE(item_id)│         │ canonical_title    │
                     └────────────────┘         │ canonical_summary  │
                                                │ first_seen_at      │
                                                │ last_viewed_at     │
                                                └────────────────────┘
```

Invariants:
- `UNIQUE(item_id)` on `beat_members` — an item belongs to at most one beat.
- `canonical_summary IS NULL` means the beat has not yet been processed by
  `merge_worker` (replaces the need for a `dirty` flag).
- Closed beats are identified by `first_seen_at < now - 48h` (replaces the
  need for a `frozen_at` column).

Data-volume intuition (order of magnitude, daily):
- items ~ 10⁴
- beats ~ 10³

## Architecture

```
                 ┌───────────────────────────────────────────────────────┐
                 │   existing pipeline (untouched)                       │
                 │   feeds → scheduler.ParseFeed → items → classifier    │
                 └────────────────────────┬──────────────────────────────┘
                                          │
                            if features.BeatsEnabled():
                                          │
                                          ▼
                       ┌────────────────────────────────────┐
                       │  embed_worker                      │
                       │  Embedder.Embed(title+summary)     │
                       │  → item_embeddings                 │
                       └──────────────┬─────────────────────┘
                                      ▼
                       ┌────────────────────────────────────┐
                       │  beat_worker (every N min)         │
                       │  kNN within 48h + overlapping topic│
                       │  sim > threshold → attach, else    │
                       │  seed new beat                     │
                       └──────────────┬─────────────────────┘
                                      ▼
                       ┌────────────────────────────────────┐
                       │  merge_worker                      │
                       │  processes beats with              │
                       │  canonical_summary IS NULL AND     │
                       │  member_count > 1                  │
                       └──────────────┬─────────────────────┘
                                      ▼
                       ┌────────────────────────────────────┐
                       │  repository.ListBeats()            │
                       │  API: /api/v1/beats (if enabled)   │
                       │  UI: inbox of beats,               │
                       │      since-last-visit diff         │
                       └────────────────────────────────────┘

 feature off path:
   workers never start; routes never mount; UI never references beats
```

All three workers live in `pkg/scheduler/` alongside `feed_processor.go`.
They follow the existing scheduler pattern (new method on `Scheduler`, new
goroutine spawned conditionally inside `Start`, mirroring the cleanup worker
at `scheduler.go:206`). No new package is introduced until size warrants it.

## Implementation sketch

### Schema (`pkg/repository/schema.sql`)

```sql
CREATE TABLE IF NOT EXISTS item_embeddings (
    item_id    INTEGER PRIMARY KEY REFERENCES items(id) ON DELETE CASCADE,
    model      TEXT    NOT NULL,
    vector     BLOB    NOT NULL,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS beats (
    id                INTEGER PRIMARY KEY AUTOINCREMENT,
    canonical_title   TEXT,
    canonical_summary TEXT,
    first_seen_at     DATETIME NOT NULL,
    last_viewed_at    DATETIME,
    created_at        DATETIME DEFAULT CURRENT_TIMESTAMP,
    updated_at        DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS beat_members (
    beat_id  INTEGER NOT NULL REFERENCES beats(id) ON DELETE CASCADE,
    item_id  INTEGER NOT NULL REFERENCES items(id) ON DELETE CASCADE,
    added_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (beat_id, item_id)
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_beat_members_item ON beat_members(item_id);
CREATE INDEX IF NOT EXISTS idx_beats_pending_merge
    ON beats(id) WHERE canonical_summary IS NULL;
```

### Feature gate (`pkg/features/`)

```go
// BeatsEnabled returns whether beat aggregation is active.
// Callers must guard any beats-related I/O with this check.
func BeatsEnabled(cfg config.Config) bool {
    return strings.TrimSpace(cfg.Embedding.Provider) != ""
}
```

### Interfaces (in `pkg/scheduler/`)

```go
type Embedder interface {
    Embed(ctx context.Context, text string) ([]float32, error)
}

type Merger interface {
    Merge(ctx context.Context, members []domain.ClassifiedItem) (domain.BeatCanonical, error)
}

type BeatStore interface {
    PutEmbedding(ctx context.Context, itemID int64, model string, v []float32) error
    NearestIn(ctx context.Context, v []float32, since time.Time, topics []string, k int) ([]domain.Neighbor, error)
    AttachOrSeed(ctx context.Context, itemID int64, neighbor *domain.Neighbor) (beatID int64, err error)
    ListPendingMerge(ctx context.Context, limit int) ([]domain.Beat, error)
    SaveCanonical(ctx context.Context, beatID int64, c domain.BeatCanonical) error
    MarkViewed(ctx context.Context, beatID int64, at time.Time) error
    UnreadMemberCount(ctx context.Context, beatID int64) (int, error)
}
```

`Embedder` has no `Model()` method — the model name comes from config at the
call site.

### Config (`config.yml`)

```yaml
embedding:
  provider: ""          # "" = feature off. e.g. "openai" | "gemini"
  model:    ""          # e.g. "text-embedding-3-small"
  api_key:  ""
beats:
  sim_threshold: 0.85
  window:        48h
  max_members:   20
```

## Rollout

1. **Phase 1a — schema + dark embedding.** Ship DDL and `embed_worker`
   behind the feature flag. No UI change. Measure cost and coverage.
2. **Phase 1b — beat clustering.** Ship `beat_worker` with `sim_threshold=0.85`,
   canonical fields left NULL. Log assignments; keep items inbox as the only
   UI. Sample 50 beats, measure precision against a golden set.
3. **Phase 1c — LLM canonical summaries.** Ship `merge_worker`. Compare
   output to the raw first-member values from phase 1b; only proceed if the
   gain is visible.
4. **Phase 1d — UI.** Switch default inbox to beats; render "N new since
   last visit" via `last_viewed_at`.

## Consequences

**Good:**
- Inbox noise drops; ranking happens per story, not per source.
- `last_viewed_at` unlocks since-last-visit cheaply.
- Embeddings are reusable (future: semantic search, related-items).
- Fully gated by config — operators without an embedding provider run the
  same binary with zero surprise.

**Cost to watch:**
- ~$0.02/day embeddings + ~$0.05/day merge LLM at 5k items/day with
  `text-embedding-3-small`. Gemini `text-embedding-005` cuts embeddings ~3×.
- SQLite vector search via sqlite-vec is fine to ~10⁶ vectors; `BeatStore`
  isolates the index so a future swap is local.

**Risks:**
- **False merges** — two distinct stories collapsed. Mitigation: threshold
  is conservative (0.85), tuned against the golden set. No in-product split
  UI in phase 1; the LLM merge step can re-cluster implicitly by producing a
  split output when it becomes an issue.
- **Missed merges** — duplicates survive. Status quo; no regression.
- **Feature drift when off** — guard checks must not rot. Enforced by a
  test that runs with `provider=""` and asserts no beats table is queried
  and no beats route is mounted during a full request/response cycle.

## Alternatives considered

- **Single layer with window-extension ("beat survives as long as new
  members keep joining").** Rejected: conflates short-window duplication
  with long-arc narrative; needs a `max_lifetime` patch that signals the
  model is wrong.
- **Soft membership** (item in multiple beats with weights). Rejected:
  most articles report one thing; the UX cost (what does "mark read" mean
  across multiple beats?) outweighs the accuracy gain. Revisit when
  empirical multi-topic mis-assignment is a common complaint.
- **Multi-tenant `user_beat_views` table now.** Rejected: newscope is
  single-tenant with no auth. Adding `user_id='default'` rows would be
  pretend-multi-user. Move view state to a dedicated table when real user
  identity exists.
- **Pairwise LLM only** (skip embeddings). 50–100× more expensive, does
  not scale.
- **Title-only fuzzy match (MinHash/SimHash).** Misses rewrites and
  translations, which dominate our duplication.
- **Dedicated `pkg/dedup/` package now.** Premature; three files belong
  next to `feed_processor.go` until they don't.
- **Persisted `frozen_at`, `dirty`, `similarity`, and `dim` columns.**
  Each is derivable (`first_seen_at + 48h`, `canonical_summary IS NULL`,
  log-only debug info, inferable from `model`). Omitted.
