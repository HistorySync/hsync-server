// Package service implements the business logic layer for the HistorySync
// Cloud Server. Services orchestrate repository calls, enforce quota rules,
// and integrate with external systems (Stripe, email, etc.).
package service

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/mail"
	"strings"
	"time"

	"github.com/google/uuid"
	"golang.org/x/crypto/argon2"

	"github.com/historysync/hsync-server/pkg/auth"
	"github.com/historysync/hsync-server/pkg/buildinfo"
	"github.com/historysync/hsync-server/pkg/config"
	"github.com/historysync/hsync-server/pkg/model"
	"github.com/historysync/hsync-server/pkg/observability"
	"github.com/historysync/hsync-server/pkg/provider"
	"github.com/historysync/hsync-server/pkg/repository"
	"github.com/historysync/hsync-server/pkg/storage"
)

// Argon2Params configures the Argon2id key derivation for password hashing.
var Argon2Params = struct {
	Time    uint32
	Memory  uint32
	Threads uint8
	KeyLen  uint32
	SaltLen int
}{
	Time:    3,
	Memory:  64 * 1024, // 64 MB
	Threads: 4,
	KeyLen:  32,
	SaltLen: 16,
}

// Common errors returned by services.
var (
	ErrEmailTaken                    = errors.New("email already registered")
	ErrInvalidEmail                  = errors.New("invalid email")
	ErrInvalidCredentials            = errors.New("invalid email or password")
	ErrWeakPassword                  = errors.New("password must be at least 10 characters")
	ErrQuotaExceeded                 = errors.New("quota exceeded")
	ErrDeviceLimit                   = errors.New("device limit reached")
	ErrBundleExists                  = errors.New("bundle already exists")
	ErrDeviceRevoked                 = errors.New("device has been revoked")
	ErrStripeDisabled                = errors.New("billing is not enabled")
	ErrResetTokenRequired            = errors.New("reset token is required")
	ErrNewPasswordRequired           = errors.New("new password is required")
	ErrPasswordResetInvalid          = errors.New("invalid or expired password reset token")
	ErrVerifyTokenRequired           = errors.New("verification token is required")
	ErrEmailVerifyInvalid            = errors.New("invalid or expired email verification token")
	ErrUserInactive                  = errors.New("user not found or inactive")
	ErrBundleNotFound                = errors.New("bundle not found")
	ErrSnapshotNotFound              = errors.New("snapshot not found")
	ErrUserNotFound                  = errors.New("user not found")
	ErrDeviceNotFound                = errors.New("device not found")
	ErrDeviceAlreadyRevoked          = errors.New("device already revoked")
	ErrDeviceNotRegistered           = errors.New("device not registered")
	ErrBillingNotSupported           = errors.New("billing not supported")
	ErrRefreshTokenRequired          = errors.New("refresh token is required")
	ErrReservationDenied             = errors.New("storage reservation denied")
	ErrTwoFactorRequired             = errors.New("two-factor authentication is required")
	ErrTwoFactorNotEnabled           = errors.New("two-factor authentication is not enabled")
	ErrTwoFactorEnabled              = errors.New("two-factor authentication is already enabled")
	ErrTwoFactorInvalidCode          = errors.New("invalid two-factor authentication code")
	ErrTwoFactorLocked               = errors.New("two-factor authentication is temporarily locked")
	ErrTwoFactorChallenge            = errors.New("invalid or expired two-factor challenge")
	ErrSignupsDisabled               = errors.New("new account registration is currently disabled")
	ErrPasskeyDisabled               = errors.New("passkey authentication is disabled")
	ErrPasskeyChallenge              = errors.New("invalid or expired passkey challenge")
	ErrPasskeyNotFound               = errors.New("passkey credential not found")
	ErrPasskeyVerification           = errors.New("passkey verification failed")
	ErrAccountDeletionBlocked        = errors.New("account deletion is blocked by policy")
	ErrAccountDeletionRequiresReview = errors.New("account deletion requires operator review")
)

// Deps holds all dependencies needed by the service layer.
type Deps struct {
	Repos             *repository.Repos
	TokenManager      *auth.TokenManager
	BlobStore         storage.BlobStorage
	StripeKey         string
	StripeWebhook     string
	StripeDisabled    bool
	Reservation       UsageReservationHook
	Notifier          provider.Notifier
	Webhook           provider.WebhookProvider
	Notification      NotificationConfig
	SecuritySecret    string
	Config            *config.Config
	DatabasePing      PingFunc
	RedisPing         PingFunc
	DatabasePoolStats func() observability.DatabasePoolStatsSnapshot
}

// ReservationRequest carries upload context for a storage reservation so the
// reservation backend can record which artifact and request triggered it.
type ReservationRequest struct {
	Reason     string
	TeamID     string
	BundleID   string
	SnapshotID string
	DeviceUUID string
	RequestID  string
}

// UsageReservationHook lets Enterprise reserve capacity before storage writes.
type UsageReservationHook interface {
	ReserveStorage(ctx context.Context, userID uuid.UUID, bytes int64, req ReservationRequest) (string, error)
	SettleStorage(ctx context.Context, reservationID string, bytes int64) error
	ReleaseStorage(ctx context.Context, reservationID string)
}

// reservationGuard tracks one Enterprise storage reservation across an upload's
// lifecycle. When no reservation hook is configured the guard stays inactive and
// every operation is a no-op, so callers fall back to the legacy quota path.
type reservationGuard struct {
	hook     UsageReservationHook
	id       string
	category string
	settled  bool
}

// reserve acquires a storage reservation when a hook is configured. With no hook
// it returns an inactive guard so the caller uses the legacy quota path.
func reserve(ctx context.Context, hook UsageReservationHook, userID uuid.UUID, bytes int64, req ReservationRequest) (*reservationGuard, error) {
	if hook == nil {
		return &reservationGuard{}, nil
	}
	id, err := hook.ReserveStorage(ctx, userID, bytes, req)
	if err != nil {
		result := "failure"
		if errors.Is(err, ErrReservationDenied) {
			result = "rejected"
		}
		observability.RecordQuotaReservation(reservationCategory(req), result)
		return nil, err
	}
	if id != "" {
		observability.RecordQuotaReservation(reservationCategory(req), "success")
	}
	return &reservationGuard{hook: hook, id: id, category: reservationCategory(req)}, nil
}

// active reports whether a reservation was acquired and still drives quota.
func (g *reservationGuard) active() bool {
	return g != nil && g.hook != nil && g.id != ""
}

// settle finalizes the reservation against the bytes actually written. After it
// succeeds, release becomes a no-op so a deferred release does not double-count.
func (g *reservationGuard) settle(ctx context.Context, bytes int64) error {
	if !g.active() {
		return nil
	}
	if err := g.hook.SettleStorage(ctx, g.id, bytes); err != nil {
		observability.RecordQuotaReservation(g.category, "rollback")
		return err
	}
	g.settled = true
	return nil
}

// release returns reserved capacity when an upload fails before settling. It is
// safe to defer: it does nothing once settled or when the guard is inactive.
func (g *reservationGuard) release(ctx context.Context) {
	if !g.active() || g.settled {
		return
	}
	g.hook.ReleaseStorage(ctx, g.id)
	observability.RecordQuotaReservation(g.category, "release")
}

func reservationCategory(req ReservationRequest) string {
	switch req.Reason {
	case "bundle_upload":
		return "bundle"
	case "snapshot_upload":
		return "snapshot"
	default:
		return "storage"
	}
}

// Services aggregates all business service instances.
type Services struct {
	Repos            *repository.Repos
	Account          *AccountService
	Auth             *AuthService
	Bundle           *BundleService
	Snapshot         *SnapshotService
	Quota            *QuotaService
	Billing          *BillingService
	Notification     *NotificationService
	Retention        *RetentionService
	History          *OperationalHistoryRetentionService
	TwoFactor        *TwoFactorService
	Passkey          *PasskeyService
	Audit            *AuditService
	SecurityStats    *SecurityStatsService
	SecurityTimeline *SecurityTimelineService
	Settings         *SettingsService
	Idempotency      *IdempotencyService
	Ops              *OpsService
	Support          *SupportContextService
}

