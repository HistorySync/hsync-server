// Package model defines the core data structures for the HistorySync server.
//
// These types correspond to database rows and API request/response bodies.
// JSON tags follow a compact naming convention to reduce wire overhead.
package model

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

// ── User ─────────────────────────────────────────────────────

// UserTier represents the subscription tier of a user account.
type UserTier string

const (
	TierFree       UserTier = "free"
	TierPro        UserTier = "pro"
	TierTeam       UserTier = "team"
	TierEnterprise UserTier = "enterprise"
)

// UserStatus represents the account status.
type UserStatus string

const (
	StatusActive    UserStatus = "active"
	StatusSuspended UserStatus = "suspended"
	StatusDeleted   UserStatus = "deleted"
)

// User is a registered account.
type User struct {
	ID            uuid.UUID  `json:"id"                db:"id"`
	Email         string     `json:"email"             db:"email"`
	PasswordHash  string     `json:"-"                 db:"password_hash"`
	DisplayName   string     `json:"display_name"      db:"display_name"`
	Tier          UserTier   `json:"tier"              db:"tier"`
	Status        UserStatus `json:"status"            db:"status"`
	EmailVerified bool       `json:"email_verified"    db:"email_verified"`
	CreatedAt     time.Time  `json:"created_at"        db:"created_at"`
	UpdatedAt     time.Time  `json:"updated_at"        db:"updated_at"`
	DeletedAt     *time.Time `json:"-"                 db:"deleted_at"`
}

// NotificationPreferences stores per-user opt-ins for notification categories
// and delivery channels. Webhook URLs may contain provider-side secrets and
// should not be written to logs.
type NotificationPreferences struct {
	UserID          uuid.UUID `json:"user_id"          db:"user_id"`
	SecurityEmail   bool      `json:"security_email"   db:"security_email"`
	SecurityWebhook bool      `json:"security_webhook" db:"security_webhook"`
	BillingEmail    bool      `json:"billing_email"    db:"billing_email"`
	BillingWebhook  bool      `json:"billing_webhook"  db:"billing_webhook"`
	WebhookURL      string    `json:"webhook_url"      db:"webhook_url"`
	WebhookSecret   string    `json:"-"                db:"webhook_secret"`
	CreatedAt       time.Time `json:"created_at"       db:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"       db:"updated_at"`
}

// DefaultNotificationPreferences returns the defaults for users without an
// explicit row yet.
func DefaultNotificationPreferences(userID uuid.UUID) NotificationPreferences {
	return NotificationPreferences{
		UserID:        userID,
		SecurityEmail: true,
		BillingEmail:  true,
	}
}

type NotificationChannel string

const (
	NotificationChannelEmail   NotificationChannel = "email"
	NotificationChannelWebhook NotificationChannel = "webhook"
)

type NotificationOutboxStatus string

const (
	NotificationOutboxPending    NotificationOutboxStatus = "pending"
	NotificationOutboxProcessing NotificationOutboxStatus = "processing"
	NotificationOutboxSent       NotificationOutboxStatus = "sent"
	NotificationOutboxFailed     NotificationOutboxStatus = "failed"
)

// NotificationOutbox is the durable delivery queue for best-effort user
// notifications. PayloadJSON contains only delivery payload fields; webhook
// endpoints and secrets are read from current user preferences at send time.
type NotificationOutbox struct {
	ID           uuid.UUID                `json:"id"             db:"id"`
	UserID       uuid.UUID                `json:"user_id"        db:"user_id"`
	Channel      NotificationChannel      `json:"channel"        db:"channel"`
	Category     string                   `json:"category"       db:"category"`
	Type         string                   `json:"type"           db:"type"`
	PayloadJSON  json.RawMessage          `json:"-"              db:"payload_json"`
	Status       NotificationOutboxStatus `json:"status"         db:"status"`
	AttemptCount int                      `json:"attempt_count"  db:"attempt_count"`
	NextRetryAt  time.Time                `json:"next_retry_at"  db:"next_retry_at"`
	LastError    string                   `json:"last_error,omitempty" db:"last_error"`
	CreatedAt    time.Time                `json:"created_at"     db:"created_at"`
	UpdatedAt    time.Time                `json:"updated_at"     db:"updated_at"`
	SentAt       *time.Time               `json:"sent_at,omitempty" db:"sent_at"`
}

