package service

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/historysync/hsync-server/pkg/model"
)

const privacyExportAuditLimit = int32(100)

type accountUserStore interface {
	GetByID(ctx context.Context, id uuid.UUID) (*model.User, error)
}

type accountDeviceStore interface {
	ListByUser(ctx context.Context, userID uuid.UUID) ([]model.Device, error)
}

type accountBundleStore interface {
	ListAllByUser(ctx context.Context, userID uuid.UUID) ([]model.BundleMeta, error)
}

type accountSnapshotStore interface {
	ListAllByUser(ctx context.Context, userID uuid.UUID) ([]model.SnapshotMeta, error)
}

type accountQuotaStore interface {
	GetUsage(ctx context.Context, userID uuid.UUID) (*model.QuotaUsage, error)
}

type accountNotificationStore interface {
	GetPreferences(ctx context.Context, userID uuid.UUID) (*model.NotificationPreferences, error)
}

type accountAuditStore interface {
	ListVisibleByUser(ctx context.Context, userID uuid.UUID, limit int32) ([]model.AuditLog, error)
}

type AccountDeps struct {
	Users                accountUserStore
	Devices              accountDeviceStore
	Bundles              accountBundleStore
	Snapshots            accountSnapshotStore
	Quota                accountQuotaStore
	Notifications        accountNotificationStore
	Audit                accountAuditStore
	RetentionGracePeriod time.Duration
}

type AccountService struct {
	users                accountUserStore
	devices              accountDeviceStore
	bundles              accountBundleStore
	snapshots            accountSnapshotStore
	quota                accountQuotaStore
	notifications        accountNotificationStore
	audit                accountAuditStore
	retentionGracePeriod time.Duration
}

type PrivacyExport struct {
	GeneratedAt             time.Time                    `json:"generated_at"`
	IncludesBlobContents    bool                         `json:"includes_blob_contents"`
	ParsesBundleOrSnapshots bool                         `json:"parses_bundle_or_snapshots"`
	Account                 PrivacyExportAccount         `json:"account"`
	Devices                 []PrivacyExportDevice        `json:"devices"`
	Quota                   PrivacyExportQuota           `json:"quota"`
	Bundles                 []PrivacyExportBundle        `json:"bundles"`
	Snapshots               []PrivacyExportSnapshot      `json:"snapshots"`
	NotificationPreferences PrivacyExportNotifications   `json:"notification_preferences"`
	Audit                   PrivacyExportAuditSummary    `json:"audit"`
	Retention               PrivacyExportRetentionPolicy `json:"retention"`
}

type PrivacyExportAccount struct {
	ID            uuid.UUID        `json:"id"`
	Email         string           `json:"email"`
	DisplayName   string           `json:"display_name"`
	Tier          model.UserTier   `json:"tier"`
	Status        model.UserStatus `json:"status"`
	EmailVerified bool             `json:"email_verified"`
	CreatedAt     time.Time        `json:"created_at"`
	UpdatedAt     time.Time        `json:"updated_at"`
}

