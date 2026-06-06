// Package repository provides data access layer implementations for
// PostgreSQL and Redis. It translates between Go models and database rows,
// and implements the query patterns needed by the service layer.
package repository

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"

	"github.com/historysync/hsync-server/pkg/model"
)

// Repos aggregates all repository instances.
type Repos struct {
	Users              *UserRepo
	Devices            *DeviceRepo
	Bundles            *BundleRepo
	Snapshots          *SnapshotRepo
	Quota              *QuotaRepo
	RefreshTokens      *RefreshTokenRepo
	DeviceRevocations  *DeviceRevocationRepo
	EmailVerifications *EmailVerificationRepo
	PasswordResets     *PasswordResetRepo
	TwoFactor          *TwoFactorRepo
	Passkeys           *PasskeyRepo
	AuditLogs          *AuditRepo
	NotificationPrefs  *NotificationPreferenceRepo
	NotificationOutbox *NotificationOutboxRepo
	SystemSettings     *SystemSettingRepo
	Idempotency        *IdempotencyRepo
}

// New creates all repository instances with the given database connections.
func New(pgPool *pgxpool.Pool, redisClient *redis.Client) *Repos {
	return &Repos{
		Users:              &UserRepo{pool: pgPool},
		Devices:            &DeviceRepo{pool: pgPool},
		Bundles:            &BundleRepo{pool: pgPool},
		Snapshots:          &SnapshotRepo{pool: pgPool},
		Quota:              &QuotaRepo{pool: pgPool, redis: redisClient},
		RefreshTokens:      &RefreshTokenRepo{pool: pgPool},
		DeviceRevocations:  &DeviceRevocationRepo{pool: pgPool},
		EmailVerifications: &EmailVerificationRepo{pool: pgPool},
		PasswordResets:     &PasswordResetRepo{pool: pgPool},
		TwoFactor:          &TwoFactorRepo{pool: pgPool},
		Passkeys:           &PasskeyRepo{pool: pgPool},
		AuditLogs:          &AuditRepo{pool: pgPool},
		NotificationPrefs:  NewNotificationPreferenceRepo(pgPool),
		NotificationOutbox: NewNotificationOutboxRepo(pgPool),
		SystemSettings:     NewSystemSettingRepo(pgPool),
		Idempotency:        &IdempotencyRepo{pool: pgPool},
	}
}

// NewPGXPool creates a PostgreSQL connection pool.
func NewPGXPool(ctx context.Context, databaseURL string) (*pgxpool.Pool, error) {
	config, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		return nil, fmt.Errorf("parse database url: %w", err)
	}
	config.MaxConns = 20
	config.MinConns = 2

	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		return nil, fmt.Errorf("create pgx pool: %w", err)
	}

	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping database: %w", err)
	}

	return pool, nil
}

// NewRedisClient creates a Redis client and verifies connectivity.
func NewRedisClient(ctx context.Context, redisURL string) (*redis.Client, error) {
	opts, err := redis.ParseURL(redisURL)
	if err != nil {
		return nil, fmt.Errorf("parse redis url: %w", err)
	}

	client := redis.NewClient(opts)
	if err := client.Ping(ctx).Err(); err != nil {
		return nil, fmt.Errorf("ping redis: %w", err)
	}

	return client, nil
}

// ── UserRepo ─────────────────────────────────────────────────

// UserRepo handles user CRUD operations.
type UserRepo struct {
	pool *pgxpool.Pool
}

// Create inserts a new user. Returns the created user with ID populated.
func (r *UserRepo) Create(ctx context.Context, user *model.User) error {
	const q = `
		INSERT INTO users (email, password_hash, display_name, tier, status, email_verified)
		VALUES ($1, $2, $3, $4, $5, $6)
		RETURNING id, created_at, updated_at`

	return r.pool.QueryRow(ctx, q,
		user.Email, user.PasswordHash, user.DisplayName,
		string(user.Tier), string(user.Status), user.EmailVerified,
	).Scan(&user.ID, &user.CreatedAt, &user.UpdatedAt)
}

// GetByID fetches a user by ID. Returns nil if not found.
func (r *UserRepo) GetByID(ctx context.Context, id uuid.UUID) (*model.User, error) {
	const q = `
		SELECT id, email, password_hash, display_name, tier, status,
		       email_verified, created_at, updated_at, deleted_at
		FROM users WHERE id = $1 AND deleted_at IS NULL`

	user := &model.User{}
	err := r.pool.QueryRow(ctx, q, id).Scan(
		&user.ID, &user.Email, &user.PasswordHash, &user.DisplayName,
		&user.Tier, &user.Status, &user.EmailVerified,
		&user.CreatedAt, &user.UpdatedAt, &user.DeletedAt,
	)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get user by id: %w", err)
	}
	return user, nil
}

// GetByEmail fetches a user by email (case-sensitive). Returns nil if not found.
func (r *UserRepo) GetByEmail(ctx context.Context, email string) (*model.User, error) {
	const q = `
		SELECT id, email, password_hash, display_name, tier, status,
		       email_verified, created_at, updated_at, deleted_at
		FROM users WHERE email = $1 AND deleted_at IS NULL`

	user := &model.User{}
	err := r.pool.QueryRow(ctx, q, email).Scan(
		&user.ID, &user.Email, &user.PasswordHash, &user.DisplayName,
		&user.Tier, &user.Status, &user.EmailVerified,
		&user.CreatedAt, &user.UpdatedAt, &user.DeletedAt,
	)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get user by email: %w", err)
	}
	return user, nil
}

// UpdateTier changes a user's subscription tier and status.
func (r *UserRepo) UpdateTier(ctx context.Context, id uuid.UUID, tier model.UserTier, status string) error {
	const q = `UPDATE users SET tier = $1, status = $2 WHERE id = $3 AND deleted_at IS NULL`
	_, err := r.pool.Exec(ctx, q, string(tier), status, id)
	return err
}

// UpdatePassword changes a user's password hash.
func (r *UserRepo) UpdatePassword(ctx context.Context, id uuid.UUID, hash string) error {
	const q = `UPDATE users SET password_hash = $1 WHERE id = $2 AND deleted_at IS NULL`
	_, err := r.pool.Exec(ctx, q, hash, id)
	return err
}

// SoftDelete marks a user as deleted.
func (r *UserRepo) SoftDelete(ctx context.Context, id uuid.UUID) error {
	now := time.Now()
	const q = `UPDATE users SET status = 'deleted', deleted_at = $1 WHERE id = $2 AND deleted_at IS NULL`
	_, err := r.pool.Exec(ctx, q, now, id)
	return err
}

// VerifyEmail marks a user's email as verified.
func (r *UserRepo) VerifyEmail(ctx context.Context, id uuid.UUID) error {
	const q = `UPDATE users SET email_verified = true WHERE id = $1`
	_, err := r.pool.Exec(ctx, q, id)
	return err
}

// List returns active users ordered by creation time descending.
func (r *UserRepo) List(ctx context.Context, limit, offset int32) ([]model.User, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}

	const q = `
		SELECT id, email, password_hash, display_name, tier, status,
		       email_verified, created_at, updated_at, deleted_at
		FROM users WHERE deleted_at IS NULL
		ORDER BY created_at DESC
		LIMIT $1 OFFSET $2`

	rows, err := r.pool.Query(ctx, q, limit, offset)
	if err != nil {
		return nil, fmt.Errorf("list users: %w", err)
	}
	defer rows.Close()

	return scanUsers(rows)
}