// New creates all service instances with their dependencies.
func New(deps Deps) *Services {
	if deps.Notifier == nil {
		deps.Notifier = provider.Registry().Notifier
	}
	if deps.Webhook == nil {
		deps.Webhook = provider.Registry().Webhook
	}
	var notifUsers NotificationUserStore
	var notifPrefs NotificationPreferenceStore
	if deps.Repos != nil {
		notifUsers = deps.Repos.Users
		notifPrefs = deps.Repos.NotificationPrefs
	}
	var notifOutbox NotificationOutboxStore
	if deps.Repos != nil {
		notifOutbox = deps.Repos.NotificationOutbox
	}
	notifSvc := NewNotificationServiceWithStoresAndOutbox(notifUsers, notifPrefs, notifOutbox, deps.Notifier, deps.Webhook, deps.Notification)
	authSvc := &AuthService{
		repos:         deps.Repos,
		tokenManager:  deps.TokenManager,
		notifications: notifSvc,
	}
	quotaSvc := &QuotaService{
		repos:         deps.Repos,
		notifications: notifSvc,
	}
	bundleSvc := &BundleService{
		repos:       deps.Repos,
		blobStore:   deps.BlobStore,
		quota:       quotaSvc,
		reservation: deps.Reservation,
	}
	snapshotSvc := &SnapshotService{
		repos:       deps.Repos,
		blobStore:   deps.BlobStore,
		quota:       quotaSvc,
		reservation: deps.Reservation,
	}
	billingSvc := &BillingService{
		repos:      deps.Repos,
		stripeKey:  deps.StripeKey,
		webhookKey: deps.StripeWebhook,
		disabled:   deps.StripeDisabled,
		provider:   provider.Registry().Billing,
	}
	retentionSvc := &RetentionService{repos: deps.Repos, blobStore: deps.BlobStore}
	historySvc := NewOperationalHistoryRetentionService(deps.Repos)
	twoFactorSvc := NewTwoFactorService(deps.Repos, deps.TokenManager, deps.SecuritySecret)
	var auditStore auditEventStore
	var securityAuditStore securityAuditStore
	var securityUserStore securityUserStore
	var securityTimelineAuditStore securityTimelineAuditStore
	var securityTimelineUserStore securityTimelineUserStore
	if deps.Repos != nil {
		auditStore = deps.Repos.AuditLogs
		securityAuditStore = deps.Repos.AuditLogs
		securityUserStore = deps.Repos.Users
		securityTimelineAuditStore = deps.Repos.AuditLogs
		securityTimelineUserStore = deps.Repos.Users
	}
	auditSvc := NewAuditService(auditStore)
	retentionSvc.auditRecorder = auditSvc
	retentionSvc.erasureReporter = provider.Registry().Erasure
	securityStatsSvc := NewSecurityStatsService(securityAuditStore, securityUserStore)
	securityTimelineSvc := NewSecurityTimelineService(securityTimelineAuditStore, securityTimelineUserStore)
	var supportUsers supportUserStore
	var supportDevices supportDeviceStore
	var supportQuota supportQuotaStore
	var supportAudit supportAuditStore
	var supportErasureJobs supportErasureJobStore
	var supportTimeline supportTimelineStore
	if deps.Repos != nil {
		supportUsers = deps.Repos.Users
		supportDevices = deps.Repos.Devices
		supportQuota = deps.Repos.Quota
		supportAudit = deps.Repos.AuditLogs
		supportErasureJobs = deps.Repos.AccountErasureJobs
		supportTimeline = securityTimelineSvc
	}
	supportSvc := NewSupportContextService(SupportContextDeps{
		Users:       supportUsers,
		Devices:     supportDevices,
		Quota:       supportQuota,
		Audit:       supportAudit,
		ErasureJobs: supportErasureJobs,
		Timeline:    supportTimeline,
	})

	// Dynamic system settings: a database-backed, whitelisted, typed override
	// layer over code-declared defaults. A nil store (no repos) keeps reads
	// working off defaults, so this never blocks startup.
	var settingStoreImpl settingStore
	if deps.Repos != nil && deps.Repos.SystemSettings != nil {
		settingStoreImpl = deps.Repos.SystemSettings
	}
	settingsSvc := NewSettingsService(settingStoreImpl, defaultSettingDefinitions())
	authSvc.settings = settingsSvc
	passkeySvc := NewPasskeyService(deps.Repos, deps.TokenManager, settingsSvc)

	var idempotencySvc *IdempotencyService
	if deps.Repos != nil {
		idempotencySvc = NewIdempotencyService(deps.Repos.Idempotency)
	}
	opsSvc := NewOpsService(OpsDeps{
		Config:            deps.Config,
		BuildInfo:         buildinfo.Current(),
		Repos:             deps.Repos,
		BlobStore:         deps.BlobStore,
		DatabasePing:      deps.DatabasePing,
		RedisPing:         deps.RedisPing,
		DatabasePoolStats: deps.DatabasePoolStats,
		Alert: OpsAlertConfig{
			Email:         opsAlertEmail(deps.Config),
			WebhookURL:    opsAlertWebhookURL(deps.Config),
			WebhookSecret: opsAlertWebhookSecret(deps.Config),
			AppName:       opsAlertAppName(deps.Config),
		},
		Notifier: deps.Notifier,
		Webhook:  deps.Webhook,
	})
	var accountUsers accountUserStore
	var accountDevices accountDeviceStore
	var accountBundles accountBundleStore
	var accountSnapshots accountSnapshotStore
	var accountQuota accountQuotaStore
	var accountAudit accountAuditStore
	var accountRefreshTokens accountRefreshTokenStore
	var accountTwoFactor accountTwoFactorStore
	var accountPasskeys accountPasskeyStore
	var accountErasureJobs accountErasureJobStore
	if deps.Repos != nil {
		accountUsers = deps.Repos.Users
		accountDevices = deps.Repos.Devices
		accountBundles = deps.Repos.Bundles
		accountSnapshots = deps.Repos.Snapshots
		accountQuota = deps.Repos.Quota
		accountAudit = deps.Repos.AuditLogs
		accountRefreshTokens = deps.Repos.RefreshTokens
		accountTwoFactor = deps.Repos.TwoFactor
		accountPasskeys = deps.Repos.Passkeys
		accountErasureJobs = deps.Repos.AccountErasureJobs
	}
	accountSvc := NewAccountService(AccountDeps{
		Users:                accountUsers,
		Devices:              accountDevices,
		Bundles:              accountBundles,
		Snapshots:            accountSnapshots,
		Quota:                accountQuota,
		Notifications:        notifSvc,
		Audit:                accountAudit,
		AuditRecorder:        auditSvc,
		RefreshTokens:        accountRefreshTokens,
		TwoFactor:            accountTwoFactor,
		Passkeys:             accountPasskeys,
		ErasureJobs:          accountErasureJobs,
		DeletionPolicy:       provider.Registry().Deletion,
		RetentionGracePeriod: retentionGracePeriod(deps.Config),
	})

	return &Services{
		Repos:            deps.Repos,
		Account:          accountSvc,
		Auth:             authSvc,
		Bundle:           bundleSvc,
		Snapshot:         snapshotSvc,
		Quota:            quotaSvc,
		Billing:          billingSvc,
		Notification:     notifSvc,
		Retention:        retentionSvc,
		History:          historySvc,
		TwoFactor:        twoFactorSvc,
		Passkey:          passkeySvc,
		Audit:            auditSvc,
		SecurityStats:    securityStatsSvc,
		SecurityTimeline: securityTimelineSvc,
		Settings:         settingsSvc,
		Idempotency:      idempotencySvc,
		Ops:              opsSvc,
		Support:          supportSvc,
	}
}

// RetentionService
// RetentionService reports on and purges data that has been soft-deleted past
// its retention grace period. It covers bundles and snapshots; user cleanup can
// be added as a further task.
func opsAlertEmail(cfg *config.Config) string {
	if cfg == nil {
		return ""
	}
	return cfg.OpsAlertEmail
}

func opsAlertWebhookURL(cfg *config.Config) string {
	if cfg == nil {
		return ""
	}
	return cfg.OpsAlertWebhookURL
}

