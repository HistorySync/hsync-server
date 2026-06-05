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
	AuditEventAdminPlanGrant            AuditEventType = "admin.billing.plan_grant"
	AuditEventAdminCreditAdjust         AuditEventType = "admin.billing.credit_adjust"
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
