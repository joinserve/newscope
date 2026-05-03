package feed

import (
	"net/url"
	"strings"
)

// ChannelImageIsUserAvatar reports whether a feed's channel <image><url>
// can be trusted as the per-user avatar for items in that feed.
//
// RSSHub overloads the channel <image> field per route convention: for a
// single-user SNS route (e.g. /threads/zuck) it is the user's profile pic;
// for a news/topic route (e.g. /cna/4469) it is whatever the upstream feed
// happens to set — typically a topic photo or site logo, NOT an avatar.
// Showing those as user avatars on the beat-card is visually wrong, so the
// renderer gates the channel-image-as-avatar branch behind this whitelist.
//
// Whitelist scope is intentionally narrow: only RSSHub namespace+subpath
// patterns where the route's contract is "one user's posts." Multi-user
// routes within the same namespace (search, tag, list) intentionally fall
// through here and rely on per-item <media:thumbnail> to carry the avatar.
//
// Bluesky (`/bsky/profile/<handle>`) follows the same single-user pattern
// and is trivial to add as another case once it lands in the priority list.
func ChannelImageIsUserAvatar(feedURL string) bool {
	namespace, sub, ok := splitRSSHubPath(feedURL)
	if !ok {
		return false
	}
	switch namespace {
	case "threads":
		// /threads/<user>; the reserved second segments are NOT user handles
		switch sub {
		case "", "search", "tag", "topic":
			return false
		}
		return true
	case "twitter":
		// /twitter/user/<handle>; /twitter/keyword|list|... are multi-user
		return sub == "user"
	case "instagram":
		// /instagram/user/<handle>; /instagram/tag/... is multi-user
		return sub == "user"
	case "facebook":
		// /facebook/page/<page> and /facebook/group/<group> are single-source
		return sub == "page" || sub == "group"
	}
	return false
}

// splitRSSHubPath returns (namespace, secondSegment, ok) for both rsshub:// and
// http(s):// forms. The "namespace" is the first path segment, "secondSegment"
// is the second; ok is false when the URL has fewer than two segments or
// cannot be parsed.
func splitRSSHubPath(feedURL string) (namespace, sub string, ok bool) {
	var rest string
	if strings.HasPrefix(feedURL, RSSHubScheme) {
		rest = strings.TrimPrefix(feedURL, RSSHubScheme)
	} else {
		u, err := url.Parse(feedURL)
		if err != nil || u.Path == "" {
			return "", "", false
		}
		rest = strings.TrimPrefix(u.Path, "/")
	}
	parts := strings.SplitN(rest, "/", 3)
	if len(parts) < 2 || parts[0] == "" || parts[1] == "" {
		return "", "", false
	}
	return parts[0], parts[1], true
}