// Count returns the number of non-deleted users.
func (r *UserRepo) Count(ctx context.Context) (int64, error) {
	const q = `SELECT COUNT(*) FROM users WHERE deleted_at IS NULL`
	var count int64
	err := r.pool.QueryRow(ctx, q).Scan(&count)
	return count, err
}

// CountByStatus returns user counts grouped by account status.
func (r *UserRepo) CountByStatus(ctx context.Context) (map[model.UserStatus]int64, error) {
	const q = `SELECT status, COUNT(*) FROM users WHERE deleted_at IS NULL GROUP BY status`
	rows, err := r.pool.Query(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("count users by status: %w", err)
	}
	defer rows.Close()

	counts := make(map[model.UserStatus]int64)
	for rows.Next() {
		var status model.UserStatus
		var count int64
		if err := rows.Scan(&status, &count); err != nil {
			return nil, fmt.Errorf("scan user status count: %w", err)
		}
		counts[status] = count
	}
	return counts, rows.Err()
}

func (r *UserRepo) TwoFactorEnabledStats(ctx context.Context) (int64, int64, error) {
	const q = `
		SELECT COUNT(u.id),
		       COUNT(tf.user_id) FILTER (WHERE tf.enabled = true)
		FROM users u
		LEFT JOIN user_two_factor tf ON tf.user_id = u.id
		WHERE u.deleted_at IS NULL`
	var totalUsers int64
	var enabledUsers int64
	if err := r.pool.QueryRow(ctx, q).Scan(&totalUsers, &enabledUsers); err != nil {
		return 0, 0, fmt.Errorf("count two factor users: %w", err)
	}
	return enabledUsers, totalUsers, nil
}

// ListDeletedBefore returns users soft-deleted before the given time (for cleanup).
func (r *UserRepo) ListDeletedBefore(ctx context.Context, before time.Time) ([]model.User, error) {
	const q = `
		SELECT id, email, password_hash, display_name, tier, status,
		       email_verified, created_at, updated_at, deleted_at
		FROM users WHERE status = 'deleted' AND deleted_at < $1
		ORDER BY deleted_at LIMIT 100`

	rows, err := r.pool.Query(ctx, q, before)
	if err != nil {
		return nil, fmt.Errorf("list deleted users: %w", err)
	}
	defer rows.Close()

	var users []model.User
	for rows.Next() {
		var u model.User
		if err := rows.Scan(&u.ID, &u.Email, &u.PasswordHash, &u.DisplayName,
			&u.Tier, &u.Status, &u.EmailVerified,
			&u.CreatedAt, &u.UpdatedAt, &u.DeletedAt); err != nil {
			return nil, fmt.Errorf("scan user: %w", err)
		}
		users = append(users, u)
	}
	return users, rows.Err()
}

func scanUsers(rows pgx.Rows) ([]model.User, error) {
	var users []model.User
	for rows.Next() {
		var u model.User
		if err := rows.Scan(&u.ID, &u.Email, &u.PasswordHash, &u.DisplayName,
			&u.Tier, &u.Status, &u.EmailVerified,
			&u.CreatedAt, &u.UpdatedAt, &u.DeletedAt); err != nil {
			return nil, fmt.Errorf("scan user: %w", err)
		}
		users = append(users, u)
	}
	return users, rows.Err()
}

// ── DeviceRepo ───────────────────────────────────────────────

// DeviceRepo handles device registration and revocation.
type DeviceRepo struct {
	pool *pgxpool.Pool
}

// Create registers a new device for a user.
func (r *DeviceRepo) Create(ctx context.Context, d *model.Device) error {
	const q = `
		INSERT INTO devices (user_id, device_uuid, device_name, platform, app_version, token_hash)
		VALUES ($1, $2, $3, $4, $5, $6)
		RETURNING id, created_at`

	return r.pool.QueryRow(ctx, q,
		d.UserID, d.DeviceUUID, d.DeviceName, d.Platform, d.AppVersion, d.TokenHash,
	).Scan(&d.ID, &d.CreatedAt)
}

// GetByID fetches a device by its internal ID.
func (r *DeviceRepo) GetByID(ctx context.Context, id uuid.UUID) (*model.Device, error) {
	const q = `
		SELECT id, user_id, device_uuid, device_name, platform, app_version,
		       token_hash, last_sync_at, revoked_at, created_at
		FROM devices WHERE id = $1`

	d := &model.Device{}
	err := r.pool.QueryRow(ctx, q, id).Scan(
		&d.ID, &d.UserID, &d.DeviceUUID, &d.DeviceName, &d.Platform, &d.AppVersion,
		&d.TokenHash, &d.LastSyncAt, &d.RevokedAt, &d.CreatedAt,
	)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get device: %w", err)
	}
	return d, nil
}

// GetByUserAndUUID fetches a device by user ID + device UUID.
func (r *DeviceRepo) GetByUserAndUUID(ctx context.Context, userID, deviceUUID uuid.UUID) (*model.Device, error) {
	const q = `
		SELECT id, user_id, device_uuid, device_name, platform, app_version,
		       token_hash, last_sync_at, revoked_at, created_at
		FROM devices WHERE user_id = $1 AND device_uuid = $2`

	d := &model.Device{}
	err := r.pool.QueryRow(ctx, q, userID, deviceUUID).Scan(
		&d.ID, &d.UserID, &d.DeviceUUID, &d.DeviceName, &d.Platform, &d.AppVersion,
		&d.TokenHash, &d.LastSyncAt, &d.RevokedAt, &d.CreatedAt,
	)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get device by uuid: %w", err)
	}
	return d, nil
}

// ListByUser returns all devices belonging to a user.
func (r *DeviceRepo) ListByUser(ctx context.Context, userID uuid.UUID) ([]model.Device, error) {
	const q = `
		SELECT id, user_id, device_uuid, device_name, platform, app_version,
		       token_hash, last_sync_at, revoked_at, created_at
		FROM devices WHERE user_id = $1
		ORDER BY created_at`

	rows, err := r.pool.Query(ctx, q, userID)
	if err != nil {
		return nil, fmt.Errorf("list devices: %w", err)
	}
	defer rows.Close()

	var devices []model.Device
	for rows.Next() {
		var d model.Device
		if err := rows.Scan(&d.ID, &d.UserID, &d.DeviceUUID, &d.DeviceName, &d.Platform, &d.AppVersion,
			&d.TokenHash, &d.LastSyncAt, &d.RevokedAt, &d.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan device: %w", err)
		}
		devices = append(devices, d)
	}
	return devices, rows.Err()
}

// CountActiveByUser returns the number of non-revoked devices for a user.
func (r *DeviceRepo) CountActiveByUser(ctx context.Context, userID uuid.UUID) (int32, error) {
	const q = `SELECT COUNT(*) FROM devices WHERE user_id = $1 AND revoked_at IS NULL`
	var count int32
	err := r.pool.QueryRow(ctx, q, userID).Scan(&count)
	return count, err
}

// CountActive returns the number of non-revoked devices.
func (r *DeviceRepo) CountActive(ctx context.Context) (int64, error) {
	const q = `SELECT COUNT(*) FROM devices WHERE revoked_at IS NULL`
	var count int64
	err := r.pool.QueryRow(ctx, q).Scan(&count)
	return count, err
}

