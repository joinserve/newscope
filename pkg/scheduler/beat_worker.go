package scheduler

import (
	"context"
	"time"

	"github.com/go-pkgz/lgr"
)

const defaultBeatBatchSize = 50

// BeatWorkerConfig holds configuration for BeatWorker.
type BeatWorkerConfig struct {
	Store      BeatStore
	Threshold  float64
	Window     time.Duration
	MaxMembers int
	Interval   time.Duration
	BatchSize  int
}

// BeatWorker periodically assigns embedded items to beats using AttachOrSeed.
type BeatWorker struct {
	store      BeatStore
	threshold  float64
	window     time.Duration
	maxMembers int
	interval   time.Duration
	batchSize  int
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
	seen := map[int64]bool{}
	for _, item := range items {
		if ctx.Err() != nil {
			return
		}
		beatID, err := w.store.AttachOrSeed(ctx, item, w.threshold, w.window, w.maxMembers)
		if err != nil {
			lgr.Printf("[WARN] beat_worker: attach item %d: %v", item.ItemID, err)
			failed++
			continue
		}
		if seen[beatID] {
			attached++
		} else {
			seeded++
			seen[beatID] = true
		}
	}
	lgr.Printf("[INFO] beat_worker: processed %d items (%d new beats, %d attached, %d failed)",
		len(items), seeded, attached, failed)
}