func opsAlertWebhookSecret(cfg *config.Config) string {
	if cfg == nil {
		return ""
	}
	return cfg.OpsAlertWebhookSecret
}

func opsAlertAppName(cfg *config.Config) string {
	if cfg == nil {
		return ""
	}
	return cfg.WebAppName
}

func retentionGracePeriod(cfg *config.Config) time.Duration {
	if cfg == nil {
		return 0
	}
	return cfg.RetentionGracePeriod
}

type RetentionService struct {
	repos           *repository.Repos
	blobStore       storage.BlobStorage
	auditRecorder   accountAuditRecorder
	erasureReporter provider.AccountErasureReporter
}

// RetentionReport summarizes the soft-deleted data eligible for purging, or, for
// the purge path, what was actually removed. Failed is the number of bundles that
// could not be purged this run (left for a later run); it is zero for a dry run.
type RetentionReport struct {
	Before         time.Time `json:"before"`
	ExpiredBundles int64     `json:"expired_bundles"`
	ExpiredBytes   int64     `json:"expired_bytes"`
	Failed         int64     `json:"failed"`
}

// ReportExpiredBundles reports how many bundles were soft-deleted longer ago than
// the grace period, and their total size, without deleting anything. It backs the
// dry-run phase of retention cleanup.
func (s *RetentionService) ReportExpiredBundles(ctx context.Context, grace time.Duration) (RetentionReport, error) {
	before := time.Now().Add(-grace)
	count, bytes, err := s.repos.Bundles.CountDeletedBefore(ctx, before)
	if err != nil {
		return RetentionReport{}, fmt.Errorf("count expired bundles: %w", err)
	}
	return RetentionReport{Before: before, ExpiredBundles: count, ExpiredBytes: bytes}, nil
}

// purgeableBundles is the subset of bundle persistence the retention purge needs:
// page through soft-deleted bundles and physically remove one. The concrete
// *repository.BundleRepo satisfies it; tests supply an in-memory fake.
type purgeableBundles interface {
	ListDeletedBefore(ctx context.Context, before time.Time) ([]model.BundleMeta, error)
	HardDelete(ctx context.Context, userID uuid.UUID, bundleID string) error
}

// blobDeleter removes a stored blob by key; storage.BlobStorage satisfies it.
type blobDeleter interface {
	Delete(ctx context.Context, key string) error
}

// PurgeExpiredBundles permanently deletes bundles that were soft-deleted longer
// ago than the grace period, returning what was removed. It is the destructive
// counterpart to ReportExpiredBundles and should run only when the caller has
// opted out of dry-run mode.
func (s *RetentionService) PurgeExpiredBundles(ctx context.Context, grace time.Duration) (RetentionReport, error) {
	before := time.Now().Add(-grace)
	return purgeExpiredBundles(ctx, s.repos.Bundles, s.blobStore, before)
}

// purgeExpiredBundles is the pure purge loop shared by PurgeExpiredBundles and its
// tests. For each soft-deleted bundle older than before it deletes the blob first
// and then the metadata row, so an interruption leaves a retryable soft-deleted
// row rather than an orphaned blob. It intentionally does NOT touch storage_usage:
// DeleteBundle already decremented it at soft-delete time, so the counters track
// only live data and a second decrement here would double-count.
//
// It pages via ListDeletedBefore (which the repository caps and orders by deletion
// time) and stops when a page yields no further progress, so a bundle whose blob
// or row delete keeps failing is counted once in Failed and left for a later run
// instead of looping forever.
func purgeExpiredBundles(ctx context.Context, bundles purgeableBundles, blobs blobDeleter, before time.Time) (RetentionReport, error) {
	report := RetentionReport{Before: before}
	// Bundles that failed this run, keyed by user/bundle, so a stuck row is retried
	// at most once per run and bounds the loop.
	failed := make(map[string]bool)

	for {
		if err := ctx.Err(); err != nil {
			return report, err
		}
		batch, err := bundles.ListDeletedBefore(ctx, before)
		if err != nil {
			return report, fmt.Errorf("list expired bundles: %w", err)
		}
		if len(batch) == 0 {
			return report, nil
		}

		progressed := 0
		for _, b := range batch {
			id := b.UserID.String() + "/" + b.BundleID
			if failed[id] {
				continue
			}
			// Blob before row: a crash after this point leaves a soft-deleted row
			// that the next run retries, never an orphaned blob.
			key := storage.BundleKey(b.UserID.String(), b.BundleID)
			if err := blobs.Delete(ctx, key); err != nil {
				failed[id] = true
				report.Failed++
				continue
			}
			if err := bundles.HardDelete(ctx, b.UserID, b.BundleID); err != nil {
				failed[id] = true
				report.Failed++
				continue
			}
			report.ExpiredBundles++
			report.ExpiredBytes += b.SizeBytes
			progressed++
		}

		if progressed == 0 {
			// The whole page was already-failed rows; stop rather than spin.
			return report, nil
		}
	}
}

// Snapshot Retention
// SnapshotReport summarizes soft-deleted snapshots eligible for purging or
// actually removed.
type SnapshotReport struct {
	Before           time.Time `json:"before"`
	ExpiredSnapshots int64     `json:"expired_snapshots"`
	ExpiredBytes     int64     `json:"expired_bytes"`
	Failed           int64     `json:"failed"`
}

// ReportExpiredSnapshots reports how many snapshots were soft-deleted longer ago
// than the grace period, and their total size, without deleting anything.
func (s *RetentionService) ReportExpiredSnapshots(ctx context.Context, grace time.Duration) (SnapshotReport, error) {
	before := time.Now().Add(-grace)
	count, bytes, err := s.repos.Snapshots.CountDeletedBefore(ctx, before)
	if err != nil {
		return SnapshotReport{}, fmt.Errorf("count expired snapshots: %w", err)
	}
	return SnapshotReport{Before: before, ExpiredSnapshots: count, ExpiredBytes: bytes}, nil
}

// purgeableSnapshots is the subset of snapshot persistence the retention purge
// needs. The concrete *repository.SnapshotRepo satisfies it.
type purgeableSnapshots interface {
	ListDeletedBefore(ctx context.Context, before time.Time) ([]model.SnapshotMeta, error)
	HardDelete(ctx context.Context, userID uuid.UUID, snapshotID string) error
}

// PurgeExpiredSnapshots permanently deletes snapshots that were soft-deleted
// longer ago than the grace period. Same safety properties as PurgeExpiredBundles:
// blob before row, no storage_usage touch (UploadSnapshot already calls
// RemoveSnapshotUsage on prune), no-progress break to bound retries.
func (s *RetentionService) PurgeExpiredSnapshots(ctx context.Context, grace time.Duration) (SnapshotReport, error) {
	before := time.Now().Add(-grace)
	return purgeExpiredSnapshots(ctx, s.repos.Snapshots, s.blobStore, before)
}

// purgeExpiredSnapshots is the pure purge loop; see purgeExpiredBundles for
// the shared rationale.
func purgeExpiredSnapshots(ctx context.Context, snapshots purgeableSnapshots, blobs blobDeleter, before time.Time) (SnapshotReport, error) {
	report := SnapshotReport{Before: before}
	failed := make(map[string]bool)

	for {
		if err := ctx.Err(); err != nil {
			return report, err
		}
		batch, err := snapshots.ListDeletedBefore(ctx, before)
		if err != nil {
			return report, fmt.Errorf("list expired snapshots: %w", err)
		}
		if len(batch) == 0 {
			return report, nil
		}

		progressed := 0
		for _, s := range batch {
			id := s.UserID.String() + "/" + s.SnapshotID
			if failed[id] {
				continue
			}
			key := storage.SnapshotKey(s.UserID.String(), s.SnapshotID)
			if err := blobs.Delete(ctx, key); err != nil {
				failed[id] = true
				report.Failed++
				continue
			}
			if err := snapshots.HardDelete(ctx, s.UserID, s.SnapshotID); err != nil {
				failed[id] = true
				report.Failed++
				continue
			}
			report.ExpiredSnapshots++
			report.ExpiredBytes += s.SizeBytes
			progressed++
		}

		if progressed == 0 {
			return report, nil
		}
	}
}

