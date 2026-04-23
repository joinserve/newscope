package repository

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/jmoiron/sqlx"
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

// beatWith2Members seeds a beat with 2 items and returns the beat ID.
func beatWith2Members(t *testing.T, repos *Repositories, mkItem func(time.Time, string, []float32) int64, pub time.Time) int64 {
	t.Helper()
	ctx := context.Background()
	id1 := mkItem(pub, "m1-"+t.Name(), []float32{1, 0, 0})
	id2 := mkItem(pub.Add(time.Minute), "m2-"+t.Name(), []float32{1, 0, 0})
	beatID, _, err := repos.Beat.AttachOrSeed(ctx,
		domain.BeatCandidate{ItemID: id1, Vector: []float32{1, 0, 0}, PublishedAt: pub}, 0.85, 48*time.Hour, 20)
	require.NoError(t, err)
	_, _, err = repos.Beat.AttachOrSeed(ctx,
		domain.BeatCandidate{ItemID: id2, Vector: []float32{1, 0, 0}, PublishedAt: pub.Add(time.Minute)}, 0.85, 48*time.Hour, 20)
	require.NoError(t, err)
	return beatID
}

func TestBeatRepository_ListPendingMerge_RemergeNotReturnedWithin24h(t *testing.T) {
	repos, cleanup, mkItem := beatTestSetup(t)
	defer cleanup()
	ctx := context.Background()
	pub := time.Now()

	beatID := beatWith2Members(t, repos, mkItem, pub)
	// simulate a completed first-time merge (canonical_merged_at = now)
	require.NoError(t, repos.Beat.SaveCanonical(ctx, beatID,
		domain.BeatCanonical{Title: "T", Summary: "S"}))

	// add a third member immediately after — added_at > canonical_merged_at but < 24h later
	id3 := mkItem(pub.Add(time.Hour), "m3-remerge24h", []float32{1, 0, 0})
	_, _, err := repos.Beat.AttachOrSeed(ctx,
		domain.BeatCandidate{ItemID: id3, Vector: []float32{1, 0, 0}, PublishedAt: pub.Add(time.Hour)}, 0.85, 48*time.Hour, 20)
	require.NoError(t, err)

	beats, err := repos.Beat.ListPendingMerge(ctx, 10)
	require.NoError(t, err)
	for _, b := range beats {
		assert.NotEqual(t, beatID, b.ID, "beat merged <24h ago must not be returned for re-merge")
	}
}

func TestBeatRepository_ListPendingMerge_RemergeReturnedAfter24h(t *testing.T) {
	repos, cleanup, mkItem := beatTestSetup(t)
	defer cleanup()
	ctx := context.Background()
	pub := time.Now()

	beatID := beatWith2Members(t, repos, mkItem, pub)
	// complete the first merge, then back-date canonical_merged_at to 25h ago
	require.NoError(t, repos.Beat.SaveCanonical(ctx, beatID,
		domain.BeatCanonical{Title: "T", Summary: "S"}))
	_, err := repos.DB.ExecContext(ctx,
		`UPDATE beats SET canonical_merged_at = datetime('now', '-25 hours') WHERE id = ?`, beatID)
	require.NoError(t, err)

	// add a new member with added_at > canonical_merged_at
	id3 := mkItem(pub.Add(2*time.Hour), "m3-remerge-after24h", []float32{1, 0, 0})
	_, err = repos.DB.ExecContext(ctx,
		`INSERT INTO beat_members (beat_id, item_id, added_at) VALUES (?, ?, strftime('%Y-%m-%d %H:%M:%f', 'now'))`,
		beatID, id3)
	require.NoError(t, err)

	beats, err := repos.Beat.ListPendingMerge(ctx, 10)
	require.NoError(t, err)
	ids := make([]int64, len(beats))
	for i, b := range beats {
		ids[i] = b.ID
	}
	assert.Contains(t, ids, beatID, "beat with new member after 24h must be returned for re-merge")
}