// Revoke marks a device as revoked.
func (r *DeviceRepo) Revoke(ctx context.Context, userID, deviceUUID uuid.UUID) error {
	now := time.Now()
	const q = `UPDATE devices SET revoked_at = $1 WHERE user_id = $2 AND device_uuid = $3 AND revoked_at IS NULL`
	_, err := r.pool.Exec(ctx, q, now, userID, deviceUUID)
	return err
}

// UpdateTokenHash stores a new device token hash.
func (r *DeviceRepo) UpdateTokenHash(ctx context.Context, id uuid.UUID, hash []byte) error {
	const q = `UPDATE devices SET token_hash = $1 WHERE id = $2`
	_, err := r.pool.Exec(ctx, q, hash, id)
	return err
}

// GetByTokenHash fetches a device by its current token hash. Returns nil if not found.
func (r *DeviceRepo) GetByTokenHash(ctx context.Context, hash []byte) (*model.Device, error) {
	const q = `
		SELECT id, user_id, device_uuid, device_name, platform, app_version,
		       token_hash, last_sync_at, revoked_at, created_at
		FROM devices WHERE token_hash = $1`

	d := &model.Device{}
	err := r.pool.QueryRow(ctx, q, hash).Scan(
		&d.ID, &d.UserID, &d.DeviceUUID, &d.DeviceName, &d.Platform, &d.AppVersion,
		&d.TokenHash, &d.LastSyncAt, &d.RevokedAt, &d.CreatedAt,
	)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get device by token hash: %w", err)
	}
	return d, nil
}

// UpdateLastSync updates the last_sync_at timestamp for a device.
func (r *DeviceRepo) UpdateLastSync(ctx context.Context, id uuid.UUID) error {
	now := time.Now()
	const q = `UPDATE devices SET last_sync_at = $1 WHERE id = $2`
	_, err := r.pool.Exec(ctx, q, now, id)
	return err
}

// ── BundleRepo ───────────────────────────────────────────────

// BundleRepo handles bundle metadata indexing and querying.
type BundleRepo struct {
	pool *pgxpool.Pool
}

// Create inserts a new bundle metadata record.
func (r *BundleRepo) Create(ctx context.Context, b *model.BundleMeta) error {
	const q = `
		INSERT INTO bundles (bundle_id, user_id, uploader_device_uuid,
		                     lamport_lo, lamport_hi, event_count, size_bytes,
		                     cipher_id, key_generation)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		RETURNING uploaded_at`

	return r.pool.QueryRow(ctx, q,
		b.BundleID, b.UserID, b.UploaderDeviceUUID,
		b.LamportLo, b.LamportHi, b.EventCount, b.SizeBytes,
		b.CipherID, b.KeyGeneration,
	).Scan(&b.UploadedAt)
}

// GetByID fetches a bundle by user ID and bundle ID.
func (r *BundleRepo) GetByID(ctx context.Context, userID uuid.UUID, bundleID string) (*model.BundleMeta, error) {
	const q = `
		SELECT bundle_id, user_id, uploader_device_uuid,
		       lamport_lo, lamport_hi, event_count, size_bytes,
		       cipher_id, key_generation, uploaded_at, deleted_at
		FROM bundles WHERE user_id = $1 AND bundle_id = $2 AND deleted_at IS NULL`

	b := &model.BundleMeta{}
	err := r.pool.QueryRow(ctx, q, userID, bundleID).Scan(
		&b.BundleID, &b.UserID, &b.UploaderDeviceUUID,
		&b.LamportLo, &b.LamportHi, &b.EventCount, &b.SizeBytes,
		&b.CipherID, &b.KeyGeneration, &b.UploadedAt, &b.DeletedAt,
	)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get bundle: %w", err)
	}
	return b, nil
}

// ExistsByID checks whether a bundle ID already exists (globally).
func (r *BundleRepo) ExistsByID(ctx context.Context, bundleID string) (bool, error) {
	const q = `SELECT EXISTS(SELECT 1 FROM bundles WHERE bundle_id = $1)`
	var exists bool
	err := r.pool.QueryRow(ctx, q, bundleID).Scan(&exists)
	return exists, err
}

// ListByDevice returns bundles for a user+device, ordered by lamport_lo ascending.
// Supports cursor-based pagination via afterLamport.
func (r *BundleRepo) ListByDevice(ctx context.Context, userID, deviceUUID uuid.UUID, afterLamport int64, limit int32) ([]model.BundleMeta, error) {
	const q = `
		SELECT bundle_id, user_id, uploader_device_uuid,
		       lamport_lo, lamport_hi, event_count, size_bytes,
		       cipher_id, key_generation, uploaded_at, deleted_at
		FROM bundles
		WHERE user_id = $1 AND uploader_device_uuid = $2
		      AND lamport_lo > $3 AND deleted_at IS NULL
		ORDER BY lamport_lo
		LIMIT $4`

	rows, err := r.pool.Query(ctx, q, userID, deviceUUID, afterLamport, limit)
	if err != nil {
		return nil, fmt.Errorf("list bundles by device: %w", err)
	}
	defer rows.Close()

	return scanBundles(rows)
}

// ListByUser returns all bundles for a user, with cursor pagination by bundle_id.
func (r *BundleRepo) ListByUser(ctx context.Context, userID uuid.UUID, cursor string, limit int32) ([]model.BundleMeta, error) {
	var rows pgx.Rows
	var err error

	if cursor == "" {
		const q = `
			SELECT bundle_id, user_id, uploader_device_uuid,
			       lamport_lo, lamport_hi, event_count, size_bytes,
			       cipher_id, key_generation, uploaded_at, deleted_at
			FROM bundles
			WHERE user_id = $1 AND deleted_at IS NULL
			ORDER BY bundle_id
			LIMIT $2`
		rows, err = r.pool.Query(ctx, q, userID, limit)
	} else {
		const q = `
			SELECT bundle_id, user_id, uploader_device_uuid,
			       lamport_lo, lamport_hi, event_count, size_bytes,
			       cipher_id, key_generation, uploaded_at, deleted_at
			FROM bundles
			WHERE user_id = $1 AND bundle_id > $2 AND deleted_at IS NULL
			ORDER BY bundle_id
			LIMIT $3`
		rows, err = r.pool.Query(ctx, q, userID, cursor, limit)
	}

	if err != nil {
		return nil, fmt.Errorf("list bundles: %w", err)
	}
	defer rows.Close()

	return scanBundles(rows)
}

// SoftDelete marks a bundle as deleted and returns the deleted metadata.
func (r *BundleRepo) SoftDelete(ctx context.Context, userID uuid.UUID, bundleID string) (*model.BundleMeta, error) {
	now := time.Now()
	const q = `
		UPDATE bundles
		SET deleted_at = $1
		WHERE user_id = $2 AND bundle_id = $3 AND deleted_at IS NULL
		RETURNING bundle_id, user_id, uploader_device_uuid,
		          lamport_lo, lamport_hi, event_count, size_bytes,
		          cipher_id, key_generation, uploaded_at, deleted_at`

	meta := &model.BundleMeta{}
	err := r.pool.QueryRow(ctx, q, now, userID, bundleID).Scan(
		&meta.BundleID, &meta.UserID, &meta.UploaderDeviceUUID,
		&meta.LamportLo, &meta.LamportHi, &meta.EventCount, &meta.SizeBytes,
		&meta.CipherID, &meta.KeyGeneration, &meta.UploadedAt, &meta.DeletedAt,
	)
	if err == pgx.ErrNoRows {
		return nil, fmt.Errorf("bundle not found")
	}
	if err != nil {
		return nil, err
	}
	return meta, nil
}