// AuthService
// AuthService handles user registration, login, and token management.
type AuthService struct {
	repos         *repository.Repos
	tokenManager  *auth.TokenManager
	notifications *NotificationService
	settings      *SettingsService
}

// RegisterInput contains the fields required for user registration.
type RegisterInput struct {
	Email       string `json:"email"`
	Password    string `json:"password"`
	DisplayName string `json:"display_name"`
}

// RegisterResult contains the tokens returned after successful registration.
type RegisterResult struct {
	User                   model.User `json:"user"`
	AccessToken            string     `json:"access_token"`
	RefreshToken           string     `json:"refresh_token"`
	ExpiresIn              int64      `json:"expires_in"`
	EmailVerificationToken string     `json:"email_verification_token,omitempty"`
	RequiresTwoFactor      bool       `json:"requires_2fa,omitempty"`
	Challenge              string     `json:"challenge,omitempty"`
}

// Register creates a new user account and returns tokens.
func (s *AuthService) Register(ctx context.Context, input RegisterInput) (*RegisterResult, error) {
	if s.settings != nil && !s.settings.GetBoolOrDefault(ctx, SettingKeySignupsEnabled) {
		return nil, ErrSignupsDisabled
	}

	email, err := normalizeEmail(input.Email)
	if err != nil {
		return nil, err
	}
	displayName := strings.TrimSpace(input.DisplayName)
	if err := validatePassword(input.Password); err != nil {
		return nil, err
	}

	// Check for duplicate email
	existing, err := s.repos.Users.GetByEmail(ctx, email)
	if err != nil {
		return nil, fmt.Errorf("check email: %w", err)
	}
	if existing != nil {
		return nil, ErrEmailTaken
	}

	// Hash password with Argon2id
	passwordHash, err := hashPassword(input.Password)
	if err != nil {
		return nil, fmt.Errorf("hash password: %w", err)
	}

	user := &model.User{
		Email:        email,
		PasswordHash: passwordHash,
		DisplayName:  displayName,
		Tier:         model.TierFree,
		Status:       model.StatusActive,
	}

	if err := s.repos.Users.Create(ctx, user); err != nil {
		return nil, fmt.Errorf("create user: %w", err)
	}

	// Initialize quota limits and usage tracking
	limits := model.TierLimits(user.Tier)
	limits.UserID = user.ID
	if err := s.repos.Quota.UpsertLimits(ctx, &limits); err != nil {
		return nil, fmt.Errorf("create quota limits: %w", err)
	}
	if err := s.repos.Quota.CreateUsage(ctx, user.ID); err != nil {
		return nil, fmt.Errorf("create usage tracking: %w", err)
	}

	// Issue tokens
	accessToken, err := s.tokenManager.IssueAccessToken(user.ID, string(user.Tier))
	if err != nil {
		return nil, fmt.Errorf("issue access token: %w", err)
	}

	refreshToken, tokenHash, err := s.issueRefreshToken(user.ID)
	if err != nil {
		return nil, fmt.Errorf("issue refresh token: %w", err)
	}

	// Store refresh token hash
	if err := s.repos.RefreshTokens.SaveRefreshToken(ctx, user.ID, tokenHash, "",
		time.Now().Add(30*24*time.Hour)); err != nil {
		return nil, fmt.Errorf("save refresh token: %w", err)
	}

	var verificationToken string
	if s.notifications != nil {
		verificationToken, err = s.createEmailVerification(ctx, user)
		if err != nil {
			return nil, err
		}
		s.notifications.SendWelcomeEmailAsync(user.Email, user.DisplayName)
		s.notifications.SendEmailVerificationAsync(user.ID, user.Email, user.DisplayName, verificationToken)
		if s.notifications.DeliveryEnabled() {
			verificationToken = ""
		}
	}

	return &RegisterResult{
		User:                   *user,
		AccessToken:            accessToken,
		RefreshToken:           refreshToken,
		ExpiresIn:              900, // 15 minutes
		EmailVerificationToken: verificationToken,
	}, nil
}

// LoginInput contains the fields required for user login.
type LoginInput struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

// Login authenticates a user and returns tokens.
func (s *AuthService) Login(ctx context.Context, input LoginInput) (*RegisterResult, error) {
	email, err := normalizeEmail(input.Email)
	if err != nil {
		return nil, ErrInvalidCredentials
	}
	if strings.TrimSpace(input.Password) == "" {
		return nil, ErrInvalidCredentials
	}

	user, err := s.repos.Users.GetByEmail(ctx, email)
	if err != nil {
		return nil, fmt.Errorf("get user: %w", err)
	}
	if user == nil {
		return nil, ErrInvalidCredentials
	}
	if user.Status != model.StatusActive {
		return nil, ErrInvalidCredentials
	}

	// Verify password
	if !verifyPassword(input.Password, user.PasswordHash) {
		return nil, ErrInvalidCredentials
	}

	twoFactor, err := s.repos.TwoFactor.Get(ctx, user.ID)
	if err != nil {
		return nil, fmt.Errorf("get two factor: %w", err)
	}
	if twoFactor != nil && twoFactor.Enabled {
		challenge, expiresIn, err := s.tokenManager.IssueLoginChallengeToken(user.ID)
		if err != nil {
			return nil, fmt.Errorf("issue two factor challenge: %w", err)
		}
		return &RegisterResult{
			User:              *user,
			RequiresTwoFactor: true,
			Challenge:         challenge,
			ExpiresIn:         expiresIn,
		}, nil
	}

	return s.issueTokens(ctx, user)
}

// RefreshPasswordInput contains the fields required to complete a password reset.
type ResetPasswordInput struct {
	Token       string `json:"token"`
	NewPassword string `json:"new_password"`
}

// StartEmailVerification creates a verification token for an existing active
// unverified user and sends the verification email when delivery is configured.
func (s *AuthService) StartEmailVerification(ctx context.Context, email string) (string, error) {
	normalizedEmail, err := normalizeEmail(email)
	if err != nil {
		return "", nil
	}

	user, err := s.repos.Users.GetByEmail(ctx, normalizedEmail)
	if err != nil {
		return "", fmt.Errorf("get user: %w", err)
	}
	if user == nil || user.Status != model.StatusActive || user.EmailVerified {
		return "", nil
	}

	token, err := s.createEmailVerification(ctx, user)
	if err != nil {
		return "", err
	}
	if s.notifications != nil {
		s.notifications.SendEmailVerificationAsync(user.ID, user.Email, user.DisplayName, token)
		if s.notifications.DeliveryEnabled() {
			return "", nil
		}
	}

	return token, nil
}

// VerifyEmail validates a verification token and marks the user's email as
// verified. Tokens are single-use because all verification tokens for the user
// are deleted after a successful verification.
func (s *AuthService) VerifyEmail(ctx context.Context, token string) error {
	if strings.TrimSpace(token) == "" {
		return ErrVerifyTokenRequired
	}

	tokenHash := hashToken(token)
	userID, err := s.repos.EmailVerifications.GetUserIDByToken(ctx, tokenHash)
	if err != nil {
		return fmt.Errorf("get email verification token user: %w", err)
	}
	if userID == nil {
		return ErrEmailVerifyInvalid
	}

	user, err := s.repos.Users.GetByID(ctx, *userID)
	if err != nil {
		return fmt.Errorf("get user: %w", err)
	}
	if user == nil || user.Status != model.StatusActive {
		return ErrUserInactive
	}

	if !user.EmailVerified {
		if err := s.repos.Users.VerifyEmail(ctx, user.ID); err != nil {
			return fmt.Errorf("verify email: %w", err)
		}
	}
	if err := s.repos.EmailVerifications.DeleteByUser(ctx, user.ID); err != nil {
		return fmt.Errorf("delete email verification tokens: %w", err)
	}

	return nil
}

