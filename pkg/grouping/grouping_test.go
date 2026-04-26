package grouping

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/umputun/newscope/pkg/domain"
)

// storeMock is a minimal in-memory Store implementation for tests.
type storeMock struct {
	groupings   []domain.Grouping
	tagSets     map[int64][]string
	assignments map[int64]*int64
	activeBeat  []int64
}

func (s *storeMock) ListGroupings(_ context.Context) ([]domain.Grouping, error) {
	return s.groupings, nil
}

func (s *storeMock) BeatTagSet(_ context.Context, beatID int64) ([]string, error) {
	tags, ok := s.tagSets[beatID]
	if !ok {
		return nil, nil
	}
	return tags, nil
}

func (s *storeMock) UpsertAssignment(_ context.Context, beatID int64, groupingID *int64) error {
	if s.assignments == nil {
		s.assignments = make(map[int64]*int64)
	}
	s.assignments[beatID] = groupingID
	return nil
}

func (s *storeMock) ActiveBeatIDs(_ context.Context, since time.Time) ([]int64, error) {
	return s.activeBeat, nil
}

func TestEngine_Reassign_NoGroupings(t *testing.T) {
	store := &storeMock{
		tagSets: map[int64][]string{1: {"ai", "china"}},
	}
	e := NewEngine(store)

	err := e.Reassign(context.Background(), 1)
	require.NoError(t, err)
	assert.Nil(t, store.assignments[1], "no groupings → assignment should be nil")
}

func TestEngine_Reassign_SingleMatch(t *testing.T) {
	gid := int64(10)
	store := &storeMock{
		groupings: []domain.Grouping{
			{ID: gid, Tags: []string{"ai"}, DisplayOrder: 0},
		},
		tagSets: map[int64][]string{1: {"ai", "llm"}},
	}
	e := NewEngine(store)

	require.NoError(t, e.Reassign(context.Background(), 1))
	require.NotNil(t, store.assignments[1])
	assert.Equal(t, gid, *store.assignments[1])
}

func TestEngine_Reassign_FirstMatchWins(t *testing.T) {
	// two groupings both match beat tags; only the lower display_order wins
	gid1, gid2 := int64(1), int64(2)
	store := &storeMock{
		groupings: []domain.Grouping{
			{ID: gid1, Tags: []string{"taiwan", "politics"}, DisplayOrder: 0},
			{ID: gid2, Tags: []string{"taiwan", "china"}, DisplayOrder: 1},
		},
		// beat has taiwan + politics + china → both groupings match
		tagSets: map[int64][]string{5: {"taiwan", "politics", "china"}},
	}
	e := NewEngine(store)

	require.NoError(t, e.Reassign(context.Background(), 5))
	require.NotNil(t, store.assignments[5])
	assert.Equal(t, gid1, *store.assignments[5], "first grouping (display_order=0) must win")
}

func TestEngine_Reassign_PartialTagsNoMatch(t *testing.T) {
	store := &storeMock{
		groupings: []domain.Grouping{
			{ID: 1, Tags: []string{"taiwan", "politics"}, DisplayOrder: 0},
		},
		// beat only has taiwan, missing politics
		tagSets: map[int64][]string{3: {"taiwan"}},
	}
	e := NewEngine(store)

	require.NoError(t, e.Reassign(context.Background(), 3))
	assert.Nil(t, store.assignments[3], "partial tag match must not assign")
}

func TestEngine_Reassign_EmptyTagsGroupingNeverMatches(t *testing.T) {
	store := &storeMock{
		groupings: []domain.Grouping{
			{ID: 1, Tags: []string{}, DisplayOrder: 0}, // no required tags
		},
		tagSets: map[int64][]string{1: {"ai"}},
	}
	e := NewEngine(store)

	require.NoError(t, e.Reassign(context.Background(), 1))
	assert.Nil(t, store.assignments[1], "grouping with empty tags must never match")
}

