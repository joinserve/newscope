# ADR 0015: Orphan cleanup when a source is deleted

- Status: Proposed
- Date: 2026-05-02
- Deciders: caspar

## Context

Deleting a feed (a "source") via `DELETE /api/v1/feeds/{id}` calls
`FeedRepository.DeleteFeed` (`pkg/repository/feed.go:204`), which
issues a single `DELETE FROM feeds WHERE id = ?`. Everything else
relies on SQLite's `ON DELETE CASCADE`.

The cascade chain in `pkg/repository/schema.sql` covers most of the
graph but leaves one branch dangling:

| Table | Cascade source | Cleared on feed delete? |
|---|---|---|
| `items` | `items.feed_id → feeds.id` (line 61) | ✅ |
| `item_embeddings` | `item_embeddings.item_id → items.id` (line 140) | ✅ (via items) |
| `beat_members` | `beat_members.item_id → items.id` (line 163) | ✅ (via items) |
| `beats` | — *(no FK to feed or items)* | ❌ |
| `beat_grouping_assignments` | `… → beats.id` (line 211) | ❌ (beat row stays) |
| `beat_title_revisions` | `… → beats.id` (line 221) | ❌ (beat row stays) |
| `beats_fts` | trigger on `beats` delete | ❌ (beat row stays) |

A `beats` row has no foreign key to `feeds` or `items` — it's only
linked indirectly through `beat_members`. When every member of a beat
is cascade-deleted with the feed, the beat row itself, its grouping
assignment, and its full revision history persist with no children.

### What we do today

Runtime filtering, not cleanup. `hydrateBeatRows`
(`pkg/repository/beat.go:783-787`) skips beats whose member list
hydrates to zero, and recent commits hardened the templates against
0-member beats reaching render:

- `192e1bd` — drop orphan beats with no hydrated members from list and detail
- `1906839` — guard `beat-card` against 0-member beats

This keeps the UI clean but the DB grows unbounded: an orphan beat is
invisible to users yet still occupies rows in `beats`,
`beat_grouping_assignments`, `beat_title_revisions`, and the
`beats_fts` shadow tables. The beat-canonicalisation worker also
re-reads them on every pass.

The existing `cleanupWorker` (`pkg/scheduler/scheduler.go:449`) only
calls `DeleteOldItems` (age + min-score). It has no notion of orphan
beats.

## Decision

Treat orphan beats as **garbage to collect**, not state to filter.
Two complementary mechanisms:

### 1. Transactional sweep at the end of `DeleteFeed`

`DeleteFeed` becomes a transaction that:

1. Deletes the feed row (cascade fans out to `items`,
   `item_embeddings`, `beat_members`).
2. Within the same tx, deletes any beat that no longer has members:

   ```sql
   DELETE FROM beats
   WHERE id NOT IN (SELECT DISTINCT beat_id FROM beat_members);
   ```

   This cascades to `beat_grouping_assignments` and
   `beat_title_revisions`, and the `beats_fts` delete trigger
   (schema.sql:184) keeps the FTS index in sync.

Why in the same transaction: deleting a feed is the dominant source
of orphan beats — a single feed can own dozens. Cleaning up in-band
means the visible "delete source" action leaves no trace, and we
don't depend on the cleanup worker running before the user notices.

### 2. Periodic sweep in `cleanupWorker`

`performCleanup` (`pkg/scheduler/scheduler.go:469`) gains a second
step after `DeleteOldItems`:

```go
orphans, err := s.itemManager.DeleteOrphanBeats(ctx)
```

Same `DELETE FROM beats WHERE id NOT IN (…)` query, run on the
existing cleanup cadence. This is the safety net for the *other*
ways a beat can lose all members:

- `DeleteOldItems` itself (low-score articles aged out)
- Manual item deletion paths (if any are added later)
- Future merge/split operations that move items between beats

Without (2), every codepath that deletes items would have to remember
to sweep orphan beats. With (2), the schedule does it.

### Where the new method lives

`DeleteOrphanBeats(ctx) (int64, error)` goes on `BeatRepository`
(`pkg/repository/beat.go`), not `ItemRepository`. The scheduler
already takes a narrow interface — extend it with the new method and
update the moq.

The single-statement form is fine: SQLite handles the
`NOT IN (SELECT …)` shape efficiently for the row counts involved
(beats are O(thousands), not millions), and the FTS / cascade
triggers do the rest.

## Out of scope

- **Backfill of pre-existing orphans.** First run of the cleanup
  worker after deploy will sweep them; no migration needed.
- **Orphans in other parts of the schema.** A grep for tables
  referencing `beats.id` shows all of them already cascade. If the
  schema gains a new beat-adjacent table later, that ADR carries the
  cleanup obligation.
- **Cascading from feed → beat directly.** Tempting to add
  `beats.source_feed_id` so cascade does the work, but a beat by
  definition spans multiple feeds — there's no single "owning" feed,
  and ADR 0010 explicitly rejects per-source beats. The "delete beat
  iff member count is zero" rule is the correct semantics.
- **Counting / observability for the sweep.** `performCleanup`
  already logs `DeleteOldItems` count; mirror that for the new
  method. Anything fancier (metrics, alerts) is premature.

## Migration / rollout

- No schema change. Single PR: extend `DeleteFeed` to a transaction +
  new sweep, add `DeleteOrphanBeats` on `BeatRepository`, wire it
  into `performCleanup`, regenerate mocks, add tests.
- Tests: table-driven on `BeatRepository` covering (a) feed delete
  removes its sole-feed beats, (b) feed delete leaves multi-feed
  beats alone, (c) `DeleteOrphanBeats` is idempotent (returns 0 when
  no orphans), (d) cascade clears `beat_grouping_assignments` and
  `beat_title_revisions`.
- Rollback: revert PR. Orphans re-accumulate, runtime filtering
  (192e1bd, beat.go:783-787) keeps the UI honest in the meantime.