// StartPasswordReset creates a password reset token for an existing active user.
func (s *AuthService) StartPasswordReset(ctx context.Context, email string) (string, error) {
	normalizedEmail, err := normalizeEmail(email)
	if err != nil {
		return "", nil
	}

	user, err := s.repos.Users.GetByEmail(ctx, normalizedEmail)
	if err != nil {
		return "", fmt.Errorf("get user: %w", err)
	}
	if user == nil || user.Status != model.StatusActive {
		return "", nil
	}

	if err := s.repos.PasswordResets.DeleteByUser(ctx, user.ID); err != nil {
		return "", fmt.Errorf("delete existing password reset tokens: %w", err)
	}

	resetToken, tokenHash, err := s.issueRefreshToken(user.ID)
	if err != nil {
		return "", fmt.Errorf("issue password reset token: %w", err)
	}

	if err := s.repos.PasswordResets.Save(ctx, user.ID, tokenHash, time.Now().Add(time.Hour)); err != nil {
		return "", fmt.Errorf("save password reset token: %w", err)
	}

	if s.notifications != nil {
		s.notifications.SendPasswordResetAsync(user.ID, user.Email, user.DisplayName, resetToken)
		if s.notifications.DeliveryEnabled() {
			return "", nil
		}
	}

	return resetToken, nil
}

// ResetPassword validates a password reset token and updates the user's password.
func (s *AuthService) ResetPassword(ctx context.Context, input ResetPasswordInput) error {
	if input.Token == "" {
		return ErrResetTokenRequired
	}
	if input.NewPassword == "" {
		return ErrNewPasswordRequired
	}
	if err := validatePassword(input.NewPassword); err != nil {
		return err
	}

	tokenHash := hashToken(input.Token)
	userID, err := s.repos.PasswordResets.GetUserIDByToken(ctx, tokenHash)
	if err != nil {
		return fmt.Errorf("get password reset token user: %w", err)
	}
	if userID == nil {
		return ErrPasswordResetInvalid
	}

	user, err := s.repos.Users.GetByID(ctx, *userID)
	if err != nil {
		return fmt.Errorf("get user: %w", err)
	}
	if user == nil || user.Status != model.StatusActive {
		return ErrUserInactive
	}

	passwordHash, err := hashPassword(input.NewPassword)
	if err != nil {
		return fmt.Errorf("hash password: %w", err)
	}

	if err := s.repos.Users.UpdatePassword(ctx, user.ID, passwordHash); err != nil {
		return fmt.Errorf("update password: %w", err)
	}
	if err := s.repos.PasswordResets.MarkUsed(ctx, tokenHash); err != nil {
		return fmt.Errorf("mark password reset token used: %w", err)
	}
	if err := s.repos.PasswordResets.DeleteByUser(ctx, user.ID); err != nil {
		return fmt.Errorf("delete password reset tokens: %w", err)
	}
	if err := s.repos.RefreshTokens.RevokeAllUserTokens(ctx, user.ID); err != nil {
		return fmt.Errorf("revoke user refresh tokens: %w", err)
	}

	return nil
}

func (s *AuthService) createEmailVerification(ctx context.Context, user *model.User) (string, error) {
	if user == nil {
		return "", ErrUserNotFound
	}
	if err := s.repos.EmailVerifications.DeleteByUser(ctx, user.ID); err != nil {
		return "", fmt.Errorf("delete existing email verification tokens: %w", err)
	}
	token, tokenHash, err := s.issueRefreshToken(user.ID)
	if err != nil {
		return "", fmt.Errorf("issue email verification token: %w", err)
	}
	if err := s.repos.EmailVerifications.Save(ctx, user.ID, tokenHash, time.Now().Add(emailVerificationTokenTTL)); err != nil {
		return "", fmt.Errorf("save email verification token: %w", err)
	}
	return token, nil
}

// RefreshAccessToken validates a refresh token and issues a new access token.
func (s *AuthService) RefreshAccessToken(ctx context.Context, refreshToken string) (string, error) {
	if strings.TrimSpace(refreshToken) == "" {
		return "", ErrRefreshTokenRequired
	}

	tokenHash := hashToken(refreshToken)
	valid, err := s.repos.RefreshTokens.IsTokenValid(ctx, tokenHash)
	if err != nil {
		return "", fmt.Errorf("check token: %w", err)
	}
	if !valid {
		return "", ErrPasswordResetInvalid
	}

	userID, err := s.repos.RefreshTokens.GetUserIDByTokenHash(ctx, tokenHash)
	if err != nil {
		return "", fmt.Errorf("get token user: %w", err)
	}
	if userID == nil {
		return "", ErrPasswordResetInvalid
	}

	user, err := s.repos.Users.GetByID(ctx, *userID)
	if err != nil {
		return "", fmt.Errorf("get user: %w", err)
	}
	if user == nil || user.Status != model.StatusActive {
		return "", ErrUserInactive
	}

	accessToken, err := s.tokenManager.IssueAccessToken(user.ID, string(user.Tier))
	if err != nil {
		return "", fmt.Errorf("issue access token: %w", err)
	}

	return accessToken, nil
}

// Logout revokes a refresh token.
func (s *AuthService) Logout(ctx context.Context, refreshToken string) error {
	if strings.TrimSpace(refreshToken) == "" {
		return ErrRefreshTokenRequired
	}

	tokenHash := hashToken(refreshToken)
	return s.repos.RefreshTokens.RevokeRefreshToken(ctx, tokenHash)
}

func (s *AuthService) issueRefreshToken(userID uuid.UUID) (tokenStr string, tokenHash []byte, err error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", nil, err
	}
	tokenStr = base64.RawURLEncoding.EncodeToString(raw)
	tokenHash = hashToken(tokenStr)
	return tokenStr, tokenHash, nil
}

func (s *AuthService) issueTokens(ctx context.Context, user *model.User) (*RegisterResult, error) {
	accessToken, err := s.tokenManager.IssueAccessToken(user.ID, string(user.Tier))
	if err != nil {
		return nil, fmt.Errorf("issue access token: %w", err)
	}
	refreshToken, tokenHash, err := s.issueRefreshToken(user.ID)
	if err != nil {
		return nil, fmt.Errorf("issue refresh token: %w", err)
	}
	if err := s.repos.RefreshTokens.SaveRefreshToken(ctx, user.ID, tokenHash, "", time.Now().Add(30*24*time.Hour)); err != nil {
		return nil, fmt.Errorf("save refresh token: %w", err)
	}
	return &RegisterResult{
		User:         *user,
		AccessToken:  accessToken,
		RefreshToken: refreshToken,
		ExpiresIn:    900,
	}, nil
}

func hashToken(token string) []byte {
	h := sha256.Sum256([]byte(token))
	return h[:]
}

func normalizeEmail(raw string) (string, error) {
	email := strings.ToLower(strings.TrimSpace(raw))
	if email == "" {
		return "", ErrInvalidEmail
	}
	addr, err := mail.ParseAddress(email)
	if err != nil || addr.Address != email {
		return "", ErrInvalidEmail
	}
	return email, nil
}

func validatePassword(password string) error {
	if len(password) < 10 {
		return ErrWeakPassword
	}
	return nil
}

// Password Helpers
func hashPassword(password string) (string, error) {
	salt := make([]byte, Argon2Params.SaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}

	hash := argon2.IDKey([]byte(password), salt,
		Argon2Params.Time,
		Argon2Params.Memory,
		Argon2Params.Threads,
		Argon2Params.KeyLen,
	)

	// Encode as: $argon2id$v=19$m=<memory>,t=<time>,p=<threads>$<base64salt>$<base64hash>
	return fmt.Sprintf("$argon2id$v=19$m=%d,t=%d,p=%d$%s$%s",
		Argon2Params.Memory, Argon2Params.Time, Argon2Params.Threads,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(hash),
	), nil
}

func verifyPassword(password, encoded string) bool {
	// Parse the encoded format: $argon2id$v=19$m=<memory>,t=<time>,p=<threads>$<b64salt>$<b64hash>
	parts := splitPHC(encoded)
	if len(parts) != 5 || parts[0] != "argon2id" || parts[1] != "v=19" {
		return false
	}

	var memory, iterations uint32
	var parallelism uint8
	if _, err := fmt.Sscanf(parts[2], "m=%d,t=%d,p=%d", &memory, &iterations, &parallelism); err != nil {
		return false
	}

	salt, err := base64.RawStdEncoding.DecodeString(parts[3])
	if err != nil {
		return false
	}

	expectedHash, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return false
	}

	got := argon2.IDKey([]byte(password), salt, iterations, memory, parallelism, uint32(len(expectedHash)))
	return subtle.ConstantTimeCompare(got, expectedHash) == 1
}

