package repository

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/historysync/hsync-server/pkg/model"
)

// IdempotencyRepo stores reusable request idempotency records.
type IdempotencyRepo struct {
	pool *pgxpool.Pool
}

// IdempotencyClaimStatus describes the outcome of claiming a key.
type IdempotencyClaimStatus string

const (
	IdempotencyClaimStarted    IdempotencyClaimStatus = "started"
	IdempotencyClaimReplayed   IdempotencyClaimStatus = "replayed"
	IdempotencyClaimConflict   IdempotencyClaimStatus = "conflict"
	IdempotencyClaimProcessing IdempotencyClaimStatus = "processing"
)

// IdempotencyClaimParams describes a claim attempt.
type IdempotencyClaimParams struct {
	Scope              string
	IdempotencyKeyHash string
	RequestFingerprint string
	Now                time.Time
	LockedUntil        time.Time
	ExpiresAt          time.Time
}

// IdempotencyClaimResult returns the claim status and associated record.
type IdempotencyClaimResult struct {
	Status IdempotencyClaimStatus
	Record model.IdempotencyRecord
}

// Claim starts processing for a new idempotency key, returns a replayable
// succeeded record, rejects mismatched payload fingerprints, or reports that a
// still-locked request is already processing.
func (r *IdempotencyRepo) Claim(ctx context.Context, p IdempotencyClaimParams) (IdempotencyClaimResult, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return IdempotencyClaimResult{}, fmt.Errorf("begin idempotency claim: %w", err)
	}
	defer tx.Rollback(ctx)

	inserted, err := insertProcessingIdempotency(ctx, tx, p)
	if err != nil {
		return IdempotencyClaimResult{}, err
	}
	if inserted != nil {
		if err := tx.Commit(ctx); err != nil {
			return IdempotencyClaimResult{}, fmt.Errorf("commit idempotency claim: %w", err)
		}
		return IdempotencyClaimResult{Status: IdempotencyClaimStarted, Record: *inserted}, nil
	}

	existing, err := getIdempotencyForUpdate(ctx, tx, p.Scope, p.IdempotencyKeyHash)
	if err != nil {
		return IdempotencyClaimResult{}, err
	}
	if existing == nil {
		return IdempotencyClaimResult{}, fmt.Errorf("idempotency record disappeared during claim")
	}
	if existing.RequestFingerprint != p.RequestFingerprint {
		if err := tx.Commit(ctx); err != nil {
			return IdempotencyClaimResult{}, fmt.Errorf("commit idempotency conflict: %w", err)
		}
		return IdempotencyClaimResult{Status: IdempotencyClaimConflict, Record: *existing}, nil
	}
	if existing.Status == model.IdempotencyStatusSucceeded {
		if err := tx.Commit(ctx); err != nil {
			return IdempotencyClaimResult{}, fmt.Errorf("commit idempotency replay: %w", err)
		}
		return IdempotencyClaimResult{Status: IdempotencyClaimReplayed, Record: *existing}, nil
	}
	if existing.Status == model.IdempotencyStatusProcessing &&
		existing.LockedUntil != nil && existing.LockedUntil.After(p.Now) {
		if err := tx.Commit(ctx); err != nil {
			return IdempotencyClaimResult{}, fmt.Errorf("commit idempotency processing: %w", err)
		}
		return IdempotencyClaimResult{Status: IdempotencyClaimProcessing, Record: *existing}, nil
	}

	reclaimed, err := reclaimIdempotency(ctx, tx, existing.ID, p)
	if err != nil {
		return IdempotencyClaimResult{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return IdempotencyClaimResult{}, fmt.Errorf("commit idempotency reclaim: %w", err)
	}
	return IdempotencyClaimResult{Status: IdempotencyClaimStarted, Record: *reclaimed}, nil
}

// MarkSucceeded records a successful response for replay.
func (r *IdempotencyRepo) MarkSucceeded(ctx context.Context, id uuid.UUID, responseStatus int, responseBody []byte, now time.Time) error {
	if len(responseBody) == 0 {
		responseBody = []byte("{}")
	}
	const q = `
		UPDATE idempotency_records
		SET status = 'succeeded', locked_until = NULL, response_status = $2,
		    response_body = $3::jsonb, error_reason = ''
		WHERE id = $1`
	if _, err := r.pool.Exec(ctx, q, id, responseStatus, string(responseBody)); err != nil {
		return fmt.Errorf("mark idempotency succeeded: %w", err)
	}
	return nil
}

