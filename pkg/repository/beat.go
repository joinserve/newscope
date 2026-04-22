package repository

import (
	"context"
	"database/sql"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/jmoiron/sqlx"

	"github.com/umputun/newscope/pkg/domain"
)

// BeatRepository groups items into beats via cosine similarity on their embeddings.
// The vector index is intentionally implemented as a brute-force scan over the
// 48h window so the existing pure-Go SQLite driver continues to work; ADR 0010
// scopes a future sqlite-vec swap behind the same interface.
type BeatRepository struct {
	db *sqlx.DB
}

// NewBeatRepository creates a beat repository.
func NewBeatRepository(db *sqlx.DB) *BeatRepository {
	return &BeatRepository{db: db}
}

// GetUnbeatItems returns classified items that have an embedding but no beat
// membership yet, ordered by published time so the worker processes older
// items first (keeping beats seeded by the earliest member of a cluster).
func (r *BeatRepository) GetUnbeatItems(ctx context.Context, limit int) ([]domain.BeatCandidate, error) {
	query := `
		SELECT i.id AS item_id, i.published AS published_at, ie.vector AS vector
		FROM items i
		JOIN item_embeddings ie ON ie.item_id = i.id
		LEFT JOIN beat_members bm ON bm.item_id = i.id
		WHERE i.classified_at IS NOT NULL
		  AND bm.item_id IS NULL
		ORDER BY i.published ASC
		LIMIT ?`

	rows, err := r.db.QueryxContext(ctx, query, limit)
	if err != nil {
		return nil, fmt.Errorf("get unbeat items: %w", err)
	}
	defer rows.Close()

	var out []domain.BeatCandidate
	for rows.Next() {
		var row struct {
			ItemID      int64     `db:"item_id"`
			PublishedAt time.Time `db:"published_at"`
			Vector      []byte    `db:"vector"`
		}
		if err := rows.StructScan(&row); err != nil {
			return nil, fmt.Errorf("scan unbeat row: %w", err)
		}
		out = append(out, domain.BeatCandidate{
			ItemID:      row.ItemID,
			PublishedAt: row.PublishedAt,
			Vector:      blobToFloat32s(row.Vector),
		})
	}
	return out, rows.Err()
}

// NearestIn finds the beat whose any member has the highest cosine similarity
// to vec, restricted to beats with at least one member published after
// windowStart. Returns beatID=0 and similarity=0 when no candidate exists.
func (r *BeatRepository) NearestIn(ctx context.Context, vec []float32, windowStart time.Time) (beatID int64, similarity float64, err error) {
	return r.nearestIn(ctx, r.db, vec, windowStart)
}

// nearestIn is the shared implementation used by NearestIn and AttachOrSeed
// (the latter passes its transaction handle in).
func (r *BeatRepository) nearestIn(ctx context.Context, q sqlx.QueryerContext, vec []float32, windowStart time.Time) (beatID int64, similarity float64, err error) {
	query := `
		SELECT bm.beat_id, ie.vector
		FROM beat_members bm
		JOIN items i ON i.id = bm.item_id
		JOIN item_embeddings ie ON ie.item_id = i.id
		WHERE i.published >= ?`

	rows, err := q.QueryxContext(ctx, query, windowStart)
	if err != nil {
		return 0, 0, fmt.Errorf("nearest beat query: %w", err)
	}
	defer rows.Close()

	bestBeat := int64(0)
	bestSim := math.Inf(-1)
	for rows.Next() {
		var rowBeatID int64
		var blob []byte
		if err := rows.Scan(&rowBeatID, &blob); err != nil {
			return 0, 0, fmt.Errorf("scan beat member: %w", err)
		}
		sim := cosine(vec, blobToFloat32s(blob))
		if sim > bestSim {
			bestSim = sim
			bestBeat = rowBeatID
		}
	}
	if err := rows.Err(); err != nil {
		return 0, 0, fmt.Errorf("nearest beat rows: %w", err)
	}
	if bestBeat == 0 {
		return 0, 0, nil
	}
	return bestBeat, bestSim, nil
}

