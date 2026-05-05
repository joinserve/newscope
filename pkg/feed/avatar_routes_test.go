package feed

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestChannelImageIsUserAvatar(t *testing.T) {
	tests := []struct {
		name string
		url  string
		want bool
	}{
		// threads single-user (channel image = user avatar)
		{"threads single-user via rsshub scheme", "rsshub://threads/zuck", true},
		{"threads single-user via resolved http", "http://localhost:1200/threads/zuck", true},
		{"threads single-user with trailing slash", "rsshub://threads/zuck/", true},

		// threads multi-user — channel image is not a single user's avatar
		{"threads search excluded", "rsshub://threads/search/AI%20Gemini/filter%3Drecent", false},
		{"threads tag excluded", "rsshub://threads/tag/ai", false},
		{"threads topic excluded", "rsshub://threads/topic/something", false},

		// twitter
		{"twitter user route", "rsshub://twitter/user/elonmusk", true},
		{"twitter user route via http", "http://localhost:1200/twitter/user/elonmusk", true},
		{"twitter keyword excluded", "rsshub://twitter/keyword/AI", false},
		{"twitter list excluded", "rsshub://twitter/list/12345", false},
		{"twitter bare second segment is not 'user'", "rsshub://twitter/elonmusk", false},

		// instagram
		{"instagram user route", "rsshub://instagram/user/zuck", true},
		{"instagram tag excluded", "rsshub://instagram/tag/coffee", false},

		// facebook (page + group are both single-source)
		{"facebook page", "rsshub://facebook/page/somepage", true},
		{"facebook group", "rsshub://facebook/group/somegroup", true},
		{"facebook unknown sub-namespace", "rsshub://facebook/profile/something", false},

		// non-whitelisted namespaces — even SNS-shaped URLs are out by design
		{"bluesky profile (not yet whitelisted)", "rsshub://bsky/profile/handle.bsky.social", false},
		{"mastodon timeline", "rsshub://mastodon/timeline/Gargron@mastodon.social", false},

		// news / non-SNS rsshub routes — must be false
		{"cna section", "rsshub://cna/aipl", false},
		{"cna topic", "rsshub://cna/4469", false},

		// regular RSS URLs — never whitelisted
		{"hacker news regular RSS", "https://hnrss.org/newest?points=100", false},
		{"plain example feed", "https://example.com/feed.xml", false},

		// malformed / edge cases — must be false, not panic
		{"empty string", "", false},
		{"only scheme", "rsshub://", false},
		{"single segment after scheme", "rsshub://threads", false},
		{"empty second segment", "rsshub://threads/", false},
		{"http with no path", "http://localhost:1200", false},
		{"http with only one segment", "http://localhost:1200/threads", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := ChannelImageIsUserAvatar(tc.url)
			assert.Equal(t, tc.want, got, "url=%q", tc.url)
		})
	}
}