// SumSizeByUser returns the total size of all non-deleted bundles for a user.
func (r *BundleRepo) SumSizeByUser(ctx context.Context, userID uuid.UUID) (int64, error) {
	const q = `SELECT COALESCE(SUM(size_bytes), 0) FROM bundles WHERE user_id = $1 AND deleted_at IS NULL`
	var total int64
	err := r.pool.QueryRow(ctx, q, userID).Scan(&total)
	return total, err
}

// CountByUser returns the number of non-deleted bundles for a user.
func (r *BundleRepo) CountByUser(ctx context.Context, userID uuid.UUID) (int32, error) {
	const q = `SELECT COUNT(*) FROM bundles WHERE user_id = $1 AND deleted_at IS NULL`
	var count int32
	err := r.pool.QueryRow(ctx, q, userID).Scan(&count)
	return count, err
}

// CountAll returns the number of non-deleted bundles.
func (r *BundleRepo) CountAll(ctx context.Context) (int64, error) {
	const q = `SELECT COUNT(*) FROM bundles WHERE deleted_at IS NULL`
	var count int64
	err := r.pool.QueryRow(ctx, q).Scan(&count)
	return count, err
}

// SumSizeAll returns the total size of all non-deleted bundles.
func (r *BundleRepo) SumSizeAll(ctx context.Context) (int64, error) {
	const q = `SELECT COALESCE(SUM(size_bytes), 0) FROM bundles WHERE deleted_at IS NULL`
	var total int64
	err := r.pool.QueryRow(ctx, q).Scan(&total)
	return total, err
}

// ListForOpsConsistency returns a bounded sample of active bundle metadata for
// operator consistency checks. The server still treats the blob payload as
// opaque; callers use only IDs and size metadata.
func (r *BundleRepo) ListForOpsConsistency(ctx context.Context, limit int32) ([]model.BundleMeta, error) {
	if limit <= 0 || limit > 1000 {
		limit = 1000
	}
	const q = `
		SELECT bundle_id, user_id, uploader_device_uuid,
		       lamport_lo, lamport_hi, event_count, size_bytes,
		       cipher_id, key_generation, uploaded_at, deleted_at
		FROM bundles
		WHERE deleted_at IS NULL
		ORDER BY uploaded_at DESC, bundle_id
		LIMIT $1`

	rows, err := r.pool.Query(ctx, q, limit)
	if err != nil {
		return nil, fmt.Errorf("list bundles for ops consistency: %w", err)
	}
	defer rows.Close()

	return scanBundles(rows)
}

// ListDeletedBefore returns bundles soft-deleted before the given time (for cleanup).
func (r *BundleRepo) ListDeletedBefore(ctx context.Context, before time.Time) ([]model.BundleMeta, error) {
	const q = `
		SELECT bundle_id, user_id, uploader_device_uuid,
		       lamport_lo, lamport_hi, event_count, size_bytes,
		       cipher_id, key_generation, uploaded_at, deleted_at
		FROM bundles WHERE deleted_at IS NOT NULL AND deleted_at < $1
		ORDER BY deleted_at LIMIT 100`

	rows, err := r.pool.Query(ctx, q, before)
	if err != nil {
		return nil, fmt.Errorf("list deleted bundles: %w", err)
	}
	defer rows.Close()

	return scanBundles(rows)
}

// HardDelete physically removes a bundle record.
func (r *BundleRepo) HardDelete(ctx context.Context, userID uuid.UUID, bundleID string) error {
	const q = `DELETE FROM bundles WHERE user_id = $1 AND bundle_id = $2`
	_, err := r.pool.Exec(ctx, q, userID, bundleID)
	return err
}

// CountDeletedBefore returns how many bundles were soft-deleted before the given
// time and their total size in bytes, for retention-cleanup reporting.
func (r *BundleRepo) CountDeletedBefore(ctx context.Context, before time.Time) (int64, int64, error) {
	const q = `
		SELECT COUNT(*), COALESCE(SUM(size_bytes), 0)
		FROM bundles WHERE deleted_at IS NOT NULL AND deleted_at < $1`
	var count, bytes int64
	if err := r.pool.QueryRow(ctx, q, before).Scan(&count, &bytes); err != nil {
		return 0, 0, fmt.Errorf("count deleted bundles: %w", err)
	}
	return count, bytes, nil
}

func scanBundles(rows pgx.Rows) ([]model.BundleMeta, error) {
	var bundles []model.BundleMeta
	for rows.Next() {
		var b model.BundleMeta
		if err := rows.Scan(&b.BundleID, &b.UserID, &b.UploaderDeviceUUID,
			&b.LamportLo, &b.LamportHi, &b.EventCount, &b.SizeBytes,
			&b.CipherID, &b.KeyGeneration, &b.UploadedAt, &b.DeletedAt); err != nil {
			return nil, fmt.Errorf("scan bundle: %w", err)
		}
		bundles = append(bundles, b)
	}
	return bundles, rows.Err()
}

// ── SnapshotRepo ─────────────────────────────────────────────

// SnapshotRepo handles snapshot metadata indexing.
type SnapshotRepo struct {
	pool *pgxpool.Pool
}

// Create inserts a new snapshot metadata record.
func (r *SnapshotRepo) Create(ctx context.Context, s *model.SnapshotMeta) error {
	const q = `
		INSERT INTO snapshots (snapshot_id, user_id, base_hlc, size_bytes, cipher_id, key_generation)
		VALUES ($1, $2, $3, $4, $5, $6)
		RETURNING created_at`

	return r.pool.QueryRow(ctx, q,
		s.SnapshotID, s.UserID, s.BaseHLC, s.SizeBytes, s.CipherID, s.KeyGeneration,
	).Scan(&s.CreatedAt)
}

// GetLatest returns the most recent snapshot for a user.
func (r *SnapshotRepo) GetLatest(ctx context.Context, userID uuid.UUID) (*model.SnapshotMeta, error) {
	const q = `
		SELECT snapshot_id, user_id, base_hlc, size_bytes, cipher_id, key_generation, created_at, deleted_at
		FROM snapshots WHERE user_id = $1 AND deleted_at IS NULL
		ORDER BY created_at DESC LIMIT 1`

	s := &model.SnapshotMeta{}
	err := r.pool.QueryRow(ctx, q, userID).Scan(
		&s.SnapshotID, &s.UserID, &s.BaseHLC, &s.SizeBytes,
		&s.CipherID, &s.KeyGeneration, &s.CreatedAt, &s.DeletedAt,
	)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get latest snapshot: %w", err)
	}
	return s, nil
}

// GetByID fetches a snapshot by user ID and snapshot ID.
func (r *SnapshotRepo) GetByID(ctx context.Context, userID uuid.UUID, snapshotID string) (*model.SnapshotMeta, error) {
	const q = `
		SELECT snapshot_id, user_id, base_hlc, size_bytes, cipher_id, key_generation, created_at, deleted_at
		FROM snapshots WHERE user_id = $1 AND snapshot_id = $2 AND deleted_at IS NULL`

	s := &model.SnapshotMeta{}
	err := r.pool.QueryRow(ctx, q, userID, snapshotID).Scan(
		&s.SnapshotID, &s.UserID, &s.BaseHLC, &s.SizeBytes,
		&s.CipherID, &s.KeyGeneration, &s.CreatedAt, &s.DeletedAt,
	)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get snapshot: %w", err)
	}
	return s, nil
}

