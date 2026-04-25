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

func TestMergeWorker_ProcessBatch(t *testing.T) {
	beats := []domain.Beat{
		{ID: 1, Members: []domain.ClassifiedItem{
			{Item: &domain.Item{Title: "A1"}},
			{Item: &domain.Item{Title: "A2"}},
		}},
		{ID: 2, Members: []domain.ClassifiedItem{
			{Item: &domain.Item{Title: "B1"}},
			{Item: &domain.Item{Title: "B2"}},
		}},
	}
	store := &mocks.BeatStoreMock{
		ListPendingMergeFunc: func(ctx context.Context, limit int) ([]domain.Beat, error) {
			return beats, nil
		},
		SaveCanonicalFunc: func(ctx context.Context, beatID int64, c domain.BeatCanonical) error {
			return nil
		},
		AppendTitleRevisionFunc: func(ctx context.Context, beatID int64, title, summary string) error {
			return nil
		},
	}
	merger := &mocks.MergerMock{
		MergeFunc: func(ctx context.Context, members []domain.ClassifiedItem) (domain.BeatCanonical, error) {
			return domain.BeatCanonical{Title: "Canonical Title", Summary: "Canonical summary."}, nil
		},
	}

	w := NewMergeWorker(MergeWorkerConfig{
		Store: store, Merger: merger, Interval: time.Minute, BatchSize: 10,
	})
	w.processBatch(context.Background())

	assert.Len(t, store.ListPendingMergeCalls(), 1)
	assert.Len(t, merger.MergeCalls(), 2)
	assert.Len(t, store.SaveCanonicalCalls(), 2)

	// verify beat IDs are threaded correctly
	assert.Equal(t, int64(1), store.SaveCanonicalCalls()[0].BeatID)
	assert.Equal(t, int64(2), store.SaveCanonicalCalls()[1].BeatID)
	// verify canonical values are forwarded
	assert.Equal(t, "Canonical Title", store.SaveCanonicalCalls()[0].C.Title)
	assert.Equal(t, "Canonical summary.", store.SaveCanonicalCalls()[0].C.Summary)
}

func TestMergeWorker_HandlesEmptyBatch(t *testing.T) {
	store := &mocks.BeatStoreMock{
		ListPendingMergeFunc: func(ctx context.Context, limit int) ([]domain.Beat, error) {
			return nil, nil
		},
	}
	merger := &mocks.MergerMock{}

	w := NewMergeWorker(MergeWorkerConfig{Store: store, Merger: merger, Interval: time.Minute})
	w.processBatch(context.Background())

	assert.Empty(t, merger.MergeCalls())
	assert.Empty(t, store.SaveCanonicalCalls())
}

func TestMergeWorker_ContinuesOnMergeError(t *testing.T) {
	beats := []domain.Beat{
		{ID: 1, Members: []domain.ClassifiedItem{{Item: &domain.Item{Title: "A"}}, {Item: &domain.Item{Title: "B"}}}},
		{ID: 2, Members: []domain.ClassifiedItem{{Item: &domain.Item{Title: "C"}}, {Item: &domain.Item{Title: "D"}}}},
	}
	store := &mocks.BeatStoreMock{
		ListPendingMergeFunc: func(ctx context.Context, limit int) ([]domain.Beat, error) { return beats, nil },
		SaveCanonicalFunc:    func(ctx context.Context, beatID int64, c domain.BeatCanonical) error { return nil },
		AppendTitleRevisionFunc: func(ctx context.Context, beatID int64, title, summary string) error {
			return nil
		},
	}
	merger := &mocks.MergerMock{
		MergeFunc: func(ctx context.Context, members []domain.ClassifiedItem) (domain.BeatCanonical, error) {
			// fail the first beat, succeed for the second
			if members[0].Title == "A" {
				return domain.BeatCanonical{}, errors.New("simulated merger failure")
			}
			return domain.BeatCanonical{Title: "T", Summary: "S"}, nil
		},
	}

	w := NewMergeWorker(MergeWorkerConfig{Store: store, Merger: merger, Interval: time.Minute, BatchSize: 10})
	w.processBatch(context.Background())

	// both beats attempted
	assert.Len(t, merger.MergeCalls(), 2)
	// only second beat saved
	assert.Len(t, store.SaveCanonicalCalls(), 1)
	assert.Equal(t, int64(2), store.SaveCanonicalCalls()[0].BeatID)
}

