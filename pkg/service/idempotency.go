package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/historysync/hsync-server/pkg/repository"
)

var (
	ErrIdempotencyConflict         = errors.New("idempotency key reused with a different request")
	ErrIdempotencyInProgress       = errors.New("idempotent request is already processing")
	ErrIdempotencyStoreUnavailable = errors.New("idempotency store unavailable")
)

type idempotencyStore interface {
	Claim(ctx context.Context, p repository.IdempotencyClaimParams) (repository.IdempotencyClaimResult, error)
	MarkSucceeded(ctx context.Context, id uuid.UUID, responseStatus int, responseBody []byte, now time.Time) error
	MarkFailed(ctx context.Context, id uuid.UUID, reason string, now time.Time) error
}

// IdempotencyService coordinates request-level idempotency for dangerous
// mutations. It is intentionally small: callers provide the scope, key, and
// payload; the service hashes the key and stores only the fingerprint.
type IdempotencyService struct {
	store idempotencyStore
	now   func() time.Time
}

// IdempotencyOptions describes one guarded execution.
type IdempotencyOptions struct {
	Scope          string
	IdempotencyKey string
	Payload        any
	RequireKey     bool
	TTL            time.Duration
	LockTTL        time.Duration
}

// IdempotencyExecution reports either a fresh execution or a replay.
type IdempotencyExecution[T any] struct {
	Data           *T
	ResponseStatus int
	Replayed       bool
}

// NewIdempotencyService wires a reusable idempotency coordinator.
func NewIdempotencyService(store idempotencyStore) *IdempotencyService {
	return &IdempotencyService{store: store, now: time.Now}
}

// DefaultWriteIdempotencyTTL is the retention window for admin mutations.
func DefaultWriteIdempotencyTTL() time.Duration {
	return 24 * time.Hour
}

// DefaultWebhookIdempotencyTTL is the retention window for payment webhooks.
func DefaultWebhookIdempotencyTTL() time.Duration {
	return 7 * 24 * time.Hour
}

// ExecuteIdempotent runs execute only when the idempotency record is freshly
// claimed or safely reclaimed. Successful responses are stored as JSON and
// replayed for later requests with the same key and payload fingerprint.
func ExecuteIdempotent[T any](
	ctx context.Context,
	svc *IdempotencyService,
	opts IdempotencyOptions,
	execute func(context.Context) (*T, int, error),
) (*IdempotencyExecution[T], error) {
	if svc == nil || svc.store == nil {
		return nil, ErrIdempotencyStoreUnavailable
	}
	key := strings.TrimSpace(opts.IdempotencyKey)
	if key == "" {
		if opts.RequireKey {
			return nil, ErrIdempotencyKeyRequired
		}
		data, status, err := execute(ctx)
		if err != nil {
			return nil, err
		}
		if status == 0 {
			status = 200
		}
		return &IdempotencyExecution[T]{Data: data, ResponseStatus: status}, nil
	}
	if strings.TrimSpace(opts.Scope) == "" {
		return nil, fmt.Errorf("%w: scope is required", ErrIdempotencyStoreUnavailable)
	}
	fingerprint, err := FingerprintPayload(opts.Payload)
	if err != nil {
		return nil, err
	}
	ttl := opts.TTL
	if ttl <= 0 {
		ttl = DefaultWriteIdempotencyTTL()
	}
	lockTTL := opts.LockTTL
	if lockTTL <= 0 {
		lockTTL = 30 * time.Second
	}
	now := svc.now()
	claim, err := svc.store.Claim(ctx, repository.IdempotencyClaimParams{
		Scope:              strings.TrimSpace(opts.Scope),
		IdempotencyKeyHash: HashIdempotencyKey(key),
		RequestFingerprint: fingerprint,
		Now:                now,
		LockedUntil:        now.Add(lockTTL),
		ExpiresAt:          now.Add(ttl),
	})
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrIdempotencyStoreUnavailable, err)
	}
	switch claim.Status {
	case repository.IdempotencyClaimReplayed:
		var data T
		if len(claim.Record.ResponseBody) > 0 {
			if err := json.Unmarshal(claim.Record.ResponseBody, &data); err != nil {
				return nil, fmt.Errorf("decode idempotency replay: %w", err)
			}
		}
		status := 200
		if claim.Record.ResponseStatus != nil && *claim.Record.ResponseStatus > 0 {
			status = *claim.Record.ResponseStatus
		}
		return &IdempotencyExecution[T]{Data: &data, ResponseStatus: status, Replayed: true}, nil
	case repository.IdempotencyClaimConflict:
		return nil, ErrIdempotencyConflict
	case repository.IdempotencyClaimProcessing:
		return nil, ErrIdempotencyInProgress
	}

	data, status, err := execute(ctx)
	if err != nil {
		_ = svc.store.MarkFailed(ctx, claim.Record.ID, err.Error(), svc.now())
		return nil, err
	}
	if status == 0 {
		status = 200
	}
	responseBody, err := json.Marshal(data)
	if err != nil {
		_ = svc.store.MarkFailed(ctx, claim.Record.ID, err.Error(), svc.now())
		return nil, fmt.Errorf("encode idempotency response: %w", err)
	}
	if err := svc.store.MarkSucceeded(ctx, claim.Record.ID, status, responseBody, svc.now()); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrIdempotencyStoreUnavailable, err)
	}
	return &IdempotencyExecution[T]{Data: data, ResponseStatus: status}, nil
}

// HashIdempotencyKey returns a SHA-256 hex digest for the plaintext key.
func HashIdempotencyKey(key string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(key)))
	return hex.EncodeToString(sum[:])
}

// FingerprintPayload returns a SHA-256 hex digest of a canonical JSON payload.
func FingerprintPayload(payload any) (string, error) {
	var data []byte
	switch typed := payload.(type) {
	case nil:
		data = []byte("null")
	case []byte:
		data = typed
	case string:
		data = []byte(typed)
	default:
		encoded, err := json.Marshal(typed)
		if err != nil {
			return "", fmt.Errorf("fingerprint idempotency payload: %w", err)
		}
		data = encoded
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}