// SoftDelete marks a snapshot as deleted and returns the deleted metadata.
func (r *SnapshotRepo) SoftDelete(ctx context.Context, userID uuid.UUID, snapshotID string) (*model.SnapshotMeta, error) {
	now := time.Now()
	const q = `
		UPDATE snapshots
		SET deleted_at = $1
		WHERE user_id = $2 AND snapshot_id = $3 AND deleted_at IS NULL
		RETURNING snapshot_id, user_id, base_hlc, size_bytes, cipher_id, key_generation, created_at, deleted_at`

	meta := &model.SnapshotMeta{}
	err := r.pool.QueryRow(ctx, q, now, userID, snapshotID).Scan(
		&meta.SnapshotID, &meta.UserID, &meta.BaseHLC, &meta.SizeBytes,
		&meta.CipherID, &meta.KeyGeneration, &meta.CreatedAt, &meta.DeletedAt,
	)
	if err == pgx.ErrNoRows {
		return nil, fmt.Errorf("snapshot not found")
	}
	if err != nil {
		return nil, err
	}
	return meta, nil
}

// CountByUser returns the number of non-deleted snapshots for a user.
func (r *SnapshotRepo) CountByUser(ctx context.Context, userID uuid.UUID) (int32, error) {
	const q = `SELECT COUNT(*) FROM snapshots WHERE user_id = $1 AND deleted_at IS NULL`
	var count int32
	err := r.pool.QueryRow(ctx, q, userID).Scan(&count)
	return count, err
}

// CountAll returns the number of non-deleted snapshots.
func (r *SnapshotRepo) CountAll(ctx context.Context) (int64, error) {
	const q = `SELECT COUNT(*) FROM snapshots WHERE deleted_at IS NULL`
	var count int64
	err := r.pool.QueryRow(ctx, q).Scan(&count)
	return count, err
}

// SumSizeAll returns the total size of all non-deleted snapshots.
func (r *SnapshotRepo) SumSizeAll(ctx context.Context) (int64, error) {
	const q = `SELECT COALESCE(SUM(size_bytes), 0) FROM snapshots WHERE deleted_at IS NULL`
	var total int64
	err := r.pool.QueryRow(ctx, q).Scan(&total)
	return total, err
}

// ListForOpsConsistency returns a bounded sample of active snapshot metadata for
// operator consistency checks. It exposes metadata only and never reads snapshot
// blob contents.
func (r *SnapshotRepo) ListForOpsConsistency(ctx context.Context, limit int32) ([]model.SnapshotMeta, error) {
	if limit <= 0 || limit > 1000 {
		limit = 1000
	}
	const q = `
		SELECT snapshot_id, user_id, base_hlc, size_bytes, cipher_id, key_generation, created_at, deleted_at
		FROM snapshots
		WHERE deleted_at IS NULL
		ORDER BY created_at DESC, snapshot_id
		LIMIT $1`

	rows, err := r.pool.Query(ctx, q, limit)
	if err != nil {
		return nil, fmt.Errorf("list snapshots for ops consistency: %w", err)
	}
	defer rows.Close()

	var snapshots []model.SnapshotMeta
	for rows.Next() {
		var s model.SnapshotMeta
		if err := rows.Scan(&s.SnapshotID, &s.UserID, &s.BaseHLC, &s.SizeBytes,
			&s.CipherID, &s.KeyGeneration, &s.CreatedAt, &s.DeletedAt); err != nil {
			return nil, fmt.Errorf("scan ops consistency snapshot: %w", err)
		}
		snapshots = append(snapshots, s)
	}
	return snapshots, rows.Err()
}

// PruneOldest marks the oldest snapshots beyond the given limit as deleted and returns them.
func (r *SnapshotRepo) PruneOldest(ctx context.Context, userID uuid.UUID, keep int32) ([]model.SnapshotMeta, error) {
	const q = `
		WITH pruned AS (
			UPDATE snapshots
			SET deleted_at = now()
			WHERE user_id = $1 AND deleted_at IS NULL
			      AND snapshot_id NOT IN (
			          SELECT snapshot_id FROM snapshots
			          WHERE user_id = $1 AND deleted_at IS NULL
			          ORDER BY created_at DESC LIMIT $2
		      )
			RETURNING snapshot_id, user_id, base_hlc, size_bytes, cipher_id, key_generation, created_at, deleted_at
		)
		SELECT snapshot_id, user_id, base_hlc, size_bytes, cipher_id, key_generation, created_at, deleted_at
		FROM pruned`

	rows, err := r.pool.Query(ctx, q, userID, keep)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var snapshots []model.SnapshotMeta
	for rows.Next() {
		var s model.SnapshotMeta
		if err := rows.Scan(&s.SnapshotID, &s.UserID, &s.BaseHLC, &s.SizeBytes, &s.CipherID, &s.KeyGeneration, &s.CreatedAt, &s.DeletedAt); err != nil {
			return nil, fmt.Errorf("scan pruned snapshot: %w", err)
		}
		snapshots = append(snapshots, s)
	}
	return snapshots, rows.Err()
}

// HardDelete physically removes a snapshot record.
func (r *SnapshotRepo) HardDelete(ctx context.Context, userID uuid.UUID, snapshotID string) error {
	const q = `DELETE FROM snapshots WHERE user_id = $1 AND snapshot_id = $2`
	_, err := r.pool.Exec(ctx, q, userID, snapshotID)
	return err
}

// CountDeletedBefore returns how many snapshots were soft-deleted before the given
// time and their total size in bytes, for retention-cleanup reporting.
func (r *SnapshotRepo) CountDeletedBefore(ctx context.Context, before time.Time) (int64, int64, error) {
	const q = `
		SELECT COUNT(*), COALESCE(SUM(size_bytes), 0)
		FROM snapshots WHERE deleted_at IS NOT NULL AND deleted_at < $1`
	var count, bytes int64
	if err := r.pool.QueryRow(ctx, q, before).Scan(&count, &bytes); err != nil {
		return 0, 0, fmt.Errorf("count deleted snapshots: %w", err)
	}
	return count, bytes, nil
}

// ListDeletedBefore returns snapshots soft-deleted before the given time (for
// cleanup). It pages in batches of 100, ordered by deletion time, to bound the
// transaction scope during hard-delete purges.
func (r *SnapshotRepo) ListDeletedBefore(ctx context.Context, before time.Time) ([]model.SnapshotMeta, error) {
	const q = `
		SELECT snapshot_id, user_id, base_hlc, size_bytes, cipher_id, key_generation, created_at, deleted_at
		FROM snapshots WHERE deleted_at IS NOT NULL AND deleted_at < $1
		ORDER BY deleted_at LIMIT 100`

	rows, err := r.pool.Query(ctx, q, before)
	if err != nil {
		return nil, fmt.Errorf("list deleted snapshots: %w", err)
	}
	defer rows.Close()

	var snapshots []model.SnapshotMeta
	for rows.Next() {
		var s model.SnapshotMeta
		if err := rows.Scan(&s.SnapshotID, &s.UserID, &s.BaseHLC, &s.SizeBytes,
			&s.CipherID, &s.KeyGeneration, &s.CreatedAt, &s.DeletedAt); err != nil {
			return nil, fmt.Errorf("scan deleted snapshot: %w", err)
		}
		snapshots = append(snapshots, s)
	}
	return snapshots, rows.Err()
}

// ── QuotaRepo ────────────────────────────────────────────────

