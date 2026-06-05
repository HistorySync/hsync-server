package model

import (
	"time"

	"github.com/google/uuid"
)

// IdempotencyStatus is the lifecycle state of a request idempotency record.
type IdempotencyStatus string

const (
	IdempotencyStatusProcessing IdempotencyStatus = "processing"
	IdempotencyStatusSucceeded  IdempotencyStatus = "succeeded"
	IdempotencyStatusFailed     IdempotencyStatus = "failed"
)

// IdempotencyRecord tracks one dangerous mutation guarded by a caller-supplied
// key. IdempotencyKeyHash stores only a SHA-256 hash of the key, never the
// plaintext value.
type IdempotencyRecord struct {
	ID                 uuid.UUID         `json:"id" db:"id"`
	Scope              string            `json:"scope" db:"scope"`
	IdempotencyKeyHash string            `json:"idempotency_key_hash" db:"idempotency_key_hash"`
	RequestFingerprint string            `json:"request_fingerprint" db:"request_fingerprint"`
	Status             IdempotencyStatus `json:"status" db:"status"`
	LockedUntil        *time.Time        `json:"locked_until,omitempty" db:"locked_until"`
	ResponseStatus     *int              `json:"response_status,omitempty" db:"response_status"`
	ResponseBody       []byte            `json:"-" db:"response_body"`
	ErrorReason        string            `json:"error_reason,omitempty" db:"error_reason"`
	ExpiresAt          time.Time         `json:"expires_at" db:"expires_at"`
	CreatedAt          time.Time         `json:"created_at" db:"created_at"`
	UpdatedAt          time.Time         `json:"updated_at" db:"updated_at"`
}
