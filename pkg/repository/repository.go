package repository

import (
	"context"
	"embed"
	"fmt"
	"strings"
	"time"

	"github.com/jmoiron/sqlx"
	_ "modernc.org/sqlite" // pure Go SQLite driver
)

//go:embed schema.sql
var schemaFS embed.FS

// Config represents database configuration
type Config struct {
	DSN             string
	MaxOpenConns    int
	MaxIdleConns    int
	ConnMaxLifetime time.Duration
}

// Repositories contains all repository instances
type Repositories struct {
	Feed           *FeedRepository
	Item           *ItemRepository
	Classification *ClassificationRepository
	Setting        *SettingRepository
	Embedding      *EmbeddingRepository
	DB             *sqlx.DB
}

// NewRepositories creates all repositories with a shared database connection
func NewRepositories(ctx context.Context, cfg Config) (*Repositories, error) {
	if cfg.DSN == "" {
		cfg.DSN = "file:newscope.db?cache=shared&mode=rwc&_txlock=immediate"
	}

	db, err := sqlx.Open("sqlite", cfg.DSN)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}

	// configure connection pool
	if cfg.MaxOpenConns > 0 {
		db.SetMaxOpenConns(cfg.MaxOpenConns)
	}
	if cfg.MaxIdleConns > 0 {
		db.SetMaxIdleConns(cfg.MaxIdleConns)
	}
	if cfg.ConnMaxLifetime > 0 {
		db.SetConnMaxLifetime(cfg.ConnMaxLifetime)
	}

	// enable foreign keys
	if _, err := db.ExecContext(ctx, "PRAGMA foreign_keys = ON"); err != nil {
		return nil, fmt.Errorf("enable foreign keys: %w", err)
	}

	// optimize SQLite settings
	pragmas := []string{
		"PRAGMA journal_mode = WAL",
		"PRAGMA synchronous = NORMAL",
		"PRAGMA cache_size = -64000", // 64MB cache
		"PRAGMA temp_store = MEMORY",
		"PRAGMA busy_timeout = 5000", // 5 second timeout for locks
	}

	for _, pragma := range pragmas {
		if _, err := db.ExecContext(ctx, pragma); err != nil {
			return nil, fmt.Errorf("execute %s: %w", pragma, err)
		}
	}

	// initialize schema
	if err := initSchema(ctx, db); err != nil {
		return nil, fmt.Errorf("init schema: %w", err)
	}

	// create repositories
	repos := &Repositories{
		Feed:           NewFeedRepository(db),
		Item:           NewItemRepository(db),
		Classification: NewClassificationRepository(db),
		Setting:        NewSettingRepository(db),
		Embedding:      NewEmbeddingRepository(db),
		DB:             db,
	}

	return repos, nil
}

// Close closes the database connection
func (r *Repositories) Close() error {
	return r.DB.Close()
}

// Ping verifies the database connection
func (r *Repositories) Ping(ctx context.Context) error {
	return r.DB.PingContext(ctx)
}

// initSchema creates tables if they don't exist and runs any needed migrations
func initSchema(ctx context.Context, db *sqlx.DB) error {
	// run pre-schema migrations against existing tables before loading schema,
	// so that CREATE INDEX statements in schema.sql see any newly added columns
	if err := migrateAddProcessedAt(ctx, db); err != nil {
		return fmt.Errorf("migrate processed_at: %w", err)
	}
	if err := migrateAddIconURL(ctx, db); err != nil {
		return fmt.Errorf("migrate icon_url: %w", err)
	}

	schema, err := schemaFS.ReadFile("schema.sql")
	if err != nil {
		return fmt.Errorf("read schema: %w", err)
	}

	if _, err := db.ExecContext(ctx, string(schema)); err != nil {
		return fmt.Errorf("execute schema: %w", err)
	}

	return nil
}

// migrateAddProcessedAt adds the processed_at column to items if missing,
// and backfills it from feedback_at so existing feedback rows land in "processed".
// Safe to run on a fresh DB: items table won't exist yet and the function is a no-op.
func migrateAddProcessedAt(ctx context.Context, db *sqlx.DB) error {
	var tableCount int
	err := db.GetContext(ctx, &tableCount,
		`SELECT count(*) FROM sqlite_master WHERE type='table' AND name='items'`)
	if err != nil {
		return fmt.Errorf("check items table: %w", err)
	}
	if tableCount == 0 {
		// fresh install — schema.sql will create the column as part of CREATE TABLE
		return nil
	}

	var columns []string
	if err := db.SelectContext(ctx, &columns,
		`SELECT name FROM pragma_table_info('items')`); err != nil {
		return fmt.Errorf("read items columns: %w", err)
	}
	for _, c := range columns {
		if c == "processed_at" {
			return nil
		}
	}

	if _, err := db.ExecContext(ctx,
		`ALTER TABLE items ADD COLUMN processed_at DATETIME`); err != nil {
		return fmt.Errorf("add processed_at column: %w", err)
	}
	if _, err := db.ExecContext(ctx,
		`UPDATE items SET processed_at = feedback_at WHERE feedback_at IS NOT NULL`); err != nil {
		return fmt.Errorf("backfill processed_at: %w", err)
	}
	return nil
}

// migrateAddIconURL adds the icon_url column to feeds if missing.
func migrateAddIconURL(ctx context.Context, db *sqlx.DB) error {
	var tableCount int
	err := db.GetContext(ctx, &tableCount,
		`SELECT count(*) FROM sqlite_master WHERE type='table' AND name='feeds'`)
	if err != nil {
		return fmt.Errorf("check feeds table: %w", err)
	}
	if tableCount == 0 {
		return nil
	}

	var columns []string
	if err := db.SelectContext(ctx, &columns,
		`SELECT name FROM pragma_table_info('feeds')`); err != nil {
		return fmt.Errorf("read feeds columns: %w", err)
	}
	for _, c := range columns {
		if c == "icon_url" {
			return nil
		}
	}

	if _, err := db.ExecContext(ctx,
		`ALTER TABLE feeds ADD COLUMN icon_url TEXT DEFAULT ''`); err != nil {
		return fmt.Errorf("add icon_url column: %w", err)
	}
	return nil
}

// criticalError wraps an error to signal repeater to stop retrying
type criticalError struct {
	err error
}

func (e *criticalError) Error() string {
	return e.err.Error()
}

// isLockError checks if an error is a SQLite lock/busy error
func isLockError(err error) bool {
	if err == nil {
		return false
	}
	errStr := err.Error()
	return strings.Contains(errStr, "SQLITE_BUSY") ||
		strings.Contains(errStr, "database is locked") ||
		strings.Contains(errStr, "database table is locked")
}
