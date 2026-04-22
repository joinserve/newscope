# ADR 0002: HTMX over SPA framework

- Status: Accepted
- Date: 2026-04-19
- Deciders: caspar

## Context

The UI needs interactive article cards, filter toggles, swipe gestures, and partial
page updates (like/dislike without full reload). Options were:

1. React/Vue SPA with a JSON API backend
2. Server-rendered HTML with HTMX for partial updates
3. Plain HTML forms with full-page reloads

The team is Go-first; maintaining a separate JS build pipeline adds friction for
a single-developer project.

## Decision

Use HTMX v2 for all dynamic interactions. Templates are rendered server-side via
`html/template`; HTMX attributes on elements trigger targeted HTTP calls that swap
partial HTML into the DOM. Vanilla JS is added only where HTMX cannot express the
interaction (swipe detection, Web Share API, keyboard shortcuts).

## Consequences

**Good:**
- No build step, no npm, no bundler.
- Server remains the single source of truth for state; no client/server sync bugs.
- Templates are readable HTML; new contributors don't need frontend tooling expertise.

**Constraints:**
- Offline or offline-first patterns are not practical.
- Complex client-side state (e.g., optimistic updates across multiple components)
  requires careful HTMX attribute composition.
- View-mode state is communicated via HTTP headers (`X-View-Mode`), which is
  unconventional and requires documentation.
