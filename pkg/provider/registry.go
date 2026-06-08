// Package provider defines the core abstraction interfaces for HistorySync.
//
// CE (Community Edition) provides default minimal implementations.
// EE (Enterprise Edition) injects full-featured implementations via init().
package provider

import (
	"context"
	"errors"
	"sync"
)

// Common Errors
var (
	ErrMultiUserNotSupported = errors.New("multi-user registration requires HistorySync Enterprise")
	ErrBillingNotSupported   = errors.New("billing requires HistorySync Enterprise")
	ErrInvalidCredentials    = errors.New("invalid credentials")
	ErrQuotaExceeded         = errors.New("quota exceeded")
	ErrDeviceLimit           = errors.New("device limit reached")
)

// Provider Interfaces
// AuthProvider defines the authentication abstraction.
type AuthProvider interface {
	// ValidateCredentials verifies email/password and returns the user.
	ValidateCredentials(email, password string) (*UserInfo, error)

	// CreateUser registers a new user account.
	// CE always returns ErrMultiUserNotSupported.
	CreateUser(req CreateUserRequest) (*UserInfo, error)

	// GetUserByID fetches a user by their unique ID.
	GetUserByID(userID string) (*UserInfo, error)

	// SupportsMultiUser reports whether multi-user registration is available.
	SupportsMultiUser() bool
}

// BillingProvider defines the billing/payment abstraction.
type BillingProvider interface {
	// CreateCheckoutSession creates a Stripe Checkout session URL.
	CreateCheckoutSession(ctx context.Context, userID, priceID string) (string, error)

	// HandleWebhook processes an incoming Stripe webhook event.
	HandleWebhook(ctx context.Context, payload []byte, signature string) error

	// GetSubscription returns the current subscription for a user.
	GetSubscription(ctx context.Context, userID string) (*SubscriptionInfo, error)

	// CreatePortalSession creates a Stripe Customer Portal URL.
	CreatePortalSession(ctx context.Context, userID string) (string, error)

	// IsEnabled reports whether billing is configured and available.
	IsEnabled() bool
}

// QuotaProvider defines the quota/limits abstraction.
type QuotaProvider interface {
	// GetLimits returns the quota limits for a user.
	GetLimits(userID string) (*QuotaLimitsInfo, error)

	// GetUsage returns current resource consumption for a user.
	GetUsage(userID string) (*QuotaUsageInfo, error)

	// CheckStorageQuota verifies that adding N bytes won't exceed the limit.
	CheckStorageQuota(userID string, additionalBytes int64) error

	// RecordUsage updates consumption tracking after an upload.
	RecordUsage(userID string, bytes int64) error
}

// ReadinessProvider contributes extra readiness checks (e.g. Enterprise
// dependencies) that are merged into the /readyz response. CE registers a
// no-op default; Enterprise injects real checks via RegisterReadiness.
type ReadinessProvider interface {
	// ReadinessChecks returns named dependency checks to surface in /readyz.
	ReadinessChecks(ctx context.Context) []ReadinessCheck
}

// OpsRestoreProvider contributes edition-specific restore rehearsal checks.
// CE registers a no-op default; Enterprise can add schema/table validation
// without CE knowing commercial table names.
type OpsRestoreProvider interface {
	// RestoreChecks returns named validation checks for restored environments.
	RestoreChecks(ctx context.Context) []OpsRestoreCheck
}

// AccountDeletionPolicy contributes edition-specific account deletion gates.
// CE owns the generic deletion workflow; Enterprise can block or require
// operator review for commercial and team policy.
type AccountDeletionPolicy interface {
	EvaluateAccountDeletion(ctx context.Context, req AccountDeletionRequest) (*AccountDeletionDecision, error)
}

// ReadinessCheck is one named dependency check surfaced by /readyz.
type ReadinessCheck struct {
	// Name is a short key, e.g. "ee_schema" or "stripe".
	Name string
	// Status is a human-readable state, e.g. "ok", "disabled", "error: ...".
	Status string
	// Healthy reports whether the dependency is in an acceptable state.
	Healthy bool
	// Critical, when true and the check is not Healthy, makes /readyz unhealthy.
	Critical bool
}