func TestBeatRepository_ListPendingMerge_RemergeNotReturnedPast48hWindow(t *testing.T) {
	repos, cleanup, mkItem := beatTestSetup(t)
	defer cleanup()
	ctx := context.Background()
	pub := time.Now()

	beatID := beatWith2Members(t, repos, mkItem, pub)
	require.NoError(t, repos.Beat.SaveCanonical(ctx, beatID,
		domain.BeatCanonical{Title: "T", Summary: "S"}))
	// back-date both canonical_merged_at (>24h) and first_seen_at (>48h — frozen)
	_, err := repos.DB.ExecContext(ctx, `
		UPDATE beats SET
		  canonical_merged_at = datetime('now', '-25 hours'),
		  first_seen_at       = datetime('now', '-49 hours')
		WHERE id = ?`, beatID)
	require.NoError(t, err)

	// add a new member
	id3 := mkItem(pub.Add(2*time.Hour), "m3-past48h", []float32{1, 0, 0})
	_, err = repos.DB.ExecContext(ctx,
		`INSERT INTO beat_members (beat_id, item_id, added_at) VALUES (?, ?, strftime('%Y-%m-%d %H:%M:%f', 'now'))`,
		beatID, id3)
	require.NoError(t, err)

	beats, err := repos.Beat.ListPendingMerge(ctx, 10)
	require.NoError(t, err)
	for _, b := range beats {
		assert.NotEqual(t, beatID, b.ID, "beat past 48h window must not be returned for re-merge")
	}
}

func TestBeatRepository_SaveCanonical_PreservesViewedAndFeedback(t *testing.T) {
	repos, cleanup, mkItem := beatTestSetup(t)
	defer cleanup()
	ctx := context.Background()
	pub := time.Now()

	beatID := beatWith2Members(t, repos, mkItem, pub)
	require.NoError(t, repos.Beat.MarkViewed(ctx, beatID))
	require.NoError(t, repos.Beat.SetFeedback(ctx, beatID, "like"))

	// re-summary (simulates PR 7 re-merge)
	require.NoError(t, repos.Beat.SaveCanonical(ctx, beatID,
		domain.BeatCanonical{Title: "New Title", Summary: "New summary."}))
	require.NoError(t, repos.Beat.SaveCanonical(ctx, beatID,
		domain.BeatCanonical{Title: "Second Title", Summary: "Second summary."}))

	var fb string
	var lv *time.Time
	require.NoError(t, repos.DB.GetContext(ctx, &fb, `SELECT feedback FROM beats WHERE id = ?`, beatID))
	require.NoError(t, repos.DB.GetContext(ctx, &lv, `SELECT last_viewed_at FROM beats WHERE id = ?`, beatID))
	assert.Equal(t, "like", fb, "feedback must survive re-summary")
	assert.NotNil(t, lv, "last_viewed_at must survive re-summary")
}

func TestBeatRepository_MigrateAddCanonicalMergedAt(t *testing.T) {
	ctx := context.Background()

	db, err := sqlx.Open("sqlite", ":memory:")
	require.NoError(t, err)
	defer db.Close()
	db.SetMaxOpenConns(1)
	_, err = db.ExecContext(ctx, "PRAGMA foreign_keys = ON")
	require.NoError(t, err)

	// old schema without canonical_merged_at
	_, err = db.ExecContext(ctx, `CREATE TABLE beats (
		id                INTEGER PRIMARY KEY AUTOINCREMENT,
		canonical_title   TEXT,
		canonical_summary TEXT,
		first_seen_at     DATETIME NOT NULL,
		last_viewed_at    DATETIME,
		feedback          TEXT DEFAULT '',
		feedback_at       DATETIME,
		created_at        DATETIME DEFAULT CURRENT_TIMESTAMP,
		updated_at        DATETIME DEFAULT CURRENT_TIMESTAMP
	)`)
	require.NoError(t, err)

	var cols []string
	require.NoError(t, db.SelectContext(ctx, &cols, `SELECT name FROM pragma_table_info('beats')`))
	assert.NotContains(t, cols, "canonical_merged_at")

	require.NoError(t, migrateAddCanonicalMergedAt(ctx, db))

	require.NoError(t, db.SelectContext(ctx, &cols, `SELECT name FROM pragma_table_info('beats')`))
	assert.Contains(t, cols, "canonical_merged_at")

	// idempotent
	require.NoError(t, migrateAddCanonicalMergedAt(ctx, db))
}