// AttachOrSeed attaches the item to the nearest beat within window whose best
// cosine similarity meets the threshold, respecting maxMembers. If no beat
// qualifies, a new beat is seeded with this item. Idempotent: if the item is
// already a member of some beat, that beat's ID is returned unchanged.
func (r *BeatRepository) AttachOrSeed(ctx context.Context, item domain.BeatCandidate, threshold float64, window time.Duration, maxMembers int) (int64, error) {
	tx, err := r.db.BeginTxx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	// idempotency: already assigned?
	var existing int64
	err = tx.GetContext(ctx, &existing, `SELECT beat_id FROM beat_members WHERE item_id = ?`, item.ItemID)
	if err == nil {
		return existing, tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return 0, fmt.Errorf("check existing membership: %w", err)
	}

	// look for a beat that qualifies within window
	windowStart := item.PublishedAt.Add(-window)
	beatID, sim, err := r.nearestIn(ctx, tx, item.Vector, windowStart)
	if err != nil {
		return 0, err
	}

	attach := beatID != 0 && sim >= threshold
	if attach && maxMembers > 0 {
		var count int
		if err := tx.GetContext(ctx, &count, `SELECT COUNT(*) FROM beat_members WHERE beat_id = ?`, beatID); err != nil {
			return 0, fmt.Errorf("member count: %w", err)
		}
		if count >= maxMembers {
			attach = false
		}
	}

	if !attach {
		// seed a new beat; canonical fields stay NULL (pr 4 populates them)
		res, err := tx.ExecContext(ctx,
			`INSERT INTO beats (first_seen_at) VALUES (?)`, item.PublishedAt)
		if err != nil {
			return 0, fmt.Errorf("insert beat: %w", err)
		}
		beatID, err = res.LastInsertId()
		if err != nil {
			return 0, fmt.Errorf("new beat id: %w", err)
		}
	} else {
		if _, err := tx.ExecContext(ctx,
			`UPDATE beats SET updated_at = strftime('%Y-%m-%d %H:%M:%f', 'now') WHERE id = ?`, beatID); err != nil {
			return 0, fmt.Errorf("bump beat updated_at: %w", err)
		}
	}

	// use millisecond precision so unread-count comparisons against
	// last_viewed_at are correct within the same second.
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO beat_members (beat_id, item_id, added_at) VALUES (?, ?, strftime('%Y-%m-%d %H:%M:%f', 'now'))`,
		beatID, item.ItemID); err != nil {
		return 0, fmt.Errorf("insert beat member: %w", err)
	}
	return beatID, tx.Commit()
}

// MarkViewed records that the user has viewed this beat at the current time;
// PR 5's UI uses this to compute the "N new since last visit" badge.
func (r *BeatRepository) MarkViewed(ctx context.Context, beatID int64) error {
	_, err := r.db.ExecContext(ctx,
		`UPDATE beats SET last_viewed_at = strftime('%Y-%m-%d %H:%M:%f', 'now') WHERE id = ?`, beatID)
	if err != nil {
		return fmt.Errorf("mark viewed: %w", err)
	}
	return nil
}

// UnreadMemberCount returns the number of members added after the beat's
// last_viewed_at. When last_viewed_at is NULL the whole membership is unread.
func (r *BeatRepository) UnreadMemberCount(ctx context.Context, beatID int64) (int, error) {
	query := `
		SELECT COUNT(*) FROM beat_members bm
		JOIN beats b ON b.id = bm.beat_id
		WHERE bm.beat_id = ? AND (b.last_viewed_at IS NULL OR bm.added_at > b.last_viewed_at)`
	var n int
	if err := r.db.GetContext(ctx, &n, query, beatID); err != nil {
		return 0, fmt.Errorf("unread count: %w", err)
	}
	return n, nil
}

// cosine returns the cosine similarity of two vectors; zero when either has
// zero norm or when lengths disagree.
func cosine(a, b []float32) float64 {
	if len(a) != len(b) || len(a) == 0 {
		return 0
	}
	var dot, na, nb float64
	for i := range a {
		ai, bi := float64(a[i]), float64(b[i])
		dot += ai * bi
		na += ai * ai
		nb += bi * bi
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return dot / (math.Sqrt(na) * math.Sqrt(nb))
}

// blobToFloat32s decodes a little-endian float32 BLOB (as written by
// EmbeddingRepository.PutEmbedding) back into a slice.
func blobToFloat32s(b []byte) []float32 {
	v := make([]float32, len(b)/4)
	for i := range v {
		v[i] = math.Float32frombits(binary.LittleEndian.Uint32(b[i*4:]))
	}
	return v
}
