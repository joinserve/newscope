package repository

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/umputun/newscope/pkg/domain"
)

func TestGroupingRepository_CreateAndList(t *testing.T) {
	repos, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()

	id1, err := repos.Grouping.CreateGrouping(ctx, domain.Grouping{Name: "AI News", Tags: []string{"ai", "llm"}})
	require.NoError(t, err)
	assert.Positive(t, id1)

	id2, err := repos.Grouping.CreateGrouping(ctx, domain.Grouping{Name: "Security", Tags: []string{"security"}})
	require.NoError(t, err)
	assert.Positive(t, id2)
	assert.NotEqual(t, id1, id2)

	list, err := repos.Grouping.ListGroupings(ctx)
	require.NoError(t, err)
	require.Len(t, list, 2)

	assert.Equal(t, "AI News", list[0].Name)
	assert.Equal(t, "ai-news", list[0].Slug)
	assert.Equal(t, []string{"ai", "llm"}, list[0].Tags) // normalizeTags sorts them

	assert.Equal(t, "Security", list[1].Name)
	assert.Equal(t, "security", list[1].Slug)
}

func TestGroupingRepository_GetGrouping(t *testing.T) {
	repos, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()

	id, err := repos.Grouping.CreateGrouping(ctx, domain.Grouping{Name: "AI News", Tags: []string{"ai"}})
	require.NoError(t, err)

	g, err := repos.Grouping.GetGrouping(ctx, id)
	require.NoError(t, err)
	assert.Equal(t, "AI News", g.Name)
	assert.Equal(t, "ai-news", g.Slug)
	assert.Equal(t, []string{"ai"}, g.Tags)

	_, err = repos.Grouping.GetGrouping(ctx, 99999)
	assert.ErrorContains(t, err, "grouping not found")
}

func TestGroupingRepository_GetGroupingBySlug(t *testing.T) {
	repos, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()

	_, err := repos.Grouping.CreateGrouping(ctx, domain.Grouping{Name: "AI News", Tags: []string{"ai"}})
	require.NoError(t, err)

	g, err := repos.Grouping.GetGroupingBySlug(ctx, "ai-news")
	require.NoError(t, err)
	assert.Equal(t, "AI News", g.Name)

	_, err = repos.Grouping.GetGroupingBySlug(ctx, "nonexistent")
	assert.ErrorContains(t, err, "grouping not found")
}

func TestGroupingRepository_Update(t *testing.T) {
	repos, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()

	id, err := repos.Grouping.CreateGrouping(ctx, domain.Grouping{Name: "Old Name", Tags: []string{"old"}})
	require.NoError(t, err)

	err = repos.Grouping.UpdateGrouping(ctx, domain.Grouping{ID: id, Name: "New Name", Tags: []string{"new", "tag"}})
	require.NoError(t, err)

	g, err := repos.Grouping.GetGrouping(ctx, id)
	require.NoError(t, err)
	assert.Equal(t, "New Name", g.Name)
	assert.Equal(t, "new-name", g.Slug)
	assert.Equal(t, []string{"new", "tag"}, g.Tags)
}

func TestGroupingRepository_Delete(t *testing.T) {
	repos, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()

	id, err := repos.Grouping.CreateGrouping(ctx, domain.Grouping{Name: "To Delete", Tags: []string{"x"}})
	require.NoError(t, err)

	err = repos.Grouping.DeleteGrouping(ctx, id)
	require.NoError(t, err)

	list, err := repos.Grouping.ListGroupings(ctx)
	require.NoError(t, err)
	assert.Empty(t, list)
}

func TestGroupingRepository_Reorder(t *testing.T) {
	repos, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()

	id1, err := repos.Grouping.CreateGrouping(ctx, domain.Grouping{Name: "First", Tags: []string{}})
	require.NoError(t, err)
	id2, err := repos.Grouping.CreateGrouping(ctx, domain.Grouping{Name: "Second", Tags: []string{}})
	require.NoError(t, err)

	// reorder: put Second before First
	err = repos.Grouping.ReorderGroupings(ctx, []int64{id2, id1})
	require.NoError(t, err)

	list, err := repos.Grouping.ListGroupings(ctx)
	require.NoError(t, err)
	require.Len(t, list, 2)
	assert.Equal(t, "Second", list[0].Name)
	assert.Equal(t, "First", list[1].Name)
}

