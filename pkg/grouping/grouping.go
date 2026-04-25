package grouping

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/go-pkgz/lgr"

	"github.com/umputun/newscope/pkg/domain"
)

// Store is the data-access contract required by Engine.
type Store interface {
	ListGroupings(ctx context.Context) ([]domain.Grouping, error)
	BeatTagSet(ctx context.Context, beatID int64) ([]string, error)
	UpsertAssignment(ctx context.Context, beatID int64, groupingID *int64) error
	ActiveBeatIDs(ctx context.Context, since time.Time) ([]int64, error)
}

// Engine implements the first-match-wins grouping assignment logic.
type Engine struct {
	store Store

	mu           sync.Mutex
	cached       []domain.Grouping
	cacheExpires time.Time
	cacheTTL     time.Duration
}

// NewEngine creates a new Engine with a 30-second groupings cache TTL.
func NewEngine(store Store) *Engine {
	return &Engine{store: store, cacheTTL: 30 * time.Second}
}

// InvalidateCache clears the cached grouping list, forcing the next call to re-fetch.
// Called after any grouping CRUD operation.
func (e *Engine) InvalidateCache() {
	e.mu.Lock()
	e.cacheExpires = time.Time{}
	e.mu.Unlock()
}

// Reassign recomputes the grouping assignment for a single beat.
// Loads the beat's tag_set, walks groupings in display_order, and upserts
// the first match (or nil if none match).
func (e *Engine) Reassign(ctx context.Context, beatID int64) error {
	tagSet, err := e.store.BeatTagSet(ctx, beatID)
	if err != nil {
		return fmt.Errorf("beat tag set for %d: %w", beatID, err)
	}

	groupings, err := e.loadGroupings(ctx)
	if err != nil {
		return fmt.Errorf("load groupings: %w", err)
	}

	var matchID *int64
	for i := range groupings {
		if isSubset(groupings[i].Tags, tagSet) {
			id := groupings[i].ID
			matchID = &id
			break
		}
	}

	if err := e.store.UpsertAssignment(ctx, beatID, matchID); err != nil {
		return fmt.Errorf("upsert assignment beat=%d: %w", beatID, err)
	}
	return nil
}

// ReassignAll recomputes assignments for all beats with first_seen_at > now-window.
// Invalidates the groupings cache before starting so it picks up any recent CRUD changes.
func (e *Engine) ReassignAll(ctx context.Context, window time.Duration) error {
	e.InvalidateCache()
	since := time.Now().Add(-window)

	beatIDs, err := e.store.ActiveBeatIDs(ctx, since)
	if err != nil {
		return fmt.Errorf("active beat ids: %w", err)
	}
	if len(beatIDs) == 0 {
		return nil
	}

	failed := 0
	for _, beatID := range beatIDs {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err := e.Reassign(ctx, beatID); err != nil {
			lgr.Printf("[WARN] grouping.Engine.ReassignAll beat=%d: %v", beatID, err)
			failed++
		}
	}

	lgr.Printf("[INFO] grouping.Engine.ReassignAll: processed %d beats, %d failed", len(beatIDs), failed)
	if failed > 0 {
		return fmt.Errorf("reassign all: %d/%d beats failed", failed, len(beatIDs))
	}
	return nil
}

// loadGroupings returns the cached grouping list, refreshing if expired.
func (e *Engine) loadGroupings(ctx context.Context) ([]domain.Grouping, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if time.Now().Before(e.cacheExpires) {
		return e.cached, nil
	}
	gs, err := e.store.ListGroupings(ctx)
	if err != nil {
		return nil, err
	}
	e.cached = gs
	e.cacheExpires = time.Now().Add(e.cacheTTL)
	return gs, nil
}

// isSubset returns true when every tag in required appears in the beatTags set.
// Comparison is case-insensitive; tags are assumed to already be lowercase (normalized
// at write time), but the extra ToLower is a cheap safety net.
func isSubset(required, beatTags []string) bool {
	if len(required) == 0 {
		return false // grouping with no tags never matches
	}
	set := make(map[string]struct{}, len(beatTags))
	for _, t := range beatTags {
		set[strings.ToLower(t)] = struct{}{}
	}
	for _, t := range required {
		if _, ok := set[strings.ToLower(t)]; !ok {
			return false
		}
	}
	return true
}
