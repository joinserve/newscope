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