func TestGroupingRepository_SlugCollision(t *testing.T) {
	repos, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()

	_, err := repos.Grouping.CreateGrouping(ctx, domain.Grouping{Name: "AI News", Tags: []string{}})
	require.NoError(t, err)

	// same name → slug collision → should get ai-news-2
	id2, err := repos.Grouping.CreateGrouping(ctx, domain.Grouping{Name: "AI News", Tags: []string{}})
	require.NoError(t, err)

	g2, err := repos.Grouping.GetGrouping(ctx, id2)
	require.NoError(t, err)
	assert.Equal(t, "ai-news-2", g2.Slug)
}

func TestGroupingRepository_TagNormalization(t *testing.T) {
	repos, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()

	// uppercase, duplicates, extra spaces
	id, err := repos.Grouping.CreateGrouping(ctx, domain.Grouping{Name: "Test", Tags: []string{"AI", " llm ", "AI", "tech"}})
	require.NoError(t, err)

	g, err := repos.Grouping.GetGrouping(ctx, id)
	require.NoError(t, err)
	// normalized: lowercase, deduped, sorted
	assert.Equal(t, []string{"ai", "llm", "tech"}, g.Tags)
}

func TestSlugify(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{"simple", "AI News", "ai-news"},
		{"special chars", "C++ Tips & Tricks", "c-tips-tricks"},
		{"leading/trailing spaces", "  hello world  ", "hello-world"},
		{"numbers", "Top 10 AI", "top-10-ai"},
		{"empty fallback", "---", "grouping"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.expected, Slugify(tc.input))
		})
	}
}

func TestGroupingRepository_BeatTagSet(t *testing.T) {
	repos, cleanup, mkItem := beatTestSetup(t)
	defer cleanup()

	ctx := context.Background()
	pub := time.Now()

	// create a beat with two members, each having different topics
	idA := mkItem(pub, "a", []float32{1, 0, 0})
	require.NoError(t, repos.Item.UpdateItemClassification(ctx, idA, &domain.Classification{Score: 5, Topics: []string{"ai", "tech"}}))
	idB := mkItem(pub.Add(time.Second), "b", []float32{1, 0, 0})
	require.NoError(t, repos.Item.UpdateItemClassification(ctx, idB, &domain.Classification{Score: 5, Topics: []string{"ai", "china"}}))

	beatID, _, err := repos.Beat.AttachOrSeed(ctx, domain.BeatCandidate{ItemID: idA, Vector: []float32{1, 0, 0}, PublishedAt: pub}, 0.85, 48*time.Hour, 20)
	require.NoError(t, err)
	_, _, err = repos.Beat.AttachOrSeed(ctx, domain.BeatCandidate{ItemID: idB, Vector: []float32{1, 0, 0}, PublishedAt: pub.Add(time.Second)}, 0.85, 48*time.Hour, 20)
	require.NoError(t, err)

	tags, err := repos.Grouping.BeatTagSet(ctx, beatID)
	require.NoError(t, err)

	// union of both members: ai, tech, china (ai deduplicated)
	assert.ElementsMatch(t, []string{"ai", "tech", "china"}, tags)
}

