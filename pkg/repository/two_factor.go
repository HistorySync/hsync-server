package repository

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/historysync/hsync-server/pkg/model"
)

// TwoFactorRepo manages TOTP secrets, lockout state, and backup codes.
type TwoFactorRepo struct {
	pool *pgxpool.Pool
}

// Get fetches a user's two-factor configuration. Returns nil if not configured.
func (r *TwoFactorRepo) Get(ctx context.Context, userID uuid.UUID) (*model.TwoFactor, error) {
	const q = `
		SELECT user_id, secret_encrypted, enabled, failed_attempts, locked_until,
		       last_used_at, enabled_at, created_at, updated_at
		FROM user_two_factor WHERE user_id = $1`

	tf := &model.TwoFactor{}
	err := r.pool.QueryRow(ctx, q, userID).Scan(
		&tf.UserID, &tf.SecretEncrypted, &tf.Enabled, &tf.FailedAttempts, &tf.LockedUntil,
		&tf.LastUsedAt, &tf.EnabledAt, &tf.CreatedAt, &tf.UpdatedAt,
	)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get two factor: %w", err)
	}
	return tf, nil
}

// UpsertSetup stores a freshly generated encrypted TOTP secret in setup mode.
func (r *TwoFactorRepo) UpsertSetup(ctx context.Context, userID uuid.UUID, secretEncrypted []byte) error {
	const q = `
		INSERT INTO user_two_factor (user_id, secret_encrypted)
		VALUES ($1, $2)
		ON CONFLICT (user_id) DO UPDATE SET
			secret_encrypted = EXCLUDED.secret_encrypted,
			enabled = false,
			failed_attempts = 0,
			locked_until = NULL,
			last_used_at = NULL,
			enabled_at = NULL,
			updated_at = now()`
	_, err := r.pool.Exec(ctx, q, userID, secretEncrypted)
	if err != nil {
		return fmt.Errorf("upsert two factor setup: %w", err)
	}
	return nil
}

// Enable marks a configured TOTP secret as active.
func (r *TwoFactorRepo) Enable(ctx context.Context, userID uuid.UUID, now time.Time) error {
	const q = `
		UPDATE user_two_factor
		SET enabled = true,
		    failed_attempts = 0,
		    locked_until = NULL,
		    last_used_at = $2,
		    enabled_at = $2,
		    updated_at = now()
		WHERE user_id = $1`
	_, err := r.pool.Exec(ctx, q, userID, now)
	if err != nil {
		return fmt.Errorf("enable two factor: %w", err)
	}
	return nil
}

// DeleteByUser removes a user's TOTP state and backup codes.
func (r *TwoFactorRepo) DeleteByUser(ctx context.Context, userID uuid.UUID) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin delete two factor: %w", err)
	}
	defer tx.Rollback(ctx)

	if _, err := tx.Exec(ctx, `DELETE FROM user_two_factor_backup_codes WHERE user_id = $1`, userID); err != nil {
		return fmt.Errorf("delete two factor backup codes: %w", err)
	}
	if _, err := tx.Exec(ctx, `DELETE FROM user_two_factor WHERE user_id = $1`, userID); err != nil {
		return fmt.Errorf("delete two factor: %w", err)
	}
	return tx.Commit(ctx)
}

// RecordSuccess clears lockout state after a valid TOTP or backup code.
func (r *TwoFactorRepo) RecordSuccess(ctx context.Context, userID uuid.UUID, now time.Time) error {
	const q = `
		UPDATE user_two_factor
		SET failed_attempts = 0,
		    locked_until = NULL,
		    last_used_at = $2,
		    updated_at = now()
		WHERE user_id = $1`
	_, err := r.pool.Exec(ctx, q, userID, now)
	if err != nil {
		return fmt.Errorf("record two factor success: %w", err)
	}
	return nil
}

// RecordFailure increments the failed attempt counter and locks when threshold
// is reached. It returns the updated failed count and lock time.
func (r *TwoFactorRepo) RecordFailure(ctx context.Context, userID uuid.UUID, threshold int, lockUntil time.Time) (int, *time.Time, error) {
	const q = `
		UPDATE user_two_factor
		SET failed_attempts = failed_attempts + 1,
		    locked_until = CASE WHEN failed_attempts + 1 >= $2 THEN $3 ELSE locked_until END,
		    updated_at = now()
		WHERE user_id = $1
		RETURNING failed_attempts, locked_until`
	var failed int
	var lockedUntil *time.Time
	err := r.pool.QueryRow(ctx, q, userID, threshold, lockUntil).Scan(&failed, &lockedUntil)
	if err != nil {
		return 0, nil, fmt.Errorf("record two factor failure: %w", err)
	}
	return failed, lockedUntil, nil
}

// ReplaceBackupCodes atomically replaces all backup codes for a user.
func (r *TwoFactorRepo) ReplaceBackupCodes(ctx context.Context, userID uuid.UUID, hashes []string) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin replace backup codes: %w", err)
	}
	defer tx.Rollback(ctx)

	if _, err := tx.Exec(ctx, `DELETE FROM user_two_factor_backup_codes WHERE user_id = $1`, userID); err != nil {
		return fmt.Errorf("delete existing backup codes: %w", err)
	}
	for _, hash := range hashes {
		if _, err := tx.Exec(ctx,
			`INSERT INTO user_two_factor_backup_codes (user_id, code_hash) VALUES ($1, $2)`,
			userID, hash); err != nil {
			return fmt.Errorf("insert backup code: %w", err)
		}
	}
	return tx.Commit(ctx)
}

// ListUnusedBackupCodes returns all unused backup code hashes for a user.
func (r *TwoFactorRepo) ListUnusedBackupCodes(ctx context.Context, userID uuid.UUID) ([]model.TwoFactorBackupCode, error) {
	const q = `
		SELECT id, user_id, code_hash, used_at, created_at
		FROM user_two_factor_backup_codes
		WHERE user_id = $1 AND used_at IS NULL
		ORDER BY created_at`
	rows, err := r.pool.Query(ctx, q, userID)
	if err != nil {
		return nil, fmt.Errorf("list backup codes: %w", err)
	}
	defer rows.Close()

	var codes []model.TwoFactorBackupCode
	for rows.Next() {
		var code model.TwoFactorBackupCode
		if err := rows.Scan(&code.ID, &code.UserID, &code.CodeHash, &code.UsedAt, &code.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan backup code: %w", err)
		}
		codes = append(codes, code)
	}
	return codes, rows.Err()
}

// CountUnusedBackupCodes returns the number of unused backup codes.
func (r *TwoFactorRepo) CountUnusedBackupCodes(ctx context.Context, userID uuid.UUID) (int, error) {
	const q = `SELECT COUNT(*) FROM user_two_factor_backup_codes WHERE user_id = $1 AND used_at IS NULL`
	var count int
	if err := r.pool.QueryRow(ctx, q, userID).Scan(&count); err != nil {
		return 0, fmt.Errorf("count backup codes: %w", err)
	}
	return count, nil
}

// MarkBackupCodeUsed consumes an unused backup code. It returns false if another
// request already used the same code.
func (r *TwoFactorRepo) MarkBackupCodeUsed(ctx context.Context, id uuid.UUID, now time.Time) (bool, error) {
	tag, err := r.pool.Exec(ctx,
		`UPDATE user_two_factor_backup_codes SET used_at = $2 WHERE id = $1 AND used_at IS NULL`,
		id, now)
	if err != nil {
		return false, fmt.Errorf("mark backup code used: %w", err)
	}
	return tag.RowsAffected() == 1, nil
}
