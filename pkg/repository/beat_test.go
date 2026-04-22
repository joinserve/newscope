package repository

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/umputun/newscope/pkg/domain"
)

// beatTestSetup makes a feed and returns a helper that creates classified items
// with embeddings at a given published time.
func beatTestSetup(t *testing.T) (repos *Repositories, cleanup func(), makeItem func(pub time.Time, title string, vec []float32) int64) {
	t.Helper()
	repos, cleanup = setupTestDB(t)
	feed := createTestFeed(t, repos, "beat-test")
	ctx := context.Background()

	counter := 0
	makeItem = func(pub time.Time, title string, vec []float32) int64 {
		counter++
		item := &domain.Item{
			FeedID:    feed.ID,
			GUID:      title,
			Title:     title,
			Link:      "https://ex.com/" + title,
			Published: pub,
		}
		require.NoError(t, repos.Item.CreateItem(ctx, item))
		now := time.Now()
		require.NoError(t, repos.Item.UpdateItemProcessed(ctx, item.ID, nil,
			&domain.Classification{Score: 5, Explanation: "", Topics: nil, Summary: "", ClassifiedAt: now}))
		require.NoError(t, repos.Embedding.PutEmbedding(ctx, item.ID, "test-model", vec))
		return item.ID
	}
	return repos, cleanup, makeItem
}

func TestBeatRepository_AttachOrSeed_EmptyCreatesSeed(t *testing.T) {
	repos, cleanup, mkItem := beatTestSetup(t)
	defer cleanup()

	ctx := context.Background()
	pub := time.Now()
	id := mkItem(pub, "a", []float32{1, 0, 0})

	vec := []float32{1, 0, 0}
	beatID, err := repos.Beat.AttachOrSeed(ctx,
		domain.BeatCandidate{ItemID: id, Vector: vec, PublishedAt: pub}, 0.85, 48*time.Hour, 20)
	require.NoError(t, err)
	require.NotZero(t, beatID)

	var n int
	require.NoError(t, repos.DB.GetContext(ctx, &n, `SELECT COUNT(*) FROM beats`))
	assert.Equal(t, 1, n)
	require.NoError(t, repos.DB.GetContext(ctx, &n, `SELECT COUNT(*) FROM beat_members WHERE beat_id = ?`, beatID))
	assert.Equal(t, 1, n)
}

func TestBeatRepository_AttachOrSeed_MatchAboveThresholdAttaches(t *testing.T) {
	repos, cleanup, mkItem := beatTestSetup(t)
	defer cleanup()

	ctx := context.Background()
	pub := time.Now()
	id1 := mkItem(pub, "a", []float32{1, 0, 0})
	id2 := mkItem(pub.Add(time.Hour), "b", []float32{0.99, 0.1, 0}) // cosine ~0.995

	beat1, err := repos.Beat.AttachOrSeed(ctx,
		domain.BeatCandidate{ItemID: id1, Vector: []float32{1, 0, 0}, PublishedAt: pub}, 0.85, 48*time.Hour, 20)
	require.NoError(t, err)

	beat2, err := repos.Beat.AttachOrSeed(ctx,
		domain.BeatCandidate{ItemID: id2, Vector: []float32{0.99, 0.1, 0}, PublishedAt: pub.Add(time.Hour)}, 0.85, 48*time.Hour, 20)
	require.NoError(t, err)

	assert.Equal(t, beat1, beat2, "similar items should share a beat")
}

func TestBeatRepository_AttachOrSeed_BelowThresholdCreatesNew(t *testing.T) {
	repos, cleanup, mkItem := beatTestSetup(t)
	defer cleanup()

	ctx := context.Background()
	pub := time.Now()
	id1 := mkItem(pub, "a", []float32{1, 0, 0})
	id2 := mkItem(pub.Add(time.Hour), "b", []float32{0, 1, 0}) // orthogonal — cosine 0

	beat1, err := repos.Beat.AttachOrSeed(ctx,
		domain.BeatCandidate{ItemID: id1, Vector: []float32{1, 0, 0}, PublishedAt: pub}, 0.85, 48*time.Hour, 20)
	require.NoError(t, err)

	beat2, err := repos.Beat.AttachOrSeed(ctx,
		domain.BeatCandidate{ItemID: id2, Vector: []float32{0, 1, 0}, PublishedAt: pub.Add(time.Hour)}, 0.85, 48*time.Hour, 20)
	require.NoError(t, err)

	assert.NotEqual(t, beat1, beat2, "dissimilar items should get separate beats")
}