func TestGroupingRepository_BeatTagSet_IncludesEntities(t *testing.T) {
	repos, cleanup, mkItem := beatTestSetup(t)
	defer cleanup()

	ctx := context.Background()
	pub := time.Now()

	// item A: topics ai/tech; item B: entities claude (no topics overlap)
	idA := mkItem(pub, "a", []float32{1, 0, 0})
	require.NoError(t, repos.Item.UpdateItemClassification(ctx, idA, &domain.Classification{Score: 5, Topics: []string{"ai", "tech"}}))
	idB := mkItem(pub.Add(time.Second), "b", []float32{1, 0, 0})
	require.NoError(t, repos.Item.UpdateItemClassification(ctx, idB, &domain.Classification{Score: 5, Topics: []string{}}))
	require.NoError(t, repos.Item.SaveEntities(ctx, idB, []string{"claude", "anthropic"}))

	beatID, _, err := repos.Beat.AttachOrSeed(ctx, domain.BeatCandidate{ItemID: idA, Vector: []float32{1, 0, 0}, PublishedAt: pub}, 0.85, 48*time.Hour, 20)
	require.NoError(t, err)
	_, _, err = repos.Beat.AttachOrSeed(ctx, domain.BeatCandidate{ItemID: idB, Vector: []float32{1, 0, 0}, PublishedAt: pub.Add(time.Second)}, 0.85, 48*time.Hour, 20)
	require.NoError(t, err)

	tags, err := repos.Grouping.BeatTagSet(ctx, beatID)
	require.NoError(t, err)

	// topics from A + entities from B are all present
	assert.ElementsMatch(t, []string{"ai", "tech", "claude", "anthropic"}, tags)
}

func TestGroupingRepository_UpsertAssignment(t *testing.T) {
	repos, cleanup, mkItem := beatTestSetup(t)
	defer cleanup()

	ctx := context.Background()
	pub := time.Now()
	itemID := mkItem(pub, "x", []float32{1, 0, 0})
	beatID, _, err := repos.Beat.AttachOrSeed(ctx, domain.BeatCandidate{ItemID: itemID, Vector: []float32{1, 0, 0}, PublishedAt: pub}, 0.85, 48*time.Hour, 20)
	require.NoError(t, err)

	gid, err := repos.Grouping.CreateGrouping(ctx, domain.Grouping{Name: "G1", Tags: []string{"ai"}})
	require.NoError(t, err)

	// assign
	require.NoError(t, repos.Grouping.UpsertAssignment(ctx, beatID, &gid))

	var got *int64
	require.NoError(t, repos.DB.GetContext(ctx, &got,
		`SELECT grouping_id FROM beat_grouping_assignments WHERE beat_id = ?`, beatID))
	require.NotNil(t, got)
	assert.Equal(t, gid, *got)

	// reassign to nil (matched nothing)
	require.NoError(t, repos.Grouping.UpsertAssignment(ctx, beatID, nil))
	require.NoError(t, repos.DB.GetContext(ctx, &got,
		`SELECT grouping_id FROM beat_grouping_assignments WHERE beat_id = ?`, beatID))
	assert.Nil(t, got)
}

func TestGroupingRepository_ActiveBeatIDs(t *testing.T) {
	repos, cleanup, mkItem := beatTestSetup(t)
	defer cleanup()

	ctx := context.Background()
	now := time.Now()

	// beat within window
	idA := mkItem(now, "a", []float32{1, 0, 0})
	bA, _, err := repos.Beat.AttachOrSeed(ctx, domain.BeatCandidate{ItemID: idA, Vector: []float32{1, 0, 0}, PublishedAt: now}, 0.85, 48*time.Hour, 20)
	require.NoError(t, err)

	ids, err := repos.Grouping.ActiveBeatIDs(ctx, now.Add(-24*time.Hour))
	require.NoError(t, err)
	assert.Contains(t, ids, bA)

	// query with future cutoff → beat is older, excluded
	ids, err = repos.Grouping.ActiveBeatIDs(ctx, now.Add(time.Minute))
	require.NoError(t, err)
	assert.NotContains(t, ids, bA)
}

func TestGroupingRepository_GroupingCounts(t *testing.T) {
	repos, cleanup, mkItem := beatTestSetup(t)
	defer cleanup()

	ctx := context.Background()
	pub := time.Now()

	// grouping G1
	gid1, err := repos.Grouping.CreateGrouping(ctx, domain.Grouping{Name: "G1", Tags: []string{"ai"}})
	require.NoError(t, err)

	// beat assigned to G1
	idA := mkItem(pub, "a", []float32{1, 0, 0})
	bA, _, err := repos.Beat.AttachOrSeed(ctx, domain.BeatCandidate{ItemID: idA, Vector: []float32{1, 0, 0}, PublishedAt: pub}, 0.85, 48*time.Hour, 20)
	require.NoError(t, err)
	require.NoError(t, repos.Grouping.UpsertAssignment(ctx, bA, &gid1))

	// unassigned beat (goes to main inbox)
	idB := mkItem(pub.Add(time.Hour), "b", []float32{0, 1, 0})
	bB, _, err := repos.Beat.AttachOrSeed(ctx, domain.BeatCandidate{ItemID: idB, Vector: []float32{0, 1, 0}, PublishedAt: pub.Add(time.Hour)}, 0.85, 48*time.Hour, 20)
	require.NoError(t, err)
	_ = bB

	counts, err := repos.Grouping.GroupingCounts(ctx)
	require.NoError(t, err)

	assert.Equal(t, 1, counts[gid1], "G1 should have 1 unread beat")
	assert.Equal(t, 1, counts[0], "main inbox (key 0) should have 1 unassigned unread beat")
}

