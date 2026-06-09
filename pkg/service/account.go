package service

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/historysync/hsync-server/pkg/model"
	"github.com/historysync/hsync-server/pkg/provider"
)

const privacyExportAuditLimit = int32(100)

type accountUserStore interface {
	GetByID(ctx context.Context, id uuid.UUID) (*model.User, error)
	SoftDelete(ctx context.Context, id uuid.UUID) error
}

type accountDeviceStore interface {
	ListByUser(ctx context.Context, userID uuid.UUID) ([]model.Device, error)
	RevokeAllByUser(ctx context.Context, userID uuid.UUID) error
}

type accountBundleStore interface {
	ListAllByUser(ctx context.Context, userID uuid.UUID) ([]model.BundleMeta, error)
	SoftDeleteAllByUser(ctx context.Context, userID uuid.UUID, deletedAt time.Time) (int64, int64, error)
}

type accountSnapshotStore interface {
	ListAllByUser(ctx context.Context, userID uuid.UUID) ([]model.SnapshotMeta, error)
	SoftDeleteAllByUser(ctx context.Context, userID uuid.UUID, deletedAt time.Time) (int64, int64, error)
}

type accountQuotaStore interface {
	GetUsage(ctx context.Context, userID uuid.UUID) (*model.QuotaUsage, error)
	RemoveBundleUsage(ctx context.Context, userID uuid.UUID, sizeBytes int64) error
	RemoveSnapshotUsage(ctx context.Context, userID uuid.UUID, sizeBytes int64) error
}

type accountNotificationStore interface {
	GetPreferences(ctx context.Context, userID uuid.UUID) (*model.NotificationPreferences, error)
}

type accountAuditStore interface {
	ListVisibleByUser(ctx context.Context, userID uuid.UUID, limit int32) ([]model.AuditLog, error)
}

type accountRefreshTokenStore interface {
	RevokeAllUserTokens(ctx context.Context, userID uuid.UUID) error
}

type accountTwoFactorStore interface {
	DeleteByUser(ctx context.Context, userID uuid.UUID) error
}

type accountPasskeyStore interface {
	DeleteCredentialsByUser(ctx context.Context, userID uuid.UUID) error
	ExpireChallengesByUser(ctx context.Context, userID uuid.UUID, now time.Time) error
}

type accountErasureJobStore interface {
	Create(ctx context.Context, job *model.AccountErasureJob) error
	UpdateSummary(ctx context.Context, id uuid.UUID, summary json.RawMessage) error
}

type accountAuditRecorder interface {
	Record(ctx context.Context, input AuditEventInput) error
}

