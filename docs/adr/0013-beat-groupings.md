# ADR 0013: User-defined beat groupings

- Status: Proposed
- Date: 2026-04-24
- Deciders: caspar

## Context

Beats (ADR 0010) collapse short-window duplicates into one card per story.
That works for "different sources reporting the same event" but does not
help with the next layer of noise: **a stream of related but distinct
events on a topic the user follows**. Three-day window of `/beats` today
shows ~12 separate beats about Taiwan-China cross-strait news, ~5 about
Claude/Anthropic, ~4 about SpaceX. Each is a legitimate beat (different
events, low cosine similarity), so the merge worker correctly leaves
them apart — but the user reads them as one stream.

The user wants a **bookmark-like layer above beats**: pick a name, pick
a tag set, get a dedicated stream that pulls those beats out of the main
inbox. Conceptually identical to email folders with rules — except the
rules match against extracted structured tags, not text search.

Two adjacent ideas were rejected before settling on this one:

- **Auto-discovered topic streams** (LLM clusters beats into "themes"):
  too magical, no user control, hard to reason about why a beat shows
  up in stream X and not Y.
- **Soft membership** (a beat appears in every matching stream): defeats
  the "concentrate the noise" goal. If `#taiwan #china` and
  `#taiwan #politics` both match a beat, the user wants exactly one of
  them to own it — chosen by their stated preference order.

Tag matching has a coverage problem: the existing classifier produces
high-level topic chips like `ai`, `china`, `politics` — useful for broad
slices, but no `claude`, `spacex`, `openai`, `anthropic`. The user wants
those as grouping keys. The fix is **entity extraction**, not free-text
search over titles.

The decisions below apply to a new `pkg/grouping/` package, new tables
in `pkg/repository/schema.sql`, the `/beats` handler in
`server/htmx_handlers.go`, and `server/templates/beats.html` (the page
header gains a grouping dropdown).

## Decisions

### 1. Groupings are user-defined records: `(name, tags[], display_order)`

A grouping is a named, ordered tag set:

```
{ id: 1, name: "Taiwan politics", slug: "taiwan-politics",
  tags: ["taiwan", "politics"], display_order: 0 }
{ id: 2, name: "Taiwan-China",    slug: "taiwan-china",
  tags: ["taiwan", "china"],     display_order: 1 }
{ id: 3, name: "Claude",          slug: "claude",
  tags: ["claude"],              display_order: 2 }
```

CRUD lives in the existing settings page. No discovery, no suggestions
in v1 — the user types the name and tags, hits save.

**Rejected alternative:** auto-discovered streams. Too opaque; user
can't predict what shows up where.

### 2. Match against extracted structured tags, not text substrings

A beat matches a grouping iff `grouping.tags ⊆ beat.tag_set`, where
`beat.tag_set` is the union over its members of:

- `items.topics` (existing, from classifier), and
- `items.entities` (new, from a new entity_worker)

The classifier produces broad topic labels (`ai`, `china`, `politics`).
A new lightweight LLM step extracts proper-noun entities (`claude`,
`spacex`, `anthropic`, `openai`, `nvidia`, `tsmc`) and stores them in
`items.entities` as a normalized lowercase JSON array.

Tags in groupings are matched case-insensitively against this combined
set. So `["taiwan", "claude"]` matches a beat whose member items
collectively carry both — `taiwan` from `topics`, `claude` from
`entities`.

**Rejected alternative:** substring search over title/summary. False
positives ("Claude Monet" matches `claude`); the user explicitly asked
for extraction (`抽取，不要搜尋`).

**Rejected alternative:** teach the existing classifier to also emit
entities in one call. Possible follow-up, but conflates the two LLM
prompts and forces a re-classification for every existing item.
Separate worker is cleaner and lets us pick a cheaper model for
extraction without dragging classification along.

### 3. Beat-level (not item-level) AND semantics

The required tags must all appear *somewhere across the beat's
members* — not all on one item. If a beat bundles two articles, one
tagged `[ai, claude]` and another `[ai, china]`, then a grouping
requiring `[claude, china]` matches.

This matches user mental model — a beat is the unit shown in the inbox,
so its tag set is the union.

