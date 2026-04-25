package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/jmoiron/sqlx"

	"github.com/umputun/newscope/pkg/domain"
)

// GroupingRepository handles CRUD for user-defined beat groupings.
type GroupingRepository struct {
	db *sqlx.DB
}

// NewGroupingRepository creates a grouping repository.
func NewGroupingRepository(db *sqlx.DB) *GroupingRepository {
	return &GroupingRepository{db: db}
}

// groupingRow is the DB scan target for a groupings row.
type groupingRow struct {
	ID           int64  `db:"id"`
	Name         string `db:"name"`
	Slug         string `db:"slug"`
	Tags         string `db:"tags"` // JSON array
	DisplayOrder int    `db:"display_order"`
	CreatedAt    string `db:"created_at"`
	UpdatedAt    string `db:"updated_at"`
}

func (r groupingRow) toDomain() (domain.Grouping, error) {
	var tags []string
	if err := json.Unmarshal([]byte(r.Tags), &tags); err != nil {
		return domain.Grouping{}, fmt.Errorf("unmarshal tags: %w", err)
	}
	g := domain.Grouping{
		ID:           r.ID,
		Name:         r.Name,
		Slug:         r.Slug,
		Tags:         tags,
		DisplayOrder: r.DisplayOrder,
	}
	return g, nil
}

// ListGroupings returns all groupings ordered by display_order ASC, id ASC.
func (r *GroupingRepository) ListGroupings(ctx context.Context) ([]domain.Grouping, error) {
	var rows []groupingRow
	if err := r.db.SelectContext(ctx, &rows,
		`SELECT id, name, slug, tags, display_order, created_at, updated_at
		 FROM groupings ORDER BY display_order ASC, id ASC`); err != nil {
		return nil, fmt.Errorf("list groupings: %w", err)
	}
	out := make([]domain.Grouping, 0, len(rows))
	for _, row := range rows {
		g, err := row.toDomain()
		if err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, nil
}

// GetGrouping returns a single grouping by id.
func (r *GroupingRepository) GetGrouping(ctx context.Context, id int64) (domain.Grouping, error) {
	var row groupingRow
	err := r.db.GetContext(ctx, &row,
		`SELECT id, name, slug, tags, display_order, created_at, updated_at
		 FROM groupings WHERE id = ?`, id)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Grouping{}, fmt.Errorf("grouping not found")
	}
	if err != nil {
		return domain.Grouping{}, fmt.Errorf("get grouping: %w", err)
	}
	return row.toDomain()
}

// GetGroupingBySlug returns a single grouping by slug.
func (r *GroupingRepository) GetGroupingBySlug(ctx context.Context, slug string) (domain.Grouping, error) {
	var row groupingRow
	err := r.db.GetContext(ctx, &row,
		`SELECT id, name, slug, tags, display_order, created_at, updated_at
		 FROM groupings WHERE slug = ?`, slug)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Grouping{}, fmt.Errorf("grouping not found")
	}
	if err != nil {
		return domain.Grouping{}, fmt.Errorf("get grouping by slug: %w", err)
	}
	return row.toDomain()
}

// CreateGrouping inserts a new grouping. Name and tags are normalized.
// display_order is set to max(existing)+1 so the new grouping lands at the end.
// The slug is derived from name; collisions are resolved by appending -2, -3, etc.
func (r *GroupingRepository) CreateGrouping(ctx context.Context, g domain.Grouping) (int64, error) {
	slug, err := r.uniqueSlug(ctx, Slugify(g.Name), 0)
	if err != nil {
		return 0, err
	}
	tags := normalizeTags(g.Tags)
	tagsJSON, err := json.Marshal(tags)
	if err != nil {
		return 0, fmt.Errorf("marshal tags: %w", err)
	}

	res, err := r.db.ExecContext(ctx, `
		INSERT INTO groupings (name, slug, tags, display_order)
		VALUES (?, ?, ?, COALESCE((SELECT MAX(display_order) FROM groupings), -1) + 1)`,
		g.Name, slug, string(tagsJSON))
	if err != nil {
		return 0, fmt.Errorf("create grouping: %w", err)
	}
	return res.LastInsertId()
}

