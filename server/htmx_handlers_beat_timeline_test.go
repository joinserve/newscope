package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/umputun/newscope/pkg/config"
	"github.com/umputun/newscope/pkg/domain"
	"github.com/umputun/newscope/server/mocks"
)

func TestBuildBeatTimeline_Segmentation(t *testing.T) {
	// three revisions at known times
	t0 := time.Date(2024, 1, 1, 10, 0, 0, 0, time.UTC)
	t1 := time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC)
	t2 := time.Date(2024, 1, 1, 14, 0, 0, 0, time.UTC)

	revisions := []domain.TitleRevision{
		{ID: 1, BeatID: 1, Title: "Rev1", GeneratedAt: t0},
		{ID: 2, BeatID: 1, Title: "Rev2", GeneratedAt: t1},
		{ID: 3, BeatID: 1, Title: "Rev3", GeneratedAt: t2},
	}

	makeItem := func(addedAt time.Time, title string) domain.ClassifiedItem {
		return domain.ClassifiedItem{
			Item:    &domain.Item{Title: title},
			AddedAt: addedAt,
		}
	}

	// bucketing rules:
	//   rev1 segment (i=0): m1 (before rev1, pre-first special case) + m2 + m5 (in [rev1, rev2))
	//   rev2 segment (i=1): m3 (in [rev2, rev3))
	//   rev3 segment (i=2): m4 (in [rev3, ∞))
	members := []domain.ClassifiedItem{
		makeItem(t0.Add(-1*time.Hour), "m1-before-rev1"),    // pre-first → rev1 segment
		makeItem(t0.Add(1*time.Hour), "m2-in-r1-window"),    // [rev1, rev2) → rev1 segment
		makeItem(t1.Add(1*time.Hour), "m3-in-r2-window"),    // [rev2, rev3) → rev2 segment
		makeItem(t2.Add(1*time.Hour), "m4-after-rev3"),      // [rev3, ∞) → rev3 segment
		makeItem(t0.Add(30*time.Minute), "m5-in-r1-window"), // [rev1, rev2) → rev1 segment
	}

	timeline := buildBeatTimeline(revisions, members)

	require.Len(t, timeline.Segments, 3, "three revisions must produce three segments")

	// newest first after reversal
	assert.Equal(t, "Rev3", timeline.Segments[0].Revision.Title, "first segment is newest")
	assert.Equal(t, "Rev2", timeline.Segments[1].Revision.Title)
	assert.Equal(t, "Rev1", timeline.Segments[2].Revision.Title, "last segment is oldest")

	assert.True(t, timeline.Segments[0].IsCurrent, "first (newest) segment must be current")
	assert.False(t, timeline.Segments[1].IsCurrent)
	assert.False(t, timeline.Segments[2].IsCurrent)

	// rev3 segment: m4 only (1)
	// rev2 segment: m3 only (1)
	// rev1 segment: m1 (pre-first) + m2 + m5 (in window) = 3
	assert.Len(t, timeline.Segments[0].Members, 1, "rev3 (current) segment: m4 only")
	assert.Len(t, timeline.Segments[1].Members, 1, "rev2 segment: m3 only")
	assert.Len(t, timeline.Segments[2].Members, 3, "rev1 segment: m1+m2+m5")
}

func TestBuildBeatTimeline_MemberBeforeFirstRevision(t *testing.T) {
	rev := time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC)

	revisions := []domain.TitleRevision{
		{ID: 1, BeatID: 1, Title: "First", GeneratedAt: rev},
	}

	early := domain.ClassifiedItem{
		Item:    &domain.Item{Title: "early article"},
		AddedAt: rev.Add(-2 * time.Hour),
	}
	late := domain.ClassifiedItem{
		Item:    &domain.Item{Title: "late article"},
		AddedAt: rev.Add(1 * time.Hour),
	}

	timeline := buildBeatTimeline(revisions, []domain.ClassifiedItem{early, late})

	require.Len(t, timeline.Segments, 1)
	seg := timeline.Segments[0]
	assert.True(t, seg.IsCurrent)

	titles := make([]string, len(seg.Members))
	for i, m := range seg.Members {
		titles[i] = m.Title
	}
	assert.Contains(t, titles, "early article", "member before first revision must land in that segment")
	assert.Contains(t, titles, "late article")
}