type NotificationFailureView struct {
	ID           uuid.UUID           `json:"id"`
	UserID       uuid.UUID           `json:"user_id"`
	Channel      NotificationChannel `json:"channel"`
	Category     string              `json:"category"`
	Type         string              `json:"type"`
	AttemptCount int                 `json:"attempt_count"`
	ErrorSummary string              `json:"error_summary"`
	CreatedAt    time.Time           `json:"created_at"`
	UpdatedAt    time.Time           `json:"updated_at"`
}

// ── Device ───────────────────────────────────────────────────

// Device represents a registered client device.
type Device struct {
	ID         uuid.UUID  `json:"id"            db:"id"`
	UserID     uuid.UUID  `json:"user_id"       db:"user_id"`
	DeviceUUID uuid.UUID  `json:"device_uuid"   db:"device_uuid"`
	DeviceName string     `json:"device_name"   db:"device_name"`
	Platform   string     `json:"platform"      db:"platform"`
	AppVersion string     `json:"app_version"   db:"app_version"`
	TokenHash  []byte     `json:"-"             db:"token_hash"`
	LastSyncAt *time.Time `json:"last_sync_at"  db:"last_sync_at"`
	RevokedAt  *time.Time `json:"revoked_at"    db:"revoked_at"`
	CreatedAt  time.Time  `json:"created_at"    db:"created_at"`
}

// ── Bundle ───────────────────────────────────────────────────

// BundleMeta is the service-side metadata for an uploaded .hsb bundle.
// The server never parses the bundle payload; this is the only data it indexes.
type BundleMeta struct {
	BundleID           string     `json:"bundle_id"            db:"bundle_id"`
	UserID             uuid.UUID  `json:"user_id"              db:"user_id"`
	UploaderDeviceUUID uuid.UUID  `json:"uploader_device_uuid" db:"uploader_device_uuid"`
	LamportLo          int64      `json:"lamport_lo"           db:"lamport_lo"`
	LamportHi          int64      `json:"lamport_hi"           db:"lamport_hi"`
	EventCount         int32      `json:"event_count"          db:"event_count"`
	SizeBytes          int64      `json:"size_bytes"           db:"size_bytes"`
	CipherID           int16      `json:"cipher_id"            db:"cipher_id"`
	KeyGeneration      int16      `json:"key_generation"       db:"key_generation"`
	UploadedAt         time.Time  `json:"uploaded_at"          db:"uploaded_at"`
	DeletedAt          *time.Time `json:"deleted_at,omitempty" db:"deleted_at"`
}

// ── Snapshot ─────────────────────────────────────────────────

// SnapshotMeta is server-side metadata for a full-database snapshot.
type SnapshotMeta struct {
	SnapshotID    string     `json:"snapshot_id"     db:"snapshot_id"`
	UserID        uuid.UUID  `json:"user_id"         db:"user_id"`
	BaseHLC       int64      `json:"base_hlc"        db:"base_hlc"`
	SizeBytes     int64      `json:"size_bytes"      db:"size_bytes"`
	CipherID      int16      `json:"cipher_id"       db:"cipher_id"`
	KeyGeneration int16      `json:"key_generation"  db:"key_generation"`
	CreatedAt     time.Time  `json:"created_at"      db:"created_at"`
	DeletedAt     *time.Time `json:"deleted_at,omitempty" db:"deleted_at"`
}

// ── Quota ────────────────────────────────────────────────────

// QuotaUsage tracks current resource consumption for a user.
type QuotaUsage struct {
	UserID      uuid.UUID `json:"user_id"      db:"user_id"`
	TotalBytes  int64     `json:"total_bytes"  db:"total_bytes"`
	BundleCount int32     `json:"bundle_count" db:"bundle_count"`
	SnapCount   int32     `json:"snap_count"   db:"snap_count"`
	UpdatedAt   time.Time `json:"updated_at"   db:"updated_at"`
}

