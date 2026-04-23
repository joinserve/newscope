package repository

import (
	"context"
	"database/sql"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
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

// AttachOrSeed attaches the item to a beat within window whose best member-
// cosine similarity meets the threshold AND still has room under maxMembers,
// preferring higher similarity; falls back to seeding a new beat only when no
// qualifying beat has capacity. Idempotent: if the item is already a member
// of some beat, that beat's ID is returned with seeded=false.
func (r *BeatRepository) AttachOrSeed(ctx context.Context, item domain.BeatCandidate, threshold float64, window time.Duration, maxMembers int) (beatID int64, seeded bool, err error) {
	tx, err := r.db.BeginTxx(ctx, nil)
	if err != nil {
		return 0, false, fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	// idempotency: already assigned?
	var existing int64
	err = tx.GetContext(ctx, &existing, `SELECT beat_id FROM beat_members WHERE item_id = ?`, item.ItemID)
	if err == nil {
		return existing, false, tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return 0, false, fmt.Errorf("check existing membership: %w", err)
	}

	// rank candidate beats within the window by max member-cosine, descending;
	// walk the list and attach to the first one under maxMembers.
	windowStart := item.PublishedAt.Add(-window)
	target, err := r.pickAttachableBeat(ctx, tx, item.Vector, windowStart, threshold, maxMembers)
	if err != nil {
		return 0, false, err
	}

	if target == 0 {
		// seed a new beat; canonical fields stay NULL (pr 4 populates them)
		res, err := tx.ExecContext(ctx,
			`INSERT INTO beats (first_seen_at) VALUES (?)`, item.PublishedAt)
		if err != nil {
			return 0, false, fmt.Errorf("insert beat: %w", err)
		}
		target, err = res.LastInsertId()
		if err != nil {
			return 0, false, fmt.Errorf("new beat id: %w", err)
		}
		seeded = true
	} else {
		if _, err := tx.ExecContext(ctx,
			`UPDATE beats SET updated_at = strftime('%Y-%m-%d %H:%M:%f', 'now') WHERE id = ?`, target); err != nil {
			return 0, false, fmt.Errorf("bump beat updated_at: %w", err)
		}
	}

	// use millisecond precision so unread-count comparisons against
	// last_viewed_at are correct within the same second.
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO beat_members (beat_id, item_id, added_at) VALUES (?, ?, strftime('%Y-%m-%d %H:%M:%f', 'now'))`,
		target, item.ItemID); err != nil {
		return 0, false, fmt.Errorf("insert beat member: %w", err)
	}
	return target, seeded, tx.Commit()
}

// pickAttachableBeat ranks beats within window by max member-cosine, descending,
// and returns the first one under maxMembers whose best-sim meets threshold.
// Returns 0 when no candidate qualifies.
func (r *BeatRepository) pickAttachableBeat(ctx context.Context, tx *sqlx.Tx, vec []float32, windowStart time.Time, threshold float64, maxMembers int) (int64, error) {
	rows, err := tx.QueryxContext(ctx, `
		SELECT bm.beat_id, ie.vector
		FROM beat_members bm
		JOIN items i ON i.id = bm.item_id
		JOIN item_embeddings ie ON ie.item_id = i.id
		WHERE i.published >= ?`, windowStart)
	if err != nil {
		return 0, fmt.Errorf("candidate beats query: %w", err)
	}
	defer rows.Close()

	bestSim := map[int64]float64{}
	for rows.Next() {
		var bID int64
		var blob []byte
		if err := rows.Scan(&bID, &blob); err != nil {
			return 0, fmt.Errorf("scan candidate row: %w", err)
		}
		sim := cosine(vec, blobToFloat32s(blob))
		if prev, ok := bestSim[bID]; !ok || sim > prev {
			bestSim[bID] = sim
		}
	}
	if err := rows.Err(); err != nil {
		return 0, fmt.Errorf("candidate rows: %w", err)
	}

	type ranked struct {
		id  int64
		sim float64
	}
	candidates := make([]ranked, 0, len(bestSim))
	for id, sim := range bestSim {
		if sim >= threshold {
			candidates = append(candidates, ranked{id, sim})
		}
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].sim > candidates[j].sim })

	for _, c := range candidates {
		if maxMembers > 0 {
			var count int
			if err := tx.GetContext(ctx, &count,
				`SELECT COUNT(*) FROM beat_members WHERE beat_id = ?`, c.id); err != nil {
				return 0, fmt.Errorf("member count for beat %d: %w", c.id, err)
			}
			if count >= maxMembers {
				continue
			}
		}
		return c.id, nil
	}
	return 0, nil
}

// ListPendingMerge returns up to limit beats that have no canonical_summary yet
// and have more than one member, with their member items loaded. The returned
// ClassifiedItems are populated with Title, Summary, and Topics only — sufficient
// for the Merger to produce a canonical representation.
func (r *BeatRepository) ListPendingMerge(ctx context.Context, limit int) ([]domain.Beat, error) {
	beatIDs, err := r.pendingMergeIDs(ctx, limit)
	if err != nil {
		return nil, err
	}
	if len(beatIDs) == 0 {
		return nil, nil
	}
	return r.loadBeatMembers(ctx, beatIDs)
}

// pendingMergeIDs returns IDs of beats that need a (re-)merge:
//   - first-time: canonical_summary IS NULL and >1 member
//   - re-merge: already merged, but a new member arrived since the last merge,
//     canonical_merged_at is older than 24h, and the beat is still within its 48h window
func (r *BeatRepository) pendingMergeIDs(ctx context.Context, limit int) ([]int64, error) {
	rows, err := r.db.QueryxContext(ctx, `
		SELECT b.id FROM beats b
		WHERE b.canonical_summary IS NULL
		  AND (SELECT COUNT(*) FROM beat_members WHERE beat_id = b.id) > 1

		UNION

		SELECT b.id FROM beats b
		WHERE b.canonical_summary IS NOT NULL
		  AND b.canonical_merged_at < datetime('now', '-24 hours')
		  AND b.first_seen_at      >= datetime('now', '-48 hours')
		  AND (SELECT MAX(added_at) FROM beat_members WHERE beat_id = b.id) > b.canonical_merged_at

		ORDER BY 1
		LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("list pending merge ids: %w", err)
	}
	defer rows.Close()

	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan beat id: %w", err)
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// loadBeatMembers fetches member items for the given beat IDs and assembles []domain.Beat.
func (r *BeatRepository) loadBeatMembers(ctx context.Context, beatIDs []int64) ([]domain.Beat, error) {
	// build IN clause — beatIDs are int64 from the DB, no user input
	query, args, err := sqlx.In(`
		SELECT bm.beat_id, i.id AS item_id, i.title, i.summary, i.topics
		FROM beat_members bm
		JOIN items i ON i.id = bm.item_id
		WHERE bm.beat_id IN (?)
		ORDER BY bm.beat_id, bm.added_at`, beatIDs)
	if err != nil {
		return nil, fmt.Errorf("build members query: %w", err)
	}
	query = r.db.Rebind(query)

	rows, err := r.db.QueryxContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("load beat members: %w", err)
	}
	defer rows.Close()

	beatMap := make(map[int64]*domain.Beat, len(beatIDs))
	for _, id := range beatIDs {
		beatMap[id] = &domain.Beat{ID: id}
	}

	for rows.Next() {
		var row struct {
			BeatID  int64  `db:"beat_id"`
			ItemID  int64  `db:"item_id"`
			Title   string `db:"title"`
			Summary string `db:"summary"`
			Topics  string `db:"topics"` // JSON array
		}
		if err := rows.StructScan(&row); err != nil {
			return nil, fmt.Errorf("scan beat member: %w", err)
		}
		var topics []string
		if row.Topics != "" && row.Topics != "null" {
			if err := json.Unmarshal([]byte(row.Topics), &topics); err != nil {
				topics = nil // tolerate malformed JSON
			}
		}
		b := beatMap[row.BeatID]
		item := domain.Item{ID: row.ItemID, Title: row.Title, Summary: row.Summary}
		b.Members = append(b.Members, domain.ClassifiedItem{
			Item:           &item,
			Classification: &domain.Classification{Topics: topics, Summary: row.Summary},
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("beat member rows: %w", err)
	}

	beats := make([]domain.Beat, 0, len(beatIDs))
	for _, id := range beatIDs {
		beats = append(beats, *beatMap[id])
	}
	return beats, nil
}

// SaveCanonical stores the LLM-generated canonical title and summary for a beat
// and records the merge timestamp. feedback, feedback_at, and last_viewed_at
// are deliberately excluded so they survive re-summary.
func (r *BeatRepository) SaveCanonical(ctx context.Context, beatID int64, c domain.BeatCanonical) error {
	_, err := r.db.ExecContext(ctx,
		`UPDATE beats SET canonical_title = ?, canonical_summary = ?,
		  canonical_merged_at = strftime('%Y-%m-%d %H:%M:%f', 'now'),
		  updated_at           = strftime('%Y-%m-%d %H:%M:%f', 'now')
		 WHERE id = ?`,
		c.Title, c.Summary, beatID)
	if err != nil {
		return fmt.Errorf("save canonical: %w", err)
	}
	return nil
}

// SetFeedback records the user's per-beat signal. feedback must be "like",
// "dislike", or "" (empty clears the signal and nulls feedback_at).
// This signal is independent from per-item feedback and is never propagated
// to items.user_feedback or the classifier's preference manager.
func (r *BeatRepository) SetFeedback(ctx context.Context, beatID int64, feedback string) error {
	switch feedback {
	case "like", "dislike", "":
	default:
		return fmt.Errorf("invalid beat feedback value %q: must be like, dislike, or empty", feedback)
	}

	var err error
	if feedback == "" {
		_, err = r.db.ExecContext(ctx,
			`UPDATE beats SET feedback = '', feedback_at = NULL,
			  updated_at = strftime('%Y-%m-%d %H:%M:%f', 'now')
			 WHERE id = ?`, beatID)
	} else {
		_, err = r.db.ExecContext(ctx,
			`UPDATE beats SET feedback = ?, feedback_at = strftime('%Y-%m-%d %H:%M:%f', 'now'),
			  updated_at = strftime('%Y-%m-%d %H:%M:%f', 'now')
			 WHERE id = ?`, feedback, beatID)
	}
	if err != nil {
		return fmt.Errorf("set beat feedback: %w", err)
	}
	return nil
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