func TestBeatRepository_AttachOrSeed_OutOfWindowCreatesNew(t *testing.T) {
	repos, cleanup, mkItem := beatTestSetup(t)
	defer cleanup()

	ctx := context.Background()
	pub := time.Now()
	id1 := mkItem(pub, "a", []float32{1, 0, 0})
	// identical vector but published 72h later — outside 48h window
	id2 := mkItem(pub.Add(72*time.Hour), "b", []float32{1, 0, 0})

	beat1, err := repos.Beat.AttachOrSeed(ctx,
		domain.BeatCandidate{ItemID: id1, Vector: []float32{1, 0, 0}, PublishedAt: pub}, 0.85, 48*time.Hour, 20)
	require.NoError(t, err)

	beat2, err := repos.Beat.AttachOrSeed(ctx,
		domain.BeatCandidate{ItemID: id2, Vector: []float32{1, 0, 0}, PublishedAt: pub.Add(72 * time.Hour)}, 0.85, 48*time.Hour, 20)
	require.NoError(t, err)

	assert.NotEqual(t, beat1, beat2, "identical vectors outside window should not merge")
}

func TestBeatRepository_AttachOrSeed_RespectsMaxMembers(t *testing.T) {
	repos, cleanup, mkItem := beatTestSetup(t)
	defer cleanup()

	ctx := context.Background()
	pub := time.Now()
	// identical vectors so all would match above any threshold
	a := mkItem(pub, "a", []float32{1, 0, 0})
	b := mkItem(pub.Add(time.Hour), "b", []float32{1, 0, 0})
	c := mkItem(pub.Add(2*time.Hour), "c", []float32{1, 0, 0})

	beatA, err := repos.Beat.AttachOrSeed(ctx,
		domain.BeatCandidate{ItemID: a, Vector: []float32{1, 0, 0}, PublishedAt: pub}, 0.85, 48*time.Hour, 2)
	require.NoError(t, err)
	beatB, err := repos.Beat.AttachOrSeed(ctx,
		domain.BeatCandidate{ItemID: b, Vector: []float32{1, 0, 0}, PublishedAt: pub.Add(time.Hour)}, 0.85, 48*time.Hour, 2)
	require.NoError(t, err)
	beatC, err := repos.Beat.AttachOrSeed(ctx,
		domain.BeatCandidate{ItemID: c, Vector: []float32{1, 0, 0}, PublishedAt: pub.Add(2 * time.Hour)}, 0.85, 48*time.Hour, 2)
	require.NoError(t, err)

	assert.Equal(t, beatA, beatB, "first two share the beat")
	assert.NotEqual(t, beatA, beatC, "third spills to a new beat once cap is hit")
}

func TestBeatRepository_AttachOrSeed_Idempotent(t *testing.T) {
	repos, cleanup, mkItem := beatTestSetup(t)
	defer cleanup()

	ctx := context.Background()
	pub := time.Now()
	id := mkItem(pub, "a", []float32{1, 0, 0})
	cand := domain.BeatCandidate{ItemID: id, Vector: []float32{1, 0, 0}, PublishedAt: pub}

	b1, err := repos.Beat.AttachOrSeed(ctx, cand, 0.85, 48*time.Hour, 20)
	require.NoError(t, err)
	b2, err := repos.Beat.AttachOrSeed(ctx, cand, 0.85, 48*time.Hour, 20)
	require.NoError(t, err)

	assert.Equal(t, b1, b2, "re-processing an already-assigned item returns the same beat")

	var n int
	require.NoError(t, repos.DB.GetContext(ctx, &n, `SELECT COUNT(*) FROM beat_members WHERE item_id = ?`, id))
	assert.Equal(t, 1, n, "no duplicate membership row")
}

func TestBeatRepository_NearestIn(t *testing.T) {
	repos, cleanup, mkItem := beatTestSetup(t)
	defer cleanup()

	ctx := context.Background()
	pub := time.Now()
	id := mkItem(pub, "a", []float32{1, 0, 0})
	beatID, err := repos.Beat.AttachOrSeed(ctx,
		domain.BeatCandidate{ItemID: id, Vector: []float32{1, 0, 0}, PublishedAt: pub}, 0.85, 48*time.Hour, 20)
	require.NoError(t, err)

	t.Run("match within window", func(t *testing.T) {
		b, sim, err := repos.Beat.NearestIn(ctx, []float32{1, 0, 0}, pub.Add(-24*time.Hour))
		require.NoError(t, err)
		assert.Equal(t, beatID, b)
		assert.InDelta(t, 1.0, sim, 1e-6)
	})
	t.Run("no candidates outside window", func(t *testing.T) {
		b, _, err := repos.Beat.NearestIn(ctx, []float32{1, 0, 0}, pub.Add(24*time.Hour))
		require.NoError(t, err)
		assert.Zero(t, b)
	})
}