// UpdateGrouping updates name, slug, and tags of an existing grouping.
func (r *GroupingRepository) UpdateGrouping(ctx context.Context, g domain.Grouping) error {
	slug, err := r.uniqueSlug(ctx, Slugify(g.Name), g.ID)
	if err != nil {
		return err
	}
	tags := normalizeTags(g.Tags)
	tagsJSON, err := json.Marshal(tags)
	if err != nil {
		return fmt.Errorf("marshal tags: %w", err)
	}
	_, err = r.db.ExecContext(ctx,
		`UPDATE groupings SET name = ?, slug = ?, tags = ? WHERE id = ?`,
		g.Name, slug, string(tagsJSON), g.ID)
	if err != nil {
		return fmt.Errorf("update grouping: %w", err)
	}
	return nil
}

// DeleteGrouping removes a grouping. ON DELETE SET NULL in the assignments
// table handles releasing any assigned beats back to the main inbox.
func (r *GroupingRepository) DeleteGrouping(ctx context.Context, id int64) error {
	_, err := r.db.ExecContext(ctx, `DELETE FROM groupings WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete grouping: %w", err)
	}
	return nil
}

// ReorderGroupings sets display_order for each id in the order provided.
// Runs as a single transaction; ids not listed are unaffected.
func (r *GroupingRepository) ReorderGroupings(ctx context.Context, idsInOrder []int64) error {
	tx, err := r.db.BeginTxx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	for i, id := range idsInOrder {
		if _, err := tx.ExecContext(ctx,
			`UPDATE groupings SET display_order = ? WHERE id = ?`, i, id); err != nil {
			return fmt.Errorf("reorder grouping %d: %w", id, err)
		}
	}
	return tx.Commit()
}

// BeatTagSet returns the union of topics and entities across all items in the given beat.
// Two queries are used because combining multiple json_each columns in one SQLite query
// is verbose and error-prone; the Go-side union is cleaner.
func (r *GroupingRepository) BeatTagSet(ctx context.Context, beatID int64) ([]string, error) {
	// COALESCE guards against SQL NULL (pre-migration rows) and json_each.value IS NOT
	// NULL guards against JSON null literals that produce a NULL value row.
	const baseQuery = `
		SELECT DISTINCT json_each.value
		FROM beat_members bm
		JOIN items i ON i.id = bm.item_id, json_each(COALESCE(%s, '[]'))
		WHERE bm.beat_id = ?
		  AND json_each.value IS NOT NULL`

	seen := make(map[string]struct{})
	for _, col := range []string{"i.topics", "i.entities"} {
		var vals []string
		if err := r.db.SelectContext(ctx, &vals, fmt.Sprintf(baseQuery, col), beatID); err != nil {
			return nil, fmt.Errorf("beat tag set (%s): %w", col, err)
		}
		for _, v := range vals {
			seen[v] = struct{}{}
		}
	}

	result := make([]string, 0, len(seen))
	for v := range seen {
		result = append(result, v)
	}
	return result, nil
}

// UpsertAssignment inserts or updates the grouping assignment for a beat.
// groupingID may be nil, recording "computed but matched no grouping".
func (r *GroupingRepository) UpsertAssignment(ctx context.Context, beatID int64, groupingID *int64) error {
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO beat_grouping_assignments (beat_id, grouping_id, computed_at)
		VALUES (?, ?, CURRENT_TIMESTAMP)
		ON CONFLICT(beat_id) DO UPDATE SET
			grouping_id = excluded.grouping_id,
			computed_at = excluded.computed_at`,
		beatID, groupingID)
	if err != nil {
		return fmt.Errorf("upsert assignment: %w", err)
	}
	return nil
}

// ActiveBeatIDs returns the IDs of beats whose first_seen_at is after since.
func (r *GroupingRepository) ActiveBeatIDs(ctx context.Context, since time.Time) ([]int64, error) {
	var ids []int64
	if err := r.db.SelectContext(ctx, &ids,
		`SELECT id FROM beats WHERE first_seen_at > ?`, since); err != nil {
		return nil, fmt.Errorf("active beat ids: %w", err)
	}
	return ids, nil
}

