package repository

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/historysync/hsync-server/pkg/model"
)

const accountErasureJobPageSize = int32(50)

// AccountErasureJobRepo persists retention-gated account erasure jobs.
type AccountErasureJobRepo struct {
	pool *pgxpool.Pool
}

func (r *AccountErasureJobRepo) Create(ctx context.Context, job *model.AccountErasureJob) error {
	if r == nil || r.pool == nil {
		return fmt.Errorf("account erasure job repository is not configured")
	}
	if job == nil {
		return fmt.Errorf("account erasure job is nil")
	}
	if job.RequestedAt.IsZero() {
		job.RequestedAt = time.Now().UTC()
	}
	if job.EligibleAt.IsZero() {
		job.EligibleAt = job.RequestedAt
	}
	if job.Status == "" {
		job.Status = model.AccountErasureJobStatusPending
	}
	if len(job.Summary) == 0 {
		job.Summary = json.RawMessage(`{}`)
	}
	const q = `
		INSERT INTO account_erasure_jobs (user_id, requested_at, eligible_at, status, summary, last_error)
		VALUES ($1, $2, $3, $4, $5, $6)
		RETURNING id, created_at, updated_at`
	return r.pool.QueryRow(ctx, q,
		job.UserID, job.RequestedAt, job.EligibleAt, string(job.Status), job.Summary, job.LastError,
	).Scan(&job.ID, &job.CreatedAt, &job.UpdatedAt)
}

func (r *AccountErasureJobRepo) ListEligible(ctx context.Context, now time.Time, limit int32) ([]model.AccountErasureJob, error) {
	if r == nil || r.pool == nil {
		return []model.AccountErasureJob{}, nil
	}
	if limit <= 0 || limit > accountErasureJobPageSize {
		limit = accountErasureJobPageSize
	}
	const q = `
		SELECT id, user_id, requested_at, eligible_at, status, summary, last_error,
		       started_at, finished_at, created_at, updated_at
		FROM account_erasure_jobs
		WHERE status IN ('pending','failed') AND eligible_at <= $1
		ORDER BY eligible_at, requested_at
		LIMIT $2`
	rows, err := r.pool.Query(ctx, q, now, limit)
	if err != nil {
		return nil, fmt.Errorf("list eligible erasure jobs: %w", err)
	}
	defer rows.Close()
	return scanAccountErasureJobs(rows)
}

func (r *AccountErasureJobRepo) ListByUser(ctx context.Context, userID uuid.UUID, limit int32) ([]model.AccountErasureJob, error) {
	if r == nil || r.pool == nil {
		return []model.AccountErasureJob{}, nil
	}
	if limit <= 0 || limit > accountErasureJobPageSize {
		limit = accountErasureJobPageSize
	}
	const q = `
		SELECT id, user_id, requested_at, eligible_at, status, summary, last_error,
		       started_at, finished_at, created_at, updated_at
		FROM account_erasure_jobs
		WHERE user_id = $1
		ORDER BY requested_at DESC, id DESC
		LIMIT $2`
	rows, err := r.pool.Query(ctx, q, userID, limit)
	if err != nil {
		return nil, fmt.Errorf("list erasure jobs by user: %w", err)
	}
	defer rows.Close()
	return scanAccountErasureJobs(rows)
}

func (r *AccountErasureJobRepo) MarkRunning(ctx context.Context, id uuid.UUID, now time.Time) (bool, error) {
	if r == nil || r.pool == nil {
		return false, fmt.Errorf("account erasure job repository is not configured")
	}
	const q = `
		UPDATE account_erasure_jobs
		SET status = 'running', started_at = $2, finished_at = NULL, last_error = ''
		WHERE id = $1 AND status IN ('pending','failed')
		      AND eligible_at <= $2
		RETURNING id`
	var scanned uuid.UUID
	err := r.pool.QueryRow(ctx, q, id, now).Scan(&scanned)
	if err == pgx.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("mark erasure job running: %w", err)
	}
	return true, nil
}

func (r *AccountErasureJobRepo) MarkCompleted(ctx context.Context, id uuid.UUID, summary json.RawMessage, now time.Time) error {
	if r == nil || r.pool == nil {
		return fmt.Errorf("account erasure job repository is not configured")
	}
	if len(summary) == 0 {
		summary = json.RawMessage(`{}`)
	}
	const q = `
		UPDATE account_erasure_jobs
		SET status = 'completed', summary = $2, last_error = '', finished_at = $3
		WHERE id = $1`
	_, err := r.pool.Exec(ctx, q, id, summary, now)
	if err != nil {
		return fmt.Errorf("mark erasure job completed: %w", err)
	}
	return nil
}

func (r *AccountErasureJobRepo) UpdateSummary(ctx context.Context, id uuid.UUID, summary json.RawMessage) error {
	if r == nil || r.pool == nil {
		return fmt.Errorf("account erasure job repository is not configured")
	}
	if len(summary) == 0 {
		summary = json.RawMessage(`{}`)
	}
	const q = `UPDATE account_erasure_jobs SET summary = $2 WHERE id = $1`
	_, err := r.pool.Exec(ctx, q, id, summary)
	if err != nil {
		return fmt.Errorf("update erasure job summary: %w", err)
	}
	return nil
}

func (r *AccountErasureJobRepo) MarkFailed(ctx context.Context, id uuid.UUID, lastError string, now time.Time) error {
	if r == nil || r.pool == nil {
		return fmt.Errorf("account erasure job repository is not configured")
	}
	const q = `
		UPDATE account_erasure_jobs
		SET status = 'failed', last_error = $2, finished_at = $3
		WHERE id = $1`
	_, err := r.pool.Exec(ctx, q, id, lastError, now)
	if err != nil {
		return fmt.Errorf("mark erasure job failed: %w", err)
	}
	return nil
}

func scanAccountErasureJobs(rows pgx.Rows) ([]model.AccountErasureJob, error) {
	jobs := []model.AccountErasureJob{}
	for rows.Next() {
		var job model.AccountErasureJob
		var startedAt pgtype.Timestamptz
		var finishedAt pgtype.Timestamptz
		if err := rows.Scan(
			&job.ID,
			&job.UserID,
			&job.RequestedAt,
			&job.EligibleAt,
			&job.Status,
			&job.Summary,
			&job.LastError,
			&startedAt,
			&finishedAt,
			&job.CreatedAt,
			&job.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan erasure job: %w", err)
		}
		if startedAt.Valid {
			t := startedAt.Time
			job.StartedAt = &t
		}
		if finishedAt.Valid {
			t := finishedAt.Time
			job.FinishedAt = &t
		}
		jobs = append(jobs, job)
	}
	return jobs, rows.Err()
}
