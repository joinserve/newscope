package domain

import "time"

// Feed represents a news feed source
type Feed struct {
	ID            int64
	URL           string
	Title         string
	Description   string
	IconURL       string
	LastFetched   *time.Time
	NextFetch     *time.Time
	FetchInterval time.Duration
	ErrorCount    int
	LastError     string
	Enabled       bool
	CreatedAt     time.Time
}

// FeedWithStats wraps Feed with rolling-window publishing stats used by the
// /feeds page to show how often each source actually publishes content
// (distinct from FetchInterval, which is how often we poll).
type FeedWithStats struct {
	Feed
	ItemCount30d int
}