// QuotaLimits defines resource caps for a user (based on tier).
type QuotaLimits struct {
	UserID              uuid.UUID  `json:"user_id"               db:"user_id"`
	StorageLimitBytes   int64      `json:"storage_limit_bytes"   db:"storage_limit_bytes"`
	MaxDevices          int32      `json:"max_devices"           db:"max_devices"`
	MaxBundleSize       int64      `json:"max_bundle_size"       db:"max_bundle_size"`
	MaxSnapshots        int32      `json:"max_snapshots"         db:"max_snapshots"`
	MaxRPM              int32      `json:"max_rpm"               db:"max_rpm"`
	BundleRetentionDays int32      `json:"bundle_retention_days" db:"bundle_retention_days"`
	OverrideReason      *string    `json:"override_reason,omitempty" db:"override_reason"`
	ExpiresAt           *time.Time `json:"expires_at,omitempty"  db:"expires_at"`
}

// ── Device Revocation ────────────────────────────────────────

// DeviceRevocation records a device revocation event for audit trail.
type DeviceRevocation struct {
	ID         uuid.UUID `json:"id"          db:"id"`
	UserID     uuid.UUID `json:"user_id"     db:"user_id"`
	DeviceUUID uuid.UUID `json:"device_uuid" db:"device_uuid"`
	RevokedAt  time.Time `json:"revoked_at"  db:"revoked_at"`
	RevokedBy  uuid.UUID `json:"revoked_by"  db:"revoked_by"`
}

// TwoFactor stores a user's TOTP configuration. The encrypted secret is never
// returned over the API.
type TwoFactor struct {
	UserID          uuid.UUID  `json:"user_id"         db:"user_id"`
	SecretEncrypted []byte     `json:"-"               db:"secret_encrypted"`
	Enabled         bool       `json:"enabled"         db:"enabled"`
	FailedAttempts  int        `json:"-"               db:"failed_attempts"`
	LockedUntil     *time.Time `json:"locked_until,omitempty" db:"locked_until"`
	LastUsedAt      *time.Time `json:"last_used_at,omitempty"  db:"last_used_at"`
	EnabledAt       *time.Time `json:"enabled_at,omitempty"    db:"enabled_at"`
	CreatedAt       time.Time  `json:"created_at"      db:"created_at"`
	UpdatedAt       time.Time  `json:"updated_at"      db:"updated_at"`
}

// TwoFactorBackupCode stores a hashed one-time backup code.
type TwoFactorBackupCode struct {
	ID        uuid.UUID  `json:"id"         db:"id"`
	UserID    uuid.UUID  `json:"user_id"    db:"user_id"`
	CodeHash  string     `json:"-"          db:"code_hash"`
	UsedAt    *time.Time `json:"used_at,omitempty" db:"used_at"`
	CreatedAt time.Time  `json:"created_at" db:"created_at"`
}

// TierLimits returns the default quota limits for a given tier.
func TierLimits(tier UserTier) QuotaLimits {
	switch tier {
	case TierFree:
		return QuotaLimits{
			StorageLimitBytes:   1 * 1024 * 1024 * 1024, // 1 GB
			MaxDevices:          1,
			MaxBundleSize:       50 * 1024 * 1024, // 50 MB
			MaxSnapshots:        1,
			MaxRPM:              100,
			BundleRetentionDays: 30,
		}
	case TierPro:
		return QuotaLimits{
			StorageLimitBytes:   10 * 1024 * 1024 * 1024, // 10 GB
			MaxDevices:          5,
			MaxBundleSize:       50 * 1024 * 1024,
			MaxSnapshots:        3,
			MaxRPM:              500,
			BundleRetentionDays: 90,
		}
	case TierTeam:
		return QuotaLimits{
			StorageLimitBytes:   50 * 1024 * 1024 * 1024, // 50 GB
			MaxDevices:          20,
			MaxBundleSize:       100 * 1024 * 1024,
			MaxSnapshots:        5,
			MaxRPM:              2000,
			BundleRetentionDays: 180,
		}
	case TierEnterprise:
		return QuotaLimits{
			StorageLimitBytes:   100 * 1024 * 1024 * 1024, // 100 GB default
			MaxDevices:          100,
			MaxBundleSize:       200 * 1024 * 1024,
			MaxSnapshots:        10,
			MaxRPM:              10000,
			BundleRetentionDays: 365,
		}
	default:
		return TierLimits(TierFree)
	}
}