func TestBeatRepository_ListBeats_GroupingFilter(t *testing.T) {
	repos, cleanup, mkItem := beatTestSetup(t)
	defer cleanup()

	ctx := context.Background()
	pub := time.Now()

	gid1, err := repos.Grouping.CreateGrouping(ctx, domain.Grouping{Name: "G1", Tags: []string{"ai"}})
	require.NoError(t, err)
	gid2, err := repos.Grouping.CreateGrouping(ctx, domain.Grouping{Name: "G2", Tags: []string{"china"}})
	require.NoError(t, err)

	// beat A → assigned to G1
	idA := mkItem(pub, "a", []float32{1, 0, 0})
	bA, _, err := repos.Beat.AttachOrSeed(ctx, domain.BeatCandidate{ItemID: idA, Vector: []float32{1, 0, 0}, PublishedAt: pub}, 0.85, 48*time.Hour, 20)
	require.NoError(t, err)
	require.NoError(t, repos.Grouping.UpsertAssignment(ctx, bA, &gid1))

	// beat B → assigned to G2
	idB := mkItem(pub.Add(time.Hour), "b", []float32{0, 1, 0})
	bB, _, err := repos.Beat.AttachOrSeed(ctx, domain.BeatCandidate{ItemID: idB, Vector: []float32{0, 1, 0}, PublishedAt: pub.Add(time.Hour)}, 0.85, 48*time.Hour, 20)
	require.NoError(t, err)
	require.NoError(t, repos.Grouping.UpsertAssignment(ctx, bB, &gid2))

	// beat C → unassigned (main inbox)
	idC := mkItem(pub.Add(2*time.Hour), "c", []float32{0, 0, 1})
	bC, _, err := repos.Beat.AttachOrSeed(ctx, domain.BeatCandidate{ItemID: idC, Vector: []float32{0, 0, 1}, PublishedAt: pub.Add(2 * time.Hour)}, 0.85, 48*time.Hour, 20)
	require.NoError(t, err)
	require.NoError(t, repos.Grouping.UpsertAssignment(ctx, bC, nil))

	// beat D → no assignment row at all (also appears in main inbox)
	idD := mkItem(pub.Add(3*time.Hour), "d", []float32{0.5, 0, 0.5})
	bD, _, err := repos.Beat.AttachOrSeed(ctx, domain.BeatCandidate{ItemID: idD, Vector: []float32{0.5, 0, 0.5}, PublishedAt: pub.Add(3 * time.Hour)}, 0.85, 48*time.Hour, 20)
	require.NoError(t, err)

	t.Run("main inbox: nil groupingID returns unassigned beats", func(t *testing.T) {
		beats, err := repos.Beat.ListBeats(ctx, nil, "", 10, 0)
		require.NoError(t, err)
		var ids []int64
		for _, b := range beats {
			ids = append(ids, b.ID)
		}
		assert.NotContains(t, ids, bA, "G1-assigned beat must not appear in main inbox")
		assert.NotContains(t, ids, bB, "G2-assigned beat must not appear in main inbox")
		assert.Contains(t, ids, bC, "nil-assignment beat must appear in main inbox")
		assert.Contains(t, ids, bD, "no-row beat must appear in main inbox")
	})

	t.Run("grouping filter returns only assigned beats", func(t *testing.T) {
		beats, err := repos.Beat.ListBeats(ctx, &gid1, "", 10, 0)
		require.NoError(t, err)
		require.Len(t, beats, 1)
		assert.Equal(t, bA, beats[0].ID)

		beats, err = repos.Beat.ListBeats(ctx, &gid2, "", 10, 0)
		require.NoError(t, err)
		require.Len(t, beats, 1)
		assert.Equal(t, bB, beats[0].ID)
	})
}

