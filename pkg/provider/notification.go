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
	SendNotification(ctx context.Context, params NotificationParams) error
}

// WebhookProvider sends sanitized notification payloads to user-configured
// webhook endpoints.
type WebhookProvider interface {
	DeliveryEnabled() bool
	Send(ctx context.Context, webhookURL, secret string, notification WebhookNotification) error
}

// WebhookNotification is the payload shape posted to user webhooks. Callers
// should only place sanitized, non-secret values in Data.
type WebhookNotification struct {
	Type      string         `json:"type"`
	Category  string         `json:"category"`
	Subject   string         `json:"subject"`
	Message   string         `json:"message"`
	Data      map[string]any `json:"data,omitempty"`
	Timestamp time.Time      `json:"timestamp"`
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

type NotificationParams struct {
	UserID      string
	Email       string
	DisplayName string
	AppName     string
	Category    string
	Type        string
	Subject     string
	Message     string
}