// splitPHC splits a PHC-format string by '$' and returns the parts.
func splitPHC(s string) []string {
	if s == "" || s[0] != '$' {
		return nil
	}
	// Remove leading $
	return splitStr(s[1:], '$')
}

func splitStr(s string, sep byte) []string {
	var parts []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == sep {
			parts = append(parts, s[start:i])
			start = i + 1
		}
	}
	parts = append(parts, s[start:])
	return parts
}

// BundleService
// BundleService handles bundle upload validation, deduplication, and listing.
type BundleService struct {
	repos       *repository.Repos
	blobStore   storage.BlobStorage
	quota       *QuotaService
	reservation UsageReservationHook
}

// UploadInput contains the metadata for a bundle upload.
type UploadInput struct {
	BundleID      string    `json:"bundle_id"`
	DeviceUUID    uuid.UUID `json:"device_uuid"`
	LamportLo     int64     `json:"lamport_lo"`
	LamportHi     int64     `json:"lamport_hi"`
	EventCount    int32     `json:"event_count"`
	SizeBytes     int64     `json:"size_bytes"`
	CipherID      int16     `json:"cipher_id"`
	KeyGeneration int16     `json:"key_generation"`
	Reader        io.Reader `json:"-"` // The file stream
	ContentType   string    `json:"-"`
	RequestID     string    `json:"-"`
	TeamID        string    `json:"-"`
}

// UploadBundle validates and persists a bundle.
func (s *BundleService) UploadBundle(ctx context.Context, userID uuid.UUID, input UploadInput) (*model.BundleMeta, error) {
	uploadResult := "failure"
	defer func() {
		observability.RecordUpload("bundle", uploadResult)
	}()

	guard, err := reserve(ctx, s.reservation, userID, input.SizeBytes, ReservationRequest{
		Reason:     "bundle_upload",
		TeamID:     input.TeamID,
		BundleID:   input.BundleID,
		DeviceUUID: input.DeviceUUID.String(),
		RequestID:  input.RequestID,
	})
	if err != nil {
		return nil, err
	}
	defer guard.release(ctx)

	// Check bundle ID uniqueness
	exists, err := s.repos.Bundles.ExistsByID(ctx, input.BundleID)
	if err != nil {
		return nil, fmt.Errorf("check bundle exists: %w", err)
	}
	if exists {
		return nil, ErrBundleExists
	}

	// Verify device belongs to user
	device, err := s.repos.Devices.GetByUserAndUUID(ctx, userID, input.DeviceUUID)
	if err != nil {
		return nil, fmt.Errorf("get device: %w", err)
	}
	if device == nil {
		return nil, ErrDeviceNotRegistered
	}
	if device.RevokedAt != nil {
		return nil, ErrDeviceRevoked
	}

	// CE quota enforcement: atomically reserve storage before writing the blob,
	// so an over-quota upload is rejected without ever touching S3. The
	// conditional UPDATE in TryAddBundleUsage is the single authoritative,
	// race-safe check. (EE reserved capacity through the hook above and settles
	// below.) Trade-off: usage is counted before the blob exists, so a crash
	// before rollback can temporarily over-count the user until usage is
	// recomputed -- bounded, and preferred over writing the blob then racing.
	storageLimit := int64(0)
	if !guard.active() {
		storageLimit, err = s.quota.StorageLimit(ctx, userID)
		if err != nil {
			return nil, err
		}
		ok, err := s.quota.TryAddBundleUsage(ctx, userID, input.SizeBytes, storageLimit)
		if err != nil {
			return nil, fmt.Errorf("reserve bundle quota: %w", err)
		}
		if !ok {
			observability.RecordQuotaReservation("bundle", "rejected")
			return nil, ErrQuotaExceeded
		}
		observability.RecordQuotaReservation("bundle", "success")
	}

	meta := &model.BundleMeta{
		BundleID:           input.BundleID,
		UserID:             userID,
		UploaderDeviceUUID: input.DeviceUUID,
		LamportLo:          input.LamportLo,
		LamportHi:          input.LamportHi,
		EventCount:         input.EventCount,
		SizeBytes:          input.SizeBytes,
		CipherID:           input.CipherID,
		KeyGeneration:      input.KeyGeneration,
	}

	if err := s.repos.Bundles.Create(ctx, meta); err != nil {
		if !guard.active() {
			_ = s.repos.Quota.RemoveBundleUsage(ctx, userID, input.SizeBytes)
			observability.RecordQuotaReservation("bundle", "rollback")
		}
		return nil, fmt.Errorf("create bundle meta: %w", err)
	}

	key := storage.BundleKey(userID.String(), input.BundleID)
	if err := s.blobStore.Put(ctx, key, input.Reader, input.SizeBytes, input.ContentType); err != nil {
		_ = s.repos.Bundles.HardDelete(ctx, userID, input.BundleID)
		if !guard.active() {
			_ = s.repos.Quota.RemoveBundleUsage(ctx, userID, input.SizeBytes)
			observability.RecordQuotaReservation("bundle", "rollback")
		}
		uploadResult = "storage_error"
		return nil, fmt.Errorf("store bundle: %w", err)
	}

	if guard.active() {
		if err := guard.settle(ctx, input.SizeBytes); err != nil {
			_, _ = s.repos.Bundles.SoftDelete(ctx, userID, input.BundleID)
			_ = s.blobStore.Delete(ctx, key)
			return nil, fmt.Errorf("settle bundle reservation: %w", err)
		}
	} else {
		s.quota.NotifyBundleUsageAdded(ctx, userID, input.SizeBytes, storageLimit)
	}

	uploadResult = "success"
	return meta, nil
}

// DownloadBundle retrieves a bundle's blob for download.
func (s *BundleService) DownloadBundle(ctx context.Context, userID uuid.UUID, bundleID string) (io.ReadCloser, *model.BundleMeta, error) {
	meta, err := s.repos.Bundles.GetByID(ctx, userID, bundleID)
	if err != nil {
		return nil, nil, fmt.Errorf("get bundle meta: %w", err)
	}
	if meta == nil {
		return nil, nil, ErrBundleNotFound
	}

	key := storage.BundleKey(userID.String(), bundleID)
	reader, err := s.blobStore.Get(ctx, key)
	if err != nil {
		return nil, nil, fmt.Errorf("get bundle blob: %w", err)
	}

	return reader, meta, nil
}

// ListBundles returns bundles for a user, optionally filtered by device.
func (s *BundleService) ListBundles(ctx context.Context, userID uuid.UUID, deviceUUID *uuid.UUID, afterLamport int64, cursor string, limit int32) ([]model.BundleMeta, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}

	if deviceUUID != nil {
		return s.repos.Bundles.ListByDevice(ctx, userID, *deviceUUID, afterLamport, limit)
	}
	return s.repos.Bundles.ListByUser(ctx, userID, cursor, limit)
}

// DeleteBundle soft-deletes a bundle.
func (s *BundleService) DeleteBundle(ctx context.Context, userID uuid.UUID, bundleID string) error {
	meta, err := s.repos.Bundles.SoftDelete(ctx, userID, bundleID)
	if err != nil {
		if errors.Is(err, ErrBundleNotFound) {
			return err
		}
		return err
	}
	if err := s.quota.RemoveBundleUsage(ctx, userID, meta.SizeBytes); err != nil {
		return fmt.Errorf("update bundle quota usage: %w", err)
	}
	return nil
}

// SnapshotService handles snapshot upload, lookup, and download.
type SnapshotService struct {
	repos       *repository.Repos
	blobStore   storage.BlobStorage
	quota       *QuotaService
	reservation UsageReservationHook
}

