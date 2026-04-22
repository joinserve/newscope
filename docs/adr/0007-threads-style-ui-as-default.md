# ADR 0007: Threads-style UI as default view

- Status: Accepted
- Date: 2026-04-19
- Deciders: caspar

## Context

The feed view had two modes: condensed list and expanded cards. Neither was optimized
for quick triage on mobile. The dominant mental model for feed consumption has shifted
toward single-column vertical scroll (Threads, Twitter) with gesture-based actions.

## Decision

Make a single-column threads-style layout the default view. Condensed and expanded
remain as explicit toggles. View mode is communicated server-side via the
`X-View-Mode` request header; an unknown or missing header defaults to `threads`.

Action model:
- Five-icon toolbar per card: like / comment placeholder / share / dislike / done.
- Mobile: swipe left = dislike, swipe right = done, double-tap = like.
- Desktop: keyboard shortcuts L / D / Space / S act on the card nearest the viewport center.

Share uses the Web Share API with clipboard fallback. Comment is a placeholder
reserved for a future AI chat reply feature.

## Consequences

**Good:**
- Mobile triage is faster; swipe gestures match user muscle memory from other apps.
- Keyboard shortcuts make desktop use practical without a mouse.
- Server controls layout decisions — no client state needed.

**Constraints:**
- Swipe handlers must ignore touches on interactive elements (links, buttons, form
  controls) to avoid conflicts; this is fragile to template changes that add new
  interactive elements inside cards.
- `X-View-Mode` is non-standard and must be documented; clients that don't set it
  (e.g., direct API consumers) silently get the threads layout.
- Comment button is a visible placeholder for an unimplemented feature — risk of
  user confusion until the feature ships.
