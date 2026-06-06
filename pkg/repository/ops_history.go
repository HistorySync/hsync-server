package repository

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/historysync/hsync-server/pkg/model"
)

const (
	defaultOpsHistoryLimit = int32(20)
	maxOpsHistoryLimit     = int32(100)
)

type opsHistoryDB interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

type OpsHistoryRepo struct {
	db opsHistoryDB
}

func NewOpsHistoryRepo(db opsHistoryDB) *OpsHistoryRepo {
	return &OpsHistoryRepo{db: db}
}

func (r *OpsHistoryRepo) Create(ctx context.Context, run *model.OpsCheckRun) error {
	if r == nil || r.db == nil {
		return fmt.Errorf("ops history repository is not configured")
	}
	if run == nil {
		return fmt.Errorf("ops check run is required")
	}
	if len(run.SummarizedFindings) == 0 {
		run.SummarizedFindings = json.RawMessage(`{}`)
	}
	if len(run.ArtifactCounts) == 0 {
		run.ArtifactCounts = json.RawMessage(`{}`)
	}
	if len(run.ReportJSON) == 0 {
		run.ReportJSON = json.RawMessage(`{}`)
	}
	const q = `
		INSERT INTO ops_check_runs (
			run_type, overall_status, started_at, finished_at, duration_millis,
			summarized_findings, artifact_counts, report_json
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		RETURNING id, created_at`
	if err := r.db.QueryRow(ctx, q,
		string(run.RunType), run.OverallStatus, run.StartedAt, run.FinishedAt, run.DurationMillis,
		run.SummarizedFindings, run.ArtifactCounts, run.ReportJSON,
	).Scan(&run.ID, &run.CreatedAt); err != nil {
		return fmt.Errorf("create ops check run: %w", err)
	}
	return nil
}

func (r *OpsHistoryRepo) ListRecent(ctx context.Context, limit int32) ([]model.OpsCheckRun, error) {
	if r == nil || r.db == nil {
		return []model.OpsCheckRun{}, nil
	}
	limit = normalizeOpsHistoryLimit(limit)
	const q = `
		SELECT id, run_type, overall_status, started_at, finished_at, duration_millis,
		       summarized_findings, artifact_counts, report_json, created_at
		FROM ops_check_runs
		ORDER BY started_at DESC
		LIMIT $1`
	rows, err := r.db.Query(ctx, q, limit)
	if err != nil {
		return nil, fmt.Errorf("list recent ops check runs: %w", err)
	}
	defer rows.Close()
	return scanOpsCheckRuns(rows)
}

func (r *OpsHistoryRepo) ListRecentFailures(ctx context.Context, limit int32) ([]model.OpsCheckRun, error) {
	if r == nil || r.db == nil {
		return []model.OpsCheckRun{}, nil
	}
	limit = normalizeOpsHistoryLimit(limit)
	const q = `
		SELECT id, run_type, overall_status, started_at, finished_at, duration_millis,
		       summarized_findings, artifact_counts, report_json, created_at
		FROM ops_check_runs
		WHERE overall_status <> 'ok'
		ORDER BY started_at DESC
		LIMIT $1`
	rows, err := r.db.Query(ctx, q, limit)
	if err != nil {
		return nil, fmt.Errorf("list failed ops check runs: %w", err)
	}
	defer rows.Close()
	return scanOpsCheckRuns(rows)
}

func scanOpsCheckRuns(rows pgx.Rows) ([]model.OpsCheckRun, error) {
	var runs []model.OpsCheckRun
	for rows.Next() {
		var run model.OpsCheckRun
		var runType string
		if err := rows.Scan(
			&run.ID, &runType, &run.OverallStatus, &run.StartedAt, &run.FinishedAt,
			&run.DurationMillis, &run.SummarizedFindings, &run.ArtifactCounts,
			&run.ReportJSON, &run.CreatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan ops check run: %w", err)
		}
		run.RunType = model.OpsRunType(runType)
		runs = append(runs, run)
	}
	return runs, rows.Err()
}

func normalizeOpsHistoryLimit(limit int32) int32 {
	if limit <= 0 || limit > maxOpsHistoryLimit {
		return defaultOpsHistoryLimit
	}
	return limit
}
