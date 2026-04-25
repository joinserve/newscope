package scheduler

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/umputun/newscope/pkg/domain"
	"github.com/umputun/newscope/pkg/scheduler/mocks"
)

func TestEntityWorker_Tick_BatchProcessed(t *testing.T) {
	items := []domain.ClassifiedItem{
		{Item: &domain.Item{ID: 1, Title: "Claude 4.5 release", Summary: "Anthropic releases claude"}},
		{Item: &domain.Item{ID: 2, Title: "SpaceX launch", Summary: "Falcon 9 launch"}},
	}

	var savedEntities [][]string
	store := &mocks.EntityStoreMock{
		ListPendingEntitiesFunc: func(ctx context.Context, limit int) ([]domain.ClassifiedItem, error) {
			return items, nil
		},
		SaveEntitiesFunc: func(ctx context.Context, itemID int64, entities []string) error {
			savedEntities = append(savedEntities, entities)
			return nil
		},
		BeatForItemFunc: func(ctx context.Context, itemID int64) (int64, bool, error) {
			return 0, false, nil
		},
	}
	extractor := &mocks.EntityExtractorMock{
		ExtractFunc: func(ctx context.Context, its []domain.ClassifiedItem) ([][]string, error) {
			return [][]string{{"claude", "anthropic"}, {"spacex", "falcon 9"}}, nil
		},
	}

	w := NewEntityWorker(EntityWorkerConfig{Store: store, Extractor: extractor})
	w.tick(context.Background())

	require.Len(t, savedEntities, 2)
	assert.Equal(t, []string{"anthropic", "claude"}, savedEntities[0]) // normalizeEntities sorts
	assert.Equal(t, []string{"falcon 9", "spacex"}, savedEntities[1])
}

func TestEntityWorker_Tick_SaveFailureContinues(t *testing.T) {
	items := []domain.ClassifiedItem{
		{Item: &domain.Item{ID: 1, Title: "A", Summary: ""}},
		{Item: &domain.Item{ID: 2, Title: "B", Summary: ""}},
	}

	var saved []int64
	store := &mocks.EntityStoreMock{
		ListPendingEntitiesFunc: func(ctx context.Context, limit int) ([]domain.ClassifiedItem, error) {
			return items, nil
		},
		SaveEntitiesFunc: func(ctx context.Context, itemID int64, entities []string) error {
			if itemID == 1 {
				return errors.New("db error")
			}
			saved = append(saved, itemID)
			return nil
		},
		BeatForItemFunc: func(ctx context.Context, itemID int64) (int64, bool, error) {
			return 0, false, nil
		},
	}
	extractor := &mocks.EntityExtractorMock{
		ExtractFunc: func(ctx context.Context, its []domain.ClassifiedItem) ([][]string, error) {
			return [][]string{{"anthropic"}, {"spacex"}}, nil
		},
	}

	w := NewEntityWorker(EntityWorkerConfig{Store: store, Extractor: extractor})
	w.tick(context.Background())

	// item 2 was still saved despite item 1 failing
	assert.Equal(t, []int64{2}, saved)
}

func TestEntityWorker_Tick_TriggersReassign(t *testing.T) {
	items := []domain.ClassifiedItem{
		{Item: &domain.Item{ID: 1, Title: "Claude release", Summary: ""}},
	}

	store := &mocks.EntityStoreMock{
		ListPendingEntitiesFunc: func(ctx context.Context, limit int) ([]domain.ClassifiedItem, error) {
			return items, nil
		},
		SaveEntitiesFunc: func(ctx context.Context, itemID int64, entities []string) error {
			return nil
		},
		BeatForItemFunc: func(ctx context.Context, itemID int64) (int64, bool, error) {
			return 42, true, nil
		},
	}
	extractor := &mocks.EntityExtractorMock{
		ExtractFunc: func(ctx context.Context, its []domain.ClassifiedItem) ([][]string, error) {
			return [][]string{{"claude"}}, nil
		},
	}

	var reassignedBeats []int64
	grouping := &mocks.GroupingAssignerMock{
		ReassignFunc: func(ctx context.Context, beatID int64) error {
			reassignedBeats = append(reassignedBeats, beatID)
			return nil
		},
	}

	w := NewEntityWorker(EntityWorkerConfig{Store: store, Extractor: extractor, Grouping: grouping})
	w.tick(context.Background())

	assert.Equal(t, []int64{42}, reassignedBeats)
}

func TestEntityWorker_Tick_AffectedBeatsDeduped(t *testing.T) {
	// two items in the same beat → reassign called once
	items := []domain.ClassifiedItem{
		{Item: &domain.Item{ID: 1, Title: "A", Summary: ""}},
		{Item: &domain.Item{ID: 2, Title: "B", Summary: ""}},
	}

	store := &mocks.EntityStoreMock{
		ListPendingEntitiesFunc: func(ctx context.Context, limit int) ([]domain.ClassifiedItem, error) {
			return items, nil
		},
		SaveEntitiesFunc: func(ctx context.Context, itemID int64, entities []string) error { return nil },
		BeatForItemFunc: func(ctx context.Context, itemID int64) (int64, bool, error) {
			return 99, true, nil // both items in beat 99
		},
	}
	extractor := &mocks.EntityExtractorMock{
		ExtractFunc: func(ctx context.Context, its []domain.ClassifiedItem) ([][]string, error) {
			return [][]string{{"a"}, {"b"}}, nil
		},
	}

	reassignCount := 0
	grouping := &mocks.GroupingAssignerMock{
		ReassignFunc: func(ctx context.Context, beatID int64) error {
			reassignCount++
			return nil
		},
	}

	w := NewEntityWorker(EntityWorkerConfig{Store: store, Extractor: extractor, Grouping: grouping})
	w.tick(context.Background())

	assert.Equal(t, 1, reassignCount, "reassign must be called once per beat, not per item")
}

func TestEntityWorker_Tick_EmptyBatch(t *testing.T) {
	store := &mocks.EntityStoreMock{
		ListPendingEntitiesFunc: func(ctx context.Context, limit int) ([]domain.ClassifiedItem, error) {
			return nil, nil
		},
	}
	extractor := &mocks.EntityExtractorMock{}

	w := NewEntityWorker(EntityWorkerConfig{Store: store, Extractor: extractor})
	w.tick(context.Background()) // must not panic or call extractor

	assert.Empty(t, extractor.ExtractCalls())
}

func TestNormalizeEntities(t *testing.T) {
	tests := []struct {
		raw  []string
		want []string
	}{
		{[]string{"Claude", "ANTHROPIC", "claude"}, []string{"anthropic", "claude"}}, // dedup + lowercase
		{[]string{"a", "ab"}, []string{"ab"}},                                         // drop len<2
		{[]string{"2024", "Q4"}, []string{"q4"}},                                      // drop pure digits
		{[]string{" SpaceX ", "Tesla"}, []string{"spacex", "tesla"}},                  // trim
		{nil, nil}, // nil input
	}

	for _, tc := range tests {
		got := normalizeEntities(tc.raw)
		assert.Equal(t, tc.want, got)
	}
}
