# ADR 0004: go-trafilatura for content extraction

- Status: Accepted
- Date: 2026-04-19
- Deciders: caspar

## Context

RSS items typically contain only a title, teaser, or truncated body. To classify
articles accurately the LLM needs the full article text. Options:

1. **go-trafilatura** — Go port of the Python trafilatura library; boilerplate removal + main-content extraction.
2. **Readability.js via JS runtime** — accurate but requires embedding a JS engine.
3. **Diffbot / Mercury / other paid API** — accurate but adds external dependency and cost.
4. **Raw HTML passed to LLM** — high token cost; noisy.

## Decision

Use `go-trafilatura` to extract both plain text and a sanitized HTML version from the
article's canonical URL. The `HTTPExtractor` downloads the page, runs trafilatura, and
returns `(text, richHTML)`. The classifier uses the plain text; the UI may use the
rich HTML for rendering.

## Consequences

**Good:**
- Pure Go; no external service, no extra runtime.
- Extraction happens inline in the processing pipeline — no additional queue or worker.
- Returns both plain text (for LLM) and sanitized HTML (for display).

**Constraints:**
- Quality varies by site. Heavy JS-rendered pages (SPA, paywalls) return sparse or
  empty content.
- No retry or fallback when extraction yields empty text; the classifier currently
  receives an empty content field and scores based on title only.
- A future improvement would be to detect empty extraction and skip LLM cost, or
  to fall back to the RSS item body.
