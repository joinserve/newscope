# Beats feature — development blueprint

Companion to [ADR 0010](../adr/0010-cross-item-deduplication-and-merge.md).
*How to build it* — PR sequencing and what to validate at each step.

## Guiding principle

Every PR must be **independently shippable and independently reversible**.
The feature gate (`config.embedding.provider == ""`) remains the global kill
switch throughout.

## Resolved prerequisites

- **Scheduler hook shape.** Existing `Scheduler.Start(ctx)` spawns each
  worker as its own goroutine and uses a per-worker config struct
  (`scheduler.go:177`, pattern at `feed_processor.go:40`). New workers
  follow the same shape, guarded by `features.BeatsEnabled(cfg)` at spawn
  time — mirroring the existing cleanup worker conditional
  (`scheduler.go:206`). No registration API to design.
- **User identity.** Newscope is single-tenant self-hosted: no auth
  middleware, no users table, no per-request user context. `beats`
  carries a single `last_viewed_at` column; multi-user is out of scope
  for this ADR.

## Open question remaining

- **Golden test set.** Pull a week of items from the running local DB and
  hand-label 20 "should merge" / 20 "shouldn't merge" pairs. This is the
  regression bar for PR 3's threshold tuning. I can't build this — you
  produce it, I consume it. Block PR 3 on it landing.

## PR sequence

### PR 1 — feature gate + empty schema

**Scope:**
- `config.embedding.*` and `config.beats.*` structs
- `pkg/features/BeatsEnabled()`
- Schema DDL for `item_embeddings`, `beats`, `beat_members` (empty tables)
- No worker code, no Embedder, no routes — the feature being off means
  *nothing mounts*, not "endpoint returns 503"

**Validates:**
- Feature flag behaves correctly in both states
- Existing items inbox is bit-for-bit unchanged when off
- No one-way door introduced

**Tests:**
- `BeatsEnabled()` on/off matrix
- Full request/response test with `provider=""` asserts no beats table is
  touched and no beats route is mounted

### PR 2 — Embedder + embed_worker (dark)

**Scope:**
- `Embedder` interface in `pkg/scheduler/`
- One implementation (OpenAI `text-embedding-3-small` or Gemini
  `text-embedding-005`)
- `embed_worker`: pulls classified-but-unembedded items, writes
  `item_embeddings`. No clustering.
- **No sqlite-vec yet** — BLOB storage only; kNN comes in PR 3

**Validates:**
- Real cost per day on real traffic (target: matches ADR estimate ±2×)
- 100% coverage of classified items within one tick cycle
- Embedder failures (rate limit, timeout) don't back up the scheduler

**Tests:**
- Mock Embedder for unit tests
- Integration test with recorded API responses

### PR 3 — beat_worker (no LLM yet)

**Scope:**
- `BeatStore.NearestIn`, `AttachOrSeed`, `MarkViewed`, `UnreadMemberCount`
- Introduce sqlite-vec extension
- canonical fields left NULL (phase 1b)
- 48h window, cosine threshold 0.85 as starting point

**Blocked by:** golden test set above.

**Validates:**
- Threshold precision on the golden set (target precision > 0.9; recall
  measured but not blocking)
- Per-tick latency well under the scheduler interval

**Tests:**
- Unit: `BeatStore` against in-memory SQLite
- Golden-set regression
- Property test: every classified item ends up in exactly one beat

**Highest-risk PR.** If precision is poor, iterate on threshold, input
text shape (title vs title+summary), or topic-overlap scope. Do not
proceed to PR 4 until this passes.

### PR 4 — merge_worker (LLM canonical)

**Scope:**
- `Merger` interface + one LLM implementation (reuse `pkg/llm/classifier`
  patterns)
- Worker processes beats with `canonical_summary IS NULL AND member_count > 1`
- Populates `canonical_title` and `canonical_summary`

**Validates:**
- Canonical summaries are noticeably better than PR 3's bare first-member
  values (subjective review — if not better, don't ship)
- LLM cost per day matches ADR estimate

**Tests:**
- Mock Merger for unit tests
- Contract test on output shape

### PR 5 — UI

**Scope:**
- `/api/v1/beats` returns beats with members (mounted only when feature on)
- HTMX template for beats inbox
- "N new since last visit" badge via `last_viewed_at`
- Writes `last_viewed_at` on beat detail view

**Validates:**
- Since-last-visit is the win the ADR promises (qualitative, on self)

**Tests:**
- Handler unit tests
- Template render tests

## What not to build

- **Stories** (multi-day event timelines). Separate concern, separate ADR
  when the need is concrete.
- **Admin "split beat" UI.** Defer until false-merge rate justifies it.
- **Multiple Embedder providers simultaneously.** One is enough; the
  interface lets us swap later.
- **LangExtract integration.** Relevant only when we start building stories.
- **Multi-tenant view state.** Add a `user_beat_views` table when auth
  lands; `beats.last_viewed_at` is the single-tenant shortcut.

## Rollback plan

Each PR is reversible:
- PR 1: revert is a no-op (empty tables stay, no data written)
- PR 2: stop worker, `DELETE FROM item_embeddings` — no reader depends on them
- PR 3: stop worker, set `embedding.provider=""` — routes unmount, UI
  renders the items inbox
- PR 4: revert canonical fields to NULL; PR 3's behaviour returns
- PR 5: set `embedding.provider=""`; UI reverts to items inbox

If a deeper rollback is needed, `DROP TABLE` the three beats tables — no
other table references them.
