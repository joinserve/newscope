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
	query := `
		INSERT OR REPLACE INTO item_embeddings (item_id, model, vector)
		VALUES (?, ?, ?)
	`
	_, err := r.db.ExecContext(ctx, query, itemID, model, float32sToBlob(v))
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
