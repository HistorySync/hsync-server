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
	"time"

	"github.com/google/uuid"
	"golang.org/x/crypto/argon2"

	"github.com/historysync/hsync-server/pkg/auth"
	"github.com/historysync/hsync-server/pkg/model"
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
	ErrEmailTaken         = errors.New("email already registered")
	ErrInvalidCredentials = errors.New("invalid email or password")
	ErrQuotaExceeded      = errors.New("quota exceeded")
	ErrDeviceLimit        = errors.New("device limit reached")
	ErrBundleExists       = errors.New("bundle already exists")
	ErrDeviceRevoked      = errors.New("device has been revoked")
	ErrStripeDisabled     = errors.New("billing is not enabled")
)

// Deps holds all dependencies needed by the service layer.
type Deps struct {
	Repos          *repository.Repos
	TokenManager   *auth.TokenManager
	BlobStore      storage.BlobStorage
	StripeKey      string
	StripeWebhook  string
	StripeDisabled bool
}

// Services aggregates all business service instances.
type Services struct {
	Repos        *repository.Repos
	Auth         *AuthService
	Bundle       *BundleService
	Quota        *QuotaService
	Billing      *BillingService
	Notification *NotificationService
}

// New creates all service instances with their dependencies.
func New(deps Deps) *Services {
	authSvc := &AuthService{
		repos:        deps.Repos,
		tokenManager: deps.TokenManager,
	}
	quotaSvc := &QuotaService{
		repos: deps.Repos,
	}
	bundleSvc := &BundleService{
		repos:     deps.Repos,
		blobStore: deps.BlobStore,
		quota:     quotaSvc,
	}
	billingSvc := &BillingService{
		repos:      deps.Repos,
		stripeKey:  deps.StripeKey,
		webhookKey: deps.StripeWebhook,
		disabled:   deps.StripeDisabled,
	}
	notifSvc := &NotificationService{}

	return &Services{
		Repos:        deps.Repos,
		Auth:         authSvc,
		Bundle:       bundleSvc,
		Quota:        quotaSvc,
		Billing:      billingSvc,
		Notification: notifSvc,
	}
}

// ── AuthService ──────────────────────────────────────────────

// AuthService handles user registration, login, and token management.
type AuthService struct {
	repos        *repository.Repos
	tokenManager *auth.TokenManager
}

// RegisterInput contains the fields required for user registration.
type RegisterInput struct {
	Email       string `json:"email"`
	Password    string `json:"password"`
	DisplayName string `json:"display_name"`
}

// RegisterResult contains the tokens returned after successful registration.
type RegisterResult struct {
	User         model.User `json:"user"`
	AccessToken  string     `json:"access_token"`
	RefreshToken string     `json:"refresh_token"`
	ExpiresIn    int64      `json:"expires_in"`
}

// Register creates a new user account and returns tokens.
func (s *AuthService) Register(ctx context.Context, input RegisterInput) (*RegisterResult, error) {
	// Check for duplicate email
	existing, err := s.repos.Users.GetByEmail(ctx, input.Email)
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
		Email:        input.Email,
		PasswordHash: passwordHash,
		DisplayName:  input.DisplayName,
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

	return &RegisterResult{
		User:         *user,
		AccessToken:  accessToken,
		RefreshToken: refreshToken,
		ExpiresIn:    900, // 15 minutes
	}, nil
}

// LoginInput contains the fields required for user login.
type LoginInput struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

