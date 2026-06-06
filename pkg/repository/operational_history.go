package repository

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

type operationalHistoryDB interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// OperationalHistoryRepo archives and purges high-volume operational history.
type OperationalHistoryRepo struct {
	db operationalHistoryDB
}

func NewOperationalHistoryRepo(pool *pgxpool.Pool) *OperationalHistoryRepo {
	return &OperationalHistoryRepo{db: pool}
}

// OperationalHistoryRetentionCounts summarizes one retention pass. Archive
// counts are hot rows moved to archive tables; purged counts are archive rows
// removed after their total retention window expires.
type OperationalHistoryRetentionCounts struct {
	ArchivedAuditLogs              int64 `json:"archived_audit_logs"`
	ArchivedOpsCheckRuns           int64 `json:"archived_ops_check_runs"`
	ArchivedNotificationOutboxRows int64 `json:"archived_notification_outbox_rows"`
	PurgedAuditLogArchives         int64 `json:"purged_audit_log_archives"`
	PurgedOpsCheckRunArchives      int64 `json:"purged_ops_check_run_archives"`
	PurgedNotificationArchives     int64 `json:"purged_notification_archives"`
}

func (r *OperationalHistoryRepo) ReportRetention(ctx context.Context, hotCutoff, archiveCutoff time.Time) (OperationalHistoryRetentionCounts, error) {
	if r == nil || r.db == nil {
		return OperationalHistoryRetentionCounts{}, nil
	}
	var counts OperationalHistoryRetentionCounts
	const q = `
		SELECT
			(SELECT COUNT(*) FROM audit_logs WHERE created_at < $1),
			(SELECT COUNT(*) FROM ops_check_runs WHERE started_at < $1),
			(SELECT COUNT(*) FROM notification_outbox WHERE status IN ('sent','discarded') AND updated_at < $1),
			(SELECT COUNT(*) FROM audit_logs_archive WHERE created_at < $2),
			(SELECT COUNT(*) FROM ops_check_runs_archive WHERE started_at < $2),
			(SELECT COUNT(*) FROM notification_outbox_archive WHERE updated_at < $2)`
	if err := r.db.QueryRow(ctx, q, hotCutoff, archiveCutoff).Scan(
		&counts.ArchivedAuditLogs,
		&counts.ArchivedOpsCheckRuns,
		&counts.ArchivedNotificationOutboxRows,
		&counts.PurgedAuditLogArchives,
		&counts.PurgedOpsCheckRunArchives,
		&counts.PurgedNotificationArchives,
	); err != nil {
		return OperationalHistoryRetentionCounts{}, fmt.Errorf("report operational history retention: %w", err)
	}
	return counts, nil
}

func (r *OperationalHistoryRepo) ApplyRetention(ctx context.Context, hotCutoff, archiveCutoff time.Time) (OperationalHistoryRetentionCounts, error) {
	if r == nil || r.db == nil {
		return OperationalHistoryRetentionCounts{}, nil
	}
	var counts OperationalHistoryRetentionCounts
	var err error
	counts.ArchivedAuditLogs, err = execRows(ctx, r.db, `
		WITH moved AS (
			DELETE FROM audit_logs
			WHERE created_at < $1
			RETURNING *
		)
		INSERT INTO audit_logs_archive (
			id, actor_user_id, event_type, target_type, target_id, ip, user_agent, metadata, created_at
		)
		SELECT id, actor_user_id, event_type, target_type, target_id, ip, user_agent, metadata, created_at
		FROM moved
		ON CONFLICT (id) DO NOTHING`, hotCutoff)
	if err != nil {
		return counts, fmt.Errorf("archive audit logs: %w", err)
	}
	counts.ArchivedOpsCheckRuns, err = execRows(ctx, r.db, `
		WITH moved AS (
			DELETE FROM ops_check_runs
			WHERE started_at < $1
			RETURNING *
		)
		INSERT INTO ops_check_runs_archive (
			id, run_type, overall_status, started_at, finished_at, duration_millis,
			summarized_findings, artifact_counts, report_json, created_at
		)
		SELECT id, run_type, overall_status, started_at, finished_at, duration_millis,
		       summarized_findings, artifact_counts, report_json, created_at
		FROM moved
		ON CONFLICT (id) DO NOTHING`, hotCutoff)
	if err != nil {
		return counts, fmt.Errorf("archive ops check runs: %w", err)
	}
	counts.ArchivedNotificationOutboxRows, err = execRows(ctx, r.db, `
		WITH moved AS (
			DELETE FROM notification_outbox
			WHERE status IN ('sent','discarded') AND updated_at < $1
			RETURNING *
		)
		INSERT INTO notification_outbox_archive (
			id, user_id, channel, category, type, payload_json, status, attempt_count,
			next_retry_at, last_error, created_at, updated_at, sent_at
		)
		SELECT id, user_id, channel, category, type, payload_json, status, attempt_count,
		       next_retry_at, last_error, created_at, updated_at, sent_at
		FROM moved
		ON CONFLICT (id) DO NOTHING`, hotCutoff)
	if err != nil {
		return counts, fmt.Errorf("archive notification outbox rows: %w", err)
	}
	counts.PurgedAuditLogArchives, err = execRows(ctx, r.db,
		`DELETE FROM audit_logs_archive WHERE created_at < $1`, archiveCutoff)
	if err != nil {
		return counts, fmt.Errorf("purge archived audit logs: %w", err)
	}
	counts.PurgedOpsCheckRunArchives, err = execRows(ctx, r.db,
		`DELETE FROM ops_check_runs_archive WHERE started_at < $1`, archiveCutoff)
	if err != nil {
		return counts, fmt.Errorf("purge archived ops check runs: %w", err)
	}
	counts.PurgedNotificationArchives, err = execRows(ctx, r.db,
		`DELETE FROM notification_outbox_archive WHERE updated_at < $1`, archiveCutoff)
	if err != nil {
		return counts, fmt.Errorf("purge archived notification outbox rows: %w", err)
	}
	return counts, nil
}

func execRows(ctx context.Context, db operationalHistoryDB, sql string, args ...any) (int64, error) {
	tag, err := db.Exec(ctx, sql, args...)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}