// UploadSnapshotInput contains the metadata for a snapshot upload.
type UploadSnapshotInput struct {
	SnapshotID    string    `json:"snapshot_id"`
	BaseHLC       int64     `json:"base_hlc"`
	SizeBytes     int64     `json:"size_bytes"`
	CipherID      int16     `json:"cipher_id"`
	KeyGeneration int16     `json:"key_generation"`
	Reader        io.Reader `json:"-"`
	ContentType   string    `json:"-"`
	RequestID     string    `json:"-"`
	TeamID        string    `json:"-"`
}

// UploadSnapshot validates and persists a snapshot.
func (s *SnapshotService) UploadSnapshot(ctx context.Context, userID uuid.UUID, input UploadSnapshotInput) (*model.SnapshotMeta, error) {
	uploadResult := "failure"
	defer func() {
		observability.RecordUpload("snapshot", uploadResult)
	}()

	guard, err := reserve(ctx, s.reservation, userID, input.SizeBytes, ReservationRequest{
		Reason:     "snapshot_upload",
		TeamID:     input.TeamID,
		SnapshotID: input.SnapshotID,
		RequestID:  input.RequestID,
	})
	if err != nil {
		return nil, err
	}
	defer guard.release(ctx)

	count, err := s.repos.Snapshots.CountByUser(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("count snapshots: %w", err)
	}
	user, err := s.repos.Users.GetByID(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("get user: %w", err)
	}
	if user == nil {
		return nil, ErrUserNotFound
	}
	limits := model.TierLimits(user.Tier)

	// CE quota enforcement: atomically reserve storage before writing the blob,
	// rejecting an over-quota upload without touching S3. Old snapshots are
	// pruned only after the replacement snapshot has been fully stored, so a
	// failed upload never reduces the user's restore points. See UploadBundle
	// for the crash-window trade-off. (EE reserved via the hook above and
	// settles below.)
	if !guard.active() {
		ok, err := s.quota.TryAddSnapshotUsage(ctx, userID, input.SizeBytes, limits.StorageLimitBytes)
		if err != nil {
			return nil, fmt.Errorf("reserve snapshot quota: %w", err)
		}
		if !ok {
			observability.RecordQuotaReservation("snapshot", "rejected")
			return nil, ErrQuotaExceeded
		}
		observability.RecordQuotaReservation("snapshot", "success")
	}

	meta := &model.SnapshotMeta{
		SnapshotID:    input.SnapshotID,
		UserID:        userID,
		BaseHLC:       input.BaseHLC,
		SizeBytes:     input.SizeBytes,
		CipherID:      input.CipherID,
		KeyGeneration: input.KeyGeneration,
	}
	if err := s.repos.Snapshots.Create(ctx, meta); err != nil {
		if !guard.active() {
			_ = s.repos.Quota.RemoveSnapshotUsage(ctx, userID, input.SizeBytes)
			observability.RecordQuotaReservation("snapshot", "rollback")
		}
		return nil, fmt.Errorf("create snapshot meta: %w", err)
	}

	key := storage.SnapshotKey(userID.String(), input.SnapshotID)
	if err := s.blobStore.Put(ctx, key, input.Reader, input.SizeBytes, input.ContentType); err != nil {
		_ = s.repos.Snapshots.HardDelete(ctx, userID, input.SnapshotID)
		if !guard.active() {
			_ = s.repos.Quota.RemoveSnapshotUsage(ctx, userID, input.SizeBytes)
			observability.RecordQuotaReservation("snapshot", "rollback")
		}
		uploadResult = "storage_error"
		return nil, fmt.Errorf("store snapshot: %w", err)
	}

	if count >= limits.MaxSnapshots {
		pruned, err := s.repos.Snapshots.PruneOldest(ctx, userID, limits.MaxSnapshots)
		if err != nil {
			_ = s.repos.Snapshots.HardDelete(ctx, userID, input.SnapshotID)
			_ = s.blobStore.Delete(ctx, key)
			if !guard.active() {
				_ = s.repos.Quota.RemoveSnapshotUsage(ctx, userID, input.SizeBytes)
				observability.RecordQuotaReservation("snapshot", "rollback")
			}
			return nil, fmt.Errorf("prune old snapshots: %w", err)
		}
		for _, snapshot := range pruned {
			if err := s.repos.Quota.RemoveSnapshotUsage(ctx, userID, snapshot.SizeBytes); err != nil {
				return nil, fmt.Errorf("update snapshot quota usage after prune: %w", err)
			}
		}
	}

	if guard.active() {
		if err := guard.settle(ctx, input.SizeBytes); err != nil {
			_, _ = s.repos.Snapshots.SoftDelete(ctx, userID, input.SnapshotID)
			_ = s.blobStore.Delete(ctx, key)
			return nil, fmt.Errorf("settle snapshot reservation: %w", err)
		}
	} else {
		s.quota.NotifySnapshotUsageAdded(ctx, userID, input.SizeBytes, limits.StorageLimitBytes)
	}

	uploadResult = "success"
	return meta, nil
}

// DownloadSnapshot retrieves a snapshot blob for download.
func (s *SnapshotService) DownloadSnapshot(ctx context.Context, userID uuid.UUID, snapshotID string) (io.ReadCloser, *model.SnapshotMeta, error) {
	meta, err := s.repos.Snapshots.GetByID(ctx, userID, snapshotID)
	if err != nil {
		return nil, nil, fmt.Errorf("get snapshot meta: %w", err)
	}
	if meta == nil {
		return nil, nil, ErrSnapshotNotFound
	}

	key := storage.SnapshotKey(userID.String(), snapshotID)
	reader, err := s.blobStore.Get(ctx, key)
	if err != nil {
		return nil, nil, fmt.Errorf("get snapshot blob: %w", err)
	}
	return reader, meta, nil
}

// RevokeDevice revokes a device owned by the user and records the event.
func (s *AuthService) RevokeDevice(ctx context.Context, userID, deviceUUID uuid.UUID) error {
	device, err := s.repos.Devices.GetByUserAndUUID(ctx, userID, deviceUUID)
	if err != nil {
		return fmt.Errorf("get device: %w", err)
	}
	if device == nil {
		return ErrDeviceNotFound
	}
	if device.RevokedAt != nil {
		return ErrDeviceAlreadyRevoked
	}

	if err := s.repos.Devices.Revoke(ctx, userID, deviceUUID); err != nil {
		return fmt.Errorf("revoke device: %w", err)
	}
	if err := s.repos.DeviceRevocations.RecordRevocation(ctx, userID, deviceUUID, userID); err != nil {
		return fmt.Errorf("record revocation: %w", err)
	}

	return nil
}

// ListRevocations returns recent device revocation events for a user.
func (s *AuthService) ListRevocations(ctx context.Context, userID uuid.UUID) ([]model.DeviceRevocation, error) {
	revs, err := s.repos.DeviceRevocations.ListByUser(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("list revocations: %w", err)
	}
	if revs == nil {
		return []model.DeviceRevocation{}, nil
	}
	return revs, nil
}

// QuotaService
// QuotaService checks and enforces resource limits.
type QuotaService struct {
	repos         *repository.Repos
	notifications *NotificationService
}

// QuotaInfo contains a user's current usage and limits.
type QuotaInfo struct {
	Storage model.QuotaUsage  `json:"storage"`
	Limits  model.QuotaLimits `json:"limits"`
}

// GetQuota returns the full quota picture for a user.
func (s *QuotaService) GetQuota(ctx context.Context, userID uuid.UUID, tier model.UserTier) (*QuotaInfo, error) {
	usage, err := s.repos.Quota.GetUsage(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("get usage: %w", err)
	}

	limits := model.TierLimits(tier)
	limits.UserID = userID

	return &QuotaInfo{
		Storage: *usage,
		Limits:  limits,
	}, nil
}

// StorageLimit returns the user's storage cap in bytes, derived from their
// current tier. Upload paths pass it to repository.TryAdd*Usage, whose atomic
// conditional UPDATE is the single authoritative, race-safe quota check.
func (s *QuotaService) StorageLimit(ctx context.Context, userID uuid.UUID) (int64, error) {
	user, err := s.repos.Users.GetByID(ctx, userID)
	if err != nil {
		return 0, fmt.Errorf("get user: %w", err)
	}
	if user == nil {
		return 0, ErrUserNotFound
	}
	return model.TierLimits(user.Tier).StorageLimitBytes, nil
}

