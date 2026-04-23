# ADR 0011: Beats inbox UI design

- Status: Accepted
- Date: 2026-04-23
- Deciders: caspar
- Supersedes (partial): ADR 0010's "Primary UX target: since-last-visit"
  section; see ADR 0010 for the backend model.

## Context

ADR 0010 committed to a beats inbox and implied the user-visible win
would be an unread badge ("N new since last visit"). The UI PR (PR 5)
went through three implementer iterations before a stable design emerged.
The final shape disagrees with ADR 0010's UX paragraph on several points
and introduces patterns that are not self-evident from the code, so they
need to be written down.

The decisions below apply to `server/templates/beat-card.html`,
`server/templates/beat-detail.html`, and the handlers behind
`/beats`, `/beats/{id}`, `/api/v1/beats/{id}/feedback`,
`/api/v1/beats/{id}/members`, and `/api/v1/beats/search`.

## Decisions

### 1. Action toolbar mirrors `article-card` exactly

Every beat card and the detail page carry the same five-icon toolbar used
by `article-card.html`: **like / comment / share / dislike / source**. Same
SVGs, same CSS classes, same order. `beat-feedback.html` as a separate
partial was tried and deleted — rendering the whole card on feedback is
simpler than coordinating a partial update.

**Rejected alternative:** a thumbs-up / thumbs-down pair. It made the beat
toolbar visually distinct from the article toolbar, which broke the
threads-style consistency recorded in ADR 0007.

### 2. Merged-member count lives on the comment button, not as a badge

A `.action-badge` on the comment icon shows the member count when
`$membersCount > 1`. Singletons (single-member beats surfaced via the
fall-through in `ListBeats`) render without the badge so they look like
regular article cards.

**Rejected alternative:** ADR 0010's "N new since last visit" badge. The
items-inbox zero-unread model was already doing that job; re-adding a
second counter on beats duplicated the signal without adding information.

### 3. Clicking the comment button expands members SNS-thread-style

The comment button's `hx-get` loads `/api/v1/beats/{id}/members`, which
returns the beat's member articles rendered through `article-card.html`
and swaps them into `#beat-members-{id}` beneath the card. The expansion
is the primary way to drill into a beat.

**Planned follow-up (tracked separately):** replace the inline expand
with a slide-right navigation to `/beats/{id}` detail, using the
browser's View Transitions API for the animation. The detail page's
header carries the canonical title; members render below. Current
inline-expand is the stepping-stone, not the endpoint.

`beatMembersHandler` also calls `MarkViewed` as a side effect so
`last_viewed_at` stays current for future UIs.

### 4. Beat cards are not wholly clickable

Earlier iterations wrapped the card in `hx-get="/beats/{id}"` with
`cursor:pointer`, so any accidental click navigated away. This was
removed. Navigation happens only through the comment button (today) or
the slide-right detail (planned).

### 5. Beat detail page drops the avatar-stack

The top of `beat-detail.html` used to stack three member favicons. It
looked cramped and did not communicate more than the canonical title
already did. The stack is removed from the detail page. The card still
uses `.avatar-stack` because a tiny stack next to a title reads as "many
sources"; a large one next to a full detail page reads as "cluttered".

## Consequences

**Good:**
- Beat cards and article cards share one mental model.
- Singletons fall through transparently; users see the raw article, not a
  half-baked beat wrapper.
- Comment-button member count is a one-glance answer to "is this a
  bundle?".

**Cost:**
- `beatFeedbackHandler` re-renders the whole `beat-card.html` on each
  like/dislike instead of swapping just the heart. Acceptable — the card
  is small and rendering cost is dwarfed by the LLM calls upstream.
- `article-card.html` is now loaded into the beat-card render path, so
  any future changes to it must be tested in both inbox views.

**Risks:**
- "Single-member beat that looks like an article card" is a deliberate
  illusion: the fact that it's a beat is load-bearing only when a second
  member attaches. If the fall-through ever stops working, users will see
  a mysterious gap in the list. Guarded by
  `ListBeats_HidesUncanonicalisedMultiMember` + a future test for
  singleton visibility.
- Comment-button expansion hardcodes `#beat-members-{id}` on the card.
  If card nesting changes, the HTMX target must be revisited.

## Related

- ADR 0007 (threads-style UI) for the action-toolbar baseline.
- ADR 0010 for the backend model; this ADR supersedes its UX paragraph.