func TestBeatRepository_SetFeedback(t *testing.T) {
	repos, cleanup, mkItem := beatTestSetup(t)
	defer cleanup()

	ctx := context.Background()
	pub := time.Now()
	id := mkItem(pub, "a", []float32{1, 0, 0})
	beatID, _, err := repos.Beat.AttachOrSeed(ctx,
		domain.BeatCandidate{ItemID: id, Vector: []float32{1, 0, 0}, PublishedAt: pub}, 0.85, 48*time.Hour, 20)
	require.NoError(t, err)

	t.Run("set like", func(t *testing.T) {
		require.NoError(t, repos.Beat.SetFeedback(ctx, beatID, "like"))

		var fb string
		var fbAt *time.Time
		require.NoError(t, repos.DB.GetContext(ctx, &fb, `SELECT feedback FROM beats WHERE id = ?`, beatID))
		require.NoError(t, repos.DB.GetContext(ctx, &fbAt, `SELECT feedback_at FROM beats WHERE id = ?`, beatID))
		assert.Equal(t, "like", fb)
		assert.NotNil(t, fbAt, "feedback_at must be set when feedback is non-empty")
	})

	t.Run("overwrite to dislike", func(t *testing.T) {
		require.NoError(t, repos.Beat.SetFeedback(ctx, beatID, "dislike"))

		var fb string
		require.NoError(t, repos.DB.GetContext(ctx, &fb, `SELECT feedback FROM beats WHERE id = ?`, beatID))
		assert.Equal(t, "dislike", fb)
	})

	t.Run("clear feedback", func(t *testing.T) {
		require.NoError(t, repos.Beat.SetFeedback(ctx, beatID, ""))

		var fb string
		var fbAt *time.Time
		require.NoError(t, repos.DB.GetContext(ctx, &fb, `SELECT feedback FROM beats WHERE id = ?`, beatID))
		require.NoError(t, repos.DB.GetContext(ctx, &fbAt, `SELECT feedback_at FROM beats WHERE id = ?`, beatID))
		assert.Empty(t, fb)
		assert.Nil(t, fbAt, "feedback_at must be NULL when feedback is cleared")
	})
}

func TestBeatRepository_SetFeedback_InvalidValue(t *testing.T) {
	repos, cleanup, mkItem := beatTestSetup(t)
	defer cleanup()

	ctx := context.Background()
	pub := time.Now()
	id := mkItem(pub, "a", []float32{1, 0, 0})
	beatID, _, err := repos.Beat.AttachOrSeed(ctx,
		domain.BeatCandidate{ItemID: id, Vector: []float32{1, 0, 0}, PublishedAt: pub}, 0.85, 48*time.Hour, 20)
	require.NoError(t, err)

	err = repos.Beat.SetFeedback(ctx, beatID, "done")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid beat feedback value")

	err = repos.Beat.SetFeedback(ctx, beatID, "spam")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid beat feedback value")
}

func TestBeatRepository_SetFeedback_DoesNotPropagateToItems(t *testing.T) {
	repos, cleanup, mkItem := beatTestSetup(t)
	defer cleanup()

	ctx := context.Background()
	pub := time.Now()
	id := mkItem(pub, "a", []float32{1, 0, 0})
	beatID, _, err := repos.Beat.AttachOrSeed(ctx,
		domain.BeatCandidate{ItemID: id, Vector: []float32{1, 0, 0}, PublishedAt: pub}, 0.85, 48*time.Hour, 20)
	require.NoError(t, err)

	require.NoError(t, repos.Beat.SetFeedback(ctx, beatID, "like"))

	var itemFeedback string
	require.NoError(t, repos.DB.GetContext(ctx, &itemFeedback,
		`SELECT COALESCE(user_feedback, '') FROM items WHERE id = ?`, id))
	assert.Empty(t, itemFeedback, "beat feedback must not propagate to items.user_feedback")
}