**Rejected alternative:** per-item AND (require every grouping tag on at
least one shared item). Stricter; cuts out the cross-source bundling
that beats were created to support.

### 4. First-match-wins by user-defined order

A beat matches *at most one* grouping — the first one in
`display_order` whose tag set is a subset of the beat's tag set. If
`Taiwan politics` (display_order=0) and `Taiwan-China` (display_order=1)
both match, the beat lands in `Taiwan politics` only.

This is the lever that lets the user steer where a multi-tag beat
concentrates: re-order the groupings to control the priority.

**Rejected alternative:** beat appears in every matching grouping.
Defeats the "concentrate" goal — the same beat shows up two or three
places, user is back to skimming duplicates.

**Rejected alternative:** specificity-wins (longer tag set beats
shorter). Implicit; fails when two groupings have the same length but
the user prefers one.

### 5. Beats matched by any grouping are removed from main inbox

`GET /beats` (no `?group=` param) returns beats with no matching
grouping. `GET /beats?group=<slug>` returns beats assigned to that
grouping. The page header is a dropdown:

```
[ All beats (53) ▾ ]    ← default, all unmatched
   ├ All beats
   ├ Taiwan politics (12)
   ├ Taiwan-China (8)
   └ Claude (5)
```

Counts reflect unread (`unread_count > 0`) within each bucket, mirroring
the existing inbox-zero gate.

This is what makes the feature load-bearing — the user gets a quieter
default inbox in exchange for promising "I'll check the named streams
when I care".

### 6. Assignments are materialized in `beat_grouping_assignments`

A separate table maps `beat_id → grouping_id` (nullable, single row per
beat). Recomputed at exactly two events:

- **Beat membership changes** — `beat_worker` recomputes the assignment
  for that one beat after `AttachOrSeed`.
- **Grouping CRUD** — when the user adds/edits/reorders/deletes a
  grouping, recompute assignments for all beats inside the active
  window (`first_seen_at > now - 48h`). ~2k beats, ~milliseconds.

This keeps the `/beats` query simple — a single `LEFT JOIN` to
`beat_grouping_assignments` plus a `WHERE` clause — and avoids
recomputing first-match-wins on every page load.

**Rejected alternative:** compute on the fly in `ListBeats`. Pagination
becomes incorrect when post-query filtering removes some rows; would
need a CTE that re-implements first-match-wins in SQL. Materialization
trades a tiny write-time cost for query simplicity.

**Rejected alternative:** trigger-based assignment maintenance.
Groupings are JSON tags; expressing first-match-wins in SQLite triggers
is painful. Worker-side computation is more readable.

### 7. Entity extraction is its own toggleable feature

Like the embedding pipeline (ADR 0010), entity extraction is gated by
config: `entities.enabled: false` (default) → no worker, no
`items.entities` writes, groupings simply match against `topics` only.

This means groupings work day one even without entity extraction, just
limited to topics the classifier already produces. Turning entities on
expands the available tag vocabulary.

## Data model

```
┌──────────────┐
│ groupings    │
│ id           │
│ name         │
│ slug         │  ← URL-safe; UNIQUE
│ tags JSON    │  ← lowercased ["taiwan","politics"]
│ display_order│  ← INT, lower = higher priority
│ created_at   │
│ updated_at   │
└──────┬───────┘
       │
       │ N
       ▼
┌────────────────────────────┐         ┌──────────┐
│ beat_grouping_assignments  │ N    1  │  beats   │
│                            │─────────│          │
│ beat_id PK FK              │         └──────────┘
│ grouping_id NULL FK        │
│ computed_at                │
└────────────────────────────┘

┌────────────────┐
│ items          │
│ ...            │
│ topics JSON    │  ← existing
│ entities JSON  │  ← NEW, lowercase strings
│ entities_extracted_at DATETIME │  ← NEW
└────────────────┘
```

Notes:
- `beat_grouping_assignments.grouping_id` NULL means "computed and matched
  no grouping" (so we can distinguish "unassigned" from "not yet
  computed" by row presence, not by a separate flag).
