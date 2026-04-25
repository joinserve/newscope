package repository

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/umputun/newscope/pkg/domain"
)

func TestBeatRepository_AppendTitleRevision_FirstWrite(t *testing.T) {
	repos, cleanup, mkItem := beatTestSetup(t)
	defer cleanup()
	ctx := context.Background()
	pub := time.Now()

	beatID := beatWith2Members(t, repos, mkItem, pub)

	err := repos.Beat.AppendTitleRevision(ctx, beatID, "Title A", "Summary A")
	require.NoError(t, err)

	var count int
	require.NoError(t, repos.DB.GetContext(ctx, &count,
		`SELECT COUNT(*) FROM beat_title_revisions WHERE beat_id = ?`, beatID))
	assert.Equal(t, 1, count)
}

func TestBeatRepository_AppendTitleRevision_IdenticalSkips(t *testing.T) {
	repos, cleanup, mkItem := beatTestSetup(t)
	defer cleanup()
	ctx := context.Background()
	pub := time.Now()

	beatID := beatWith2Members(t, repos, mkItem, pub)

	require.NoError(t, repos.Beat.AppendTitleRevision(ctx, beatID, "Title A", "Summary A"))
	require.NoError(t, repos.Beat.AppendTitleRevision(ctx, beatID, "Title A", "Summary A"))

	var count int
	require.NoError(t, repos.DB.GetContext(ctx, &count,
		`SELECT COUNT(*) FROM beat_title_revisions WHERE beat_id = ?`, beatID))
	assert.Equal(t, 1, count, "identical revision must not create a second row")
}

func TestBeatRepository_AppendTitleRevision_DifferentContentAppends(t *testing.T) {
	repos, cleanup, mkItem := beatTestSetup(t)
	defer cleanup()
	ctx := context.Background()
	pub := time.Now()

	beatID := beatWith2Members(t, repos, mkItem, pub)

	require.NoError(t, repos.Beat.AppendTitleRevision(ctx, beatID, "Title A", "Summary A"))
	require.NoError(t, repos.Beat.AppendTitleRevision(ctx, beatID, "Title B", "Summary B"))

	var count int
	require.NoError(t, repos.DB.GetContext(ctx, &count,
		`SELECT COUNT(*) FROM beat_title_revisions WHERE beat_id = ?`, beatID))
	assert.Equal(t, 2, count, "different content must create a new row")
}

func TestBeatRepository_AppendTitleRevision_IndependentBeats(t *testing.T) {
	repos, cleanup, mkItem := beatTestSetup(t)
	defer cleanup()
	ctx := context.Background()
	pub := time.Now()

	// use orthogonal vectors so the two beats don't merge into one
	id1 := mkItem(pub, "ind-a1", []float32{1, 0, 0})
	id2 := mkItem(pub.Add(time.Minute), "ind-a2", []float32{1, 0, 0})
	beatA, _, _ := repos.Beat.AttachOrSeed(ctx, domain.BeatCandidate{ItemID: id1, Vector: []float32{1, 0, 0}, PublishedAt: pub}, 0.85, 48*time.Hour, 20)
	repos.Beat.AttachOrSeed(ctx, domain.BeatCandidate{ItemID: id2, Vector: []float32{1, 0, 0}, PublishedAt: pub.Add(time.Minute)}, 0.85, 48*time.Hour, 20)

	id3 := mkItem(pub.Add(2*time.Minute), "ind-b1", []float32{0, 1, 0})
	id4 := mkItem(pub.Add(3*time.Minute), "ind-b2", []float32{0, 1, 0})
	beatB, _, _ := repos.Beat.AttachOrSeed(ctx, domain.BeatCandidate{ItemID: id3, Vector: []float32{0, 1, 0}, PublishedAt: pub.Add(2 * time.Minute)}, 0.85, 48*time.Hour, 20)
	repos.Beat.AttachOrSeed(ctx, domain.BeatCandidate{ItemID: id4, Vector: []float32{0, 1, 0}, PublishedAt: pub.Add(3 * time.Minute)}, 0.85, 48*time.Hour, 20)

	require.NoError(t, repos.Beat.AppendTitleRevision(ctx, beatA, "Title A", "Summary A"))
	require.NoError(t, repos.Beat.AppendTitleRevision(ctx, beatB, "Title A", "Summary A"))

	var countA, countB int
	require.NoError(t, repos.DB.GetContext(ctx, &countA,
		`SELECT COUNT(*) FROM beat_title_revisions WHERE beat_id = ?`, beatA))
	require.NoError(t, repos.DB.GetContext(ctx, &countB,
		`SELECT COUNT(*) FROM beat_title_revisions WHERE beat_id = ?`, beatB))
	assert.Equal(t, 1, countA)
	assert.Equal(t, 1, countB)
}

func TestBeatRepository_ListTitleRevisions_AscOrder(t *testing.T) {
	repos, cleanup, mkItem := beatTestSetup(t)
	defer cleanup()
	ctx := context.Background()
	pub := time.Now()

	beatID := beatWith2Members(t, repos, mkItem, pub)

	require.NoError(t, repos.Beat.AppendTitleRevision(ctx, beatID, "First Title", "First Summary"))
	// insert a different revision so the second write is not skipped
	require.NoError(t, repos.Beat.AppendTitleRevision(ctx, beatID, "Second Title", "Second Summary"))

	revisions, err := repos.Beat.ListTitleRevisions(ctx, beatID)
	require.NoError(t, err)
	require.Len(t, revisions, 2)
	assert.Equal(t, "First Title", revisions[0].Title, "oldest revision must be first")
	assert.Equal(t, "Second Title", revisions[1].Title, "newest revision must be last")
	assert.False(t, revisions[1].GeneratedAt.Before(revisions[0].GeneratedAt), "ASC order by generated_at")
}