func TestBeatRepository_SetFeedback_SurvivesSaveCanonical(t *testing.T) {
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

	require.NoError(t, repos.Beat.SetFeedback(ctx, beatID, "like"))
	require.NoError(t, repos.Beat.SaveCanonical(ctx, beatID, domain.BeatCanonical{
		Title: "Re-summarized Title", Summary: "Re-summarized summary.",
	}))

	var fb string
	require.NoError(t, repos.DB.GetContext(ctx, &fb, `SELECT feedback FROM beats WHERE id = ?`, beatID))
	assert.Equal(t, "like", fb, "feedback must survive a SaveCanonical re-summary")
}

func TestBeatRepository_MigrateAddBeatFeedback(t *testing.T) {
	ctx := context.Background()

	// open a raw in-memory DB and apply the old schema (beats without feedback columns)
	db, err := sqlx.Open("sqlite", ":memory:")
	require.NoError(t, err)
	defer db.Close()

	db.SetMaxOpenConns(1)

	_, err = db.ExecContext(ctx, "PRAGMA foreign_keys = ON")
	require.NoError(t, err)

	// create the minimal beats table as it existed before PR 6
	_, err = db.ExecContext(ctx, `CREATE TABLE beats (
		id                INTEGER PRIMARY KEY AUTOINCREMENT,
		canonical_title   TEXT,
		canonical_summary TEXT,
		first_seen_at     DATETIME NOT NULL,
		last_viewed_at    DATETIME,
		created_at        DATETIME DEFAULT CURRENT_TIMESTAMP,
		updated_at        DATETIME DEFAULT CURRENT_TIMESTAMP
	)`)
	require.NoError(t, err)

	// confirm the columns are absent before migration
	var cols []string
	require.NoError(t, db.SelectContext(ctx, &cols, `SELECT name FROM pragma_table_info('beats')`))
	assert.NotContains(t, cols, "feedback")
	assert.NotContains(t, cols, "feedback_at")

	// run the migration
	require.NoError(t, migrateAddBeatFeedback(ctx, db))

	// confirm both columns now exist
	require.NoError(t, db.SelectContext(ctx, &cols, `SELECT name FROM pragma_table_info('beats')`))
	assert.Contains(t, cols, "feedback")
	assert.Contains(t, cols, "feedback_at")

	// idempotent: running again must not error
	require.NoError(t, migrateAddBeatFeedback(ctx, db))
}

func TestBeatRepository_Search_ByTitle(t *testing.T) {
	repos, cleanup, mkItem := beatTestSetup(t)
	defer cleanup()
	ctx := context.Background()

	pub := time.Now()
	id := mkItem(pub, "ukraine-war", []float32{1, 0, 0})
	beatID, _, err := repos.Beat.AttachOrSeed(ctx,
		domain.BeatCandidate{ItemID: id, Vector: []float32{1, 0, 0}, PublishedAt: pub}, 0.5, 48*time.Hour, 20)
	require.NoError(t, err)
	require.NoError(t, repos.Beat.SaveCanonical(ctx, beatID, domain.BeatCanonical{
		Title:   "Ukraine War Update",
		Summary: "Fighting continues in the eastern regions.",
	}))

	results, err := repos.Beat.Search(ctx, "Ukraine", 10)
	require.NoError(t, err)
	require.Len(t, results, 1)
	assert.Equal(t, beatID, results[0].ID)
	assert.Equal(t, "Ukraine War Update", results[0].CanonicalTitle)
	assert.Equal(t, 1, results[0].MemberCount)
}

