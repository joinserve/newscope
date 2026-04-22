# ADR 0005: Optional LLM streaming via config flag

- Status: Accepted
- Date: 2026-04-19
- Deciders: caspar

## Context

The LLM classifier makes batch API calls that can take several seconds. Two modes
are possible: wait for the full response, or use the streaming chat completion API
and process tokens as they arrive.

Streaming reduces perceived latency but adds code complexity: the response must be
assembled from a stream of delta tokens rather than parsed from a single JSON body.

## Decision

Expose a `use_streaming` boolean in `LLMConfig`. When `true`, the classifier uses
`CreateChatCompletionStream`; when `false` (default), it uses `CreateChatCompletion`.
Both paths produce the same structured output.

## Consequences

**Good:**
- Operators can trade simplicity for latency based on their model/API endpoint.
- The non-streaming path is simpler to debug (single response, easy to log).

**Constraints:**
- Two code paths must be maintained; bugs in one may not surface in the other.
- Streaming offers no benefit for batch classification where all results are needed
  before any item is written — the gain is only in response assembly latency, not
  throughput.
- If the project moves to structured outputs (JSON mode), streaming support may
  need revisiting as partial JSON is harder to validate.
