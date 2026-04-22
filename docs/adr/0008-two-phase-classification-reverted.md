# ADR 0008: Two-phase classification tried and reverted

- Status: Superseded (reverted to single-phase)
- Date: 2026-04-20 (introduced), 2026-04-21 (reverted)
- Deciders: caspar

## Context

LLM classification (score + summary in one call) was the single most expensive
operation. The hypothesis was: score first, then only summarize articles that pass
a relevance threshold. This would skip summarization for most items and reduce cost.

## What was tried (D1–D7)

`ClassifyArticles` was split into two methods:

- **Phase 1 — `ScoreArticles`**: returns score + topics only. Runs on every item.
- **Phase 2 — `SummarizeArticle`**: returns summary. Runs only when score ≥ `summary_threshold`.

A new `summarized_at` column tracked phase-2 completion. An on-demand
`POST /api/v1/summarize/{id}` endpoint let users trigger phase-2 manually.
A **Summarize** button appeared on cards missing a summary.

## Why it was reverted

Three failure modes emerged quickly in practice:

1. **Stale UI states**: phase-1 items appeared in the inbox with a score but no
   summary, creating visually incomplete cards that users saw before phase-2 ran.
2. **Silent phase-2 failures**: no retry mechanism or orphan sweeper was wired up.
   Items could permanently miss phase-2 if the worker failed mid-run.
3. **Confusing button behavior**: the manual Summarize button added user-facing
   complexity for a backend optimization users shouldn't need to think about.

The cost problem was solved differently: switching to a cheaper model
(`gpt-4o-mini` class) at the deployment layer, keeping the single-phase
`ClassifyArticles` that returns score + summary in one call.

## Decision (after revert)

Restore single-phase `ClassifyArticles`. Remove `summarized_at`, `summary_threshold`,
the Summarize button, and the on-demand endpoint. UI polish added during the
two-phase experiment (swipe layout, `stripHTML`, `card-summary-fallback` styling,
feed `icon_url`) is retained.

## Consequences

**Good:**
- No stale intermediate states; every classified item has both score and summary.
- Simpler code: one classifier method, one column, no phase coordination.
- Cost target met via model selection, not architectural complexity.

**Lesson:**
Cost-reduction through pipeline splitting is fragile if there is no robust retry /
compensating sweep for the expensive phase. Don't split a pipeline stage unless the
retry and orphan-recovery paths are designed and tested at the same time.

## Alternatives that remain valid

- A background sweep that detects items where `classified_at IS NOT NULL AND summary IS NULL`
  could recover orphans without exposing the two-phase model to the UI. This was not
  implemented before the revert.
- A proper work-queue (e.g., embedded task queue) would make retry and dead-letter
  handling explicit. Deferred until volume justifies the complexity.