// GroupingCounts returns a map of grouping_id → unread beat count for the dropdown.
// Key 0 represents the main inbox (beats with no assignment or assignment.grouping_id IS NULL).
func (r *GroupingRepository) GroupingCounts(ctx context.Context) (map[int64]int, error) {
	type row struct {
		GID   int64 `db:"gid"`
		Count int   `db:"cnt"`
	}
	var rows []row
	err := r.db.SelectContext(ctx, &rows, `
		WITH unread AS (
			SELECT b.id AS beat_id
			FROM beats b
			JOIN beat_members bm ON bm.beat_id = b.id
			JOIN items i ON i.id = bm.item_id
			GROUP BY b.id
			HAVING (COUNT(bm.item_id) = 1 OR b.canonical_title IS NOT NULL)
			   AND SUM(CASE WHEN b.last_viewed_at IS NULL OR bm.added_at > b.last_viewed_at THEN 1 ELSE 0 END) > 0
		)
		SELECT COALESCE(a.grouping_id, 0) AS gid, COUNT(*) AS cnt
		FROM unread u
		LEFT JOIN beat_grouping_assignments a ON a.beat_id = u.beat_id
		GROUP BY gid`)
	if err != nil {
		return nil, fmt.Errorf("grouping counts: %w", err)
	}
	out := make(map[int64]int, len(rows))
	for _, r := range rows {
		out[r.GID] = r.Count
	}
	return out, nil
}

// SuggestTags returns up to limit distinct tags (from items.topics and items.entities)
// whose lowercase representation starts with the given prefix (case-insensitive).
// Results are sorted alphabetically.
func (r *GroupingRepository) SuggestTags(ctx context.Context, prefix string, limit int) ([]string, error) {
	if limit <= 0 {
		limit = 50
	}
	var tags []string
	err := r.db.SelectContext(ctx, &tags, `
		SELECT DISTINCT tag FROM (
			SELECT json_each.value AS tag
			FROM items, json_each(COALESCE(topics, '[]'))
			WHERE json_each.value IS NOT NULL AND length(json_each.value) >= 2
			UNION
			SELECT json_each.value AS tag
			FROM items, json_each(COALESCE(entities, '[]'))
			WHERE json_each.value IS NOT NULL AND length(json_each.value) >= 2
		)
		WHERE lower(tag) LIKE lower(?) || '%'
		ORDER BY tag
		LIMIT ?`, prefix, limit)
	if err != nil {
		return nil, fmt.Errorf("suggest tags: %w", err)
	}
	return tags, nil
}

// uniqueSlug returns a slug that does not conflict with any existing row
// (excluding the row with excludeID, so updates don't conflict with themselves).
func (r *GroupingRepository) uniqueSlug(ctx context.Context, base string, excludeID int64) (string, error) {
	slug := base
	for i := 2; ; i++ {
		var count int
		if err := r.db.GetContext(ctx, &count,
			`SELECT COUNT(*) FROM groupings WHERE slug = ? AND id != ?`, slug, excludeID); err != nil {
			return "", fmt.Errorf("check slug: %w", err)
		}
		if count == 0 {
			return slug, nil
		}
		slug = fmt.Sprintf("%s-%d", base, i)
	}
}

// Slugify converts a name to a URL-safe lowercase hyphenated string.
func Slugify(name string) string {
	// lowercase
	s := strings.ToLower(name)
	// replace non-alphanumeric runs with a hyphen
	re := regexp.MustCompile(`[^a-z0-9]+`)
	s = re.ReplaceAllString(s, "-")
	// trim leading/trailing hyphens
	s = strings.Trim(s, "-")
	if s == "" {
		// fallback for purely non-ASCII names — use unicode letters/digits only
		var b strings.Builder
		for _, r := range strings.ToLower(name) {
			if unicode.IsLetter(r) || unicode.IsDigit(r) {
				b.WriteRune(r)
			} else {
				b.WriteRune('-')
			}
		}
		s = strings.Trim(b.String(), "-")
	}
	if s == "" {
		return "grouping"
	}
	return s
}

// normalizeTags lowercases, deduplicates, and sorts a tag slice.
func normalizeTags(tags []string) []string {
	seen := make(map[string]struct{}, len(tags))
	out := make([]string, 0, len(tags))
	for _, t := range tags {
		t = strings.ToLower(strings.TrimSpace(t))
		if t == "" {
			continue
		}
		if _, ok := seen[t]; ok {
			continue
		}
		seen[t] = struct{}{}
		out = append(out, t)
	}
	sort.Strings(out)
	return out
}
