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
	beatID, _, err := repos.Beat.AttachOrSeed(ctx,
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

	beat1, _, err := repos.Beat.AttachOrSeed(ctx,
		domain.BeatCandidate{ItemID: id1, Vector: []float32{1, 0, 0}, PublishedAt: pub}, 0.85, 48*time.Hour, 20)
	require.NoError(t, err)

	beat2, _, err := repos.Beat.AttachOrSeed(ctx,
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

	beat1, _, err := repos.Beat.AttachOrSeed(ctx,
		domain.BeatCandidate{ItemID: id1, Vector: []float32{1, 0, 0}, PublishedAt: pub}, 0.85, 48*time.Hour, 20)
	require.NoError(t, err)

	beat2, _, err := repos.Beat.AttachOrSeed(ctx,
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

	beat1, _, err := repos.Beat.AttachOrSeed(ctx,
		domain.BeatCandidate{ItemID: id1, Vector: []float32{1, 0, 0}, PublishedAt: pub}, 0.85, 48*time.Hour, 20)
	require.NoError(t, err)

	beat2, _, err := repos.Beat.AttachOrSeed(ctx,
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

	beatA, _, err := repos.Beat.AttachOrSeed(ctx,
		domain.BeatCandidate{ItemID: a, Vector: []float32{1, 0, 0}, PublishedAt: pub}, 0.85, 48*time.Hour, 2)
	require.NoError(t, err)
	beatB, _, err := repos.Beat.AttachOrSeed(ctx,
		domain.BeatCandidate{ItemID: b, Vector: []float32{1, 0, 0}, PublishedAt: pub.Add(time.Hour)}, 0.85, 48*time.Hour, 2)
	require.NoError(t, err)
	beatC, _, err := repos.Beat.AttachOrSeed(ctx,
		domain.BeatCandidate{ItemID: c, Vector: []float32{1, 0, 0}, PublishedAt: pub.Add(2 * time.Hour)}, 0.85, 48*time.Hour, 2)
	require.NoError(t, err)

	assert.Equal(t, beatA, beatB, "first two share the beat")
	assert.NotEqual(t, beatA, beatC, "third spills to a new beat once cap is hit")
}

// When the nearest qualifying beat is at capacity, AttachOrSeed should spill
// to the next best beat that still meets the threshold and has room, rather
// than seeding a fresh beat (which would split the story).
func TestBeatRepository_AttachOrSeed_SpillsToSecondBestWhenNearestFull(t *testing.T) {
	repos, cleanup, mkItem := beatTestSetup(t)
	defer cleanup()

	ctx := context.Background()
	pub := time.Now()

	// 2D vectors at known angles from the x-axis so both beats sit above
	// threshold for B while A is clearly the nearest.
	vecA := []float32{1, 0, 0}         // 0° — seeds beat A
	vecC := []float32{0.819, 0.574, 0} // 35° — cos(A,C)=0.819 < 0.85, separate beat
	vecB := []float32{0.966, 0.259, 0} // 15° — cos(A,B)=0.966, cos(C,B)=cos(20°)=0.940

	a1 := mkItem(pub, "a1", vecA)
	a2 := mkItem(pub.Add(time.Hour), "a2", vecA)
	c1 := mkItem(pub.Add(2*time.Hour), "c1", vecC)
	b1 := mkItem(pub.Add(3*time.Hour), "b1", vecB)

	beatA, _, err := repos.Beat.AttachOrSeed(ctx,
		domain.BeatCandidate{ItemID: a1, Vector: vecA, PublishedAt: pub}, 0.85, 48*time.Hour, 2)
	require.NoError(t, err)
	// fill beat A to cap
	beatA2, _, err := repos.Beat.AttachOrSeed(ctx,
		domain.BeatCandidate{ItemID: a2, Vector: vecA, PublishedAt: pub.Add(time.Hour)}, 0.85, 48*time.Hour, 2)
	require.NoError(t, err)
	require.Equal(t, beatA, beatA2)
	// c is below threshold to A — seeds beat C
	beatC, _, err := repos.Beat.AttachOrSeed(ctx,
		domain.BeatCandidate{ItemID: c1, Vector: vecC, PublishedAt: pub.Add(2 * time.Hour)}, 0.85, 48*time.Hour, 2)
	require.NoError(t, err)
	require.NotEqual(t, beatA, beatC)

	// b's nearest is beat A (full) — must spill to beat C, not seed a third
	beatB, seededB, err := repos.Beat.AttachOrSeed(ctx,
		domain.BeatCandidate{ItemID: b1, Vector: vecB, PublishedAt: pub.Add(3 * time.Hour)}, 0.85, 48*time.Hour, 2)
	require.NoError(t, err)
	assert.False(t, seededB, "spill should attach, not seed")
	assert.Equal(t, beatC, beatB, "B should land in the second-best qualifying beat")
}

func TestBeatRepository_AttachOrSeed_SeededFlag(t *testing.T) {
	repos, cleanup, mkItem := beatTestSetup(t)
	defer cleanup()

	ctx := context.Background()
	pub := time.Now()
	id1 := mkItem(pub, "a", []float32{1, 0, 0})
	id2 := mkItem(pub.Add(time.Hour), "b", []float32{1, 0, 0})

	_, seeded1, err := repos.Beat.AttachOrSeed(ctx,
		domain.BeatCandidate{ItemID: id1, Vector: []float32{1, 0, 0}, PublishedAt: pub}, 0.85, 48*time.Hour, 20)
	require.NoError(t, err)
	assert.True(t, seeded1, "first item in an empty store seeds a beat")

	_, seeded2, err := repos.Beat.AttachOrSeed(ctx,
		domain.BeatCandidate{ItemID: id2, Vector: []float32{1, 0, 0}, PublishedAt: pub.Add(time.Hour)}, 0.85, 48*time.Hour, 20)
	require.NoError(t, err)
	assert.False(t, seeded2, "matching item attaches to existing beat")

	// idempotent re-call on an already-assigned item: seeded=false
	_, seededRe, err := repos.Beat.AttachOrSeed(ctx,
		domain.BeatCandidate{ItemID: id1, Vector: []float32{1, 0, 0}, PublishedAt: pub}, 0.85, 48*time.Hour, 20)
	require.NoError(t, err)
	assert.False(t, seededRe, "re-processing an existing member does not seed")
}

func TestBeatRepository_AttachOrSeed_Idempotent(t *testing.T) {
	repos, cleanup, mkItem := beatTestSetup(t)
	defer cleanup()

	ctx := context.Background()
	pub := time.Now()
	id := mkItem(pub, "a", []float32{1, 0, 0})
	cand := domain.BeatCandidate{ItemID: id, Vector: []float32{1, 0, 0}, PublishedAt: pub}

	b1, _, err := repos.Beat.AttachOrSeed(ctx, cand, 0.85, 48*time.Hour, 20)
	require.NoError(t, err)
	b2, _, err := repos.Beat.AttachOrSeed(ctx, cand, 0.85, 48*time.Hour, 20)
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
	beatID, _, err := repos.Beat.AttachOrSeed(ctx,
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
	beatID, _, err := repos.Beat.AttachOrSeed(ctx,
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
	_, _, err = repos.Beat.AttachOrSeed(ctx,
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

	_, _, err = repos.Beat.AttachOrSeed(ctx,
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
		_, _, err := repos.Beat.AttachOrSeed(ctx, it, 0.85, 48*time.Hour, 20)
		require.NoError(t, err)
	}

	for _, id := range ids {
		var n int
		require.NoError(t, repos.DB.GetContext(ctx, &n,
			`SELECT COUNT(*) FROM beat_members WHERE item_id = ?`, id))
		assert.Equal(t, 1, n, "item %d must be in exactly one beat", id)
	}
}

func TestBeatRepository_ListPendingMerge_OnlyMultiMember(t *testing.T) {
	repos, cleanup, mkItem := beatTestSetup(t)
	defer cleanup()

	ctx := context.Background()
	pub := time.Now()

	// beat with 2 members — qualifies for merge
	id1 := mkItem(pub, "a", []float32{1, 0, 0})
	id2 := mkItem(pub.Add(time.Hour), "b", []float32{1, 0, 0})
	b1, _, err := repos.Beat.AttachOrSeed(ctx,
		domain.BeatCandidate{ItemID: id1, Vector: []float32{1, 0, 0}, PublishedAt: pub}, 0.85, 48*time.Hour, 20)
	require.NoError(t, err)
	_, _, err = repos.Beat.AttachOrSeed(ctx,
		domain.BeatCandidate{ItemID: id2, Vector: []float32{1, 0, 0}, PublishedAt: pub.Add(time.Hour)}, 0.85, 48*time.Hour, 20)
	require.NoError(t, err)

	// singleton beat — must not appear in pending
	id3 := mkItem(pub.Add(2*time.Hour), "c", []float32{0, 1, 0})
	_, _, err = repos.Beat.AttachOrSeed(ctx,
		domain.BeatCandidate{ItemID: id3, Vector: []float32{0, 1, 0}, PublishedAt: pub.Add(2 * time.Hour)}, 0.85, 48*time.Hour, 20)
	require.NoError(t, err)

	beats, err := repos.Beat.ListPendingMerge(ctx, 10)
	require.NoError(t, err)
	require.Len(t, beats, 1, "only the 2-member beat should be pending")
	assert.Equal(t, b1, beats[0].ID)
	assert.Len(t, beats[0].Members, 2)
	// members carry title and topics
	titles := []string{beats[0].Members[0].Title, beats[0].Members[1].Title}
	assert.ElementsMatch(t, []string{"a", "b"}, titles)
}

func TestBeatRepository_ListPendingMerge_ExcludesAlreadyMerged(t *testing.T) {
	repos, cleanup, mkItem := beatTestSetup(t)
	defer cleanup()

	ctx := context.Background()
	pub := time.Now()

	id1 := mkItem(pub, "a", []float32{1, 0, 0})
	id2 := mkItem(pub.Add(time.Hour), "b", []float32{1, 0, 0})
	beatID, _, err := repos.Beat.AttachOrSeed(ctx,
		domain.BeatCandidate{ItemID: id1, Vector: []float32{1, 0, 0}, PublishedAt: pub}, 0.85, 48*time.Hour, 20)
	require.NoError(t, err)
	_, _, err = repos.Beat.AttachOrSeed(ctx,
		domain.BeatCandidate{ItemID: id2, Vector: []float32{1, 0, 0}, PublishedAt: pub.Add(time.Hour)}, 0.85, 48*time.Hour, 20)
	require.NoError(t, err)

	// once canonical_summary is set, beat must not appear again
	require.NoError(t, repos.Beat.SaveCanonical(ctx, beatID,
		domain.BeatCanonical{Title: "Title", Summary: "Summary"}))

	beats, err := repos.Beat.ListPendingMerge(ctx, 10)
	require.NoError(t, err)
	assert.Empty(t, beats, "merged beat must not be returned again")
}

func TestBeatRepository_ListPendingMerge_RespectsLimit(t *testing.T) {
	repos, cleanup, mkItem := beatTestSetup(t)
	defer cleanup()

	ctx := context.Background()
	pub := time.Now()

	// create 3 separate 2-member beats via distinct vectors
	vecs := [][2][]float32{
		{{1, 0, 0}, {1, 0, 0}},
		{{0, 1, 0}, {0, 1, 0}},
		{{0, 0, 1}, {0, 0, 1}},
	}
	for i, pair := range vecs {
		offset := time.Duration(i*4) * time.Hour
		i1 := mkItem(pub.Add(offset), fmt.Sprintf("x%d", i*2), pair[0])
		i2 := mkItem(pub.Add(offset+time.Hour), fmt.Sprintf("x%d", i*2+1), pair[1])
		_, _, err := repos.Beat.AttachOrSeed(ctx,
			domain.BeatCandidate{ItemID: i1, Vector: pair[0], PublishedAt: pub.Add(offset)}, 0.85, 48*time.Hour, 20)
		require.NoError(t, err)
		_, _, err = repos.Beat.AttachOrSeed(ctx,
			domain.BeatCandidate{ItemID: i2, Vector: pair[1], PublishedAt: pub.Add(offset + time.Hour)}, 0.85, 48*time.Hour, 20)
		require.NoError(t, err)
	}

	beats, err := repos.Beat.ListPendingMerge(ctx, 2)
	require.NoError(t, err)
	assert.Len(t, beats, 2, "limit should be respected")
}

func TestBeatRepository_ListBeats_SortsAndAggregates(t *testing.T) {
	repos, cleanup, mkItem := beatTestSetup(t)
	defer cleanup()
	ctx := context.Background()
	pub := time.Now()

	// beat 1: max score 5.0
	id1 := mkItem(pub, "a", []float32{1, 0, 0})
	id1b := mkItem(pub, "a2", []float32{1, 0, 0})
	b1, _, _ := repos.Beat.AttachOrSeed(ctx, domain.BeatCandidate{ItemID: id1, Vector: []float32{1, 0, 0}, PublishedAt: pub}, 0.85, 48*time.Hour, 20)
	repos.Beat.AttachOrSeed(ctx, domain.BeatCandidate{ItemID: id1b, Vector: []float32{1, 0, 0}, PublishedAt: pub}, 0.85, 48*time.Hour, 20)
	repos.Item.UpdateItemClassification(ctx, id1, &domain.Classification{Score: 5.0})
	repos.Item.UpdateItemClassification(ctx, id1b, &domain.Classification{Score: 5.0})
	require.NoError(t, repos.Beat.SaveCanonical(ctx, b1, domain.BeatCanonical{Title: "T1"}))

	// beat 2: max score 8.0
	id2 := mkItem(pub.Add(time.Hour), "b", []float32{0, 1, 0})
	id3 := mkItem(pub.Add(time.Hour), "c", []float32{0, 1, 0})
	b2, _, _ := repos.Beat.AttachOrSeed(ctx, domain.BeatCandidate{ItemID: id2, Vector: []float32{0, 1, 0}, PublishedAt: pub.Add(time.Hour)}, 0.85, 48*time.Hour, 20)
	repos.Beat.AttachOrSeed(ctx, domain.BeatCandidate{ItemID: id3, Vector: []float32{0, 1, 0}, PublishedAt: pub.Add(time.Hour)}, 0.85, 48*time.Hour, 20)
	repos.Item.UpdateItemClassification(ctx, id2, &domain.Classification{Score: 6.0})
	repos.Item.UpdateItemClassification(ctx, id3, &domain.Classification{Score: 8.0})
	require.NoError(t, repos.Beat.SaveCanonical(ctx, b2, domain.BeatCanonical{Title: "T2"}))

	// beat 3: max score 7.0
	id4 := mkItem(pub.Add(2*time.Hour), "d", []float32{0, 0, 1})
	id4b := mkItem(pub.Add(2*time.Hour), "d2", []float32{0, 0, 1})
	b3, _, _ := repos.Beat.AttachOrSeed(ctx, domain.BeatCandidate{ItemID: id4, Vector: []float32{0, 0, 1}, PublishedAt: pub.Add(2 * time.Hour)}, 0.85, 48*time.Hour, 20)
	repos.Beat.AttachOrSeed(ctx, domain.BeatCandidate{ItemID: id4b, Vector: []float32{0, 0, 1}, PublishedAt: pub.Add(2 * time.Hour)}, 0.85, 48*time.Hour, 20)
	repos.Item.UpdateItemClassification(ctx, id4, &domain.Classification{Score: 7.0})
	repos.Item.UpdateItemClassification(ctx, id4b, &domain.Classification{Score: 7.0})
	require.NoError(t, repos.Beat.SaveCanonical(ctx, b3, domain.BeatCanonical{Title: "T3"}))

	beats, err := repos.Beat.ListBeats(ctx, 10, 0)
	require.NoError(t, err)
	require.Len(t, beats, 3)

	assert.Equal(t, b2, beats[0].ID, "highest max score should be first")
	assert.Equal(t, 8.0, beats[0].AggregateScore)
	assert.Equal(t, b3, beats[1].ID)
	assert.Equal(t, 7.0, beats[1].AggregateScore)
	assert.Equal(t, b1, beats[2].ID)
	assert.Equal(t, 5.0, beats[2].AggregateScore)
}

func TestBeatRepository_ListBeats_UnreadCount(t *testing.T) {
	repos, cleanup, mkItem := beatTestSetup(t)
	defer cleanup()
	ctx := context.Background()
	pub := time.Now()

	id1 := mkItem(pub, "a", []float32{1, 0, 0})
	b1, _, _ := repos.Beat.AttachOrSeed(ctx, domain.BeatCandidate{ItemID: id1, Vector: []float32{1, 0, 0}, PublishedAt: pub}, 0.85, 48*time.Hour, 20)

	require.NoError(t, repos.Beat.SaveCanonical(ctx, b1, domain.BeatCanonical{Title: "T1"}))

	// last viewed is set
	require.NoError(t, repos.Beat.MarkViewed(ctx, b1))

	// add 2 new members
	time.Sleep(10 * time.Millisecond) // ensure added_at > last_viewed_at
	id2 := mkItem(pub.Add(time.Hour), "b", []float32{1, 0, 0})
	id3 := mkItem(pub.Add(time.Hour), "c", []float32{1, 0, 0})
	repos.Beat.AttachOrSeed(ctx, domain.BeatCandidate{ItemID: id2, Vector: []float32{1, 0, 0}, PublishedAt: pub.Add(time.Hour)}, 0.85, 48*time.Hour, 20)
	repos.Beat.AttachOrSeed(ctx, domain.BeatCandidate{ItemID: id3, Vector: []float32{1, 0, 0}, PublishedAt: pub.Add(time.Hour)}, 0.85, 48*time.Hour, 20)

	beats, err := repos.Beat.ListBeats(ctx, 10, 0)
	require.NoError(t, err)
	require.Len(t, beats, 1)
	assert.Equal(t, 2, beats[0].UnreadCount)
}

func TestBeatRepository_GetBeat_EagerLoadsMembers(t *testing.T) {
	repos, cleanup, mkItem := beatTestSetup(t)
	defer cleanup()
	ctx := context.Background()
	pub := time.Now()

	id1 := mkItem(pub, "a", []float32{1, 0, 0})
	id2 := mkItem(pub.Add(time.Hour), "b", []float32{1, 0, 0})
	id3 := mkItem(pub.Add(time.Hour), "c", []float32{1, 0, 0})

	b1, _, _ := repos.Beat.AttachOrSeed(ctx, domain.BeatCandidate{ItemID: id1, Vector: []float32{1, 0, 0}, PublishedAt: pub}, 0.85, 48*time.Hour, 20)
	repos.Beat.AttachOrSeed(ctx, domain.BeatCandidate{ItemID: id2, Vector: []float32{1, 0, 0}, PublishedAt: pub.Add(time.Hour)}, 0.85, 48*time.Hour, 20)
	repos.Beat.AttachOrSeed(ctx, domain.BeatCandidate{ItemID: id3, Vector: []float32{1, 0, 0}, PublishedAt: pub.Add(time.Hour)}, 0.85, 48*time.Hour, 20)

	repos.Item.UpdateItemClassification(ctx, id1, &domain.Classification{Score: 5.0})
	repos.Item.UpdateItemClassification(ctx, id2, &domain.Classification{Score: 9.0})
	repos.Item.UpdateItemClassification(ctx, id3, &domain.Classification{Score: 7.0})

	beat, err := repos.Beat.GetBeat(ctx, b1)
	require.NoError(t, err)

	assert.Equal(t, 9.0, beat.AggregateScore)
	require.Len(t, beat.Members, 3)

	// Should be score-desc order
	assert.Equal(t, "b", beat.Members[0].Title)
	assert.Equal(t, 9.0, beat.Members[0].GetRelevanceScore())

	assert.Equal(t, "c", beat.Members[1].Title)
	assert.Equal(t, 7.0, beat.Members[1].GetRelevanceScore())

	assert.Equal(t, "a", beat.Members[2].Title)
	assert.Equal(t, 5.0, beat.Members[2].GetRelevanceScore())
}

func TestBeatRepository_SaveCanonical(t *testing.T) {
	repos, cleanup, mkItem := beatTestSetup(t)
	defer cleanup()

	ctx := context.Background()
	pub := time.Now()
	id1 := mkItem(pub, "a", []float32{1, 0, 0})
	id2 := mkItem(pub.Add(time.Hour), "b", []float32{1, 0, 0})
	beatID, _, err := repos.Beat.AttachOrSeed(ctx,
		domain.BeatCandidate{ItemID: id1, Vector: []float32{1, 0, 0}, PublishedAt: pub}, 0.85, 48*time.Hour, 20)
	require.NoError(t, err)
	_, _, err = repos.Beat.AttachOrSeed(ctx,
		domain.BeatCandidate{ItemID: id2, Vector: []float32{1, 0, 0}, PublishedAt: pub.Add(time.Hour)}, 0.85, 48*time.Hour, 20)
	require.NoError(t, err)

	err = repos.Beat.SaveCanonical(ctx, beatID, domain.BeatCanonical{
		Title:   "Canonical Title",
		Summary: "Canonical summary of the beat.",
	})
	require.NoError(t, err)

	var title, summary string
	require.NoError(t, repos.DB.GetContext(ctx, &title,
		`SELECT canonical_title FROM beats WHERE id = ?`, beatID))
	require.NoError(t, repos.DB.GetContext(ctx, &summary,
		`SELECT canonical_summary FROM beats WHERE id = ?`, beatID))
	assert.Equal(t, "Canonical Title", title)
	assert.Equal(t, "Canonical summary of the beat.", summary)
}
