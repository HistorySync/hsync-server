// Package repository provides data access layer implementations for
// PostgreSQL and Redis. It translates between Go models and database rows,
// and implements the query patterns needed by the service layer.
package repository

import (
	"context"
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
	Users            *UserRepo
	Devices          *DeviceRepo
	Bundles          *BundleRepo
	Snapshots        *SnapshotRepo
	Quota            *QuotaRepo
	RefreshTokens    *RefreshTokenRepo
	DeviceRevocations *DeviceRevocationRepo
}

// New creates all repository instances with the given database connections.
func New(pgPool *pgxpool.Pool, redisClient *redis.Client) *Repos {
	return &Repos{
		Users:             &UserRepo{pool: pgPool},
		Devices:           &DeviceRepo{pool: pgPool},
		Bundles:           &BundleRepo{pool: pgPool},
		Snapshots:         &SnapshotRepo{pool: pgPool},
		Quota:             &QuotaRepo{pool: pgPool, redis: redisClient},
		RefreshTokens:     &RefreshTokenRepo{pool: pgPool},
		DeviceRevocations: &DeviceRevocationRepo{pool: pgPool},
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
		       email_verified, stripe_customer_id, created_at, updated_at, deleted_at
		FROM users WHERE id = $1 AND deleted_at IS NULL`

	user := &model.User{}
	err := r.pool.QueryRow(ctx, q, id).Scan(
		&user.ID, &user.Email, &user.PasswordHash, &user.DisplayName,
		&user.Tier, &user.Status, &user.EmailVerified, &user.StripeCustomerID,
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
		       email_verified, stripe_customer_id, created_at, updated_at, deleted_at
		FROM users WHERE email = $1 AND deleted_at IS NULL`

	user := &model.User{}
	err := r.pool.QueryRow(ctx, q, email).Scan(
		&user.ID, &user.Email, &user.PasswordHash, &user.DisplayName,
		&user.Tier, &user.Status, &user.EmailVerified, &user.StripeCustomerID,
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

// UpdateStripeCustomerID associates a Stripe customer with a user.
func (r *UserRepo) UpdateStripeCustomerID(ctx context.Context, id uuid.UUID, customerID string) error {
	const q = `UPDATE users SET stripe_customer_id = $1 WHERE id = $2 AND deleted_at IS NULL`
	_, err := r.pool.Exec(ctx, q, customerID, id)
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

// ListDeletedBefore returns users soft-deleted before the given time (for cleanup).
func (r *UserRepo) ListDeletedBefore(ctx context.Context, before time.Time) ([]model.User, error) {
	const q = `
		SELECT id, email, password_hash, display_name, tier, status,
		       email_verified, stripe_customer_id, created_at, updated_at, deleted_at
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
			&u.Tier, &u.Status, &u.EmailVerified, &u.StripeCustomerID,
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

// SoftDelete marks a bundle as deleted.
func (r *BundleRepo) SoftDelete(ctx context.Context, userID uuid.UUID, bundleID string) error {
	now := time.Now()
	const q = `UPDATE bundles SET deleted_at = $1 WHERE user_id = $2 AND bundle_id = $3 AND deleted_at IS NULL`
	tag, err := r.pool.Exec(ctx, q, now, userID, bundleID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("bundle not found or already deleted")
	}
	return nil
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

// SoftDelete marks a snapshot as deleted.
func (r *SnapshotRepo) SoftDelete(ctx context.Context, userID uuid.UUID, snapshotID string) error {
	now := time.Now()
	const q = `UPDATE snapshots SET deleted_at = $1 WHERE user_id = $2 AND snapshot_id = $3 AND deleted_at IS NULL`
	_, err := r.pool.Exec(ctx, q, now, userID, snapshotID)
	return err
}

// CountByUser returns the number of non-deleted snapshots for a user.
func (r *SnapshotRepo) CountByUser(ctx context.Context, userID uuid.UUID) (int32, error) {
	const q = `SELECT COUNT(*) FROM snapshots WHERE user_id = $1 AND deleted_at IS NULL`
	var count int32
	err := r.pool.QueryRow(ctx, q, userID).Scan(&count)
	return count, err
}

// PruneOldest deletes the oldest snapshots beyond the given limit.
func (r *SnapshotRepo) PruneOldest(ctx context.Context, userID uuid.UUID, keep int32) error {
	const q = `
		UPDATE snapshots SET deleted_at = now()
		WHERE user_id = $1 AND deleted_at IS NULL
		      AND snapshot_id NOT IN (
		          SELECT snapshot_id FROM snapshots
		          WHERE user_id = $1 AND deleted_at IS NULL
		          ORDER BY created_at DESC LIMIT $2
		      )`
	_, err := r.pool.Exec(ctx, q, userID, keep)
	return err
}

// ── QuotaRepo ────────────────────────────────────────────────

// QuotaRepo handles storage usage tracking and quota enforcement.
type QuotaRepo struct {
	pool  *pgxpool.Pool
	redis *redis.Client
}

// GetUsage retrieves the current storage usage for a user.
func (r *QuotaRepo) GetUsage(ctx context.Context, userID uuid.UUID) (*model.QuotaUsage, error) {
	const q = `
		SELECT user_id, total_bytes, bundle_count, snap_count, updated_at
		FROM storage_usage WHERE user_id = $1`

	u := &model.QuotaUsage{}
	err := r.pool.QueryRow(ctx, q, userID).Scan(
		&u.UserID, &u.TotalBytes, &u.BundleCount, &u.SnapCount, &u.UpdatedAt,
	)
	if err == pgx.ErrNoRows {
		return &model.QuotaUsage{UserID: userID}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get usage: %w", err)
	}
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
