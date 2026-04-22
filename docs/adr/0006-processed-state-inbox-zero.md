# ADR 0006: Processed state for inbox-zero workflow

- Status: Accepted
- Date: 2026-04-19
- Deciders: caspar

## Context

The original model had two outcomes for an article: liked or disliked. Both updated
the user's preference score and dismissed the article from the default view. This
coupled two concerns:

1. **Visibility** — has the user seen and acted on this article?
2. **Relevance signal** — does this article represent something the user wants more of?

Users wanted a neutral "dismiss" action that clears an article from the inbox without
biasing future recommendations — an inbox-zero pattern.

## Decision

Add a `processed_at DATETIME` column to `items`. An item is visible in the default
inbox when `processed_at IS NULL`. Existing like/dislike actions now set `processed_at`
in addition to `user_feedback`. A new **Done** action sets only `processed_at`,
leaving `user_feedback` untouched.

Filter precedence: `ShowLikedOnly` > `ShowProcessed` > default inbox. A **Show Processed**
toggle lets users browse dismissed items without affecting the default view.

Migration: `ALTER TABLE ADD COLUMN processed_at` + backfill
`processed_at = feedback_at` for existing feedback rows.

## Consequences

**Good:**
- Separates inbox management from preference learning; the recommendation model
  only sees explicit like/dislike signals.
- Done action gives users a clutter-free inbox without training the model to show
  less of something they merely finished reading.

**Constraints:**
- A third filter state (`ShowProcessed`) adds UI complexity; filter precedence must
  be documented and enforced consistently across all query paths including search.
- `processed_at` is append-only — there is no "un-done" action. Items cannot be
  restored to the inbox without a direct DB edit.
