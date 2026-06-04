package provider

import (
	"context"
	"errors"
	"time"
)

const (
	SMTPTLSModeNone     = "none"
	SMTPTLSModeStartTLS = "starttls"
	SMTPTLSModeTLS      = "tls"
)

var ErrNotificationNotConfigured = errors.New("notification provider is not configured")

// Notifier sends user-facing notifications. Implementations should treat
// delivery as best-effort and avoid leaking secrets in returned errors.
type Notifier interface {
	DeliveryEnabled() bool
	SendWelcome(ctx context.Context, params WelcomeParams) error
	SendEmailVerification(ctx context.Context, params EmailVerificationParams) error
	SendPasswordReset(ctx context.Context, params PasswordResetParams) error
	SendQuotaWarning(ctx context.Context, params QuotaWarningParams) error
	SendQuotaExhausted(ctx context.Context, params QuotaExhaustedParams) error
	SendQuotaRestored(ctx context.Context, params QuotaRestoredParams) error
}

type WelcomeParams struct {
	UserID      string
	Email       string
	DisplayName string
	AppName     string
}

type EmailVerificationParams struct {
	UserID          string
	Email           string
	DisplayName     string
	AppName         string
	VerificationURL string
	ExpiresIn       time.Duration
}

type PasswordResetParams struct {
	UserID      string
	Email       string
	DisplayName string
	AppName     string
	ResetURL    string
	ExpiresIn   time.Duration
}

type QuotaWarningParams struct {
	UserID        string
	Email         string
	DisplayName   string
	AppName       string
	UsageBytes    int64
	LimitBytes    int64
	UsagePercent  int
	BundleCount   int64
	SnapshotCount int64
}

type QuotaExhaustedParams struct {
	UserID        string
	Email         string
	DisplayName   string
	AppName       string
	UsageBytes    int64
	LimitBytes    int64
	UsagePercent  int
	BundleCount   int64
	SnapshotCount int64
}

type QuotaRestoredParams struct {
	UserID        string
	Email         string
	DisplayName   string
	AppName       string
	UsageBytes    int64
	LimitBytes    int64
	UsagePercent  int
	BundleCount   int64
	SnapshotCount int64
}
