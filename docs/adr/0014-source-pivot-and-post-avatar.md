# ADR 0014: Source-as-beats pivot, feed-card refresh, post-avatar overlay

- Status: Proposed
- Date: 2026-04-27
- Deciders: caspar

## Context

Five UX gaps surfaced together while using the beats-first inbox:

1. **Settings page** rows visually drift right — labels are not flush
   left, breaking the "value on the right, label on the left" rhythm of
   every other settings UI.
2. **Feed cards** in `/feeds` look out of place: text-only buttons in a
   classic two-column layout, while the rest of the app has migrated to
   Threads-style article cards (avatar + content + icon toolbar). The
   feed name is also plain text, even though `/source/{name}` exists.
3. **`/source/{name}`** still shows individual articles (unread / read
   sections). After the move to beats-first navigation (ADR 0011 +
   ADR 0010), clicking a username inside a beat takes the user out of
   beat-mode and into the legacy articles view. The page exists, the
   route is fine — the *content* is wrong.
4. **`avatar-stack`** in beat-card stacks up to 3 favicons + a `+N`
   overflow chip. With most beats covering 4–8 sources, the stack is
   visually noisy and the `+N` chip is an afterthought rather than the
   primary signal of "this story has many sources."
5. **RSSHub explorer** ("/feeds/rsshub", ADR 0009) makes the user pick
   a category before they can see any namespace. For users who already
   know the platform name (twitter, threads, mastodon, hacker-news),
   walking through Categories → Platforms is friction.

A sixth gap is related but bigger and isolated, so it gets its own
phase (see Decision 6 / Phase 2):

6. **Beat-card avatars are wrong for SNS feeds.** A Threads beat shows
   the manually-configured Threads platform icon — never the actual
   user's profile photo. RSSHub returns the user avatar in the channel
   `<image>` element, but newscope's parser drops it. The user wants
   the post-author avatar as the primary image, with the platform icon
   as a small overlay (think iMessage badge).

The decisions below split into two phases. **Phase 1 = decisions 1-5**
(no schema changes, no backfill). **Phase 2 = decision 6** — schema +
parser + render — gated on validation that RSSHub's channel-image
contract holds for the SNS routes the user cares about (Twitter,
Threads, Mastodon).

## Decisions

### 1. `/source/{name}` shows beats, not articles

The route stays. The handler swaps backend calls:

- **Before:** two `ListItems` calls (unread + read), `source.html`
  template renders article cards in two sections.
- **After:** single `ListBeats` call with a new `feedID` filter, page
  renders the beat-card list (same template fragment as `/beats`).

Rationale: in beats-first navigation, every other "list of stories"
view returns beats. `/source/{name}` is the only exception today, and
the inconsistency leaks (decision 2 below makes the leak worse by
adding more entry points). The legacy article view has no other
referrer worth keeping; we drop it rather than maintain a parallel
codepath.

`ListBeats` gains an optional `feedID *int64` filter — added as a
`WHERE EXISTS (SELECT 1 FROM items i WHERE i.beat_id = beats.id AND
i.feed_id = ?)`. The membership table is already in place
(`items.beat_id`), no schema change needed.

The page title becomes the feed display name (unchanged); the
back-button URL is preserved (`r.Referer()` fallback `/beats`).

### 2. Feed cards adopt the article-card visual

`feeds.html` + `feed-card.html` rewrite to mirror `article-card.html`:

- Avatar (left, 48px) — uses the feed's icon (and, after Phase 2, the
  auto-extracted image with overlay; Phase 1 just uses `IconURL`).
- Content (right): feed name as a clickable `<a href="/source/{name}">`
  (becomes beats-by-source after decision 1), URL as muted secondary
  text, status pill (enabled/disabled).
- **Icon toolbar** at the bottom: same shape as article-card actions
  (`role="toolbar"`, square-ish icon buttons with `<svg class="icon">`):
  - Edit (pencil) — toggles inline edit form (kept as-is)
  - Toggle status (power) — POSTs to existing endpoint
  - Fetch now (refresh) — POSTs to existing endpoint, shows spinner
  - Delete (trash) — confirm + DELETE

No new endpoints. Pure template + CSS work. The "edit form" stays
the existing inline expand; just the button becomes an icon.

Rationale: visual consistency with the rest of the app, and titles
becoming clickable wires up decision 1's entry point without any
extra UI.

### 3. RSSHub explorer gets a top-of-page autocomplete

A search input above the categories grid. As the user types, the
client fetches matching namespaces and renders an autocomplete
dropdown of up to 10 results. **No search-results page**: clicking a
result jumps straight into that namespace's routes view (existing
state-3 of `rsshub-explorer.html`).

Backend: existing `GET /api/v1/rsshub/namespaces?category=X` becomes
`GET /api/v1/rsshub/namespaces` with two optional params:

- `category` (existing): filter by category (current behavior)
- `q` (new): substring match on `name` and `key`, case-insensitive,
  matched in either field; if both `category` and `q` are set, both
  must match. Cap at 50 results server-side.

Frontend: 250ms debounce on the input, fetch `/api/v1/rsshub/namespaces?q=…`,
render `<ul class="rsshub-search-suggestions">` below the input,
keyboard navigation (↑/↓ + Enter) is nice-to-have but not required
for v1.

Rationale: typing "twitter" then Enter is the "I know what I want"
shortcut; the existing category drill-down stays for discovery.

### 4. Avatar-stack collapses to first + `+N`

Beat-card avatar logic becomes:

- 1 member: single 40px avatar (unchanged from today's single-member
  branch).
- ≥2 members: the first member's avatar at 40px, with a `+N` chip
  overlapping its bottom-right (where N = `len(Members) - 1`).

The chip is the primary "this is a multi-source story" signal — same
visual weight as the unread dot on a notification, not a tiny
afterthought. CSS is a single `.avatar-stack` rule, no nth-child
chain. `avatar-stack` keeps its name so we don't churn references
elsewhere.

Rationale: three overlapping favicons compete for attention without
conveying useful identity ("which three of the eight sources?"). One
clear avatar + a count is faster to parse and matches how iMessage /
Slack / Threads display group conversations.

### 5. Settings page row alignment

Bug, not architecture. `.setting-item-content` is `flex: 1` but no
explicit alignment, and the `gap: 1rem` between content and value
plus `.setting-value`'s `flex-shrink: 0` allows the label to drift
when the value is wide. Fix in CSS: ensure label aligns left
explicitly (`align-items: flex-start` on `.setting-item-content`,
verify the label has no inherited indentation). No template change.

### 6. Feed gains an auto-extracted `image_url`, beat-card overlays it (Phase 2)

#### Data model

- New column `feeds.image_url TEXT NOT NULL DEFAULT ''`
  - migrate via `migrateAddFeedImageURL` in `pkg/repository/repository.go`
    (mirror existing `migrateAddIconURL`, ADR 0007 era).
- `feeds.icon_url` (existing): unchanged, **manually set** by the
  user — the platform/brand icon (e.g. Threads logo).
- `feeds.image_url` (new): **auto-extracted** from the parsed RSS
  channel `<image><url>` (or Atom `<icon>`/`<logo>`); written by the
  fetch worker after each successful fetch. Never user-edited.

#### Parser

`pkg/feed/parser.go` extracts the channel image URL from the parsed
feed (gofeed exposes it as `feed.Image.URL`). The fetch worker passes
this to a new `UpdateFeedImageURL(ctx, id, url)` repository method,
called after the items are written. If the URL is empty or matches
the existing stored value, skip the write.

#### Render contract

`beat-card.html` avatar logic becomes:

```
post := first-member.feed.image_url
brand := first-member.feed.icon_url

if post != "" && post != brand:
    main = post (40px)
    badge = brand (16px overlay, bottom-right)
else:
    main = brand or post (whichever is non-empty)
    no badge
```

The badge is the size of the iMessage badge "but bigger" per the
user's request — target ~16-18px (vs the current 8px unread dot).

#### Validation gate (do this before writing code)

Before merging Phase 2, verify with curl that the channel `<image>`
field is populated for the SNS sources the user actually uses:

- ✅ Threads (`/threads/zuck`) — confirmed populated (this ADR's research)
- ⚠️ Twitter (`/twitter/user/elonmusk`) — local RSSHub doesn't have
  Twitter API configured; verify against a working RSSHub instance
- ⚠️ Mastodon, BlueSky, etc. — verify

If a source returns empty channel image, Phase 2 still ships (no
regression — beat-card falls back to `icon_url` exactly like today),
but the user should know which platforms won't get the new behavior.

## Out of scope

- Per-item author avatars (e.g. multi-author blog feeds). RSS doesn't
  carry this and the user's request maps to the SNS one-feed-one-user
  model.
- Caching/CDN for the auto-extracted images (we link directly to the
  source URL — same as `icon_url` today; if it breaks, the existing
  `onerror` fallback in templates kicks in).
- Backfilling `image_url` for existing feeds — they fill in on the
  next scheduled fetch. A one-shot CLI is cheap to add later if needed.
- Replacing the avatar-stack on `/beats` *list* AND beat-detail at
  the same time. Decision 4 covers the list view; beat-detail's
  member rendering is unchanged.

## Migration / rollout

- Phases 1 and 2 ship as separate PRs in the same branch sequence.
- Phase 1 has no DB changes — pure template/CSS/handler work, ships
  whenever ready.
- Phase 2 has one additive column + one parser change + one render
  change. The migration is idempotent (`ALTER TABLE … ADD COLUMN IF
  NOT EXISTS`). Rollback is "revert the PR" — `image_url` stays
  populated but unused.
