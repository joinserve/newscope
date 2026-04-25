package repository

import (
	"context"
	"testing"

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