// QuotaRepo handles storage usage tracking and quota enforcement.
type QuotaRepo struct {
	pool  *pgxpool.Pool
	redis *redis.Client
}

// quotaCacheTTL is how long a GetUsage result is cached in Redis. The cache is
// read-through with write-invalidation, so a shorter TTL limits the window where
// a stale entry survives a missed invalidation (e.g. during RecalculateAllUsage).
const quotaCacheTTL = 30 * time.Second

func quotaCacheKey(userID uuid.UUID) string {
	return "quota:usage:" + userID.String()
}

// cacheUsage writes u into Redis with a short TTL. When redis is nil this is a
// no-op. Errors are silent (best-effort cache); the authoritative source is PG.
func (r *QuotaRepo) cacheUsage(ctx context.Context, u *model.QuotaUsage) {
	if r.redis == nil {
		return
	}
	data, err := json.Marshal(u)
	if err != nil {
		return
	}
	r.redis.Set(ctx, quotaCacheKey(u.UserID), data, quotaCacheTTL)
}

// invalidateUsageCache removes the cached usage for userID from Redis. When redis
// is nil this is a no-op. Errors are silent; a stale cache entry expires naturally.
func (r *QuotaRepo) invalidateUsageCache(ctx context.Context, userID uuid.UUID) {
	if r.redis == nil {
		return
	}
	r.redis.Del(ctx, quotaCacheKey(userID))
}

// getCachedUsage tries to read a cached QuotaUsage from Redis. It returns nil, nil
// on a cache miss or when Redis is unavailable, so the caller falls through to PG.
func (r *QuotaRepo) getCachedUsage(ctx context.Context, userID uuid.UUID) *model.QuotaUsage {
	if r.redis == nil {
		return nil
	}
	data, err := r.redis.Get(ctx, quotaCacheKey(userID)).Bytes()
	if err != nil {
		return nil
	}
	u := &model.QuotaUsage{}
	if err := json.Unmarshal(data, u); err != nil {
		return nil
	}
	return u
}

// GetUsage retrieves the current storage usage for a user. When Redis is
// configured it serves as a read-through cache (short TTL, invalidated on every
// write), reducing PostgreSQL reads for quota checks without weakening the
// atomic conditional UPDATE that is the authoritative enforcement point.
func (r *QuotaRepo) GetUsage(ctx context.Context, userID uuid.UUID) (*model.QuotaUsage, error) {
	if cached := r.getCachedUsage(ctx, userID); cached != nil {
		return cached, nil
	}

	const q = `
		SELECT user_id, total_bytes, bundle_count, snap_count, updated_at
		FROM storage_usage WHERE user_id = $1`

	u := &model.QuotaUsage{}
	err := r.pool.QueryRow(ctx, q, userID).Scan(
		&u.UserID, &u.TotalBytes, &u.BundleCount, &u.SnapCount, &u.UpdatedAt,
	)
	if err == pgx.ErrNoRows {
		empty := &model.QuotaUsage{UserID: userID}
		r.cacheUsage(ctx, empty)
		return empty, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get usage: %w", err)
	}
	r.cacheUsage(ctx, u)
	return u, nil
}

// GetLimits retrieves the quota limits for a user.
func (r *QuotaRepo) GetLimits(ctx context.Context, userID uuid.UUID) (*model.QuotaLimits, error) {
	const q = `
		SELECT user_id, storage_limit_bytes, max_devices, max_bundle_size,
		       max_snapshots, max_rpm, bundle_retention_days, override_reason, expires_at
		FROM quota_limits WHERE user_id = $1`

	l := &model.QuotaLimits{}
	err := r.pool.QueryRow(ctx, q, userID).Scan(
		&l.UserID, &l.StorageLimitBytes, &l.MaxDevices, &l.MaxBundleSize,
		&l.MaxSnapshots, &l.MaxRPM, &l.BundleRetentionDays, &l.OverrideReason, &l.ExpiresAt,
	)
	if err != nil {
		return nil, fmt.Errorf("get limits: %w", err)
	}
	return l, nil
}

// UpsertLimits inserts or updates quota limits for a user.
func (r *QuotaRepo) UpsertLimits(ctx context.Context, limits *model.QuotaLimits) error {
	const q = `
		INSERT INTO quota_limits (user_id, storage_limit_bytes, max_devices, max_bundle_size,
		                          max_snapshots, max_rpm, bundle_retention_days)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		ON CONFLICT (user_id) DO UPDATE SET
			storage_limit_bytes = EXCLUDED.storage_limit_bytes,
			max_devices = EXCLUDED.max_devices,
			max_bundle_size = EXCLUDED.max_bundle_size,
			max_snapshots = EXCLUDED.max_snapshots,
			max_rpm = EXCLUDED.max_rpm,
			bundle_retention_days = EXCLUDED.bundle_retention_days`

	_, err := r.pool.Exec(ctx, q,
		limits.UserID, limits.StorageLimitBytes, limits.MaxDevices,
		limits.MaxBundleSize, limits.MaxSnapshots, limits.MaxRPM,
		limits.BundleRetentionDays,
	)
	return err
}

// CreateUsage inserts an initial storage_usage row for a new user.
func (r *QuotaRepo) CreateUsage(ctx context.Context, userID uuid.UUID) error {
	const q = `INSERT INTO storage_usage (user_id) VALUES ($1) ON CONFLICT DO NOTHING`
	_, err := r.pool.Exec(ctx, q, userID)
	return err
}

// AddBundleUsage increments storage usage counters for a stored bundle.
func (r *QuotaRepo) AddBundleUsage(ctx context.Context, userID uuid.UUID, sizeBytes int64) error {
	const q = `
		INSERT INTO storage_usage (user_id, total_bytes, bundle_count)
		VALUES ($1, $2, 1)
		ON CONFLICT (user_id) DO UPDATE SET
			total_bytes = storage_usage.total_bytes + EXCLUDED.total_bytes,
			bundle_count = storage_usage.bundle_count + EXCLUDED.bundle_count,
			updated_at = now()`
	_, err := r.pool.Exec(ctx, q, userID, sizeBytes)
	if err != nil {
		return fmt.Errorf("add bundle usage: %w", err)
	}
	r.invalidateUsageCache(ctx, userID)
	return nil
}

// TryAddBundleUsage atomically reserves storage for a bundle only if doing so
// keeps the user at or below storageLimitBytes, returning false when the
// addition would exceed the limit. The conditional UPDATE serializes concurrent
// uploads on the row, closing the check-then-write race that otherwise lets
// simultaneous uploads oversell quota. AddBundleUsage is retained for callers
// (e.g. Enterprise providers) that enforce limits separately.
func (r *QuotaRepo) TryAddBundleUsage(ctx context.Context, userID uuid.UUID, sizeBytes, storageLimitBytes int64) (bool, error) {
	// Ensure the usage row exists so the conditional UPDATE can distinguish
	// "over quota" (0 rows updated) from "row not yet created".
	if _, err := r.pool.Exec(ctx,
		`INSERT INTO storage_usage (user_id) VALUES ($1) ON CONFLICT DO NOTHING`, userID); err != nil {
		return false, fmt.Errorf("ensure usage row: %w", err)
	}
	const q = `
		UPDATE storage_usage
		SET total_bytes = total_bytes + $2,
		    bundle_count = bundle_count + 1,
		    updated_at = now()
		WHERE user_id = $1 AND total_bytes + $2 <= $3`
	tag, err := r.pool.Exec(ctx, q, userID, sizeBytes, storageLimitBytes)
	if err != nil {
		return false, fmt.Errorf("add bundle usage: %w", err)
	}
	ok := tag.RowsAffected() == 1
	if ok {
		r.invalidateUsageCache(ctx, userID)
	}
	return ok, nil
}