func (s *QuotaService) TryAddBundleUsage(ctx context.Context, userID uuid.UUID, sizeBytes, storageLimitBytes int64) (bool, error) {
	return s.repos.Quota.TryAddBundleUsage(ctx, userID, sizeBytes, storageLimitBytes)
}

func (s *QuotaService) TryAddSnapshotUsage(ctx context.Context, userID uuid.UUID, sizeBytes, storageLimitBytes int64) (bool, error) {
	return s.repos.Quota.TryAddSnapshotUsage(ctx, userID, sizeBytes, storageLimitBytes)
}

func (s *QuotaService) NotifyBundleUsageAdded(ctx context.Context, userID uuid.UUID, sizeBytes, storageLimitBytes int64) {
	if s.notifications == nil || storageLimitBytes <= 0 {
		return
	}
	after, err := s.repos.Quota.GetUsage(ctx, userID)
	if err != nil || after == nil {
		return
	}
	before := *after
	before.TotalBytes -= sizeBytes
	before.BundleCount--
	if before.TotalBytes < 0 {
		before.TotalBytes = 0
	}
	if before.BundleCount < 0 {
		before.BundleCount = 0
	}
	s.notifications.MaybeNotifyQuotaIncrease(userID, before, *after, storageLimitBytes)
}

func (s *QuotaService) NotifySnapshotUsageAdded(ctx context.Context, userID uuid.UUID, sizeBytes, storageLimitBytes int64) {
	if s.notifications == nil || storageLimitBytes <= 0 {
		return
	}
	after, err := s.repos.Quota.GetUsage(ctx, userID)
	if err != nil || after == nil {
		return
	}
	before := *after
	before.TotalBytes -= sizeBytes
	before.SnapCount--
	if before.TotalBytes < 0 {
		before.TotalBytes = 0
	}
	if before.SnapCount < 0 {
		before.SnapCount = 0
	}
	s.notifications.MaybeNotifyQuotaIncrease(userID, before, *after, storageLimitBytes)
}

func (s *QuotaService) RemoveBundleUsage(ctx context.Context, userID uuid.UUID, sizeBytes int64) error {
	limit, limitErr := s.StorageLimit(ctx, userID)
	before, beforeErr := s.repos.Quota.GetUsage(ctx, userID)
	if err := s.repos.Quota.RemoveBundleUsage(ctx, userID, sizeBytes); err != nil {
		return err
	}
	if limitErr == nil && beforeErr == nil && before != nil && s.notifications != nil {
		if after, err := s.repos.Quota.GetUsage(ctx, userID); err == nil && after != nil {
			s.notifications.MaybeNotifyQuotaRestored(userID, *before, *after, limit)
		}
	}
	return nil
}

func (s *QuotaService) RemoveSnapshotUsage(ctx context.Context, userID uuid.UUID, sizeBytes int64) error {
	limit, limitErr := s.StorageLimit(ctx, userID)
	before, beforeErr := s.repos.Quota.GetUsage(ctx, userID)
	if err := s.repos.Quota.RemoveSnapshotUsage(ctx, userID, sizeBytes); err != nil {
		return err
	}
	if limitErr == nil && beforeErr == nil && before != nil && s.notifications != nil {
		if after, err := s.repos.Quota.GetUsage(ctx, userID); err == nil && after != nil {
			s.notifications.MaybeNotifyQuotaRestored(userID, *before, *after, limit)
		}
	}
	return nil
}

// UsageRecalculation reports a user's storage usage before and after a recompute
// so callers can see the magnitude of any correction.
type UsageRecalculation struct {
	Before model.QuotaUsage `json:"before"`
	After  model.QuotaUsage `json:"after"`
}

// RecalculateUsage corrects a user's storage_usage counters by recomputing them
// from the authoritative bundle and snapshot rows, returning the before/after so
// callers can see the magnitude of any correction.
func (s *QuotaService) RecalculateUsage(ctx context.Context, userID uuid.UUID) (*UsageRecalculation, error) {
	user, err := s.repos.Users.GetByID(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("get user: %w", err)
	}
	if user == nil {
		return nil, ErrUserNotFound
	}

	before, err := s.repos.Quota.GetUsage(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("get usage: %w", err)
	}
	after, err := s.repos.Quota.RecalculateUsage(ctx, userID)
	if err != nil {
		return nil, err
	}
	return &UsageRecalculation{Before: *before, After: *after}, nil
}

// RecalculateAllUsage reconciles storage_usage for every active user in one bulk
// pass, returning the number of users reconciled. It backs the periodic
// maintenance task; per-user corrections use RecalculateUsage.
func (s *QuotaService) RecalculateAllUsage(ctx context.Context) (int64, error) {
	return s.repos.Quota.RecalculateAllUsage(ctx)
}

// CheckDeviceLimit verifies the user can register more devices.
func (s *QuotaService) CheckDeviceLimit(ctx context.Context, userID uuid.UUID, tier model.UserTier) error {
	count, err := s.repos.Devices.CountActiveByUser(ctx, userID)
	if err != nil {
		return fmt.Errorf("count devices: %w", err)
	}

	limits := model.TierLimits(tier)
	if count >= limits.MaxDevices {
		return fmt.Errorf("%w: %d/%d devices", ErrDeviceLimit, count, limits.MaxDevices)
	}

	return nil
}

// BillingService
// BillingService integrates with Stripe for subscription management.
type BillingService struct {
	repos      *repository.Repos
	stripeKey  string
	webhookKey string
	disabled   bool
	provider   provider.BillingProvider
}

// CreateCheckoutSession initiates a checkout session through the configured billing provider.
func (s *BillingService) CreateCheckoutSession(ctx context.Context, userID uuid.UUID, priceID string) (string, error) {
	if s.disabled || s.provider == nil || !s.provider.IsEnabled() {
		return "", ErrStripeDisabled
	}
	url, err := s.provider.CreateCheckoutSession(ctx, userID.String(), priceID)
	if err != nil {
		if errors.Is(err, provider.ErrBillingNotSupported) {
			return "", ErrBillingNotSupported
		}
		return "", err
	}
	return url, nil
}

// CreatePortalSession initiates a customer billing portal session.
func (s *BillingService) CreatePortalSession(ctx context.Context, userID uuid.UUID) (string, error) {
	if s.disabled || s.provider == nil || !s.provider.IsEnabled() {
		return "", ErrStripeDisabled
	}
	url, err := s.provider.CreatePortalSession(ctx, userID.String())
	if err != nil {
		if errors.Is(err, provider.ErrBillingNotSupported) {
			return "", ErrBillingNotSupported
		}
		return "", err
	}
	return url, nil
}

// HandleWebhook processes incoming billing webhook events.
func (s *BillingService) HandleWebhook(ctx context.Context, payload []byte, signature string) error {
	if s.disabled || s.provider == nil || !s.provider.IsEnabled() {
		return ErrStripeDisabled
	}
	if err := s.provider.HandleWebhook(ctx, payload, signature); err != nil {
		if errors.Is(err, provider.ErrBillingNotSupported) {
			return ErrBillingNotSupported
		}
		return err
	}
	return nil
}

// GetSubscription returns the current subscription status.
func (s *BillingService) GetSubscription(ctx context.Context, userID uuid.UUID) (map[string]any, error) {
	if s.disabled || s.provider == nil || !s.provider.IsEnabled() {
		return map[string]any{"status": "disabled"}, nil
	}
	sub, err := s.provider.GetSubscription(ctx, userID.String())
	if err != nil {
		return nil, err
	}
	if sub == nil {
		return map[string]any{"status": "none"}, nil
	}
	return map[string]any{
		"tier":               sub.Tier,
		"status":             sub.Status,
		"current_period_end": sub.CurrentPeriodEnd,
	}, nil
}

// ListInvoices returns recent invoices for a user.
func (s *BillingService) ListInvoices(ctx context.Context, userID uuid.UUID) ([]map[string]any, error) {
	if s.disabled || s.provider == nil || !s.provider.IsEnabled() {
		return []map[string]any{}, nil
	}
	return nil, ErrBillingNotSupported
}

// NotificationService