// MarkFailed records a failed attempt. A later retry with the same key and
// payload may reclaim the record after the processing lock has elapsed.
func (r *IdempotencyRepo) MarkFailed(ctx context.Context, id uuid.UUID, reason string, now time.Time) error {
	const q = `
		UPDATE idempotency_records
		SET status = 'failed', locked_until = NULL, error_reason = $2
		WHERE id = $1`
	if _, err := r.pool.Exec(ctx, q, id, trimFailureReason(reason)); err != nil {
		return fmt.Errorf("mark idempotency failed: %w", err)
	}
	return nil
}

func trimFailureReason(reason string) string {
	if len(reason) <= 500 {
		return reason
	}
	return reason[:500]
}

func insertProcessingIdempotency(ctx context.Context, tx pgx.Tx, p IdempotencyClaimParams) (*model.IdempotencyRecord, error) {
	const q = `
		INSERT INTO idempotency_records (
			scope, idempotency_key_hash, request_fingerprint, status,
			locked_until, expires_at)
		VALUES ($1, $2, $3, 'processing', $4, $5)
		ON CONFLICT (scope, idempotency_key_hash) DO NOTHING
		RETURNING id, scope, idempotency_key_hash, request_fingerprint, status,
		          locked_until, response_status, response_body, error_reason,
		          expires_at, created_at, updated_at`
	rows, err := tx.Query(ctx, q, p.Scope, p.IdempotencyKeyHash, p.RequestFingerprint, p.LockedUntil, p.ExpiresAt)
	if err != nil {
		return nil, fmt.Errorf("insert idempotency record: %w", err)
	}
	defer rows.Close()
	records, err := scanIdempotencyRecords(rows)
	if err != nil {
		return nil, err
	}
	if len(records) == 0 {
		return nil, nil
	}
	return &records[0], nil
}

func getIdempotencyForUpdate(ctx context.Context, tx pgx.Tx, scope, keyHash string) (*model.IdempotencyRecord, error) {
	const q = `
		SELECT id, scope, idempotency_key_hash, request_fingerprint, status,
		       locked_until, response_status, response_body, error_reason,
		       expires_at, created_at, updated_at
		FROM idempotency_records
		WHERE scope = $1 AND idempotency_key_hash = $2
		FOR UPDATE`
	rows, err := tx.Query(ctx, q, scope, keyHash)
	if err != nil {
		return nil, fmt.Errorf("get idempotency record: %w", err)
	}
	defer rows.Close()
	records, err := scanIdempotencyRecords(rows)
	if err != nil {
		return nil, err
	}
	if len(records) == 0 {
		return nil, nil
	}
	return &records[0], nil
}

func reclaimIdempotency(ctx context.Context, tx pgx.Tx, id uuid.UUID, p IdempotencyClaimParams) (*model.IdempotencyRecord, error) {
	const q = `
		UPDATE idempotency_records
		SET status = 'processing', locked_until = $2, expires_at = $3,
		    response_status = NULL, response_body = '{}'::jsonb, error_reason = ''
		WHERE id = $1
		RETURNING id, scope, idempotency_key_hash, request_fingerprint, status,
		          locked_until, response_status, response_body, error_reason,
		          expires_at, created_at, updated_at`
	rows, err := tx.Query(ctx, q, id, p.LockedUntil, p.ExpiresAt)
	if err != nil {
		return nil, fmt.Errorf("reclaim idempotency record: %w", err)
	}
	defer rows.Close()
	records, err := scanIdempotencyRecords(rows)
	if err != nil {
		return nil, err
	}
	if len(records) == 0 {
		return nil, fmt.Errorf("idempotency record not found while reclaiming")
	}
	return &records[0], nil
}

func scanIdempotencyRecords(rows pgx.Rows) ([]model.IdempotencyRecord, error) {
	var records []model.IdempotencyRecord
	for rows.Next() {
		var r model.IdempotencyRecord
		if err := rows.Scan(
			&r.ID, &r.Scope, &r.IdempotencyKeyHash, &r.RequestFingerprint,
			&r.Status, &r.LockedUntil, &r.ResponseStatus, &r.ResponseBody,
			&r.ErrorReason, &r.ExpiresAt, &r.CreatedAt, &r.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan idempotency record: %w", err)
		}
		records = append(records, r)
	}
	return records, rows.Err()
}