// Login authenticates a user and returns tokens.
func (s *AuthService) Login(ctx context.Context, input LoginInput) (*RegisterResult, error) {
	user, err := s.repos.Users.GetByEmail(ctx, input.Email)
	if err != nil {
		return nil, fmt.Errorf("get user: %w", err)
	}
	if user == nil {
		return nil, ErrInvalidCredentials
	}
	if user.Status != model.StatusActive {
		return nil, fmt.Errorf("account is %s", user.Status)
	}

	// Verify password
	if !verifyPassword(input.Password, user.PasswordHash) {
		return nil, ErrInvalidCredentials
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

	if err := s.repos.RefreshTokens.SaveRefreshToken(ctx, user.ID, tokenHash, "",
		time.Now().Add(30*24*time.Hour)); err != nil {
		return nil, fmt.Errorf("save refresh token: %w", err)
	}

	return &RegisterResult{
		User:         *user,
		AccessToken:  accessToken,
		RefreshToken: refreshToken,
		ExpiresIn:    900,
	}, nil
}

// RefreshAccessToken validates a refresh token and issues a new access token.
func (s *AuthService) RefreshAccessToken(ctx context.Context, refreshToken string) (string, error) {
	tokenHash := hashToken(refreshToken)
	valid, err := s.repos.RefreshTokens.IsTokenValid(ctx, tokenHash)
	if err != nil {
		return "", fmt.Errorf("check token: %w", err)
	}
	if !valid {
		return "", errors.New("invalid or expired refresh token")
	}

	// We need user info. We'll do it lazily by looking up the token.
	// For now, we revoke the old and issue new pair.
	// In a full implementation, the token would include the user ID.
	_ = s.repos.RefreshTokens.RevokeRefreshToken(ctx, tokenHash)

	return "", errors.New("refresh requires user context - implement with user_id in token payload")
}

// Logout revokes a refresh token.
func (s *AuthService) Logout(ctx context.Context, refreshToken string) error {
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

func hashToken(token string) []byte {
	h := sha256.Sum256([]byte(token))
	return h[:]
}

// ── Password Helpers ────────────────────────────────────────

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
	if len(parts) != 6 || parts[0] != "argon2id" {
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

// ── BundleService ────────────────────────────────────────────

// BundleService handles bundle upload validation, deduplication, and listing.
type BundleService struct {
	repos     *repository.Repos
	blobStore storage.BlobStorage
	quota     *QuotaService
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
}

// UploadBundle validates and persists a bundle.
func (s *BundleService) UploadBundle(ctx context.Context, userID uuid.UUID, input UploadInput) (*model.BundleMeta, error) {
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
		return nil, fmt.Errorf("device not registered")
	}
	if device.RevokedAt != nil {
		return nil, ErrDeviceRevoked
	}

	// Quota check
	if err := s.quota.CheckStorageQuota(ctx, userID, input.SizeBytes); err != nil {
		return nil, err
	}

	// Store the blob
	key := storage.BundleKey(userID.String(), input.BundleID)
	if err := s.blobStore.Put(ctx, key, input.Reader, input.SizeBytes, input.ContentType); err != nil {
		return nil, fmt.Errorf("store bundle: %w", err)
	}

	// Write metadata
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
		// Rollback: delete the uploaded blob
		_ = s.blobStore.Delete(ctx, key)
		return nil, fmt.Errorf("create bundle meta: %w", err)
	}

	return meta, nil
}

// DownloadBundle retrieves a bundle's blob for download.
func (s *BundleService) DownloadBundle(ctx context.Context, userID uuid.UUID, bundleID string) (io.ReadCloser, *model.BundleMeta, error) {
	meta, err := s.repos.Bundles.GetByID(ctx, userID, bundleID)
	if err != nil {
		return nil, nil, fmt.Errorf("get bundle meta: %w", err)
	}
	if meta == nil {
		return nil, nil, errors.New("bundle not found")
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
	return s.repos.Bundles.SoftDelete(ctx, userID, bundleID)
}

// ── QuotaService ─────────────────────────────────────────────

// QuotaService checks and enforces resource limits.
type QuotaService struct {
	repos *repository.Repos
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

// CheckStorageQuota verifies the user has enough storage remaining.
func (s *QuotaService) CheckStorageQuota(ctx context.Context, userID uuid.UUID, additionalBytes int64) error {
	usage, err := s.repos.Quota.GetUsage(ctx, userID)
	if err != nil {
		return fmt.Errorf("get usage: %w", err)
	}

	user, err := s.repos.Users.GetByID(ctx, userID)
	if err != nil {
		return fmt.Errorf("get user: %w", err)
	}
	if user == nil {
		return errors.New("user not found")
	}

	limits := model.TierLimits(user.Tier)

	if usage.TotalBytes+additionalBytes > limits.StorageLimitBytes {
		return fmt.Errorf("%w: current=%d, limit=%d, requested=%d",
			ErrQuotaExceeded, usage.TotalBytes, limits.StorageLimitBytes, additionalBytes)
	}

	return nil
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

// ── BillingService ───────────────────────────────────────────

// BillingService integrates with Stripe for subscription management.
type BillingService struct {
	repos      *repository.Repos
	stripeKey  string
	webhookKey string
	disabled   bool
}

// CreateCheckoutSession initiates a Stripe checkout.
func (s *BillingService) CreateCheckoutSession(ctx context.Context, userID uuid.UUID, priceID string) (string, error) {
	if s.disabled {
		return "", ErrStripeDisabled
	}
	// TODO: Integrate Stripe SDK, create checkout session
	return "", errors.New("stripe integration not implemented")
}

// HandleWebhook processes incoming Stripe webhook events.
func (s *BillingService) HandleWebhook(ctx context.Context, payload []byte, signature string) error {
	if s.disabled {
		return ErrStripeDisabled
	}
	// TODO: Verify Stripe signature, process events
	return errors.New("stripe webhook not implemented")
}

// GetSubscription returns the current subscription status.
func (s *BillingService) GetSubscription(ctx context.Context, userID uuid.UUID) (map[string]any, error) {
	if s.disabled {
		return map[string]any{"status": "disabled"}, nil
	}
	return map[string]any{"status": "active"}, nil
}

// ── NotificationService ──────────────────────────────────────

// NotificationService sends email and push notifications.
type NotificationService struct{}

// SendWelcomeEmail sends a welcome email to a newly registered user.
func (s *NotificationService) SendWelcomeEmail(email, displayName string) error {
	// TODO: Implement email sending (SMTP / SendGrid / SES)
	return nil
}

// SendQuotaWarning sends a quota warning notification.
func (s *NotificationService) SendQuotaWarning(email string, usagePercent float64) error {
	// TODO: Implement
	return nil
}
