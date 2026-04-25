//go:generate moq -out mocks/entity_store.go -pkg mocks -skip-ensure -fmt goimports . EntityStore
//go:generate moq -out mocks/entity_extractor.go -pkg mocks -skip-ensure -fmt goimports . EntityExtractor

package scheduler

import (
	"context"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/go-pkgz/lgr"

	"github.com/umputun/newscope/pkg/domain"
)

const defaultEntityBatchSize = 20

// EntityStore persists entity extraction results.
type EntityStore interface {
	ListPendingEntities(ctx context.Context, limit int) ([]domain.ClassifiedItem, error)
	SaveEntities(ctx context.Context, itemID int64, entities []string) error
	BeatForItem(ctx context.Context, itemID int64) (int64, bool, error)
}

// EntityExtractor extracts named entities from a batch of items.
type EntityExtractor interface {
	Extract(ctx context.Context, items []domain.ClassifiedItem) ([][]string, error)
}

// EntityWorkerConfig holds configuration for the entity worker.
type EntityWorkerConfig struct {
	Store     EntityStore
	Extractor EntityExtractor
	Grouping  GroupingAssigner // may be nil
	Interval  time.Duration
	Batch     int
}

// EntityWorker periodically extracts named entities from classified items and
// triggers grouping reassignment for affected beats.
type EntityWorker struct {
	store     EntityStore
	extractor EntityExtractor
	grouping  GroupingAssigner
	interval  time.Duration
	batch     int
}

// NewEntityWorker creates a new entity worker.
func NewEntityWorker(cfg EntityWorkerConfig) *EntityWorker {
	batch := cfg.Batch
	if batch <= 0 {
		batch = defaultEntityBatchSize
	}
	return &EntityWorker{
		store:     cfg.Store,
		extractor: cfg.Extractor,
		grouping:  cfg.Grouping,
		interval:  cfg.Interval,
		batch:     batch,
	}
}

// Run runs the entity worker until ctx is done.
func (w *EntityWorker) Run(ctx context.Context) {
	ticker := time.NewTicker(w.interval)
	defer ticker.Stop()

	w.tick(ctx)

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			w.tick(ctx)
		}
	}
}

func (w *EntityWorker) tick(ctx context.Context) {
	items, err := w.store.ListPendingEntities(ctx, w.batch)
	if err != nil {
		lgr.Printf("[ERROR] entity_worker: list pending: %v", err)
		return
	}
	if len(items) == 0 {
		return
	}

	lgr.Printf("[DEBUG] entity_worker: extracting entities for %d items", len(items))

	ents, err := w.extractor.Extract(ctx, items)
	if err != nil {
		lgr.Printf("[ERROR] entity_worker: extract: %v", err)
		return
	}

	if len(ents) != len(items) {
		lgr.Printf("[ERROR] entity_worker: extractor returned %d results for %d items", len(ents), len(items))
		return
	}

	affectedBeats := make(map[int64]struct{})
	ok, failed := 0, 0
	for i, item := range items {
		if ctx.Err() != nil {
			return
		}
		cleaned := normalizeEntities(ents[i])
		if err := w.store.SaveEntities(ctx, item.ID, cleaned); err != nil {
			lgr.Printf("[WARN] entity_worker: save entities for item %d: %v", item.ID, err)
			failed++
			continue
		}
		beatID, hasBeat, err := w.store.BeatForItem(ctx, item.ID)
		if err != nil {
			lgr.Printf("[WARN] entity_worker: beat for item %d: %v", item.ID, err)
		} else if hasBeat {
			affectedBeats[beatID] = struct{}{}
		}
		ok++
	}
	lgr.Printf("[INFO] entity_worker: extracted entities for %d/%d items (%d failed)", ok, len(items), failed)

	if w.grouping != nil {
		for beatID := range affectedBeats {
			if err := w.grouping.Reassign(ctx, beatID); err != nil {
				lgr.Printf("[WARN] entity_worker: reassign beat %d: %v", beatID, err)
			}
		}
		if len(affectedBeats) > 0 {
			lgr.Printf("[DEBUG] entity_worker: triggered reassign for %d beats", len(affectedBeats))
		}
	}
}

// normalizeEntities lowercases, trims, deduplicates, and filters entity tokens.
// Drops tokens shorter than 2 chars or composed entirely of digits.
func normalizeEntities(raw []string) []string {
	seen := make(map[string]struct{}, len(raw))
	var out []string
	for _, e := range raw {
		tok := strings.ToLower(strings.TrimSpace(e))
		if len([]rune(tok)) < 2 {
			continue
		}
		if isAllDigits(tok) {
			continue
		}
		if _, dup := seen[tok]; dup {
			continue
		}
		seen[tok] = struct{}{}
		out = append(out, tok)
	}
	sort.Strings(out)
	return out
}

func isAllDigits(s string) bool {
	for _, r := range s {
		if !unicode.IsDigit(r) {
			return false
		}
	}
	return true
}
