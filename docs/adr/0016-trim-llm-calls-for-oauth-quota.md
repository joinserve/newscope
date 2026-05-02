# ADR 0016: Trim LLM call surface to fit ChatGPT OAuth quota

- Status: Proposed
- Date: 2026-05-02
- Deciders: caspar

## Context

newscope's LLM path runs through litellm to a ChatGPT subscription via
OAuth (see the comment in `pkg/llm/completion.go:13-15`, which is also
why streaming is forced on that path — non-streaming is broken
upstream). That path has no per-token billing, but it has hard weekly
and 5-hour usage caps tied to the subscription tier.

Observation on 2026-05-02: weekly cap reset on 2026-04-29; 46% of the
weekly budget was consumed in 3 days. Linear extrapolation puts the
account over cap before the next reset.

### What we already did

joinserve-infra PR #115 dialled the schedule down:

| Setting | Before | After | Effect |
|---|---|---|---|
| `schedule.update_interval` | `30m` | `2h` | 4× fewer feed cycles |
| `classification.batch_size` | `5` | `15` | 3× fewer classification calls |

That bought breathing room (~12× fewer calls per day under steady
state) but does not address per-call inefficiency. The same ChatGPT
weight model also penalises long prompts, and several of newscope's
LLM call sites are doing redundant work that survives the throttle.

### Why not switch to a paid API key

Direct OpenAI API would dissolve the cap and is cheap on `gpt-5-nano`,
but it costs real money where OAuth costs nothing. The decision to
stay on OAuth is upstream of this ADR; this ADR works within that
constraint.

### Where the per-call fat is

| Call site | File | Wasted work |
|---|---|---|
| Article classification | `pkg/llm/classifier.go:170-194` | system prompt repeats forbidden-prefix list that `cleanSummary` already strips deterministically; bundles a 50-item canonical-topics list every call |
| Forbidden-prefix retry loop | `pkg/llm/classifier.go:163-274` | up to 3 outer attempts × 5 repeater retries = 20 full-batch calls before falling back to `cleanSummary` (which the last attempt invokes anyway) |
| Merger retry loop | `pkg/llm/merger.go:88-143` | mirror of the above on the merger path |
| Entity extraction | `pkg/scheduler/entity_extractor.go:40-78` | a *separate* LLM call for every classified item batch, sending title + summary that the classifier already saw seconds earlier |
| Preference summary update | `pkg/scheduler/preference_manager.go:70-120` | "update" path resends the full 50 most recent feedbacks every time, on top of the existing `current_summary` that already encodes them |

## Decision

Five code-side reductions that drop request count and per-call payload
without changing observable output quality. Each is independent and
can land in its own PR.

### 1. Fold entity extraction into the classifier

**Today.** `EntityWorker` (`pkg/scheduler/entity_worker.go:67-138`)
polls items where `classified_at IS NOT NULL AND
entities_extracted_at IS NULL` (`pkg/repository/item.go:426-440`) and
invokes `LLMEntityExtractor.Extract`
(`pkg/scheduler/entity_extractor.go:40`). That's a second round-trip
sending title + summary the classifier just produced.

**Change.** Add an `entities` field (lowercase, ≤5 strings,
companies/products/people/locations only) to the classifier's JSON
schema and system prompt. `EntityWorker` becomes a thin DB worker that
pulls entities off the classification result and persists them via
`SaveEntities` — no LLM call. The `entities_extracted_at` column stays
as the marker for grouping reassignment, so `GroupingAssigner.Reassign`
still fires on the same trigger.

**Files.** `pkg/llm/classifier.go` (system prompt, `Classification`
struct, parsing), `pkg/scheduler/entity_worker.go`,
`pkg/scheduler/entity_extractor.go` (deletable),
`pkg/scheduler/feed_processor.go:229` (propagate entities into the
classification write path), `pkg/repository/classification.go`
(persist entities alongside the rest of the classification).

**Impact.** Eliminates the entity-extractor LLM call site entirely.
Order-of-magnitude on request count: at the current 15-item batch
size, this used to be one classifier call + one entity call per
batch → halves the LLM call count for the article path.

**Risk.** Classifier prompt grows by the entity instructions
(~80 tokens) and the response by 5 short strings per item — small
relative to the savings. Quality risk: the classifier sees full
`Content`, the entity extractor today only sees `Summary`; classifier
output should be at least as good, often better.

### 2. Delete the forbidden-prefix retry loop, keep `cleanSummary`

**Today.** `Classifier.classify`
(`pkg/llm/classifier.go:163-274`) wraps the API call in two layers:
an outer loop that re-prompts the LLM up to `SummaryRetryAttempts`
times when any returned summary starts with a forbidden phrase, and
the `repeater.NewBackoff(5, …)` inside that loop for transient
errors. The last outer attempt falls back to `cleanSummary`
(`pkg/llm/classifier.go:417-443`), which deterministically strips the
prefix and capitalises the next character.

`Merger.Merge` (`pkg/llm/merger.go:88-143`) replicates the same
pattern.

**Change.** Drop the outer retry loop on both paths. Always run
`cleanSummary` on the response. Keep `repeater` for transient API
errors (network blips, 5xx) but not for content-shape disagreements.