type PrivacyExportDevice struct {
	DeviceUUID uuid.UUID  `json:"device_uuid"`
	DeviceName string     `json:"device_name"`
	Platform   string     `json:"platform"`
	AppVersion string     `json:"app_version"`
	LastSyncAt *time.Time `json:"last_sync_at,omitempty"`
	RevokedAt  *time.Time `json:"revoked_at,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
}

type PrivacyExportBundle struct {
	BundleID           string    `json:"bundle_id"`
	UploaderDeviceUUID uuid.UUID `json:"uploader_device_uuid"`
	LamportLo          int64     `json:"lamport_lo"`
	LamportHi          int64     `json:"lamport_hi"`
	EventCount         int32     `json:"event_count"`
	SizeBytes          int64     `json:"size_bytes"`
	CipherID           int16     `json:"cipher_id"`
	KeyGeneration      int16     `json:"key_generation"`
	UploadedAt         time.Time `json:"uploaded_at"`
}

type PrivacyExportSnapshot struct {
	SnapshotID    string    `json:"snapshot_id"`
	BaseHLC       int64     `json:"base_hlc"`
	SizeBytes     int64     `json:"size_bytes"`
	CipherID      int16     `json:"cipher_id"`
	KeyGeneration int16     `json:"key_generation"`
	CreatedAt     time.Time `json:"created_at"`
}

type PrivacyExportQuota struct {
	Usage  model.QuotaUsage  `json:"usage"`
	Limits model.QuotaLimits `json:"limits"`
}

type PrivacyExportNotifications struct {
	SecurityEmail   bool   `json:"security_email"`
	SecurityWebhook bool   `json:"security_webhook"`
	BillingEmail    bool   `json:"billing_email"`
	BillingWebhook  bool   `json:"billing_webhook"`
	WebhookURL      string `json:"webhook_url,omitempty"`
	WebhookURLSet   bool   `json:"webhook_url_set"`
}

type PrivacyExportAuditSummary struct {
	Limit     int32                     `json:"limit"`
	Truncated bool                      `json:"truncated"`
	Entries   []PrivacyExportAuditEntry `json:"entries"`
}

type PrivacyExportAuditEntry struct {
	EventType  model.AuditEventType `json:"event_type"`
	TargetType string               `json:"target_type"`
	TargetID   string               `json:"target_id"`
	CreatedAt  time.Time            `json:"created_at"`
	Metadata   map[string]any       `json:"metadata,omitempty"`
}

type PrivacyExportRetentionPolicy struct {
	GracePeriodSeconds int64 `json:"grace_period_seconds"`
}

func NewAccountService(deps AccountDeps) *AccountService {
	return &AccountService{
		users:                deps.Users,
		devices:              deps.Devices,
		bundles:              deps.Bundles,
		snapshots:            deps.Snapshots,
		quota:                deps.Quota,
		notifications:        deps.Notifications,
		audit:                deps.Audit,
		retentionGracePeriod: deps.RetentionGracePeriod,
	}
}

func (s *AccountService) ExportPrivacyMetadata(ctx context.Context, userID uuid.UUID) (*PrivacyExport, error) {
	if s == nil || s.users == nil {
		return nil, fmt.Errorf("account service is not configured")
	}

	user, err := s.users.GetByID(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("get user: %w", err)
	}
	if user == nil {
		return nil, ErrUserNotFound
	}

	devices, err := listAccountDevices(ctx, s.devices, userID)
	if err != nil {
		return nil, err
	}
	bundles, err := listAccountBundles(ctx, s.bundles, userID)
	if err != nil {
		return nil, err
	}
	snapshots, err := listAccountSnapshots(ctx, s.snapshots, userID)
	if err != nil {
		return nil, err
	}
	usage, err := getAccountQuotaUsage(ctx, s.quota, userID)
	if err != nil {
		return nil, err
	}
	prefs, err := getAccountNotificationPreferences(ctx, s.notifications, userID)
	if err != nil {
		return nil, err
	}
	auditSummary, err := listAccountAuditSummary(ctx, s.audit, userID)
	if err != nil {
		return nil, err
	}

	limits := model.TierLimits(user.Tier)
	limits.UserID = user.ID
	usage.UserID = user.ID

	return &PrivacyExport{
		GeneratedAt:             time.Now().UTC(),
		IncludesBlobContents:    false,
		ParsesBundleOrSnapshots: false,
		Account: PrivacyExportAccount{
			ID:            user.ID,
			Email:         user.Email,
			DisplayName:   user.DisplayName,
			Tier:          user.Tier,
			Status:        user.Status,
			EmailVerified: user.EmailVerified,
			CreatedAt:     user.CreatedAt,
			UpdatedAt:     user.UpdatedAt,
		},
		Devices: devices,
		Quota: PrivacyExportQuota{
			Usage:  *usage,
			Limits: limits,
		},
		Bundles:   bundles,
		Snapshots: snapshots,
		NotificationPreferences: PrivacyExportNotifications{
			SecurityEmail:   prefs.SecurityEmail,
			SecurityWebhook: prefs.SecurityWebhook,
			BillingEmail:    prefs.BillingEmail,
			BillingWebhook:  prefs.BillingWebhook,
			WebhookURL:      maskExportWebhookURL(prefs.WebhookURL),
			WebhookURLSet:   strings.TrimSpace(prefs.WebhookURL) != "",
		},
		Audit: auditSummary,
		Retention: PrivacyExportRetentionPolicy{
			GracePeriodSeconds: int64(s.retentionGracePeriod / time.Second),
		},
	}, nil
}

func listAccountDevices(ctx context.Context, store accountDeviceStore, userID uuid.UUID) ([]PrivacyExportDevice, error) {
	if store == nil {
		return []PrivacyExportDevice{}, nil
	}
	devices, err := store.ListByUser(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("list devices: %w", err)
	}
	exported := make([]PrivacyExportDevice, 0, len(devices))
	for _, device := range devices {
		exported = append(exported, PrivacyExportDevice{
			DeviceUUID: device.DeviceUUID,
			DeviceName: device.DeviceName,
			Platform:   device.Platform,
			AppVersion: device.AppVersion,
			LastSyncAt: device.LastSyncAt,
			RevokedAt:  device.RevokedAt,
			CreatedAt:  device.CreatedAt,
		})
	}
	return exported, nil
}

func listAccountBundles(ctx context.Context, store accountBundleStore, userID uuid.UUID) ([]PrivacyExportBundle, error) {
	if store == nil {
		return []PrivacyExportBundle{}, nil
	}
	bundles, err := store.ListAllByUser(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("list bundles: %w", err)
	}
	exported := make([]PrivacyExportBundle, 0, len(bundles))
	for _, bundle := range bundles {
		exported = append(exported, PrivacyExportBundle{
			BundleID:           bundle.BundleID,
			UploaderDeviceUUID: bundle.UploaderDeviceUUID,
			LamportLo:          bundle.LamportLo,
			LamportHi:          bundle.LamportHi,
			EventCount:         bundle.EventCount,
			SizeBytes:          bundle.SizeBytes,
			CipherID:           bundle.CipherID,
			KeyGeneration:      bundle.KeyGeneration,
			UploadedAt:         bundle.UploadedAt,
		})
	}
	return exported, nil
}

func listAccountSnapshots(ctx context.Context, store accountSnapshotStore, userID uuid.UUID) ([]PrivacyExportSnapshot, error) {
	if store == nil {
		return []PrivacyExportSnapshot{}, nil
	}
	snapshots, err := store.ListAllByUser(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("list snapshots: %w", err)
	}
	exported := make([]PrivacyExportSnapshot, 0, len(snapshots))
	for _, snapshot := range snapshots {
		exported = append(exported, PrivacyExportSnapshot{
			SnapshotID:    snapshot.SnapshotID,
			BaseHLC:       snapshot.BaseHLC,
			SizeBytes:     snapshot.SizeBytes,
			CipherID:      snapshot.CipherID,
			KeyGeneration: snapshot.KeyGeneration,
			CreatedAt:     snapshot.CreatedAt,
		})
	}
	return exported, nil
}

func getAccountQuotaUsage(ctx context.Context, store accountQuotaStore, userID uuid.UUID) (*model.QuotaUsage, error) {
	if store == nil {
		return &model.QuotaUsage{UserID: userID}, nil
	}
	usage, err := store.GetUsage(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("get quota usage: %w", err)
	}
	if usage == nil {
		return &model.QuotaUsage{UserID: userID}, nil
	}
	return usage, nil
}

func getAccountNotificationPreferences(ctx context.Context, store accountNotificationStore, userID uuid.UUID) (*model.NotificationPreferences, error) {
	if store == nil {
		defaults := model.DefaultNotificationPreferences(userID)
		return &defaults, nil
	}
	prefs, err := store.GetPreferences(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("get notification preferences: %w", err)
	}
	if prefs == nil {
		defaults := model.DefaultNotificationPreferences(userID)
		return &defaults, nil
	}
	return prefs, nil
}

func listAccountAuditSummary(ctx context.Context, store accountAuditStore, userID uuid.UUID) (PrivacyExportAuditSummary, error) {
	summary := PrivacyExportAuditSummary{
		Limit:   privacyExportAuditLimit,
		Entries: []PrivacyExportAuditEntry{},
	}
	if store == nil {
		return summary, nil
	}
	logs, err := store.ListVisibleByUser(ctx, userID, privacyExportAuditLimit+1)
	if err != nil {
		return summary, fmt.Errorf("list visible audit logs: %w", err)
	}
	if len(logs) > int(privacyExportAuditLimit) {
		summary.Truncated = true
		logs = logs[:privacyExportAuditLimit]
	}
	summary.Entries = make([]PrivacyExportAuditEntry, 0, len(logs))
	for _, entry := range logs {
		summary.Entries = append(summary.Entries, PrivacyExportAuditEntry{
			EventType:  entry.EventType,
			TargetType: entry.TargetType,
			TargetID:   entry.TargetID,
			CreatedAt:  entry.CreatedAt,
			Metadata:   entry.Metadata,
		})
	}
	return summary, nil
}

func maskExportWebhookURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return "<redacted>"
	}
	return parsed.Scheme + "://" + parsed.Host + "/..."
}
