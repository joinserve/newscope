package scheduler

import (
	"context"
	"time"

	"github.com/go-pkgz/lgr"
)

const defaultMergeBatchSize = 20

// MergeWorkerConfig holds configuration for MergeWorker.
type MergeWorkerConfig struct {
	Store     BeatStore
	Merger    Merger
	Interval  time.Duration
	BatchSize int
}

// MergeWorker processes beats whose canonical_summary is NULL and have more
// than one member, generating a canonical title and summary via the Merger.
type MergeWorker struct {
	store     BeatStore
	merger    Merger
	interval  time.Duration
	batchSize int
}

// NewMergeWorker creates a new merge worker.
func NewMergeWorker(cfg MergeWorkerConfig) *MergeWorker {
	batchSize := cfg.BatchSize
	if batchSize <= 0 {
		batchSize = defaultMergeBatchSize
	}
	return &MergeWorker{
		store:     cfg.Store,
		merger:    cfg.Merger,
		interval:  cfg.Interval,
		batchSize: batchSize,
	}
}

// Run runs the merge worker until ctx is done.
func (w *MergeWorker) Run(ctx context.Context) {
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

// processBatch fetches pending beats, calls the Merger for each, and saves the result.
func (w *MergeWorker) processBatch(ctx context.Context) {
	beats, err := w.store.ListPendingMerge(ctx, w.batchSize)
	if err != nil {
		lgr.Printf("[ERROR] merge_worker: list pending merge: %v", err)
		return
	}
	if len(beats) == 0 {
		return
	}

	lgr.Printf("[DEBUG] merge_worker: merging %d beats", len(beats))
	ok, failed := 0, 0
	for _, beat := range beats {
		if ctx.Err() != nil {
			return
		}
		canonical, err := w.merger.Merge(ctx, beat.Members)
		if err != nil {
			lgr.Printf("[WARN] merge_worker: merge beat %d: %v", beat.ID, err)
			failed++
			continue
		}
		if err := w.store.SaveCanonical(ctx, beat.ID, canonical); err != nil {
			lgr.Printf("[WARN] merge_worker: save canonical for beat %d: %v", beat.ID, err)
			failed++
			continue
		}
		if err := w.store.AppendTitleRevision(ctx, beat.ID, canonical.Title, canonical.Summary); err != nil {
			lgr.Printf("[WARN] merge_worker: append title revision beat=%d: %v", beat.ID, err)
		}
		ok++
	}
	lgr.Printf("[INFO] merge_worker: merged %d/%d beats (%d failed)", ok, len(beats), failed)
}
