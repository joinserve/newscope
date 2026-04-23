# ADR 0012: Topic-chip inline navigation

- Status: Proposed
- Date: 2026-04-24
- Deciders: caspar

## Context

Three UX itches surfaced after the beats UI landed (ADR 0011):

1. The `username › topic` line exists on `article-card.html` but not on
   `beat-card.html`, so the "which topic does this card belong to" signal
   is missing from the beats inbox.
2. Clicking the existing topic link does an HTMX in-place swap and does
   NOT push the URL, so the filtered view can't be shared or bookmarked
   and the browser's back button does nothing useful.
3. There is no visual hint for which tags are "big" — a tag appearing on
   5+ articles looks identical to a one-off tag. Users have to read tag
   text to find the ones worth drilling into.

Separately, `beat-card.html`'s feed-name is a `<span>`, not an `<a>`. This
is a regression against earlier design where clicking the feed-name
navigated to `/source/{name}`.

The decisions below apply to `server/templates/article-card.html`,
`server/templates/beat-card.html`, `server/templates/beat-detail.html`,
the `articlesHandler` / `sourceHandler` topic-filter paths, and a new
tag-frequency helper on the repository layer.

## Decisions

### 1. "Big tag" = global threshold of ≥5 articles

A tag is "big" iff at least 5 articles across the whole corpus carry it.
Computed by aggregating `classifications.topics` and cached in memory with
a short TTL (recompute on every-few-minute interval — exact cadence at
implementation time).

**Rejected alternative:** dynamic top-K per result set. It would give
single-source pages a non-empty list of chips automatically, but the same
tag would render differently across pages, breaking the "#ai is a big
thing" mental model.

### 2. Big tags render as `#tag` chips with a blue background; non-big tags keep the plain link style

The `#` prefix and coloured background are the visual separator, not a
section header — ADR 0012 explicitly does not introduce per-tag page
sections (see Decision 3 below). Non-big tags stay as plain underlined
links so single-source pages still show something clickable when no big
tag is present.

**Rejected alternative:** per-tag unique colours. GitHub-style colours
require a palette and per-tag assignment logic. Single accent colour is
one CSS rule and reads consistently.

**Rejected alternative:** section headers / grouped rendering. Infinite
scroll + group headers conflict: either groups repeat per batch (visually
noisy) or the scroll pattern has to be replaced. User chose chip-only
visual distinction so infinite scroll stays intact.

### 3. Clicking any topic chip navigates via URL push, not in-place HTMX swap

The existing `hx-get="/articles"` + `hx-target="#articles-with-pagination"`
is kept but gains `hx-push-url="true"`, so the filtered view is
shareable, bookmarkable, and browser-back works. Same pattern already
used by `beats.html`'s search form.

**Rejected alternative:** plain `<a href="/articles?topic=...">`. Loses
the no-flash HTMX in-place swap. `hx-push-url` gives us both.

### 4. `beat-card.html` gains the same `> #topic` line

Primary topic for a beat = the topic most frequently occurring across its
members' `classifications.topics`. Ties broken by first-encountered.
Rendered identically to article-card (plain or chip, same rules).

Computed in the repository layer when loading a beat's members so the
template doesn't carry business logic.

**Rejected alternative:** re-derive primary topic from
`canonical_summary` via LLM. Extra LLM cost for a secondary display
element. Member-vote is free and good-enough.

### 5. `beat-card.html` feed-name becomes clickable for single-member beats only

Single-member beats render `<a href="/source/{name}">`. Multi-member
beats keep `<span>3 sources</span>` — the text is a count, not a name,
so there is no single target to link to. Beat detail page
(`/beats/{id}`) remains the drill-in for multi-member beats.

### 6. Scope: items inbox (`/articles`, `/source/{name}`) and beats inbox

All three list pages get the chip (big-tag highlight + `#` prefix + URL
push) and clickable feed-name. Beat detail page reuses the beat-card
chip. Global search (`/search`) is out of scope for this ADR — its
template reuses `article-card.html` so it will inherit Decisions 2 and 3
automatically, which is fine.

### 7. Grouping auto-off when a topic filter is active

If the user has an explicit topic filter applied (via URL `?topic=ai` or
the existing filter dropdown), chips on the resulting page still render
with the same "big-tag highlight" rule, but clicking a big tag that IS
the active filter becomes a "clear filter" action (navigates to the same
page without the `?topic=` param). Unfiltered pages keep current
behaviour.

## Consequences

**Good:**
- Beats and articles inboxes share one header shape (feed-name › topic →
  time → score).
- Big-tag chips give one-glance orientation without forcing section
  headers or breaking infinite scroll.
- URL push makes filtered views shareable — users can paste
  `/articles?topic=ai` to peers.
- Feed-name regression on beat-card fixed as a by-catch.

**Cost:**
- New "big tag" computation runs on every list render unless cached. TTL
  cache is cheap but adds one more piece of state.
- Beat member loading now computes a primary topic; adds one pass over
  each member's topic list. Negligible vs. LLM/embedding cost.
- `/articles`, `/source/{name}`, and `/beats` all depend on the
  big-tag-set data. Any future schema change to topics must be tested in
  three render paths.

**Risks:**
- The ≥5 threshold is arbitrary. If the corpus grows, "big" will drift —
  every tag will be big, and the chip stops communicating anything. Plan
  to revisit when big-tag count > ~10-15% of distinct tags (subjective,
  on self).
- `hx-push-url` means back-button on a freshly-filtered list returns to
  pre-filter state, which is what users expect — but existing deep links
  into filtered views will now preserve state on refresh, changing the
  "refresh clears filter" behaviour if users depended on it.

## Related

- ADR 0011 (beats UI) for the card layout and action-toolbar baseline.
- ADR 0010 for the beats backend model.