- `items.entities` and `items.topics` together form the matchable tag
  set; both are JSON arrays of lowercase strings.

## Architecture

```
                     classified items
                            │
                            ▼
            ┌────────────────────────────┐
            │ entity_worker (NEW, gated) │
            │ items where                │
            │   entities_extracted_at IS │
            │   NULL AND classified_at   │
            │   IS NOT NULL              │
            │                            │
            │ LLM → ["claude","openai"]  │
            └─────────────┬──────────────┘
                          ▼
            items.entities + entities_extracted_at

         beat_worker (existing)
                          │
                          │ on AttachOrSeed
                          ▼
            ┌────────────────────────────┐
            │ grouping.Reassign(beatID)  │
            │  load beat tag_set         │
            │  walk groupings in order   │
            │  first match wins          │
            │  upsert assignment row     │
            └────────────────────────────┘

         settings UI: grouping CRUD
                          │
                          ▼
            grouping.ReassignAll(window=48h)
              walks all active beats,
              same logic.

         /beats handler
                          │
                          ▼
            ListBeats(groupSlug *string)
              LEFT JOIN beat_grouping_assignments
              WHERE
                groupSlug == nil → assignment.grouping_id IS NULL
                groupSlug != nil → assignment.grouping_id = X
```

## Implementation sketch

### Schema

```sql
CREATE TABLE IF NOT EXISTS groupings (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    name          TEXT    NOT NULL,
    slug          TEXT    NOT NULL UNIQUE,
    tags          JSON    NOT NULL DEFAULT '[]',
    display_order INTEGER NOT NULL DEFAULT 0,
    created_at    DATETIME DEFAULT CURRENT_TIMESTAMP,
    updated_at    DATETIME DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX IF NOT EXISTS idx_groupings_order ON groupings(display_order);

CREATE TABLE IF NOT EXISTS beat_grouping_assignments (
    beat_id      INTEGER PRIMARY KEY REFERENCES beats(id) ON DELETE CASCADE,
    grouping_id  INTEGER REFERENCES groupings(id) ON DELETE SET NULL,
    computed_at  DATETIME DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX IF NOT EXISTS idx_assignments_grouping
    ON beat_grouping_assignments(grouping_id);

ALTER TABLE items ADD COLUMN entities JSON DEFAULT '[]';
ALTER TABLE items ADD COLUMN entities_extracted_at DATETIME;
CREATE INDEX IF NOT EXISTS idx_items_entities_pending
    ON items(entities_extracted_at) WHERE entities_extracted_at IS NULL;
```

### Config

```yaml
entities:
  enabled:  false        # gate; off = entity_worker never starts
  provider: "openai"     # reuse existing LLM client
  model:    "gpt-4o-mini"
  batch:    20           # items per LLM call
```

### Interfaces

```go
// pkg/scheduler/entity_worker.go
type EntityExtractor interface {
    Extract(ctx context.Context, items []domain.ClassifiedItem) ([][]string, error)
}

// pkg/grouping/grouping.go
type Engine interface {
    Reassign(ctx context.Context, beatID int64) error
    ReassignAll(ctx context.Context, window time.Duration) error
}

// pkg/repository (additions)
type GroupingStore interface {
    ListGroupings(ctx context.Context) ([]domain.Grouping, error)
    CreateGrouping(ctx context.Context, g domain.Grouping) (int64, error)
    UpdateGrouping(ctx context.Context, g domain.Grouping) error
    DeleteGrouping(ctx context.Context, id int64) error
    ReorderGroupings(ctx context.Context, idsInOrder []int64) error
    UpsertAssignment(ctx context.Context, beatID int64, groupingID *int64) error
    BeatTagSet(ctx context.Context, beatID int64) ([]string, error)
}
```

### Routes

```
GET    /beats?group=<slug>           filtered list
GET    /settings/groupings           CRUD page
POST   /api/v1/groupings             create
PUT    /api/v1/groupings/{id}        update (name, tags)
POST   /api/v1/groupings/reorder     accepts ordered id list
DELETE /api/v1/groupings/{id}        delete
```

### UI

`beats.html` header replaces the "Beats" title with a dropdown:

