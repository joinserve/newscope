package repository

import (
	"context"
	"encoding/binary"
	"fmt"
	"math"

	"github.com/jmoiron/sqlx"
)

// EmbeddingRepository handles embedding storage for beat aggregation.
type EmbeddingRepository struct {
	db *sqlx.DB
}

// NewEmbeddingRepository creates a new embedding repository.
func NewEmbeddingRepository(db *sqlx.DB) *EmbeddingRepository {
	return &EmbeddingRepository{db: db}
}

// PutEmbedding inserts or replaces the embedding vector for an item.
func (r *EmbeddingRepository) PutEmbedding(ctx context.Context, itemID int64, model string, v []float32) error {
	// Guard against the cleanup-vs-embed race: the cleanup job may delete a
	// low-score item between the embed worker selecting it and storing its
	// vector. A plain INSERT then fails the items(id) foreign key. Inserting
	// only while the item still exists turns that race into a silent no-op
	// instead of a noisy "FOREIGN KEY constraint failed" warning.
	query := `
		INSERT OR REPLACE INTO item_embeddings (item_id, model, vector)
		SELECT ?, ?, ? WHERE EXISTS (SELECT 1 FROM items WHERE id = ?)
	`
	_, err := r.db.ExecContext(ctx, query, itemID, model, float32sToBlob(v), itemID)
	if err != nil {
		return fmt.Errorf("put embedding: %w", err)
	}
	return nil
}

// float32sToBlob encodes a float32 slice as a little-endian byte blob.
func float32sToBlob(v []float32) []byte {
	buf := make([]byte, len(v)*4)
	for i, f := range v {
		binary.LittleEndian.PutUint32(buf[i*4:], math.Float32bits(f))
	}
	return buf
}
