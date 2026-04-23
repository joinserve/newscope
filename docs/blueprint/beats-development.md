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

## Prerequisite work before PR 3

PR 3 is blocked on a golden test set of labelled item pairs. To avoid the
friction of hand-querying the DB, we build a small CLI tool that surfaces
candidate pairs and writes labels to a fixture file.

### Session 1.5 — labeling tool (independent session)

**What to build:** `cmd/beats-label/` — a standalone CLI that reads
candidate item pairs, displays them side-by-side in the terminal, and
writes y/n/skip decisions to `docs/fixtures/beats-golden.json`.

**Candidate generation (the key to this being fast):** random pairs would
waste the user's time on obvious negatives. Two sources, merged:

1. **High-similarity pairs from existing embeddings.** Once PR 2 has run
   for a few days, `item_embeddings` contains enough data to compute top-K
   cosine-similar pairs within a 48h window. Cluster by 0.1-wide similarity
   buckets (0.5–0.6, 0.6–0.7, 0.7–0.8, 0.8–0.9, 0.9–1.0) and sample ~10
   pairs from each bucket. This gives the threshold-boundary cases — the
   ones that matter most for tuning.
2. **Random within-window pairs as negatives.** Sample ~20 random pairs in
   a 48h window to anchor the "obviously shouldn't merge" region of the set.

No embeddings yet? Fall back to title-trigram Jaccard for bucketing; it's
coarser but enough to avoid pure-random waste.

**Session goal output:** ~60 candidate pairs total → user labels in ~10
minutes → ≥40 decisions (20 merge + 20 no-merge; skips are fine).

**Behaviour:**
- Reads `config.yml` just enough to locate the database DSN.
- Pairs rendered in terminal with item IDs, feed name, published time, full
  title, first 200 chars of summary, and the computed similarity.
- Keys: `y` merge, `n` not merge, `s` skip, `q` quit. `q` is non-destructive
  — progress so far is already saved.
- Fixture format: JSON array of `{a_id, b_id, should_merge, similarity,
  labelled_at}` records. Idempotent append: re-running with the same
  candidate does not duplicate.
- **No LLM calls anywhere.** The tool is for generating ground truth; it
  must not use model output as input.

**Scope discipline:**
- Single file `cmd/beats-label/main.go` — ~150 lines, no new packages.
- Zero new runtime dependencies beyond stdlib + what newscope already uses
  (`sqlx`, the project's config loader).
- Unit tests only for the fixture-file writer (append-without-duplication)
  and the candidate-bucketing logic. The UI loop does not need tests.
- This tool is ops glue, not a product feature. It ships merged to `master`
  so contributors can reuse it, but no UI, no server routes, no feature
  flag.

**Session goal:** user runs `go run ./cmd/beats-label`, labels ~60 pairs,
commits `docs/fixtures/beats-golden.json`. PR 3 session reads that fixture
as its regression bar.

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

**Blocked by:** `docs/fixtures/beats-golden.json` produced by the labeling
tool above.

**Validates:**
- Threshold precision on the golden set (target precision > 0.9; recall
  measured but not blocking)
- Per-tick latency well under the scheduler interval

**Tests:**
- Unit: `BeatStore` against in-memory SQLite
- Golden-set regression loads `docs/fixtures/beats-golden.json` and asserts
  the clustering decision matches the label for each pair
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
- `/beats`, `/beats/{id}` handlers + member-expand / feedback / search routes
  under `/api/v1/beats/...` (mounted only when feature on)
- HTMX templates for beats inbox and beat detail

**UI shape — see ADR 0011.** Final design diverges from this blueprint's
original "since-last-visit" framing; the landed design uses an
article-card-mirroring toolbar with an SNS-thread-style member expand.

**Validates:**
- Beats inbox is usable on self without falling back to the items inbox

**Tests:**
- Handler unit tests
- Template render tests

## Session 5 — backend cleanup (runs BEFORE PR 5 UI)

Three independently-shippable backend PRs. **Build order: PR 6 → 7 → 8, then
PR 5.** PR 5's templates depend on the fields and routes added here:

- PR 6 adds `beats.feedback` (the ❤ button needs somewhere to write).
- PR 7 adds the re-summary mechanic so canonical text refreshes as new
  members attach.
- PR 8 adds the search route (PR 5 wires the search box).

UI work stays out of this session; PR 5 picks up all four pieces at once.

**PR 9 — late-addition cleanup (landed alongside PR 5):** decouple item
feedback from `processed_at` so liking a member article no longer bumps it
to the top of the items inbox. Surfaced while testing the beats UI; tracked
here rather than in a separate session because the fix is small and tied to
the same UX round.

### PR 6 — beat-level feedback

**Decision (recorded):** beat ❤ expresses interest in the *event*, not in any
member article. It does NOT propagate to members — disliking a journalist is
a separate signal. Beat ❤ also does not affect read-state; "viewed" remains
the only read trigger.

**Scope:**
- New column `beats.feedback` (`like` / `dislike` / NULL).
- `BeatStore.SetFeedback(ctx, beatID, feedback) error` + reader on the Beat
  type so handlers can render it.
- Feedback persists across re-summary (PR 7) and re-attach.
- **Not** mirrored into `classifications.feedback` for any member; the
  classifier preference summary continues to read only per-item feedback.

**Tests:**
- Round-trip set/get.
- Re-summary in PR 7 must preserve `feedback`.

### PR 7 — beat re-summary on member attach

**Problem:** today, `beats.canonical_summary` is written once when the beat
crosses 1→2 members, then never again. New members landing within the 48h
window add to the beat but don't update the user-facing canonical text.

**Scope:**
- When `AttachOrSeed` adds a member to an *existing* beat, mark the beat
  re-merge-eligible (e.g. set `canonical_dirty_at` or null out
  `canonical_summary` once a per-day debounce permits).
- `merge_worker` picks up dirty beats just like NULL-canonical ones.
- **Cadence cap: at most one re-merge per beat per 24h.** Avoids burning LLM
  spend (and rewriting the user's mental anchor) on every drip-update.
- Only beats still inside the 48h attach window are eligible — once the
  window has closed, the canonical is frozen.
- `feedback` and `last_viewed_at` MUST be preserved across re-summary.
  (Unread-badge semantics are moot — ADR 0011 dropped the badge in favour
  of the comment-button member count.)

**Tests:**
- New member within 24h cap → no immediate re-merge.
- New member after 24h cap → re-merge enqueued, ❤ + last_viewed_at intact.
- New member after 48h window → no re-merge.

### PR 8 — beat search

**Scope:**
- FTS5 virtual table over `canonical_title` + `canonical_summary`,
  back-filled and kept in sync via triggers.
- Fall-through to member-title FTS for beats whose canonical is still NULL
  (single-member beats, or pre-PR-7 stale entries).
- `BeatStore.Search(ctx, query, limit) ([]Beat, error)` + handler/route.
- No semantic search yet; the embeddings table makes adding it later cheap
  but FTS5 covers the immediate need without API spend.

**Tests:**
- FTS index populated on beat insert and on canonical update from PR 7.
- Single-member fallthrough returns the beat when only the member title
  matches.

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
- PR 6: drop `beats.feedback` column; readers tolerate absence
- PR 7: stop re-merge worker; existing canonicals stay frozen as before
- PR 8: drop FTS table + triggers; `Search` route 404s

If a deeper rollback is needed, `DROP TABLE` the three beats tables — no
other table references them.