func TestMergeWorker_ContinuesOnSaveError(t *testing.T) {
	beats := []domain.Beat{
		{ID: 1, Members: []domain.ClassifiedItem{{Item: &domain.Item{Title: "A"}}, {Item: &domain.Item{Title: "B"}}}},
		{ID: 2, Members: []domain.ClassifiedItem{{Item: &domain.Item{Title: "C"}}, {Item: &domain.Item{Title: "D"}}}},
	}
	saveCount := 0
	store := &mocks.BeatStoreMock{
		ListPendingMergeFunc: func(ctx context.Context, limit int) ([]domain.Beat, error) { return beats, nil },
		SaveCanonicalFunc: func(ctx context.Context, beatID int64, c domain.BeatCanonical) error {
			saveCount++
			if beatID == 1 {
				return errors.New("db error")
			}
			return nil
		},
		AppendTitleRevisionFunc: func(ctx context.Context, beatID int64, title, summary string) error {
			return nil
		},
	}
	merger := &mocks.MergerMock{
		MergeFunc: func(ctx context.Context, members []domain.ClassifiedItem) (domain.BeatCanonical, error) {
			return domain.BeatCanonical{Title: "T", Summary: "S"}, nil
		},
	}

	w := NewMergeWorker(MergeWorkerConfig{Store: store, Merger: merger, Interval: time.Minute, BatchSize: 10})
	w.processBatch(context.Background())

	assert.Len(t, merger.MergeCalls(), 2)
	assert.Equal(t, 2, saveCount)
}

func TestMergeWorker_RunTickCancels(t *testing.T) {
	store := &mocks.BeatStoreMock{
		ListPendingMergeFunc: func(ctx context.Context, limit int) ([]domain.Beat, error) { return nil, nil },
	}
	merger := &mocks.MergerMock{}

	w := NewMergeWorker(MergeWorkerConfig{Store: store, Merger: merger, Interval: 10 * time.Millisecond})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { w.Run(ctx); close(done) }()

	time.Sleep(30 * time.Millisecond)
	cancel()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("MergeWorker.Run did not exit after context cancel")
	}
	assert.GreaterOrEqual(t, len(store.ListPendingMergeCalls()), 1)
}

func TestMergeWorker_ProcessesRemerge(t *testing.T) {
	// a re-merge beat already has canonical fields set; worker must treat it identically
	beat := domain.Beat{
		ID: 42,
		Members: []domain.ClassifiedItem{
			{Item: &domain.Item{Title: "Old A"}},
			{Item: &domain.Item{Title: "Old B"}},
			{Item: &domain.Item{Title: "New C"}}, // new member that triggered re-merge
		},
	}
	store := &mocks.BeatStoreMock{
		ListPendingMergeFunc: func(ctx context.Context, limit int) ([]domain.Beat, error) {
			return []domain.Beat{beat}, nil
		},
		SaveCanonicalFunc: func(ctx context.Context, beatID int64, c domain.BeatCanonical) error {
			return nil
		},
		AppendTitleRevisionFunc: func(ctx context.Context, beatID int64, title, summary string) error {
			return nil
		},
	}
	merger := &mocks.MergerMock{
		MergeFunc: func(ctx context.Context, members []domain.ClassifiedItem) (domain.BeatCanonical, error) {
			return domain.BeatCanonical{Title: "Updated Title", Summary: "Updated summary."}, nil
		},
	}

	w := NewMergeWorker(MergeWorkerConfig{Store: store, Merger: merger, Interval: time.Minute, BatchSize: 10})
	w.processBatch(context.Background())

	require.Len(t, merger.MergeCalls(), 1)
	assert.Len(t, merger.MergeCalls()[0].Members, 3, "all three members passed to Merger")
	require.Len(t, store.SaveCanonicalCalls(), 1)
	assert.Equal(t, int64(42), store.SaveCanonicalCalls()[0].BeatID)
	assert.Equal(t, "Updated Title", store.SaveCanonicalCalls()[0].C.Title)
}

