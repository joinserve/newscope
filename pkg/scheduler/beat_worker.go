package scheduler

import (
	"context"
	"time"

	"github.com/go-pkgz/lgr"
)

const defaultBeatBatchSize = 50

// GroupingAssigner reassigns a beat to its best-matching grouping.
type GroupingAssigner interface {
	Reassign(ctx context.Context, beatID int64) error
}

// BeatWorkerConfig holds configuration for BeatWorker.
type BeatWorkerConfig struct {
	Store      BeatStore
	Threshold  float64
	Window     time.Duration
	MaxMembers int
	Interval   time.Duration
	BatchSize  int
	Grouping   GroupingAssigner // optional; nil disables assignment
}

// BeatWorker periodically assigns embedded items to beats using AttachOrSeed.
type BeatWorker struct {
	store      BeatStore
	threshold  float64
	window     time.Duration
	maxMembers int
	interval   time.Duration
	batchSize  int
	grouping   GroupingAssigner // may be nil
}

// NewBeatWorker creates a new beat worker.
func NewBeatWorker(cfg BeatWorkerConfig) *BeatWorker {
	batchSize := cfg.BatchSize
	if batchSize <= 0 {
		batchSize = defaultBeatBatchSize
	}
	return &BeatWorker{
		store:      cfg.Store,
		threshold:  cfg.Threshold,
		window:     cfg.Window,
		maxMembers: cfg.MaxMembers,
		interval:   cfg.Interval,
		batchSize:  batchSize,
		grouping:   cfg.Grouping,
	}
}

// Run runs the beat worker until ctx is done.
func (w *BeatWorker) Run(ctx context.Context) {
	ticker := time.NewTicker(w.interval)
	defer ticker.Stop()

	w.processBatch(ctx)

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			w.processBatch(ctx)
		}
	}
}

// processBatch fetches a batch of unbeat items and assigns each to a beat.
func (w *BeatWorker) processBatch(ctx context.Context) {
	items, err := w.store.GetUnbeatItems(ctx, w.batchSize)
	if err != nil {
		lgr.Printf("[ERROR] beat_worker: get unbeat items: %v", err)
		return
	}
	if len(items) == 0 {
		return
	}

	lgr.Printf("[DEBUG] beat_worker: assigning %d items", len(items))
	attached, seeded, failed := 0, 0, 0
	for _, item := range items {
		if ctx.Err() != nil {
			return
		}
		beatID, isSeeded, err := w.store.AttachOrSeed(ctx, item, w.threshold, w.window, w.maxMembers)
		if err != nil {
			lgr.Printf("[WARN] beat_worker: attach item %d: %v", item.ItemID, err)
			failed++
			continue
		}
		if isSeeded {
			seeded++
		} else {
			attached++
		}
		if w.grouping != nil {
			if err := w.grouping.Reassign(ctx, beatID); err != nil {
				lgr.Printf("[WARN] beat_worker: grouping reassign beat=%d: %v", beatID, err)
				// non-fatal: grouping assignment failure does not block beat processing
			}
		}
	}
	lgr.Printf("[INFO] beat_worker: processed %d items (%d new beats, %d attached, %d failed)",
		len(items), seeded, attached, failed)
}
