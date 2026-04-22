package repository

import (
	"context"
	"math"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/umputun/newscope/pkg/domain"
)

func TestEmbeddingRepository_PutEmbedding(t *testing.T) {
	repos, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()
	feed := createTestFeed(t, repos, "Test Feed")

	item := &domain.Item{
		FeedID:    feed.ID,
		GUID:      "test-item-1",
		Title:     "Test Article",
		Link:      "https://example.com/article",
		Published: time.Now(),
	}
	require.NoError(t, repos.Item.CreateItem(ctx, item))

	vec := []float32{0.1, 0.2, 0.3, 0.4}

	t.Run("insert new embedding", func(t *testing.T) {
		err := repos.Embedding.PutEmbedding(ctx, item.ID, "text-embedding-3-small", vec)
		require.NoError(t, err)

		var count int
		err = repos.DB.GetContext(ctx, &count, "SELECT COUNT(*) FROM item_embeddings WHERE item_id = ?", item.ID)
		require.NoError(t, err)
		assert.Equal(t, 1, count)
	})

	t.Run("replace existing embedding", func(t *testing.T) {
		updated := []float32{0.5, 0.6, 0.7, 0.8}
		err := repos.Embedding.PutEmbedding(ctx, item.ID, "text-embedding-3-small", updated)
		require.NoError(t, err)

		var count int
		err = repos.DB.GetContext(ctx, &count, "SELECT COUNT(*) FROM item_embeddings WHERE item_id = ?", item.ID)
		require.NoError(t, err)
		assert.Equal(t, 1, count)
	})
}

func TestEmbeddingRepository_PutEmbedding_CascadeDelete(t *testing.T) {
	repos, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()
	feed := createTestFeed(t, repos, "Test Feed")

	item := &domain.Item{
		FeedID:    feed.ID,
		GUID:      "cascade-test-item",
		Title:     "Cascade Test",
		Link:      "https://example.com/cascade",
		Published: time.Now(),
	}
	require.NoError(t, repos.Item.CreateItem(ctx, item))
	require.NoError(t, repos.Embedding.PutEmbedding(ctx, item.ID, "model", []float32{1.0, 2.0}))

	// deleting the item must cascade-delete the embedding
	_, err := repos.DB.ExecContext(ctx, "DELETE FROM items WHERE id = ?", item.ID)
	require.NoError(t, err)

	var count int
	err = repos.DB.GetContext(ctx, &count, "SELECT COUNT(*) FROM item_embeddings WHERE item_id = ?", item.ID)
	require.NoError(t, err)
	assert.Equal(t, 0, count)
}

func TestFloat32sToBlob(t *testing.T) {
	tests := []struct {
		name string
		v    []float32
	}{
		{"empty", []float32{}},
		{"single", []float32{1.5}},
		{"multi", []float32{0.1, -0.5, 3.14, math.MaxFloat32}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			blob := float32sToBlob(tc.v)
			assert.Len(t, blob, len(tc.v)*4)
		})
	}
}

func TestItemRepository_GetUnembeddedItems(t *testing.T) {
	repos, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()
	feed := createTestFeed(t, repos, "Test Feed")

	// helper to insert a classified item with a summary
	classify := func(guid, title string) *domain.Item {
		item := &domain.Item{
			FeedID:    feed.ID,
			GUID:      guid,
			Title:     title,
			Link:      "https://example.com/" + guid,
			Published: time.Now(),
		}
		require.NoError(t, repos.Item.CreateItem(ctx, item))
		_, err := repos.DB.ExecContext(ctx,
			`UPDATE items SET classified_at = datetime('now'), summary = 'summary of '+title WHERE id = ?`,
			item.ID)
		require.NoError(t, err)
		return item
	}

	itemA := classify("item-a", "Article A")
	itemB := classify("item-b", "Article B")
	itemC := classify("item-c", "Article C") // will get an embedding

	// embed itemC so it is excluded
	require.NoError(t, repos.Embedding.PutEmbedding(ctx, itemC.ID, "model", []float32{1.0}))

	t.Run("returns only unembedded classified items", func(t *testing.T) {
		items, err := repos.Item.GetUnembeddedItems(ctx, 10)
		require.NoError(t, err)
		ids := make([]int64, len(items))
		for i, item := range items {
			ids[i] = item.ID
		}
		assert.Contains(t, ids, itemA.ID)
		assert.Contains(t, ids, itemB.ID)
		assert.NotContains(t, ids, itemC.ID)
	})

	t.Run("respects limit", func(t *testing.T) {
		items, err := repos.Item.GetUnembeddedItems(ctx, 1)
		require.NoError(t, err)
		assert.Len(t, items, 1)
	})

	t.Run("unclassified items excluded", func(t *testing.T) {
		unclassified := &domain.Item{
			FeedID:    feed.ID,
			GUID:      "unclassified",
			Title:     "Not yet classified",
			Link:      "https://example.com/unclassified",
			Published: time.Now(),
		}
		require.NoError(t, repos.Item.CreateItem(ctx, unclassified))

		items, err := repos.Item.GetUnembeddedItems(ctx, 100)
		require.NoError(t, err)
		for _, item := range items {
			assert.NotEqual(t, unclassified.ID, item.ID)
		}
	})
}