```html
<details class="grouping-switcher">
  <summary>{{.CurrentGroupingName}} ({{.CurrentCount}})</summary>
  <ul>
    <li><a href="/beats">All beats ({{.AllCount}})</a></li>
    {{range .Groupings}}
    <li><a href="/beats?group={{.Slug}}">{{.Name}} ({{.Count}})</a></li>
    {{end}}
    <li><a href="/settings/groupings" class="meta">Manage…</a></li>
  </ul>
</details>
```

Settings page gets a `Groupings` section with: list (drag-to-reorder),
inline edit, delete confirmation, "Add grouping" form (name + tag chips).

## Rollout

1. **Phase A — schema + grouping CRUD + assignment table.** Ship the
   tables, the repository methods, the settings UI. No grouping ever
   matches anything (entity_worker not running, classifier-only tags).
   Verify the dropdown works and shows the user-defined names.
2. **Phase B — assignment engine.** Wire `Reassign` into `beat_worker`'s
   `AttachOrSeed` path and into grouping CRUD. Beats now move into
   user-defined groupings purely on `topics` matches. Document that
   `claude`-style tags need Phase C.
3. **Phase C — entity_worker.** Ship the gated entity extractor. With it
   on, the available tag vocabulary expands to proper-noun entities,
   and previously-unmatched groupings start filling.
4. **Phase D — polish.** Drag-to-reorder UX, entity tag autocomplete in
   the grouping form (suggest from observed `entities` distribution),
   per-grouping unread badge in the dropdown.

## Consequences

**Good:**
- Main inbox shrinks by exactly the amount the user has named — quiet
  by construction, not by heuristic.
- Each grouping is a stream the user picked, so "why is this here"
  always has a one-sentence answer (tag list + first-match order).
- Entities give the classifier-impoverished tags (`claude`, `spacex`)
  a home without polluting the topic taxonomy.
- Feature is layered: groupings work day one without entities; entities
  are a cost-controlled add-on.

**Cost:**
- New worker, new table, new settings UI surface area.
- Entity LLM cost: order-of-magnitude similar to `merge_worker` (one
  small call per item, batched). Gate keeps it off until the user opts in.
- `ListBeats` gains one `LEFT JOIN`. Negligible at current volume.

**Risks:**
- **First-match ordering surprise** — user adds a broad grouping
  (`#ai`) to the top, every Claude/OpenAI beat falls into it instead
  of the specific groupings below. Mitigation: the dropdown shows
  counts per grouping, so emptied-out groupings are visible
  immediately and the user re-orders.
- **Entity hallucination** — LLM emits "Claude" for a Claude Monet
  article. Mitigation: extraction prompt is constrained to a curated
  vocabulary (companies, products, public figures, places), with a
  note to skip ambiguous mentions. Errors surface as "wrong stream"
  complaints; recoverable by re-extracting with a tighter prompt.
- **Stale assignments after entity_worker catches up** — entities for
  an item arrive after its beat already had `Reassign` run. Fix:
  `entity_worker` triggers `Reassign(beatID)` for any beat whose
  members it just enriched.

## Alternatives considered

- **Auto-clustered topic streams** — LLM groups beats into themes with
  no user input. Opaque; user can't tune.
- **Soft membership** (beat appears in every matching grouping) —
  defeats the "concentrate the noise" goal.
- **Specificity-wins ordering** — implicit; ties are awkward; user
  loses the ordering lever.
- **Substring text matching over title/summary** — user explicitly
  rejected (`抽取，不要搜尋`); false positives unavoidable.
- **Teach the existing classifier to emit entities in one prompt** —
  conflates two model concerns; would force a re-classify for every
  existing item to populate entities. Possible future merge once both
  are stable.
- **Trigger-based assignment maintenance** — first-match-wins is
  painful in SQLite triggers; worker-side is readable.

## Related

- ADR 0010 (beats backend) — beats are the units being grouped.
- ADR 0011 (beats UI) — header dropdown extends the existing page
  layout.
- ADR 0012 (topic chip navigation) — chips drawn from `topics`, will
  *not* be drawn from `entities` (entities are matching-only, not
  user-facing chips, until product feedback says otherwise).