type AccountDeps struct {
	Users                accountUserStore
	Devices              accountDeviceStore
	Bundles              accountBundleStore
	Snapshots            accountSnapshotStore
	Quota                accountQuotaStore
	Notifications        accountNotificationStore
	Audit                accountAuditStore
	AuditRecorder        accountAuditRecorder
	RefreshTokens        accountRefreshTokenStore
	TwoFactor            accountTwoFactorStore
	Passkeys             accountPasskeyStore
	ErasureJobs          accountErasureJobStore
	DeletionPolicy       provider.AccountDeletionPolicy
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
	auditRecorder        accountAuditRecorder
	refreshTokens        accountRefreshTokenStore
	twoFactor            accountTwoFactorStore
	passkeys             accountPasskeyStore
	erasureJobs          accountErasureJobStore
	deletionPolicy       provider.AccountDeletionPolicy
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

type AccountDeletionInput struct {
	UserID    uuid.UUID
	RequestID string
	IP        string
	UserAgent string
}

type AccountDeletionResult struct {
	Status                   string                      `json:"status"`
	DeletedAt                *time.Time                  `json:"deleted_at,omitempty"`
	RetentionGraceSeconds    int64                       `json:"retention_grace_seconds"`
	Policy                   AccountDeletionPolicyResult `json:"policy"`
	RevokedRefreshTokens     bool                        `json:"revoked_refresh_tokens"`
	RevokedDeviceTokens      bool                        `json:"revoked_device_tokens"`
	RemovedTwoFactor         bool                        `json:"removed_two_factor"`
	RemovedPasskeys          bool                        `json:"removed_passkeys"`
	ExpiredSessionChallenges bool                        `json:"expired_session_challenges"`
	SoftDeletedBundles       int64                       `json:"soft_deleted_bundles"`
	SoftDeletedBundleBytes   int64                       `json:"soft_deleted_bundle_bytes"`
	SoftDeletedSnapshots     int64                       `json:"soft_deleted_snapshots"`
	SoftDeletedSnapshotBytes int64                       `json:"soft_deleted_snapshot_bytes"`
	ErasureJobID             uuid.UUID                   `json:"erasure_job_id,omitempty"`
	ErasureEligibleAt        *time.Time                  `json:"erasure_eligible_at,omitempty"`
}

type AccountDeletionPolicyResult struct {
	Allowed        bool                                   `json:"allowed"`
	RequiresReview bool                                   `json:"requires_review"`
	Reasons        []provider.AccountDeletionPolicyReason `json:"reasons,omitempty"`
}

func NewAccountService(deps AccountDeps) *AccountService {
	policy := deps.DeletionPolicy
	if policy == nil {
		policy = &provider.AllowAccountDeletionPolicy{}
	}
	return &AccountService{
		users:                deps.Users,
		devices:              deps.Devices,
		bundles:              deps.Bundles,
		snapshots:            deps.Snapshots,
		quota:                deps.Quota,
		notifications:        deps.Notifications,
		audit:                deps.Audit,
		auditRecorder:        deps.AuditRecorder,
		refreshTokens:        deps.RefreshTokens,
		twoFactor:            deps.TwoFactor,
		passkeys:             deps.Passkeys,
		erasureJobs:          deps.ErasureJobs,
		deletionPolicy:       policy,
		retentionGracePeriod: deps.RetentionGracePeriod,
	}
}

func (s *AccountService) DeleteAccount(ctx context.Context, input AccountDeletionInput) (*AccountDeletionResult, error) {
	userID := input.UserID
	s.recordDeletionAudit(ctx, input, model.AuditEventAccountDeletionRequest, map[string]any{
		"request_id": input.RequestID,
		"status":     "requested",
	})
	if s == nil || s.users == nil {
		s.recordDeletionResult(ctx, input, nil, "failed", "not_configured")
		return nil, fmt.Errorf("account service is not configured")
	}

	user, err := s.users.GetByID(ctx, userID)
	if err != nil {
		s.recordDeletionResult(ctx, input, nil, "failed", "get_user")
		return nil, fmt.Errorf("get user: %w", err)
	}
	if user == nil {
		s.recordDeletionResult(ctx, input, nil, "failed", "user_not_found")
		return nil, ErrUserNotFound
	}

	decision, err := s.deletionPolicy.EvaluateAccountDeletion(ctx, provider.AccountDeletionRequest{
		UserID: user.ID.String(),
		Email:  user.Email,
		Tier:   string(user.Tier),
		Status: string(user.Status),
	})
	if err != nil {
		s.recordDeletionResult(ctx, input, nil, "failed", "policy_error")
		return nil, fmt.Errorf("evaluate account deletion policy: %w", err)
	}
	policyResult := accountDeletionPolicyResult(decision)
	result := &AccountDeletionResult{
		Status:                "pending",
		RetentionGraceSeconds: int64(s.retentionGracePeriod / time.Second),
		Policy:                policyResult,
	}
	if policyResult.RequiresReview {
		result.Status = "review_required"
		s.recordDeletionResult(ctx, input, result, result.Status, "policy_review_required")
		return result, ErrAccountDeletionRequiresReview
	}
	if !policyResult.Allowed {
		result.Status = "blocked"
		s.recordDeletionResult(ctx, input, result, result.Status, "policy_blocked")
		return result, ErrAccountDeletionBlocked
	}

	now := time.Now().UTC()
	eligibleAt := now.Add(s.retentionGracePeriod)
	job := &model.AccountErasureJob{
		UserID:      userID,
		RequestedAt: now,
		EligibleAt:  eligibleAt,
		Status:      model.AccountErasureJobStatusPending,
	}
	if s.erasureJobs != nil {
		if err := s.erasureJobs.Create(ctx, job); err != nil {
			s.recordDeletionResult(ctx, input, result, "failed", "create_erasure_job")
			return nil, fmt.Errorf("create erasure job: %w", err)
		}
		result.ErasureJobID = job.ID
		result.ErasureEligibleAt = &job.EligibleAt
		s.recordDeletionAudit(ctx, input, model.AuditEventAccountErasureJobCreated, map[string]any{
			"request_id":  input.RequestID,
			"job_id":      job.ID.String(),
			"eligible_at": job.EligibleAt,
			"status":      string(job.Status),
		})
	}
	if s.refreshTokens != nil {
		if err := s.refreshTokens.RevokeAllUserTokens(ctx, userID); err != nil {
			s.recordDeletionResult(ctx, input, result, "failed", "revoke_refresh_tokens")
			return nil, fmt.Errorf("revoke refresh tokens: %w", err)
		}
		result.RevokedRefreshTokens = true
	}
	if s.devices != nil {
		if err := s.devices.RevokeAllByUser(ctx, userID); err != nil {
			s.recordDeletionResult(ctx, input, result, "failed", "revoke_device_tokens")
			return nil, fmt.Errorf("revoke device tokens: %w", err)
		}
		result.RevokedDeviceTokens = true
	}
	if s.twoFactor != nil {
		if err := s.twoFactor.DeleteByUser(ctx, userID); err != nil {
			s.recordDeletionResult(ctx, input, result, "failed", "delete_two_factor")
			return nil, fmt.Errorf("delete two factor state: %w", err)
		}
		result.RemovedTwoFactor = true
	}
	if s.passkeys != nil {
		if err := s.passkeys.ExpireChallengesByUser(ctx, userID, now); err != nil {
			s.recordDeletionResult(ctx, input, result, "failed", "expire_passkey_challenges")
			return nil, fmt.Errorf("expire passkey challenges: %w", err)
		}
		result.ExpiredSessionChallenges = true
		if err := s.passkeys.DeleteCredentialsByUser(ctx, userID); err != nil {
			s.recordDeletionResult(ctx, input, result, "failed", "delete_passkeys")
			return nil, fmt.Errorf("delete passkeys: %w", err)
		}
		result.RemovedPasskeys = true
	}
	if s.bundles != nil {
		count, bytes, err := s.bundles.SoftDeleteAllByUser(ctx, userID, now)
		if err != nil {
			s.recordDeletionResult(ctx, input, result, "failed", "soft_delete_bundles")
			return nil, fmt.Errorf("soft delete bundles: %w", err)
		}
		result.SoftDeletedBundles = count
		result.SoftDeletedBundleBytes = bytes
		if bytes > 0 && s.quota != nil {
			if err := s.quota.RemoveBundleUsage(ctx, userID, bytes); err != nil {
				s.recordDeletionResult(ctx, input, result, "failed", "remove_bundle_usage")
				return nil, fmt.Errorf("remove bundle usage: %w", err)
			}
		}
	}
	if s.snapshots != nil {
		count, bytes, err := s.snapshots.SoftDeleteAllByUser(ctx, userID, now)
		if err != nil {
			s.recordDeletionResult(ctx, input, result, "failed", "soft_delete_snapshots")
			return nil, fmt.Errorf("soft delete snapshots: %w", err)
		}
		result.SoftDeletedSnapshots = count
		result.SoftDeletedSnapshotBytes = bytes
		if bytes > 0 && s.quota != nil {
			if err := s.quota.RemoveSnapshotUsage(ctx, userID, bytes); err != nil {
				s.recordDeletionResult(ctx, input, result, "failed", "remove_snapshot_usage")
				return nil, fmt.Errorf("remove snapshot usage: %w", err)
			}
		}
	}
	if s.erasureJobs != nil && job.ID != uuid.Nil {
		summary, err := json.Marshal(map[string]any{
			"status":                       "pending_retention",
			"requested_at":                 now,
			"eligible_at":                  eligibleAt,
			"soft_deleted_bundles":         result.SoftDeletedBundles,
			"soft_deleted_bundle_bytes":    result.SoftDeletedBundleBytes,
			"soft_deleted_snapshots":       result.SoftDeletedSnapshots,
			"soft_deleted_snapshot_bytes":  result.SoftDeletedSnapshotBytes,
			"zero_knowledge_boundary_note": "sync blobs remain opaque encrypted objects and are not parsed or decrypted",
		})
		if err != nil {
			s.recordDeletionResult(ctx, input, result, "failed", "encode_erasure_job_summary")
			return nil, fmt.Errorf("encode erasure job summary: %w", err)
		}
		if err := s.erasureJobs.UpdateSummary(ctx, job.ID, summary); err != nil {
			s.recordDeletionResult(ctx, input, result, "failed", "update_erasure_job_summary")
			return nil, fmt.Errorf("update erasure job summary: %w", err)
		}
	}
	if err := s.users.SoftDelete(ctx, userID); err != nil {
		s.recordDeletionResult(ctx, input, result, "failed", "soft_delete_user")
		return nil, fmt.Errorf("soft delete user: %w", err)
	}
	result.Status = "deleted"
	result.DeletedAt = &now
	s.recordDeletionResult(ctx, input, result, result.Status, "")
	return result, nil
}

func accountDeletionPolicyResult(decision *provider.AccountDeletionDecision) AccountDeletionPolicyResult {
	if decision == nil {
		return AccountDeletionPolicyResult{Allowed: true}
	}
	return AccountDeletionPolicyResult{
		Allowed:        decision.Allowed && !decision.RequiresReview,
		RequiresReview: decision.RequiresReview,
		Reasons:        decision.Reasons,
	}
}

func (s *AccountService) recordDeletionResult(ctx context.Context, input AccountDeletionInput, result *AccountDeletionResult, status, failure string) {
	metadata := map[string]any{
		"request_id": input.RequestID,
		"status":     status,
	}
	if failure != "" {
		metadata["failure"] = failure
	}
	if result != nil {
		metadata["policy_allowed"] = result.Policy.Allowed
		metadata["policy_requires_review"] = result.Policy.RequiresReview
		metadata["policy_reasons"] = result.Policy.Reasons
		metadata["revoked_refresh_tokens"] = result.RevokedRefreshTokens
		metadata["revoked_device_tokens"] = result.RevokedDeviceTokens
		metadata["removed_two_factor"] = result.RemovedTwoFactor
		metadata["removed_passkeys"] = result.RemovedPasskeys
		metadata["expired_session_challenges"] = result.ExpiredSessionChallenges
		metadata["soft_deleted_bundles"] = result.SoftDeletedBundles
		metadata["soft_deleted_bundle_bytes"] = result.SoftDeletedBundleBytes
		metadata["soft_deleted_snapshots"] = result.SoftDeletedSnapshots
		metadata["soft_deleted_snapshot_bytes"] = result.SoftDeletedSnapshotBytes
		if result.ErasureJobID != uuid.Nil {
			metadata["erasure_job_id"] = result.ErasureJobID.String()
		}
		if result.ErasureEligibleAt != nil {
			metadata["erasure_eligible_at"] = *result.ErasureEligibleAt
		}
	}
	s.recordDeletionAudit(ctx, input, model.AuditEventAccountDeletionResult, metadata)
}

func (s *AccountService) recordDeletionAudit(ctx context.Context, input AccountDeletionInput, eventType model.AuditEventType, metadata map[string]any) {
	if s == nil || s.auditRecorder == nil {
		return
	}
	if metadata == nil {
		metadata = map[string]any{}
	}
	userID := input.UserID
	if userID == uuid.Nil {
		return
	}
	metadata["step_up_required"] = true
	_ = s.auditRecorder.Record(ctx, AuditEventInput{
		ActorUserID: &userID,
		EventType:   eventType,
		TargetType:  "user",
		TargetID:    userID.String(),
		IP:          input.IP,
		UserAgent:   input.UserAgent,
		Metadata:    metadata,
	})
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
