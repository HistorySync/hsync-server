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
	AuditEventPasskeyAdded              AuditEventType = "auth.passkey.added"
	AuditEventPasskeyDeleted            AuditEventType = "auth.passkey.deleted"
	AuditEventDeviceTokenIssued         AuditEventType = "device.token.issued"
	AuditEventDeviceTokenRotated        AuditEventType = "device.token.rotated"
	AuditEventDeviceTokenRejected       AuditEventType = "device.token.rejected"
	AuditEventPrivacyExport             AuditEventType = "account.privacy_export"
	AuditEventAccountDeletionRequest    AuditEventType = "account.deletion.request"
	AuditEventAccountDeletionResult     AuditEventType = "account.deletion.result"
	AuditEventAccountErasureJobCreated  AuditEventType = "account.erasure.job_created"
	AuditEventAccountErasureJobStarted  AuditEventType = "account.erasure.job_started"
	AuditEventAccountErasureJobFinished AuditEventType = "account.erasure.job_finished"
	AuditEventAccountErasureJobFailed   AuditEventType = "account.erasure.job_failed"
	AuditEventAdminConfigChange         AuditEventType = "admin.config.change"
	AuditEventAdminSecurityTimelineRead AuditEventType = "admin.security.timeline.read"
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
	Email       string
	Since       time.Time
	Until       time.Time
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

type SecurityTimelineFilter struct {
	UserID string `json:"user_id,omitempty"`
	Email  string `json:"email,omitempty"`
	IPHash string `json:"ip_hash,omitempty"`
	Action string `json:"action,omitempty"`
	Since  string `json:"since,omitempty"`
	Until  string `json:"until,omitempty"`
	Limit  int32  `json:"limit,omitempty"`
	Offset int32  `json:"offset,omitempty"`
}

type SecurityTimelineSummary struct {
	TotalEvents             int64                 `json:"total_events"`
	AuthFailures            int64                 `json:"auth_failures"`
	StepUpEvents            int64                 `json:"step_up_events"`
	PasskeyChanges          int64                 `json:"passkey_changes"`
	AccountDeletionOrExport int64                 `json:"account_deletion_or_export"`
	Actions                 []AuditEventTypeCount `json:"actions"`
}

type SecurityTimelineEvent struct {
	ID          string         `json:"id"`
	Source      string         `json:"source"`
	Action      AuditEventType `json:"action"`
	Category    string         `json:"category"`
	UserID      string         `json:"user_id,omitempty"`
	ActorUserID string         `json:"actor_user_id,omitempty"`
	EmailHint   string         `json:"email_hint,omitempty"`
	IPHash      string         `json:"ip_hash,omitempty"`
	TargetType  string         `json:"target_type,omitempty"`
	TargetID    string         `json:"target_id,omitempty"`
	Metadata    map[string]any `json:"metadata,omitempty"`
	CreatedAt   time.Time      `json:"created_at"`
}

type SecurityTimelineResponse struct {
	GeneratedAt time.Time               `json:"generated_at"`
	Filter      SecurityTimelineFilter  `json:"filter"`
	Summary     SecurityTimelineSummary `json:"summary"`
	Events      []SecurityTimelineEvent `json:"events"`
	Total       int64                   `json:"total"`
	Truncated   bool                    `json:"truncated"`
}