// RemoveBundleUsage decrements storage usage counters for a deleted bundle.
func (r *QuotaRepo) RemoveBundleUsage(ctx context.Context, userID uuid.UUID, sizeBytes int64) error {
	const q = `
		UPDATE storage_usage
		SET total_bytes = GREATEST(total_bytes - $2, 0),
		    bundle_count = GREATEST(bundle_count - 1, 0),
		    updated_at = now()
		WHERE user_id = $1`
	_, err := r.pool.Exec(ctx, q, userID, sizeBytes)
	if err != nil {
		return fmt.Errorf("remove bundle usage: %w", err)
	}
	r.invalidateUsageCache(ctx, userID)
	return nil
}

// AddSnapshotUsage increments storage usage counters for a stored snapshot.
func (r *QuotaRepo) AddSnapshotUsage(ctx context.Context, userID uuid.UUID, sizeBytes int64) error {
	const q = `
		INSERT INTO storage_usage (user_id, total_bytes, snap_count)
		VALUES ($1, $2, 1)
		ON CONFLICT (user_id) DO UPDATE SET
			total_bytes = storage_usage.total_bytes + EXCLUDED.total_bytes,
			snap_count = storage_usage.snap_count + EXCLUDED.snap_count,
			updated_at = now()`
	_, err := r.pool.Exec(ctx, q, userID, sizeBytes)
	if err != nil {
		return fmt.Errorf("add snapshot usage: %w", err)
	}
	r.invalidateUsageCache(ctx, userID)
	return nil
}

// TryAddSnapshotUsage atomically reserves storage for a snapshot only if doing
// so keeps the user at or below storageLimitBytes, returning false when the
// addition would exceed the limit. See TryAddBundleUsage for the rationale.
func (r *QuotaRepo) TryAddSnapshotUsage(ctx context.Context, userID uuid.UUID, sizeBytes, storageLimitBytes int64) (bool, error) {
	if _, err := r.pool.Exec(ctx,
		`INSERT INTO storage_usage (user_id) VALUES ($1) ON CONFLICT DO NOTHING`, userID); err != nil {
		return false, fmt.Errorf("ensure usage row: %w", err)
	}
	const q = `
		UPDATE storage_usage
		SET total_bytes = total_bytes + $2,
		    snap_count = snap_count + 1,
		    updated_at = now()
		WHERE user_id = $1 AND total_bytes + $2 <= $3`
	tag, err := r.pool.Exec(ctx, q, userID, sizeBytes, storageLimitBytes)
	if err != nil {
		return false, fmt.Errorf("add snapshot usage: %w", err)
	}
	ok := tag.RowsAffected() == 1
	if ok {
		r.invalidateUsageCache(ctx, userID)
	}
	return ok, nil
}

// RemoveSnapshotUsage decrements storage usage counters for a deleted snapshot.
func (r *QuotaRepo) RemoveSnapshotUsage(ctx context.Context, userID uuid.UUID, sizeBytes int64) error {
	const q = `
		UPDATE storage_usage
		SET total_bytes = GREATEST(total_bytes - $2, 0),
		    snap_count = GREATEST(snap_count - 1, 0),
		    updated_at = now()
		WHERE user_id = $1`
	_, err := r.pool.Exec(ctx, q, userID, sizeBytes)
	if err != nil {
		return fmt.Errorf("remove snapshot usage: %w", err)
	}
	r.invalidateUsageCache(ctx, userID)
	return nil
}

// RecalculateUsage recomputes a user's storage_usage row from the authoritative
// bundle and snapshot rows (excluding soft-deleted ones), correcting drift such
// as the transient over-count a crash mid-upload can leave behind. It is
// idempotent and safe to re-run. A concurrent upload during the recompute may be
// missed and is corrected by the next run, so this is a best-effort reconcile
// rather than a transactional invariant. It returns the post-recompute usage.
func (r *QuotaRepo) RecalculateUsage(ctx context.Context, userID uuid.UUID) (*model.QuotaUsage, error) {
	const q = `
		INSERT INTO storage_usage (user_id, total_bytes, bundle_count, snap_count, updated_at)
		VALUES (
			$1,
			COALESCE((SELECT SUM(size_bytes) FROM bundles   WHERE user_id = $1 AND deleted_at IS NULL), 0)
			+ COALESCE((SELECT SUM(size_bytes) FROM snapshots WHERE user_id = $1 AND deleted_at IS NULL), 0),
			(SELECT COUNT(*) FROM bundles   WHERE user_id = $1 AND deleted_at IS NULL),
			(SELECT COUNT(*) FROM snapshots WHERE user_id = $1 AND deleted_at IS NULL),
			now()
		)
		ON CONFLICT (user_id) DO UPDATE SET
			total_bytes  = EXCLUDED.total_bytes,
			bundle_count = EXCLUDED.bundle_count,
			snap_count   = EXCLUDED.snap_count,
			updated_at   = now()
		RETURNING user_id, total_bytes, bundle_count, snap_count, updated_at`

	u := &model.QuotaUsage{}
	if err := r.pool.QueryRow(ctx, q, userID).Scan(
		&u.UserID, &u.TotalBytes, &u.BundleCount, &u.SnapCount, &u.UpdatedAt,
	); err != nil {
		return nil, fmt.Errorf("recalculate usage: %w", err)
	}
	r.invalidateUsageCache(ctx, userID)
	return u, nil
}

// RecalculateAllUsage reconciles storage_usage for every active user in a single
// bulk statement, recomputing counters from the authoritative bundle and
// snapshot rows. It is the periodic, whole-fleet counterpart to RecalculateUsage
// and returns the number of user rows reconciled.
func (r *QuotaRepo) RecalculateAllUsage(ctx context.Context) (int64, error) {
	const q = `
		INSERT INTO storage_usage (user_id, total_bytes, bundle_count, snap_count, updated_at)
		SELECT
			u.id,
			COALESCE(b.bytes, 0) + COALESCE(s.bytes, 0),
			COALESCE(b.cnt, 0),
			COALESCE(s.cnt, 0),
			now()
		FROM users u
		LEFT JOIN (
			SELECT user_id, SUM(size_bytes) AS bytes, COUNT(*) AS cnt
			FROM bundles WHERE deleted_at IS NULL GROUP BY user_id
		) b ON b.user_id = u.id
		LEFT JOIN (
			SELECT user_id, SUM(size_bytes) AS bytes, COUNT(*) AS cnt
			FROM snapshots WHERE deleted_at IS NULL GROUP BY user_id
		) s ON s.user_id = u.id
		WHERE u.deleted_at IS NULL
		ON CONFLICT (user_id) DO UPDATE SET
			total_bytes  = EXCLUDED.total_bytes,
			bundle_count = EXCLUDED.bundle_count,
			snap_count   = EXCLUDED.snap_count,
			updated_at   = now()`

	tag, err := r.pool.Exec(ctx, q)
	if err != nil {
		return 0, fmt.Errorf("recalculate all usage: %w", err)
	}
	return tag.RowsAffected(), nil
}

// ── Refresh Token Repo ──────────────────────────────────────

// RefreshTokenRepo manages refresh token storage.
type RefreshTokenRepo struct {
	pool *pgxpool.Pool
}

