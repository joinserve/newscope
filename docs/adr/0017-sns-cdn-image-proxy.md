# ADR 0017: Server-side image proxy for SNS CDN avatars

- Status: Proposed
- Date: 2026-05-03
- Deciders: caspar

## Context

ADR 0014 Phase 2 (PR #50) wires up per-item author avatars from
RSSHub SNS routes (Threads, Instagram, Twitter, Bluesky, Facebook).
The data path works end-to-end: RSSHub emits `<media:thumbnail>` with
the author's profile-pic URL, the parser stores it on
`items.image_url`, the SELECTs propagate it to
`ClassifiedItem.AuthorImageURL`, and the beat-card template renders
`<img src="{instagram-cdn-url}">` with the brand favicon as a
bottom-right badge.

In practice every SNS avatar falls back to the letter avatar.
Probing Instagram's CDN response:

```
HTTP/2 200
content-type: image/jpeg
cross-origin-resource-policy: same-origin
```

The `Cross-Origin-Resource-Policy: same-origin` header (CORP, Fetch
spec) is enforced by the browser before CORS. There is no client-side
attribute (`crossorigin`, `referrerpolicy`, `importance`) that
bypasses it — the bytes arrive but the browser refuses to hand them
to the `<img>` element, returning
`ERR_BLOCKED_BY_RESPONSE.NotSameOrigin` in DevTools. PR #50's commit
`425b6a4` (`referrerpolicy=no-referrer`) was shipped on a
misdiagnosis; it stays as harmless and is not what unblocks
rendering.

Instagram's CDN setting CORP `same-origin` is intentional —
preventing their CDN content from being embedded on third-party sites
is a deliberate part of their security posture. Twitter, Facebook,
and similar SNS CDNs follow the same pattern.

The standard solution, used by Mastodon instances, Discord, and every
project that surfaces SNS imagery on a third-party origin, is a
server-side image proxy: the server fetches the image, strips the
CORP header, and re-serves it from the same origin as the page.

## Decisions

### 1. New endpoint: `GET /api/v1/img-proxy?url=<encoded>`

The `url` query parameter is a fully-qualified `https://` URL. The
handler validates the URL against a hardcoded allowlist (decision 2),
fetches it server-side, **strips** `Cross-Origin-Resource-Policy`,
`Cross-Origin-Embedder-Policy`, and `Cross-Origin-Opener-Policy`
from the upstream response, and streams the body to the client with:

- `Content-Type` copied from upstream (must be `image/*`)
- `Cache-Control: public, max-age=86400` (one-day browser cache)
- The original status code on success (200), `502 Bad Gateway` on
  any upstream failure or non-image content type

Response body for failures is empty — the template's existing
`onerror` falls back to the letter avatar exactly as it does today
when CORP blocks the direct fetch.

### 2. Hardcoded host allowlist (suffix match)

The handler maintains a hardcoded allowlist of host suffixes the
proxy is willing to fetch:

```
.cdninstagram.com
.fbcdn.net
.twimg.com
.bsky.social
.bsky.app
```

Plus future SNS CDNs added via code change as new platforms
integrate. **Adding a host requires a code change**; allowlists are
not config-driven. Rationale: integrating a new SNS source already
requires code changes (RSSHub route handling, parser tuning,
sometimes the `ChannelImageIsUserAvatar` whitelist from PR #50); the
proxy allowlist belongs in the same change. Config-driven allowlists
widen the SSRF surface for marginal convenience.

The match is **suffix-based on the resolved hostname**, not regex.
Suffix-match means `evil.cdninstagram.com.attacker.tld` does not
match `.cdninstagram.com`.

### 3. SSRF defenses

Hostname allowlist is necessary but not sufficient. After DNS
resolution, the resolved IP must also pass:

- not `IsLoopback()`
- not `IsPrivate()` (RFC 1918 + similar)
- not `IsLinkLocalUnicast()` / `IsLinkLocalMulticast()`
- not `169.254.169.254` (AWS / GCP cloud metadata)
- not IPv6 link-local (`fe80::/10`)
- not unspecified (`0.0.0.0`, `::`)

Implementation: resolve via `net.LookupIP`, iterate all returned IPs,
reject the request if **any** IP fails the test. Use a custom
`http.Transport` with a `DialContext` that re-checks the actual IP
the connection is going to (defends against DNS rebinding mid-fetch).

A 10-second per-request timeout caps slowloris-style abuse. A 10 MB
read cap on the response body caps a malicious upstream from filling
memory; abort the stream once the cap is hit.

### 4. No server-side cache (browser cache only)

The proxy does not maintain a local LRU/disk cache.
`Cache-Control: public, max-age=86400` is the only caching layer.
Reasoning:

- Personal-scale deployment makes server-side cache premature
  optimization; browser cache covers ~95 % of repeat hits
- Adds complexity (LRU sizing, eviction, persistence across pod
  restarts, cache-key normalization)
- Easy to add in a follow-up ADR if `/api/v1/img-proxy` shows up as
  a hot path in metrics

### 5. Template change is a one-liner

`server/templates/beat-card.html` (the line introduced by PR #50):

```diff
- <img src="{{.AuthorImageURL}}" ...>
+ <img src="/api/v1/img-proxy?url={{urlquery .AuthorImageURL}}" ...>
```

The `urlquery` template function is `template.URLQueryEscaper` from
the standard funcMap — already available, no registration needed.

### 6. Scope: author avatar only

This proxy is scoped to the per-item author avatar pipeline
introduced in PR #50. It does **not** proxy:

- `extractImage`-derived hero images in article-card / beat-card
  content (existing code, unrelated CORP behavior, separate decision)
- Feed `icon_url` (manually-set, typically pointing at favicon hosts
  that don't set CORP)
- Embedded images inside RSS item descriptions (rendered through
  bluemonday `safeHTML`; CORP enforcement happens per-image, but
  bundling these into the proxy doubles the SSRF surface for
  marginal benefit)

If those surfaces hit the same CORP issue, a separate ADR extends
the proxy scope.

## Out of scope

- **Authentication / per-user limits.** newscope is single-tenant;
  the proxy is rate-limited only by the host's bandwidth.
- **Image transformations** (resize, format conversion). The proxy
  is a pass-through.
- **Caching at the CDN layer** (Cloudflare etc.). Browser cache is
  the only assumed layer.
- **Replacing the letter-avatar `onerror` fallback.** Out of scope;
  the existing chain works correctly when the proxy returns 502.

## Migration / rollout

1. This ADR (PR, doc only).
2. Implementation PR: handler + allowlist + SSRF guard + tests.
   No template change in this PR.
3. After (2) merges and deploys, update `beat-card.html`'s
   `<img src=…>` to point at `/api/v1/img-proxy?url=…`. This can
   either land as a final commit on PR #50's branch, or as a small
   standalone PR after PR #50 merges.
4. Verify in production: open a beat-detail page with a Threads
   member; the avatar renders.

Rollback: revert the template change. Direct CDN URL returns; users
see letter fallbacks again. The proxy endpoint is harmless to leave
in place even if unused.
