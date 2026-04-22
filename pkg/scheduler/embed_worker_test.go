package scheduler

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/umputun/newscope/pkg/domain"
	"github.com/umputun/newscope/pkg/scheduler/mocks"
)

func TestEmbedWorker_ProcessBatch(t *testing.T) {
	items := []domain.Item{
		{ID: 1, Title: "Article One", Summary: "Summary one"},
		{ID: 2, Title: "Article Two", Summary: ""},
	}

	embedder := &mocks.EmbedderMock{
		EmbedFunc: func(ctx context.Context, text string) ([]float32, error) {
			return []float32{0.1, 0.2, 0.3}, nil
		},
	}
	store := &mocks.EmbedStoreMock{
		PutEmbeddingFunc: func(ctx context.Context, itemID int64, model string, v []float32) error {
			return nil
		},
	}
	itemMgr := &mocks.ItemManagerMock{
		GetUnembeddedItemsFunc: func(ctx context.Context, limit int) ([]domain.Item, error) {
			return items, nil
		},
	}

	w := NewEmbedWorker(EmbedWorkerConfig{
		Embedder:  embedder,
		Store:     store,
		Items:     itemMgr,
		Model:     "test-model",
		Interval:  time.Minute,
		BatchSize: 10,
	})

	w.processBatch(context.Background())

	assert.Len(t, embedder.EmbedCalls(), 2)
	assert.Len(t, store.PutEmbeddingCalls(), 2)

	// first item uses title + summary
	assert.Equal(t, "Article One Summary one", embedder.EmbedCalls()[0].Text)
	// second item has no summary — use title only
	assert.Equal(t, "Article Two", embedder.EmbedCalls()[1].Text)

	// verify model is passed through
	assert.Equal(t, "test-model", store.PutEmbeddingCalls()[0].Model)
}

func TestEmbedWorker_EmbedderFailure(t *testing.T) {
	items := []domain.Item{{ID: 1, Title: "Failing item", Summary: "s"}}

	embedder := &mocks.EmbedderMock{
		EmbedFunc: func(ctx context.Context, text string) ([]float32, error) {
			return nil, errors.New("rate limited")
		},
	}
	store := &mocks.EmbedStoreMock{}
	itemMgr := &mocks.ItemManagerMock{
		GetUnembeddedItemsFunc: func(ctx context.Context, limit int) ([]domain.Item, error) {
			return items, nil
		},
	}

	w := NewEmbedWorker(EmbedWorkerConfig{
		Embedder: embedder, Store: store, Items: itemMgr, Model: "m", Interval: time.Minute,
	})

	// must not panic or crash
	require.NotPanics(t, func() { w.processBatch(context.Background()) })
	assert.Empty(t, store.PutEmbeddingCalls())
}

func TestEmbedWorker_StoreFailure(t *testing.T) {
	items := []domain.Item{{ID: 1, Title: "Article", Summary: "s"}}

	embedder := &mocks.EmbedderMock{
		EmbedFunc: func(ctx context.Context, text string) ([]float32, error) {
			return []float32{0.1}, nil
		},
	}
	store := &mocks.EmbedStoreMock{
		PutEmbeddingFunc: func(ctx context.Context, itemID int64, model string, v []float32) error {
			return errors.New("db locked")
		},
	}
	itemMgr := &mocks.ItemManagerMock{
		GetUnembeddedItemsFunc: func(ctx context.Context, limit int) ([]domain.Item, error) {
			return items, nil
		},
	}

	w := NewEmbedWorker(EmbedWorkerConfig{
		Embedder: embedder, Store: store, Items: itemMgr, Model: "m", Interval: time.Minute,
	})

	require.NotPanics(t, func() { w.processBatch(context.Background()) })
	assert.Len(t, embedder.EmbedCalls(), 1)
	assert.Len(t, store.PutEmbeddingCalls(), 1)
}

func TestEmbedWorker_EmptyBatch(t *testing.T) {
	embedder := &mocks.EmbedderMock{}
	store := &mocks.EmbedStoreMock{}
	itemMgr := &mocks.ItemManagerMock{
		GetUnembeddedItemsFunc: func(ctx context.Context, limit int) ([]domain.Item, error) {
			return []domain.Item{}, nil
		},
	}

	w := NewEmbedWorker(EmbedWorkerConfig{
		Embedder: embedder, Store: store, Items: itemMgr, Model: "m", Interval: time.Minute,
	})

	w.processBatch(context.Background())
	assert.Empty(t, embedder.EmbedCalls())
}

func TestEmbedWorker_ContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	callCount := 0
	embedder := &mocks.EmbedderMock{
		EmbedFunc: func(ctx context.Context, text string) ([]float32, error) {
			callCount++
			return []float32{0.1}, nil
		},
	}
	store := &mocks.EmbedStoreMock{
		PutEmbeddingFunc: func(ctx context.Context, itemID int64, model string, v []float32) error {
			return nil
		},
	}
	itemMgr := &mocks.ItemManagerMock{
		GetUnembeddedItemsFunc: func(ctx context.Context, limit int) ([]domain.Item, error) {
			// return many items; worker should stop early due to canceled context
			items := make([]domain.Item, 10)
			for i := range items {
				items[i] = domain.Item{ID: int64(i + 1), Title: "Article"}
			}
			return items, nil
		},
	}

	w := NewEmbedWorker(EmbedWorkerConfig{
		Embedder: embedder, Store: store, Items: itemMgr, Model: "m", Interval: time.Minute,
	})

	// run with already-canceled context; must return quickly
	done := make(chan struct{})
	go func() {
		w.Run(ctx)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after context cancellation")
	}
}

func TestEmbedWorker_DefaultBatchSize(t *testing.T) {
	itemMgr := &mocks.ItemManagerMock{
		GetUnembeddedItemsFunc: func(ctx context.Context, limit int) ([]domain.Item, error) {
			assert.Equal(t, defaultEmbedBatchSize, limit)
			return nil, nil
		},
	}
	w := NewEmbedWorker(EmbedWorkerConfig{
		Embedder:  &mocks.EmbedderMock{},
		Store:     &mocks.EmbedStoreMock{},
		Items:     itemMgr,
		Model:     "m",
		Interval:  time.Minute,
		BatchSize: 0, // triggers default
	})
	w.processBatch(context.Background())
	assert.Len(t, itemMgr.GetUnembeddedItemsCalls(), 1)
}

func TestEmbeddingText(t *testing.T) {
	tests := []struct {
		title, summary, want string
	}{
		{"Title", "Summary", "Title Summary"},
		{"Title", "", "Title"},
		{"", "Summary", " Summary"},
	}
	for _, tc := range tests {
		assert.Equal(t, tc.want, embeddingText(tc.title, tc.summary))
	}
}
