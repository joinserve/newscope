package scheduler

import (
	"context"
	"time"

	"github.com/go-pkgz/lgr"
)

const defaultEmbedBatchSize = 50

// EmbedWorkerConfig holds configuration for EmbedWorker.
type EmbedWorkerConfig struct {
	Embedder  Embedder
	Store     EmbedStore
	Items     ItemManager
	Model     string
	Interval  time.Duration
	BatchSize int
}

// EmbedWorker fetches classified items without embeddings and stores their vectors.
type EmbedWorker struct {
	embedder  Embedder
	store     EmbedStore
	items     ItemManager
	model     string
	interval  time.Duration
	batchSize int
}

// NewEmbedWorker creates a new embed worker.
func NewEmbedWorker(cfg EmbedWorkerConfig) *EmbedWorker {
	batchSize := cfg.BatchSize
	if batchSize <= 0 {
		batchSize = defaultEmbedBatchSize
	}
	return &EmbedWorker{
		embedder:  cfg.Embedder,
		store:     cfg.Store,
		items:     cfg.Items,
		model:     cfg.Model,
		interval:  cfg.Interval,
		batchSize: batchSize,
	}
}

// Run runs the embed worker until ctx is done.
func (w *EmbedWorker) Run(ctx context.Context) {
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

// processBatch fetches a batch of unembedded items and embeds each one.
func (w *EmbedWorker) processBatch(ctx context.Context) {
	items, err := w.items.GetUnembeddedItems(ctx, w.batchSize)
	if err != nil {
		lgr.Printf("[ERROR] embed_worker: get unembedded items: %v", err)
		return
	}
	if len(items) == 0 {
		return
	}

	lgr.Printf("[DEBUG] embed_worker: embedding %d items", len(items))
	ok, failed := 0, 0
	for _, item := range items {
		if ctx.Err() != nil {
			return
		}
		vec, err := w.embedder.Embed(ctx, embeddingText(item.Title, item.Summary))
		if err != nil {
			lgr.Printf("[WARN] embed_worker: embed item %d: %v", item.ID, err)
			failed++
			continue
		}
		if err := w.store.PutEmbedding(ctx, item.ID, w.model, vec); err != nil {
			lgr.Printf("[WARN] embed_worker: store embedding for item %d: %v", item.ID, err)
			failed++
			continue
		}
		ok++
	}
	lgr.Printf("[INFO] embed_worker: embedded %d/%d items (%d failed)", ok, len(items), failed)
}

// embeddingText builds the input text for the embedder from an item's title and summary.
func embeddingText(title, summary string) string {
	if summary == "" {
		return title
	}
	return title + " " + summary
}