func TestBeatRepository_ListTitleRevisions_PopulatesFields(t *testing.T) {
	repos, cleanup, mkItem := beatTestSetup(t)
	defer cleanup()
	ctx := context.Background()
	pub := time.Now()

	beatID := beatWith2Members(t, repos, mkItem, pub)

	require.NoError(t, repos.Beat.AppendTitleRevision(ctx, beatID, "My Title", "My Summary"))

	revisions, err := repos.Beat.ListTitleRevisions(ctx, beatID)
	require.NoError(t, err)
	require.Len(t, revisions, 1)

	rev := revisions[0]
	assert.NotZero(t, rev.ID)
	assert.Equal(t, beatID, rev.BeatID)
	assert.Equal(t, "My Title", rev.Title)
	assert.Equal(t, "My Summary", rev.Summary)
	assert.False(t, rev.GeneratedAt.IsZero())
}

func TestBeatRepository_ListTitleRevisions_EmptyForNewBeat(t *testing.T) {
	repos, cleanup, mkItem := beatTestSetup(t)
	defer cleanup()
	ctx := context.Background()
	pub := time.Now()

	beatID := beatWith2Members(t, repos, mkItem, pub)

	revisions, err := repos.Beat.ListTitleRevisions(ctx, beatID)
	require.NoError(t, err)
	assert.Empty(t, revisions)
}

// TestBeatRepository_AppendTitleRevision_IntegrationWithSaveCanonical verifies
// the pattern expected by merge_worker: SaveCanonical then AppendTitleRevision.
func TestBeatRepository_AppendTitleRevision_IntegrationWithSaveCanonical(t *testing.T) {
	repos, cleanup, mkItem := beatTestSetup(t)
	defer cleanup()
	ctx := context.Background()
	pub := time.Now()

	beatID := beatWith2Members(t, repos, mkItem, pub)

	canonical := domain.BeatCanonical{Title: "Canon Title", Summary: "Canon Summary"}
	require.NoError(t, repos.Beat.SaveCanonical(ctx, beatID, canonical))
	require.NoError(t, repos.Beat.AppendTitleRevision(ctx, beatID, canonical.Title, canonical.Summary))

	revisions, err := repos.Beat.ListTitleRevisions(ctx, beatID)
	require.NoError(t, err)
	require.Len(t, revisions, 1)
	assert.Equal(t, "Canon Title", revisions[0].Title)
}

func TestMigrateBackfillTitleRevisions(t *testing.T) {
	repos, cleanup, mkItem := beatTestSetup(t)
	defer cleanup()
	ctx := context.Background()
	pub := time.Now()

	// create two beats, each with a canonical title (simulating pre-existing data)
	beatA := beatWith2Members(t, repos, mkItem, pub)
	require.NoError(t, repos.Beat.SaveCanonical(ctx, beatA, domain.BeatCanonical{
		Title: "Beat A Title", Summary: "Beat A Summary",
	}))

	// beatB uses an orthogonal vector; threshold=0.9 ensures it seeds a new beat
	idB1 := mkItem(pub, "b1", []float32{0, 1, 0})
	idB2 := mkItem(pub.Add(time.Minute), "b2", []float32{0, 1, 0})
	beatB, _, _ := repos.Beat.AttachOrSeed(ctx,
		domain.BeatCandidate{ItemID: idB1, Vector: []float32{0, 1, 0}, PublishedAt: pub}, 0.9, 48*time.Hour, 20)
	repos.Beat.AttachOrSeed(ctx,
		domain.BeatCandidate{ItemID: idB2, Vector: []float32{0, 1, 0}, PublishedAt: pub.Add(time.Minute)}, 0.9, 48*time.Hour, 20)
	require.NoError(t, repos.Beat.SaveCanonical(ctx, beatB, domain.BeatCanonical{
		Title: "Beat B Title", Summary: "Beat B Summary",
	}))

	// ensure no revisions exist yet (simulates state before this migration was added)
	var count int
	require.NoError(t, repos.DB.GetContext(ctx, &count, `SELECT COUNT(*) FROM beat_title_revisions`))
	assert.Equal(t, 0, count, "precondition: no revisions")

	// run the backfill
	require.NoError(t, migrateBackfillTitleRevisions(ctx, repos.DB))

	// each beat should have exactly one revision row
	revsA, err := repos.Beat.ListTitleRevisions(ctx, beatA)
	require.NoError(t, err)
	require.Len(t, revsA, 1)
	assert.Equal(t, "Beat A Title", revsA[0].Title)
	assert.Equal(t, "Beat A Summary", revsA[0].Summary)
	assert.False(t, revsA[0].GeneratedAt.IsZero())

	revsB, err := repos.Beat.ListTitleRevisions(ctx, beatB)
	require.NoError(t, err)
	require.Len(t, revsB, 1)
	assert.Equal(t, "Beat B Title", revsB[0].Title)

	// running again must be a no-op (idempotent)
	require.NoError(t, migrateBackfillTitleRevisions(ctx, repos.DB))
	require.NoError(t, repos.DB.GetContext(ctx, &count, `SELECT COUNT(*) FROM beat_title_revisions`))
	assert.Equal(t, 2, count, "second run must not insert duplicate rows")
}