// SaveRefreshToken stores a new refresh token hash.
func (r *RefreshTokenRepo) SaveRefreshToken(ctx context.Context, userID uuid.UUID, tokenHash []byte, deviceInfo string, expiresAt time.Time) error {
	const q = `
		INSERT INTO refresh_tokens (user_id, token_hash, device_info, expires_at)
		VALUES ($1, $2, $3, $4)`
	_, err := r.pool.Exec(ctx, q, userID, tokenHash, deviceInfo, expiresAt)
	return err
}

// RevokeRefreshToken marks a token as revoked.
func (r *RefreshTokenRepo) RevokeRefreshToken(ctx context.Context, tokenHash []byte) error {
	now := time.Now()
	const q = `UPDATE refresh_tokens SET revoked_at = $1 WHERE token_hash = $2 AND revoked_at IS NULL`
	_, err := r.pool.Exec(ctx, q, now, tokenHash)
	return err
}

// RevokeAllUserTokens revokes all refresh tokens for a user (e.g., on logout-all).
func (r *RefreshTokenRepo) RevokeAllUserTokens(ctx context.Context, userID uuid.UUID) error {
	now := time.Now()
	const q = `UPDATE refresh_tokens SET revoked_at = $1 WHERE user_id = $2 AND revoked_at IS NULL`
	_, err := r.pool.Exec(ctx, q, now, userID)
	return err
}

// IsTokenValid checks whether a refresh token hash is still valid.
func (r *RefreshTokenRepo) IsTokenValid(ctx context.Context, tokenHash []byte) (bool, error) {
	const q = `
		SELECT EXISTS(
			SELECT 1 FROM refresh_tokens
			WHERE token_hash = $1 AND revoked_at IS NULL AND expires_at > now()
		)`
	var valid bool
	err := r.pool.QueryRow(ctx, q, tokenHash).Scan(&valid)
	return valid, err
}

// GetUserIDByTokenHash returns the owning user ID for a valid refresh token.
func (r *RefreshTokenRepo) GetUserIDByTokenHash(ctx context.Context, tokenHash []byte) (*uuid.UUID, error) {
	const q = `
		SELECT user_id
		FROM refresh_tokens
		WHERE token_hash = $1 AND revoked_at IS NULL AND expires_at > now()
		LIMIT 1`

	var userID uuid.UUID
	if err := r.pool.QueryRow(ctx, q, tokenHash).Scan(&userID); err != nil {
		if err == pgx.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("get refresh token user: %w", err)
	}
	return &userID, nil
}

// ── Email Verification Repo ────────────────────────────────

// EmailVerificationRepo manages email verification tokens.
type EmailVerificationRepo struct {
	pool *pgxpool.Pool
}

// Save stores a hashed email verification token.
func (r *EmailVerificationRepo) Save(ctx context.Context, userID uuid.UUID, tokenHash []byte, expiresAt time.Time) error {
	const q = `
		INSERT INTO email_verifications (user_id, token_hash, expires_at)
		VALUES ($1, $2, $3)`
	_, err := r.pool.Exec(ctx, q, userID, tokenHash, expiresAt)
	return err
}

// GetUserIDByToken returns the user ID for a valid verification token.
func (r *EmailVerificationRepo) GetUserIDByToken(ctx context.Context, tokenHash []byte) (*uuid.UUID, error) {
	const q = `
		SELECT user_id
		FROM email_verifications
		WHERE token_hash = $1 AND expires_at > now()
		ORDER BY created_at DESC
		LIMIT 1`

	var userID uuid.UUID
	if err := r.pool.QueryRow(ctx, q, tokenHash).Scan(&userID); err != nil {
		if err == pgx.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("get email verification token: %w", err)
	}
	return &userID, nil
}

// DeleteByUser removes all verification tokens for a user.
func (r *EmailVerificationRepo) DeleteByUser(ctx context.Context, userID uuid.UUID) error {
	const q = `DELETE FROM email_verifications WHERE user_id = $1`
	_, err := r.pool.Exec(ctx, q, userID)
	return err
}

// ── Password Reset Repo ────────────────────────────────────

// PasswordResetRepo manages password reset tokens.
type PasswordResetRepo struct {
	pool *pgxpool.Pool
}

// Save stores a hashed password reset token.
func (r *PasswordResetRepo) Save(ctx context.Context, userID uuid.UUID, tokenHash []byte, expiresAt time.Time) error {
	const q = `
		INSERT INTO password_resets (user_id, token_hash, expires_at)
		VALUES ($1, $2, $3)`
	_, err := r.pool.Exec(ctx, q, userID, tokenHash, expiresAt)
	return err
}

// GetUserIDByToken returns the user ID for a valid reset token.
func (r *PasswordResetRepo) GetUserIDByToken(ctx context.Context, tokenHash []byte) (*uuid.UUID, error) {
	const q = `
		SELECT user_id
		FROM password_resets
		WHERE token_hash = $1 AND used_at IS NULL AND expires_at > now()
		ORDER BY created_at DESC
		LIMIT 1`

	var userID uuid.UUID
	if err := r.pool.QueryRow(ctx, q, tokenHash).Scan(&userID); err != nil {
		if err == pgx.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("get password reset token: %w", err)
	}
	return &userID, nil
}

// MarkUsed marks a password reset token as used.
func (r *PasswordResetRepo) MarkUsed(ctx context.Context, tokenHash []byte) error {
	const q = `UPDATE password_resets SET used_at = now() WHERE token_hash = $1 AND used_at IS NULL`
	_, err := r.pool.Exec(ctx, q, tokenHash)
	return err
}

// DeleteByUser removes all password reset tokens for a user.
func (r *PasswordResetRepo) DeleteByUser(ctx context.Context, userID uuid.UUID) error {
	const q = `DELETE FROM password_resets WHERE user_id = $1`
	_, err := r.pool.Exec(ctx, q, userID)
	return err
}

// ── Device Revocation Repo ──────────────────────────────────

// DeviceRevocationRepo manages the device revocation event log.
type DeviceRevocationRepo struct {
	pool *pgxpool.Pool
}

// RecordRevocation logs a device revocation event.
func (r *DeviceRevocationRepo) RecordRevocation(ctx context.Context, userID, deviceUUID, revokedBy uuid.UUID) error {
	const q = `
		INSERT INTO device_revocations (user_id, device_uuid, revoked_by)
		VALUES ($1, $2, $3)`
	_, err := r.pool.Exec(ctx, q, userID, deviceUUID, revokedBy)
	return err
}

// ListByUser returns all revocation events for a user.
func (r *DeviceRevocationRepo) ListByUser(ctx context.Context, userID uuid.UUID) ([]model.DeviceRevocation, error) {
	const q = `
		SELECT id, user_id, device_uuid, revoked_at, revoked_by
		FROM device_revocations WHERE user_id = $1
		ORDER BY revoked_at DESC LIMIT 100`

	rows, err := r.pool.Query(ctx, q, userID)
	if err != nil {
		return nil, fmt.Errorf("list revocations: %w", err)
	}
	defer rows.Close()

	var revs []model.DeviceRevocation
	for rows.Next() {
		var r model.DeviceRevocation
		if err := rows.Scan(&r.ID, &r.UserID, &r.DeviceUUID, &r.RevokedAt, &r.RevokedBy); err != nil {
			return nil, fmt.Errorf("scan revocation: %w", err)
		}
		revs = append(revs, r)
	}
	return revs, rows.Err()
}