**Impact.** Happy path is unchanged (cleanSummary is a no-op when no
prefix matches). Error path: a batch that today costs up to ~20
full-prompt calls before succumbing to cleanSummary now costs 1 call
+ string manipulation.

**Risk.** When the LLM produces a forbidden prefix on a long sentence,
re-prompting *might* generate a substantively better rewrite than the
mechanical strip. In practice the prefix is a meta-language wrapper
around the same content ("The article discusses X happening" → "X
happening"), so the loss is cosmetic. The retry was a belt-on-belt-on-
suspenders construction; cleanSummary is sufficient.

### 3. Make `summary_retry_attempts: 0` actually disable retries

`pkg/llm/classifier.go:158-160`:

```go
retryAttempts := c.config.Classification.SummaryRetryAttempts
if retryAttempts == 0 {
    retryAttempts = 3
}
```

Zero is currently overloaded as "unset". Distinguishing the two —
either via pointer field or a sentinel like `-1` — lets the user opt
out without code surgery.

This decision is **moot if (2) lands**: the loop disappears entirely,
and `summary_retry_attempts` becomes dead config. Document the
deprecation in the config example. Keep this entry in the ADR so the
config-level history is captured.

### 4. Trim the system prompt

`defaultSystemPrompt` (`pkg/llm/classifier.go:68-116`) carries:

- 3 good summary examples (English, English, Russian)
- 5 bad examples
- 10 topic examples spanning every score band
- An IMPORTANT block restating "low-relevance articles MUST have
  topics"

Cut to one good example, one bad example, three topic examples
covering low/mid/high score bands. Drop the IMPORTANT block — the
schema-level rule already says topics are required for every article.

**Why this matters under OAuth.** The ChatGPT subscription weight
model penalises prompt size in addition to call count. The forbidden-
prefix mention in the prompt is now redundant with `cleanSummary`
running unconditionally (per (2)).

**Risk.** Examples drive style. Mitigation: capture the current
output for ~30 articles before the change; rerun against the same
articles after; review summaries side-by-side. Revert on regression.

### 5. Differential preference summary update

`PreferenceManager.UpdatePreferenceSummary`
(`pkg/scheduler/preference_manager.go:70-120`) fetches
`feedbackExamples = 50` and forwards all of them to
`Classifier.UpdatePreferenceSummary` even though the call is named
"Update" and is supposed to refine an *existing* summary that already
encodes prior feedbacks.

**Change.** On the update path, fetch only feedbacks recorded after
`last_summary_feedback_count` (the marker is already persisted at
line 134). Cap at 50 as a safety bound, but typical case is
≤25 (since the threshold gates the call at 25 new feedbacks).
`generateInitialPreferenceSummary` keeps the full-50 fetch.

**Impact.** Halves the input on every preference update, which fires
every 25 new feedbacks. Output unchanged in steady state — the
current summary already digests historical feedback.

**Risk.** Low. If some context is lost between summary generations,
the worst case is a slightly stale summary that gets refreshed on
the next cycle.

## Out of scope

- **Switching newscope's LLM endpoint to a paid OpenAI API key.**
  Cost vs. capacity tradeoff; if pursued, gets its own ADR. Most of
  the changes here remain useful in that world too (entity merging,
  retry-loop deletion).
- **Truncating `article.Content` before classification.** Discussed
  and rejected — `pkg/content/extractor.go:182-184` only enforces a
  *minimum* length; capping the maximum risks degrading summary
  quality on long-form articles. Quality is the explicit constraint
  for this ADR.
- **Switching to a smaller model.** Same cost-decision territory.
- **Embeddings batching (`pkg/scheduler/embedder.go:30-32`).**
  Embeddings go through litellm to a Gemini endpoint, separate quota
  lane from the OAuth chat path. Worthwhile but not on this critical
  path.
- **Prompt caching (OpenAI auto-cache for ≥1024-token prefixes).**
  Only applies on the direct API path. The OAuth path doesn't expose
  the cached-tokens accounting and isn't billed per token anyway.

## Migration / rollout

Each numbered decision is its own PR. Suggested order:

1. **(2) Delete retry loops.** Simplest diff, largest error-path
   relief. Tests: drop the retry-loop tests in
   `classifier_test.go` / `merger_test.go`; assert `cleanSummary`
   is invoked on every response containing a forbidden prefix.
2. **(5) Differential preference update.** Small, isolated change in
   `preference_manager.go`. Test: feed history ≥50 entries, advance
   counter, verify only post-counter entries reach the classifier.
3. **(4) Trim system prompt.** A/B sample 30 articles pre-merge;
   compare summaries; merge if no regression.
4. **(1) Fold entity extraction.** Largest blast radius — touches
   classifier schema, scheduler wiring, repository persistence,
   moq regeneration. Land last so it can build on the trimmed
   classifier prompt.
5. **(3) Config semantics.** Becomes deprecation-doc-only after (2).

Rollback is per-PR revert. The infra-side throttle from PR #115
remains the safety net throughout — if any of these changes
regress quality, the schedule is already conservative enough to
absorb a temporary revert without busting the cap.