func TestBeatRepository_MarkViewedAndUnreadCount(t *testing.T) {
	repos, cleanup, mkItem := beatTestSetup(t)
	defer cleanup()

	ctx := context.Background()
	pub := time.Now()
	id1 := mkItem(pub, "a", []float32{1, 0, 0})
	beatID, err := repos.Beat.AttachOrSeed(ctx,
		domain.BeatCandidate{ItemID: id1, Vector: []float32{1, 0, 0}, PublishedAt: pub}, 0.85, 48*time.Hour, 20)
	require.NoError(t, err)

	// before MarkViewed: whole membership is unread
	n, err := repos.Beat.UnreadMemberCount(ctx, beatID)
	require.NoError(t, err)
	assert.Equal(t, 1, n)

	require.NoError(t, repos.Beat.MarkViewed(ctx, beatID))
	// add a new member after viewing
	time.Sleep(10 * time.Millisecond) // ensure added_at > last_viewed_at
	id2 := mkItem(pub.Add(time.Hour), "b", []float32{1, 0, 0})
	_, err = repos.Beat.AttachOrSeed(ctx,
		domain.BeatCandidate{ItemID: id2, Vector: []float32{1, 0, 0}, PublishedAt: pub.Add(time.Hour)}, 0.85, 48*time.Hour, 20)
	require.NoError(t, err)

	n, err = repos.Beat.UnreadMemberCount(ctx, beatID)
	require.NoError(t, err)
	assert.Equal(t, 1, n, "only the post-view member is unread")
}

func TestBeatRepository_GetUnbeatItems(t *testing.T) {
	repos, cleanup, mkItem := beatTestSetup(t)
	defer cleanup()

	ctx := context.Background()
	pub := time.Now()
	id1 := mkItem(pub, "a", []float32{1, 0, 0})
	id2 := mkItem(pub.Add(time.Hour), "b", []float32{1, 0, 0})

	items, err := repos.Beat.GetUnbeatItems(ctx, 10)
	require.NoError(t, err)
	assert.Len(t, items, 2, "both items start unassigned")

	_, err = repos.Beat.AttachOrSeed(ctx,
		domain.BeatCandidate{ItemID: id1, Vector: []float32{1, 0, 0}, PublishedAt: pub}, 0.85, 48*time.Hour, 20)
	require.NoError(t, err)

	items, err = repos.Beat.GetUnbeatItems(ctx, 10)
	require.NoError(t, err)
	require.Len(t, items, 1)
	assert.Equal(t, id2, items[0].ItemID)
}

// Property: every classified+embedded item ends up in exactly one beat after a
// full sweep of the worker.
func TestBeatRepository_PropertyEveryItemInExactlyOneBeat(t *testing.T) {
	repos, cleanup, mkItem := beatTestSetup(t)
	defer cleanup()

	ctx := context.Background()
	base := time.Now()
	// a mix of similar and dissimilar vectors
	vectors := [][]float32{
		{1, 0, 0}, {0.99, 0.1, 0}, {0.98, 0.15, 0},
		{0, 1, 0}, {0.1, 0.99, 0},
		{0, 0, 1},
	}
	var ids []int64
	for i, v := range vectors {
		ids = append(ids, mkItem(base.Add(time.Duration(i)*time.Hour), fmt.Sprintf("v%d", i), v))
	}

	items, err := repos.Beat.GetUnbeatItems(ctx, 100)
	require.NoError(t, err)
	for _, it := range items {
		_, err := repos.Beat.AttachOrSeed(ctx, it, 0.85, 48*time.Hour, 20)
		require.NoError(t, err)
	}

	for _, id := range ids {
		var n int
		require.NoError(t, repos.DB.GetContext(ctx, &n,
			`SELECT COUNT(*) FROM beat_members WHERE item_id = ?`, id))
		assert.Equal(t, 1, n, "item %d must be in exactly one beat", id)
	}
}