func TestBuildBeatTimeline_NoRevisions(t *testing.T) {
	members := []domain.ClassifiedItem{
		{Item: &domain.Item{Title: "A"}, AddedAt: time.Now()},
		{Item: &domain.Item{Title: "B"}, AddedAt: time.Now()},
	}

	timeline := buildBeatTimeline(nil, members)

	require.Len(t, timeline.Segments, 1, "no revisions yields a single catch-all segment")
	seg := timeline.Segments[0]
	assert.True(t, seg.IsCurrent, "catch-all segment is current")
	assert.Len(t, seg.Members, 2)
	assert.True(t, seg.Revision.GeneratedAt.IsZero(), "revision time is zero for no-revision segment")
}

func TestBuildBeatTimeline_NoRevisionsNoMembers(t *testing.T) {
	timeline := buildBeatTimeline(nil, nil)
	assert.Empty(t, timeline.Segments, "no revisions and no members yields empty timeline")
}

func TestBeatDetailHandler_TimelineRender(t *testing.T) {
	cfg := &mocks.ConfigProviderMock{
		GetServerConfigFunc: func() (string, time.Duration) { return ":8080", 30 * time.Second },
		GetFullConfigFunc: func() *config.Config {
			return &config.Config{
				Embedding: config.EmbeddingConfig{Provider: "test"},
				Server: struct {
					Listen   string        `yaml:"listen" json:"listen" jsonschema:"default=:8080,description=HTTP server listen address"`
					Timeout  time.Duration `yaml:"timeout" json:"timeout" jsonschema:"default=30s,description=HTTP server timeout"`
					PageSize int           `yaml:"page_size" json:"page_size" jsonschema:"default=50,minimum=1,description=Articles per page for pagination"`
					BaseURL  string        `yaml:"base_url" json:"base_url" jsonschema:"default=http://localhost:8080,description=Base URL for RSS feeds and external links"`
				}{PageSize: 50, BaseURL: "http://localhost"},
			}
		},
	}

	revTime := time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC)
	title := "Canonical Beat Title"

	database := &mocks.DatabaseMock{
		GetBeatFunc: func(ctx context.Context, beatID int64) (domain.BeatWithMembers, error) {
			return domain.BeatWithMembers{
				ID:             beatID,
				CanonicalTitle: &title,
				Members: []domain.ClassifiedItem{
					{Item: &domain.Item{Title: "Article 1", Link: "https://ex.com/1"}, FeedName: "Feed A", AddedAt: revTime.Add(1 * time.Hour)},
					{Item: &domain.Item{Title: "Article 2", Link: "https://ex.com/2"}, FeedName: "Feed B", AddedAt: revTime.Add(-1 * time.Hour)},
				},
			}, nil
		},
		MarkViewedFunc: func(ctx context.Context, beatID int64) error { return nil },
		ListTitleRevisionsFunc: func(ctx context.Context, beatID int64) ([]domain.TitleRevision, error) {
			return []domain.TitleRevision{
				{ID: 1, BeatID: beatID, Title: "Canonical Beat Title", Summary: "Summary text.", GeneratedAt: revTime},
			}, nil
		},
	}

	srv := testServer(t, cfg, database, &mocks.SchedulerMock{})
	req := httptest.NewRequest("GET", "/beats/42", http.NoBody)
	req.SetPathValue("id", "42")
	w := httptest.NewRecorder()

	srv.beatDetailHandler(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	body := w.Body.String()

	assert.Contains(t, body, "Timeline", "timeline heading must be present")
	assert.Contains(t, body, "current", "current badge or class must appear for the single revision segment")
	assert.Contains(t, body, "Article 1", "member articles must appear in the timeline")
	// expandable details/summary structure
	assert.Contains(t, body, "<details", "each member must be wrapped in a details element")
	assert.Contains(t, body, "<summary", "each member must have a summary row")
	assert.Contains(t, body, "expand-chevron", "chevron toggle must be present")
	// full article-card actions in the expanded area
	assert.Contains(t, body, "action-like", "like button must be in expanded card")
	assert.Contains(t, body, "action-dislike", "dislike button must be in expanded card")
	assert.Contains(t, body, "action-share", "share button must be in expanded card")
}
