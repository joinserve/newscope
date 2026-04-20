package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"

	"github.com/jmoiron/sqlx"

	"github.com/umputun/newscope/pkg/domain"
)

// SettingRepository handles setting-related database operations
type SettingRepository struct {
	db *sqlx.DB
}

// NewSettingRepository creates a new setting repository
func NewSettingRepository(db *sqlx.DB) *SettingRepository {
	return &SettingRepository{db: db}
}

// GetSetting retrieves a setting value
func (r *SettingRepository) GetSetting(ctx context.Context, key string) (string, error) {
	var value string
	err := r.db.GetContext(ctx, &value, "SELECT value FROM settings WHERE key = ?", key)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("get setting: %w", err)
	}
	return value, nil
}

// SetSetting stores a setting value
func (r *SettingRepository) SetSetting(ctx context.Context, key, value string) error {
	query := `
		INSERT INTO settings (key, value) VALUES (?, ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value
	`
	_, err := r.db.ExecContext(ctx, query, key, value)
	if err != nil {
		return fmt.Errorf("set setting: %w", err)
	}
	return nil
}

// GetSummaryThreshold returns the configured summary threshold or the default
// (domain.DefaultSummaryThreshold) when unset or unparseable.
func (r *SettingRepository) GetSummaryThreshold(ctx context.Context) (float64, error) {
	raw, err := r.GetSetting(ctx, domain.SettingSummaryThreshold)
	if err != nil {
		return 0, err
	}
	if raw == "" {
		return domain.DefaultSummaryThreshold, nil
	}
	v, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return domain.DefaultSummaryThreshold, nil
	}
	return v, nil
}

// SetSummaryThreshold persists the threshold value. Caller is responsible for
// validating the range (typically 0-10).
func (r *SettingRepository) SetSummaryThreshold(ctx context.Context, v float64) error {
	return r.SetSetting(ctx, domain.SettingSummaryThreshold, strconv.FormatFloat(v, 'f', -1, 64))
}