func TestBeatRepository_Search_BySummary(t *testing.T) {
	repos, cleanup, mkItem := beatTestSetup(t)
	defer cleanup()
	ctx := context.Background()

	pub := time.Now()
	id := mkItem(pub, "climate", []float32{1, 0, 0})
	beatID, _, err := repos.Beat.AttachOrSeed(ctx,
		domain.BeatCandidate{ItemID: id, Vector: []float32{1, 0, 0}, PublishedAt: pub}, 0.5, 48*time.Hour, 20)
	require.NoError(t, err)
	require.NoError(t, repos.Beat.SaveCanonical(ctx, beatID, domain.BeatCanonical{
		Title:   "Climate Summit",
		Summary: "World leaders agree on carbon reduction targets.",
	}))

	results, err := repos.Beat.Search(ctx, "carbon", 10)
	require.NoError(t, err)
	require.Len(t, results, 1)
	assert.Equal(t, beatID, results[0].ID)
}

func TestBeatRepository_Search_EmptyQueryReturnsNil(t *testing.T) {
	repos, cleanup, _ := beatTestSetup(t)
	defer cleanup()

	results, err := repos.Beat.Search(context.Background(), "", 10)
	require.NoError(t, err)
	assert.Nil(t, results)
}

func TestBeatRepository_Search_NoMatch(t *testing.T) {
	repos, cleanup, mkItem := beatTestSetup(t)
	defer cleanup()
	ctx := context.Background()

	pub := time.Now()
	id := mkItem(pub, "tech-news", []float32{1, 0, 0})
	beatID, _, err := repos.Beat.AttachOrSeed(ctx,
		domain.BeatCandidate{ItemID: id, Vector: []float32{1, 0, 0}, PublishedAt: pub}, 0.5, 48*time.Hour, 20)
	require.NoError(t, err)
	require.NoError(t, repos.Beat.SaveCanonical(ctx, beatID, domain.BeatCanonical{
		Title:   "Apple Releases New iPhone",
		Summary: "The latest iPhone features improved cameras.",
	}))

	results, err := repos.Beat.Search(ctx, "soccer", 10)
	require.NoError(t, err)
	assert.Empty(t, results)
}

func TestBeatRepository_Search_RespectsLimit(t *testing.T) {
	repos, cleanup, mkItem := beatTestSetup(t)
	defer cleanup()
	ctx := context.Background()

	// use orthogonal vectors so each item seeds its own beat (cosine=0 < threshold 0.5)
	vecs := [][]float32{{1, 0, 0}, {0, 1, 0}, {0, 0, 1}}
	for i := 0; i < 3; i++ {
		pub := time.Now().Add(time.Duration(i) * time.Hour)
		id := mkItem(pub, fmt.Sprintf("item-%d", i), vecs[i])
		beatID, _, err := repos.Beat.AttachOrSeed(ctx,
			domain.BeatCandidate{ItemID: id, Vector: vecs[i], PublishedAt: pub},
			0.5, 48*time.Hour, 20)
		require.NoError(t, err)
		require.NoError(t, repos.Beat.SaveCanonical(ctx, beatID, domain.BeatCanonical{
			Title:   fmt.Sprintf("Summit %d", i),
			Summary: "Leaders gather at annual summit.",
		}))
	}

	results, err := repos.Beat.Search(ctx, "summit", 2)
	require.NoError(t, err)
	assert.Len(t, results, 2)
}

func TestBeatRepository_Search_MemberTitleFallthrough(t *testing.T) {
	repos, cleanup, mkItem := beatTestSetup(t)
	defer cleanup()
	ctx := context.Background()

	// seed a single-member beat without calling SaveCanonical — canonical_title stays NULL
	pub := time.Now()
	id := mkItem(pub, "Quantum Computing Breakthrough", []float32{1, 0, 0})
	beatID, _, err := repos.Beat.AttachOrSeed(ctx,
		domain.BeatCandidate{ItemID: id, Vector: []float32{1, 0, 0}, PublishedAt: pub}, 0.5, 48*time.Hour, 20)
	require.NoError(t, err)

	// verify canonical is indeed NULL
	var canonTitle *string
	require.NoError(t, repos.DB.GetContext(ctx, &canonTitle,
		`SELECT canonical_title FROM beats WHERE id = ?`, beatID))
	assert.Nil(t, canonTitle, "canonical_title must be NULL for this test to be valid")

	// searching for a word from the item title must return the beat via fallthrough
	results, err := repos.Beat.Search(ctx, "Quantum", 10)
	require.NoError(t, err)
	require.Len(t, results, 1, "single-member NULL-canonical beat must be findable via member item title")
	assert.Equal(t, beatID, results[0].ID)
	assert.Equal(t, 1, results[0].MemberCount)
}