// OpsRestoreCheck is one edition-specific restore validation item.
type OpsRestoreCheck struct {
	Name       string `json:"name"`
	Required   bool   `json:"required"`
	Status     string `json:"status"`
	Message    string `json:"message"`
	Action     string `json:"action,omitempty"`
	ErrorClass string `json:"error_class,omitempty"`
}

type AccountDeletionRequest struct {
	UserID string
	Email  string
	Tier   string
	Status string
}

type AccountDeletionDecision struct {
	Allowed        bool
	RequiresReview bool
	Reasons        []AccountDeletionPolicyReason
	Metadata       map[string]any
}

type AccountDeletionPolicyReason struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Review  bool   `json:"review,omitempty"`
}

// Shared Types
// CreateUserRequest carries registration data.
type CreateUserRequest struct {
	Email       string
	Password    string
	DisplayName string
}

// UserInfo is the provider's view of a user (decoupled from model.User).
type UserInfo struct {
	ID          string
	Email       string
	DisplayName string
	Tier        string
	Status      string
}

// SubscriptionInfo carries subscription status.
type SubscriptionInfo struct {
	Tier             string
	Status           string
	CurrentPeriodEnd int64 // Unix timestamp
}

// QuotaLimitsInfo carries tier-based resource caps.
type QuotaLimitsInfo struct {
	StorageLimitBytes   int64
	MaxDevices          int32
	MaxBundleSize       int64
	MaxSnapshots        int32
	MaxRPM              int32
	BundleRetentionDays int32
}

// QuotaUsageInfo carries current resource consumption.
type QuotaUsageInfo struct {
	TotalBytes  int64
	BundleCount int32
	SnapCount   int32
}

// Registry
// ProviderRegistry holds the active provider implementations.
// Enterprise packages call Register*() in their init() to replace defaults.
type ProviderRegistry struct {
	Auth       AuthProvider
	Billing    BillingProvider
	Quota      QuotaProvider
	Readiness  ReadinessProvider
	OpsRestore OpsRestoreProvider
	Deletion   AccountDeletionPolicy
	Notifier   Notifier
	Webhook    WebhookProvider
}

var (
	registry = &ProviderRegistry{
		Auth:       defaultAuthProvider,
		Billing:    defaultBillingProvider,
		Quota:      defaultQuotaProvider,
		Readiness:  defaultReadinessProvider,
		OpsRestore: defaultOpsRestoreProvider,
		Deletion:   defaultAccountDeletionPolicy,
		Notifier:   NewLogNotifier(),
		Webhook:    NewWebhookNotifier(WebhookConfig{}),
	}
	regMu sync.RWMutex
)

// Registry returns the global provider registry (read-only from callers).
func Registry() *ProviderRegistry {
	regMu.RLock()
	defer regMu.RUnlock()
	return registry
}

// RegisterAuth replaces the current AuthProvider.
func RegisterAuth(p AuthProvider) {
	regMu.Lock()
	defer regMu.Unlock()
	registry.Auth = p
}

// RegisterBilling replaces the current BillingProvider.
func RegisterBilling(p BillingProvider) {
	regMu.Lock()
	defer regMu.Unlock()
	registry.Billing = p
}

// RegisterQuota replaces the current QuotaProvider.
func RegisterQuota(p QuotaProvider) {
	regMu.Lock()
	defer regMu.Unlock()
	registry.Quota = p
}

// RegisterReadiness replaces the current ReadinessProvider.
func RegisterReadiness(p ReadinessProvider) {
	regMu.Lock()
	defer regMu.Unlock()
	registry.Readiness = p
}

// RegisterOpsRestore replaces the current OpsRestoreProvider.
func RegisterOpsRestore(p OpsRestoreProvider) {
	regMu.Lock()
	defer regMu.Unlock()
	registry.OpsRestore = p
}

// RegisterAccountDeletionPolicy replaces the current AccountDeletionPolicy.
func RegisterAccountDeletionPolicy(p AccountDeletionPolicy) {
	regMu.Lock()
	defer regMu.Unlock()
	registry.Deletion = p
}

// RegisterNotifier replaces the current notification provider.
func RegisterNotifier(p Notifier) {
	regMu.Lock()
	defer regMu.Unlock()
	registry.Notifier = p
}

// RegisterWebhook replaces the current webhook notification provider.
func RegisterWebhook(p WebhookProvider) {
	regMu.Lock()
	defer regMu.Unlock()
	registry.Webhook = p
}
