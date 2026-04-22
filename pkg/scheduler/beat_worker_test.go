package scheduler

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/umputun/newscope/pkg/domain"
	"github.com/umputun/newscope/pkg/scheduler/mocks"
)

func TestBeatWorker_ProcessBatch(t *testing.T) {
	items := []domain.BeatCandidate{
		{ItemID: 1, Vector: []float32{1, 0, 0}, PublishedAt: time.Now()},
		{ItemID: 2, Vector: []float32{0.99, 0.1, 0}, PublishedAt: time.Now().Add(time.Hour)},
		{ItemID: 3, Vector: []float32{0, 1, 0}, PublishedAt: time.Now().Add(2 * time.Hour)},
	}
	attachCalls := 0
	store := &mocks.BeatStoreMock{
		GetUnbeatItemsFunc: func(ctx context.Context, limit int) ([]domain.BeatCandidate, error) { return items, nil },
		AttachOrSeedFunc: func(ctx context.Context, item domain.BeatCandidate, threshold float64, window time.Duration, maxMembers int) (int64, error) {
			attachCalls++
			return int64(attachCalls), nil
		},
	}

	w := NewBeatWorker(BeatWorkerConfig{
		Store: store, Threshold: 0.85, Window: 48 * time.Hour, MaxMembers: 20,
		Interval: time.Minute, BatchSize: 10,
	})
	w.processBatch(context.Background())

	assert.Len(t, store.AttachOrSeedCalls(), 3)
	// parameters threaded through
	c0 := store.AttachOrSeedCalls()[0]
	assert.InDelta(t, 0.85, c0.Threshold, 1e-9)
	assert.Equal(t, 48*time.Hour, c0.Window)
	assert.Equal(t, 20, c0.MaxMembers)
}

func TestBeatWorker_HandlesEmptyBatch(t *testing.T) {
	store := &mocks.BeatStoreMock{
		GetUnbeatItemsFunc: func(ctx context.Context, limit int) ([]domain.BeatCandidate, error) { return nil, nil },
	}
	w := NewBeatWorker(BeatWorkerConfig{Store: store, Interval: time.Minute})
	// must not panic or call AttachOrSeed
	w.processBatch(context.Background())
	assert.Empty(t, store.AttachOrSeedCalls())
}

func TestBeatWorker_ContinuesOnAttachError(t *testing.T) {
	items := []domain.BeatCandidate{
		{ItemID: 1, Vector: []float32{1, 0, 0}, PublishedAt: time.Now()},
		{ItemID: 2, Vector: []float32{1, 0, 0}, PublishedAt: time.Now()},
	}
	store := &mocks.BeatStoreMock{
		GetUnbeatItemsFunc: func(ctx context.Context, limit int) ([]domain.BeatCandidate, error) { return items, nil },
		AttachOrSeedFunc: func(ctx context.Context, item domain.BeatCandidate, threshold float64, window time.Duration, maxMembers int) (int64, error) {
			if item.ItemID == 1 {
				return 0, errors.New("simulated failure")
			}
			return 10, nil
		},
	}
	w := NewBeatWorker(BeatWorkerConfig{Store: store, Interval: time.Minute, BatchSize: 10})
	w.processBatch(context.Background())
	// both items attempted despite first failing
	assert.Len(t, store.AttachOrSeedCalls(), 2)
}

func TestBeatWorker_RunTickCancels(t *testing.T) {
	store := &mocks.BeatStoreMock{
		GetUnbeatItemsFunc: func(ctx context.Context, limit int) ([]domain.BeatCandidate, error) { return nil, nil },
	}
	w := NewBeatWorker(BeatWorkerConfig{Store: store, Interval: 10 * time.Millisecond})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { w.Run(ctx); close(done) }()

	time.Sleep(30 * time.Millisecond)
	cancel()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("BeatWorker.Run did not exit after context cancel")
	}
	// at least one initial call + some tick calls
	assert.GreaterOrEqual(t, len(store.GetUnbeatItemsCalls()), 1)
}