func TestEngine_Reassign_CaseInsensitive(t *testing.T) {
	gid := int64(7)
	store := &storeMock{
		groupings: []domain.Grouping{
			{ID: gid, Tags: []string{"Claude"}, DisplayOrder: 0},
		},
		tagSets: map[int64][]string{9: {"claude"}}, // lowercase in beat
	}
	e := NewEngine(store)

	require.NoError(t, e.Reassign(context.Background(), 9))
	require.NotNil(t, store.assignments[9])
	assert.Equal(t, gid, *store.assignments[9])
}

func TestEngine_ReassignAll_WindowBoundary(t *testing.T) {
	gid := int64(1)
	store := &storeMock{
		groupings:  []domain.Grouping{{ID: gid, Tags: []string{"ai"}, DisplayOrder: 0}},
		tagSets:    map[int64][]string{10: {"ai"}, 20: {"other"}},
		activeBeat: []int64{10, 20},
	}
	e := NewEngine(store)

	require.NoError(t, e.ReassignAll(context.Background(), 48*time.Hour))
	require.NotNil(t, store.assignments[10])
	assert.Equal(t, gid, *store.assignments[10], "beat 10 matches grouping")
	assert.Nil(t, store.assignments[20], "beat 20 has no matching grouping")
}

func TestEngine_ReassignAll_InvalidatesCacheBeforeRun(t *testing.T) {
	// prime the cache with stale groupings
	staleGID := int64(99)
	freshGID := int64(1)

	callCount := 0
	store := &storeMock{
		activeBeat: []int64{5},
		tagSets:    map[int64][]string{5: {"ai"}},
	}

	freshGroupings := []domain.Grouping{{ID: freshGID, Tags: []string{"ai"}, DisplayOrder: 0}}

	// first call returns stale (different gid), subsequent returns fresh
	e := NewEngine(store)
	store.groupings = []domain.Grouping{{ID: staleGID, Tags: []string{"ai"}, DisplayOrder: 0}}
	_ = e.Reassign(context.Background(), 5) // prime cache with staleGID

	// now swap groupings; without cache invalidation, ReassignAll would still use staleGID
	store.groupings = freshGroupings
	callCount++
	require.NoError(t, e.ReassignAll(context.Background(), 48*time.Hour))

	require.NotNil(t, store.assignments[5])
	assert.Equal(t, freshGID, *store.assignments[5], "ReassignAll must invalidate cache and pick up fresh groupings")
	_ = callCount
}

func TestEngine_Reassign_NilTagSet(t *testing.T) {
	// when BeatTagSet returns no tags (nil), no grouping can match
	store := &storeMock{
		groupings: []domain.Grouping{{ID: 1, Tags: []string{"ai"}, DisplayOrder: 0}},
	}
	e := NewEngine(store)
	store.tagSets = nil // returns nil tags for any beat

	require.NoError(t, e.Reassign(context.Background(), 99))
	assert.Nil(t, store.assignments[99])
}

func TestIsSubset(t *testing.T) {
	tests := []struct {
		required []string
		beatTags []string
		want     bool
	}{
		{[]string{"ai"}, []string{"ai", "llm"}, true},
		{[]string{"ai", "llm"}, []string{"ai", "llm"}, true},
		{[]string{"ai", "china"}, []string{"ai", "llm"}, false},
		{[]string{}, []string{"ai"}, false},                      // empty required never matches
		{[]string{"AI"}, []string{"ai"}, true},                   // case-insensitive
		{[]string{"ai"}, []string{"AI"}, true},                   // case-insensitive reverse
		{[]string{"ai"}, []string{}, false},                      // no beat tags
		{[]string{"a", "b", "c"}, []string{"c", "b", "a"}, true}, // order-independent
	}

	for _, tc := range tests {
		got := isSubset(tc.required, tc.beatTags)
		assert.Equal(t, tc.want, got, "isSubset(%v, %v)", tc.required, tc.beatTags)
	}
}