func TestMergeWorker_DefaultBatchSize(t *testing.T) {
	store := &mocks.BeatStoreMock{
		ListPendingMergeFunc: func(ctx context.Context, limit int) ([]domain.Beat, error) {
			assert.Equal(t, defaultMergeBatchSize, limit)
			return nil, nil
		},
	}
	merger := &mocks.MergerMock{}
	w := NewMergeWorker(MergeWorkerConfig{Store: store, Merger: merger, Interval: time.Minute})
	w.processBatch(context.Background())
}

func TestMergeWorker_AppendTitleRevision_CalledAfterSave(t *testing.T) {
	// two beats with different content — worker calls AppendTitleRevision for each
	beats := []domain.Beat{
		{ID: 10, Members: []domain.ClassifiedItem{
			{Item: &domain.Item{Title: "X1"}},
			{Item: &domain.Item{Title: "X2"}},
		}},
		{ID: 20, Members: []domain.ClassifiedItem{
			{Item: &domain.Item{Title: "Y1"}},
			{Item: &domain.Item{Title: "Y2"}},
		}},
	}

	callCount := 0
	store := &mocks.BeatStoreMock{
		ListPendingMergeFunc: func(ctx context.Context, limit int) ([]domain.Beat, error) { return beats, nil },
		SaveCanonicalFunc:    func(ctx context.Context, beatID int64, c domain.BeatCanonical) error { return nil },
		AppendTitleRevisionFunc: func(ctx context.Context, beatID int64, title, summary string) error {
			callCount++
			return nil
		},
	}
	merger := &mocks.MergerMock{
		MergeFunc: func(ctx context.Context, members []domain.ClassifiedItem) (domain.BeatCanonical, error) {
			return domain.BeatCanonical{Title: "T", Summary: "S"}, nil
		},
	}

	w := NewMergeWorker(MergeWorkerConfig{Store: store, Merger: merger, Interval: time.Minute, BatchSize: 10})
	w.processBatch(context.Background())

	assert.Equal(t, 2, callCount, "AppendTitleRevision must be called once per successfully saved beat")
}

func TestMergeWorker_AppendTitleRevision_ErrorDoesNotStopProcessing(t *testing.T) {
	// AppendTitleRevision failure is non-blocking: ok++ must still increment
	beats := []domain.Beat{
		{ID: 1, Members: []domain.ClassifiedItem{
			{Item: &domain.Item{Title: "A"}},
			{Item: &domain.Item{Title: "B"}},
		}},
		{ID: 2, Members: []domain.ClassifiedItem{
			{Item: &domain.Item{Title: "C"}},
			{Item: &domain.Item{Title: "D"}},
		}},
	}

	store := &mocks.BeatStoreMock{
		ListPendingMergeFunc: func(ctx context.Context, limit int) ([]domain.Beat, error) { return beats, nil },
		SaveCanonicalFunc:    func(ctx context.Context, beatID int64, c domain.BeatCanonical) error { return nil },
		AppendTitleRevisionFunc: func(ctx context.Context, beatID int64, title, summary string) error {
			return errors.New("revision write failed")
		},
	}
	merger := &mocks.MergerMock{
		MergeFunc: func(ctx context.Context, members []domain.ClassifiedItem) (domain.BeatCanonical, error) {
			return domain.BeatCanonical{Title: "T", Summary: "S"}, nil
		},
	}

	w := NewMergeWorker(MergeWorkerConfig{Store: store, Merger: merger, Interval: time.Minute, BatchSize: 10})
	w.processBatch(context.Background())

	// SaveCanonical succeeded for both beats — AppendTitleRevision error must not affect the ok count
	assert.Len(t, store.SaveCanonicalCalls(), 2, "both beats must be saved despite AppendTitleRevision errors")
	assert.Len(t, store.AppendTitleRevisionCalls(), 2, "AppendTitleRevision must still be called for both beats")
}