func TestBeatRepository_Search_SpecialCharsDoNotError(t *testing.T) {
	repos, cleanup, mkItem := beatTestSetup(t)
	defer cleanup()
	ctx := context.Background()

	pub := time.Now()
	id := mkItem(pub, "AI Model", []float32{1, 0, 0})
	beatID, _, err := repos.Beat.AttachOrSeed(ctx,
		domain.BeatCandidate{ItemID: id, Vector: []float32{1, 0, 0}, PublishedAt: pub}, 0.5, 48*time.Hour, 20)
	require.NoError(t, err)
	require.NoError(t, repos.Beat.SaveCanonical(ctx, beatID, domain.BeatCanonical{
		Title:   "AI Model Release",
		Summary: "A new AI model was released.",
	}))

	// these queries contain FTS5 special characters that would error without escaping
	for _, q := range []string{`"AI"`, `AI*`, `(AI OR model)`, `AI"model`} {
		results, err := repos.Beat.Search(ctx, q, 10)
		require.NoError(t, err, "query %q must not produce a DB error", q)
		_ = results
	}
}

func TestBeatRepository_MigrateBackfillBeatsFTS(t *testing.T) {
	ctx := context.Background()

	// open a raw in-memory DB with beats and beats_fts tables but no insert triggers,
	// simulating an existing deployment before this migration ran.
	db, err := sqlx.Open("sqlite", ":memory:")
	require.NoError(t, err)
	defer db.Close()
	db.SetMaxOpenConns(1)

	_, err = db.ExecContext(ctx, `CREATE TABLE beats (
		id                INTEGER PRIMARY KEY AUTOINCREMENT,
		canonical_title   TEXT,
		canonical_summary TEXT,
		first_seen_at     DATETIME NOT NULL,
		created_at        DATETIME DEFAULT CURRENT_TIMESTAMP,
		updated_at        DATETIME DEFAULT CURRENT_TIMESTAMP
	)`)
	require.NoError(t, err)

	_, err = db.ExecContext(ctx, `CREATE VIRTUAL TABLE beats_fts USING fts5(
		canonical_title,
		canonical_summary,
		content=beats,
		content_rowid=id,
		tokenize='porter unicode61'
	)`)
	require.NoError(t, err)

	// insert a beat directly — no trigger exists, so beats_fts shadow tables stay empty
	_, err = db.ExecContext(ctx,
		`INSERT INTO beats (first_seen_at, canonical_title, canonical_summary) VALUES (?, ?, ?)`,
		time.Now(), "Uniqueword Beat Title", "Uniqueword beat summary.")
	require.NoError(t, err)

	// verify the beat is NOT yet findable via FTS (shadow tables empty, no trigger)
	var matchCount int
	require.NoError(t, db.GetContext(ctx, &matchCount,
		`SELECT COUNT(*) FROM beats_fts WHERE beats_fts MATCH 'Uniqueword'`))
	assert.Equal(t, 0, matchCount, "FTS index should be empty before migration")

	// run backfill migration
	require.NoError(t, migrateBackfillBeatsFTS(ctx, db))

	// beat should now be findable via FTS
	require.NoError(t, db.GetContext(ctx, &matchCount,
		`SELECT COUNT(*) FROM beats_fts WHERE beats_fts MATCH 'Uniqueword'`))
	assert.Equal(t, 1, matchCount, "beat should be indexed after migration")

	// idempotent: second run must not error or produce duplicates
	require.NoError(t, migrateBackfillBeatsFTS(ctx, db))
	require.NoError(t, db.GetContext(ctx, &matchCount,
		`SELECT COUNT(*) FROM beats_fts WHERE beats_fts MATCH 'Uniqueword'`))
	assert.Equal(t, 1, matchCount, "migration must be idempotent")
}
