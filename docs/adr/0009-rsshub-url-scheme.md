# ADR 0009: rsshub:// URL scheme for RSSHub feeds

- Status: Accepted
- Date: 2026-04-21
- Deciders: caspar

## Context

RSSHub generates RSS feeds for hundreds of platforms via parameterized routes (e.g.,
`/github/trending/daily`). Users want to subscribe to these routes without knowing
or hardcoding the RSSHub host URL, which differs between self-hosted instances and
the public demo.

Two options:
1. Store the full HTTP URL and require users to know the host at subscription time.
2. Introduce an abstraction that decouples the route from the host.

## Decision

Add a `rsshub://` URL scheme. Feed URLs stored as `rsshub://<path>` are rewritten
to `http://{config.rsshub.host}/<path>` by the parser at fetch time. The host is
configured once in `RSSHubConfig.Host`.

A server-side explorer (`/rsshub`) lets users browse RSSHub's category → namespace → route
tree, fill route parameters, and preview the feed before saving — all without
leaving the app. The explorer proxies the RSSHub radar API and calls
`scheduler.ParseFeed` for live previews.

Routes requiring parameters encode them as URL path segments; the explorer
surfaces `routeDef.parameters[name]` hints and an example URL for each route.

## Consequences

**Good:**
- Feed subscriptions are host-agnostic; moving to a different RSSHub instance
  requires changing only `rsshub.host` in config, not editing every feed URL.
- The explorer reduces the friction of discovering and adding RSSHub routes.
- Preview uses the real parse path, so users see actual errors before committing.

**Constraints:**
- `rsshub://` is a private scheme with no standard semantics; URLs are meaningless
  outside Newscope. If a feed is exported and imported into another reader, the
  scheme will fail.
- If `rsshub.host` is empty, all `rsshub://` feeds fail with a config-unavailable
  error — there is no graceful fallback.
- Explorer routes are proxied server-side; Newscope must be network-reachable to
  the RSSHub instance (not just the client browser).

## Deploy dependency

Requires `rsshub.host` set in the deployment config (Helm values or `config.yml`).
Without it, existing `rsshub://` feed subscriptions silently fail.
