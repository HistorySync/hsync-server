package repository

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/historysync/hsync-server/pkg/model"
)

type systemSettingDB interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

// SystemSettingRepo persists dynamic system setting overrides. It is a thin
// key-value store; the service layer owns the whitelist, typing, and masking.
type SystemSettingRepo struct {
	db systemSettingDB
}

func NewSystemSettingRepo(db systemSettingDB) *SystemSettingRepo {
	return &SystemSettingRepo{db: db}
}

// Get returns the stored override for key, or nil when no row exists. A nil
// result tells the caller to fall back to the code-declared default, so a
// missing row is not an error.
func (r *SystemSettingRepo) Get(ctx context.Context, key string) (*model.SystemSetting, error) {
	if r == nil || r.db == nil {
		return nil, nil
	}
	const q = `
		SELECT key, value, value_type, description, updated_at
		FROM system_settings
		WHERE key = $1`

	s := &model.SystemSetting{}
	err := r.db.QueryRow(ctx, q, key).Scan(
		&s.Key, &s.Value, &s.ValueType, &s.Description, &s.UpdatedAt,
	)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get system setting: %w", err)
	}
	return s, nil
}

// Upsert writes the override value for a key. updated_at is maintained by the
// table's BEFORE UPDATE trigger (and the column default on insert), so it is
// not set here and is read back via RETURNING.
func (r *SystemSettingRepo) Upsert(ctx context.Context, s *model.SystemSetting) error {
	if r == nil || r.db == nil {
		return fmt.Errorf("system setting repository is not configured")
	}
	const q = `
		INSERT INTO system_settings (key, value, value_type, description)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (key) DO UPDATE SET
			value = EXCLUDED.value,
			value_type = EXCLUDED.value_type,
			description = EXCLUDED.description
		RETURNING updated_at`

	if err := r.db.QueryRow(ctx, q, s.Key, s.Value, s.ValueType, s.Description).Scan(&s.UpdatedAt); err != nil {
		return fmt.Errorf("upsert system setting: %w", err)
	}
	return nil
}