func TestItemRepository_SaveAndListPendingEntities(t *testing.T) {
	repos, cleanup, mkItem := beatTestSetup(t)
	defer cleanup()

	ctx := context.Background()
	pub := time.Now()

	// mkItem always classifies items; both A and B start as pending
	idA := mkItem(pub, "a", []float32{1, 0, 0})
	idB := mkItem(pub.Add(time.Second), "b", []float32{0, 1, 0})

	pending, err := repos.Item.ListPendingEntities(ctx, 10)
	require.NoError(t, err)
	require.Len(t, pending, 2)

	// save entities for A → A is no longer pending
	require.NoError(t, repos.Item.SaveEntities(ctx, idA, []string{"claude", "anthropic"}))

	pending, err = repos.Item.ListPendingEntities(ctx, 10)
	require.NoError(t, err)
	require.Len(t, pending, 1)
	assert.Equal(t, idB, pending[0].ID)
}

func TestItemRepository_BeatForItem(t *testing.T) {
	repos, cleanup, mkItem := beatTestSetup(t)
	defer cleanup()

	ctx := context.Background()
	pub := time.Now()

	idA := mkItem(pub, "a", []float32{1, 0, 0})
	beatID, _, err := repos.Beat.AttachOrSeed(ctx, domain.BeatCandidate{ItemID: idA, Vector: []float32{1, 0, 0}, PublishedAt: pub}, 0.85, 48*time.Hour, 20)
	require.NoError(t, err)

	gotBeat, ok, err := repos.Item.BeatForItem(ctx, idA)
	require.NoError(t, err)
	assert.True(t, ok)
	assert.Equal(t, beatID, gotBeat)

	// item not in any beat
	idB := mkItem(pub.Add(time.Second), "b", []float32{0, 1, 0})
	_, ok, err = repos.Item.BeatForItem(ctx, idB)
	require.NoError(t, err)
	assert.False(t, ok)
}

func TestGroupingRepository_SuggestTags(t *testing.T) {
	repos, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()
	feed := createTestFeed(t, repos, "test-feed")

	// seed items with topics and entities
	_, err := repos.DB.ExecContext(ctx, `
		INSERT INTO items (feed_id, guid, title, link, published, topics, entities, relevance_score, classified_at)
		VALUES
			(?, 'g1', 'T1', 'http://a', datetime('now'), '["anthropic","ai","llm"]', '["claude","openai"]', 8.0, datetime('now')),
			(?, 'g2', 'T2', 'http://b', datetime('now'), '["security","privacy"]', '["apple","google"]', 7.0, datetime('now'))`,
		feed.ID, feed.ID)
	require.NoError(t, err)

	t.Run("prefix match", func(t *testing.T) {
		tags, err := repos.Grouping.SuggestTags(ctx, "an", 50)
		require.NoError(t, err)
		assert.Contains(t, tags, "anthropic")
		assert.NotContains(t, tags, "ai") // "ai" doesn't start with "an"
	})

	t.Run("empty prefix returns all", func(t *testing.T) {
		tags, err := repos.Grouping.SuggestTags(ctx, "", 100)
		require.NoError(t, err)
		assert.Contains(t, tags, "anthropic")
		assert.Contains(t, tags, "claude")
		assert.Contains(t, tags, "security")
		assert.Contains(t, tags, "apple")
	})

	t.Run("case insensitive", func(t *testing.T) {
		tags, err := repos.Grouping.SuggestTags(ctx, "ANT", 50)
		require.NoError(t, err)
		assert.Contains(t, tags, "anthropic")
	})

	t.Run("limit respected", func(t *testing.T) {
		tags, err := repos.Grouping.SuggestTags(ctx, "", 2)
		require.NoError(t, err)
		assert.Len(t, tags, 2)
	})
}
