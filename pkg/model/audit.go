package model

import (
	"time"

	"github.com/google/uuid"
)

type AuditEventType string

const (
	AuditEventLoginSuccess              AuditEventType = "auth.login.success"
	AuditEventLoginFailure              AuditEventType = "auth.login.failure"
	AuditEventTwoFactorChallengeSuccess AuditEventType = "auth.2fa.challenge.success"
	AuditEventTwoFactorChallengeFailure AuditEventType = "auth.2fa.challenge.failure"
	AuditEventTwoFactorEnable           AuditEventType = "auth.2fa.enable"
	AuditEventTwoFactorDisable          AuditEventType = "auth.2fa.disable"
	AuditEventAdminConfigChange         AuditEventType = "admin.config.change"
	AuditEventNotificationOutboxRetry   AuditEventType = "admin.notification_outbox.retry"
	AuditEventNotificationOutboxRequeue AuditEventType = "admin.notification_outbox.requeue"
	AuditEventNotificationOutboxDiscard AuditEventType = "admin.notification_outbox.discard"
)

type AuditLog struct {
	ID          uuid.UUID      `json:"id" db:"id"`
	ActorUserID *uuid.UUID     `json:"actor_user_id,omitempty" db:"actor_user_id"`
	EventType   AuditEventType `json:"event_type" db:"event_type"`
	TargetType  string         `json:"target_type" db:"target_type"`
	TargetID    string         `json:"target_id" db:"target_id"`
	IP          string         `json:"ip" db:"ip"`
	UserAgent   string         `json:"user_agent" db:"user_agent"`
	Metadata    map[string]any `json:"metadata" db:"metadata"`
	CreatedAt   time.Time      `json:"created_at" db:"created_at"`
}

type AuditListFilter struct {
	ActorUserID *uuid.UUID
	EventType   AuditEventType
	TargetType  string
	TargetID    string
	Limit       int32
	Offset      int32
}

type SecurityStats struct {
	GeneratedAt  time.Time              `json:"generated_at"`
	Last24h      SecurityStatsWindow    `json:"last_24h"`
	Last7d       SecurityStatsWindow    `json:"last_7d"`
	TwoFactor    SecurityTwoFactorStats `json:"two_factor"`
	EventsByType []AuditEventTypeCount  `json:"events_by_type"`
}

type SecurityStatsWindow struct {
	Since                     time.Time `json:"since"`
	Until                     time.Time `json:"until"`
	LoginSuccess              int64     `json:"login_success"`
	LoginFailure              int64     `json:"login_failure"`
	TwoFactorChallengeSuccess int64     `json:"two_factor_challenge_success"`
	TwoFactorChallengeFailure int64     `json:"two_factor_challenge_failure"`
}

type SecurityTwoFactorStats struct {
	EnabledUsers int64   `json:"enabled_users"`
	TotalUsers   int64   `json:"total_users"`
	EnabledRatio float64 `json:"enabled_ratio"`
}

type AuditEventTypeCount struct {
	EventType AuditEventType `json:"event_type"`
	Count     int64          `json:"count"`
}

type SecurityEventWindowCount struct {
	EventType AuditEventType
	Last24h   int64
	Last7d    int64
}
