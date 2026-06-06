package repository

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

type operationalHistoryDB interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
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

type OperationalExportFilter struct {
	RecordType string
	Source     string
	Type       string
	Status     string
	From       time.Time
	To         time.Time
	Limit      int32
}

type OperationalExportRecord struct {
	RecordType string         `json:"record_type"`
	Source     string         `json:"source"`
	ID         string         `json:"id"`
	Timestamp  time.Time      `json:"timestamp"`
	Type       string         `json:"type"`
	Status     string         `json:"status,omitempty"`
	Actor      string         `json:"actor,omitempty"`
	Target     string         `json:"target,omitempty"`
	Details    map[string]any `json:"details"`
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

func (r *OperationalHistoryRepo) Export(ctx context.Context, filter OperationalExportFilter) ([]OperationalExportRecord, error) {
	if r == nil || r.db == nil {
		return []OperationalExportRecord{}, nil
	}
	filter = normalizeOperationalExportFilter(filter)
	var records []OperationalExportRecord
	sources := []string{filter.Source}
	if filter.Source == "all" {
		sources = []string{"hot", "archive"}
	}
	for _, source := range sources {
		var (
			batch []OperationalExportRecord
			err   error
		)
		switch filter.RecordType {
		case "audit_logs":
			batch, err = r.exportAuditLogs(ctx, source, filter)
		case "ops_history":
			batch, err = r.exportOpsHistory(ctx, source, filter)
		case "notification_outbox":
			batch, err = r.exportNotificationOutbox(ctx, source, filter)
		default:
			return nil, fmt.Errorf("unsupported operational export record type")
		}
		if err != nil {
			return nil, err
		}
		records = append(records, batch...)
	}
	sort.Slice(records, func(i, j int) bool {
		return records[i].Timestamp.After(records[j].Timestamp)
	})
	if len(records) > int(filter.Limit) {
		records = records[:filter.Limit]
	}
	return records, nil
}

func (r *OperationalHistoryRepo) exportAuditLogs(ctx context.Context, source string, filter OperationalExportFilter) ([]OperationalExportRecord, error) {
	table := sourceTable(source, "audit_logs", "audit_logs_archive")
	q, args := exportQuery("created_at", "event_type", "", filter, `
		SELECT id::text, created_at, event_type, COALESCE(actor_user_id::text, ''),
		       target_type, target_id, metadata::text
		FROM `+table)
	rows, err := r.db.Query(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("export audit logs: %w", err)
	}
	defer rows.Close()
	var out []OperationalExportRecord
	for rows.Next() {
		var id, typ, actor, targetType, targetID, details string
		var ts time.Time
		if err := rows.Scan(&id, &ts, &typ, &actor, &targetType, &targetID, &details); err != nil {
			return nil, fmt.Errorf("scan audit export: %w", err)
		}
		out = append(out, OperationalExportRecord{
			RecordType: "audit_logs",
			Source:     source,
			ID:         id,
			Timestamp:  ts,
			Type:       typ,
			Actor:      actor,
			Target:     joinTarget(targetType, targetID),
			Details:    decodeDetails(details),
		})
	}
	return out, rows.Err()
}

func (r *OperationalHistoryRepo) exportOpsHistory(ctx context.Context, source string, filter OperationalExportFilter) ([]OperationalExportRecord, error) {
	table := sourceTable(source, "ops_check_runs", "ops_check_runs_archive")
	q, args := exportQuery("started_at", "run_type", "overall_status", filter, `
		SELECT id::text, started_at, run_type, overall_status, report_json::text
		FROM `+table)
	rows, err := r.db.Query(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("export ops history: %w", err)
	}
	defer rows.Close()
	var out []OperationalExportRecord
	for rows.Next() {
		var id, typ, status, details string
		var ts time.Time
		if err := rows.Scan(&id, &ts, &typ, &status, &details); err != nil {
			return nil, fmt.Errorf("scan ops export: %w", err)
		}
		out = append(out, OperationalExportRecord{
			RecordType: "ops_history",
			Source:     source,
			ID:         id,
			Timestamp:  ts,
			Type:       typ,
			Status:     status,
			Details:    decodeDetails(details),
		})
	}
	return out, rows.Err()
}

func (r *OperationalHistoryRepo) exportNotificationOutbox(ctx context.Context, source string, filter OperationalExportFilter) ([]OperationalExportRecord, error) {
	table := sourceTable(source, "notification_outbox", "notification_outbox_archive")
	q, args := exportQuery("updated_at", "type", "status", filter, `
		SELECT id::text, updated_at, type, status, user_id::text, channel, category, payload_json::text
		FROM `+table)
	rows, err := r.db.Query(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("export notification outbox: %w", err)
	}
	defer rows.Close()
	var out []OperationalExportRecord
	for rows.Next() {
		var id, typ, status, userID, channel, category, details string
		var ts time.Time
		if err := rows.Scan(&id, &ts, &typ, &status, &userID, &channel, &category, &details); err != nil {
			return nil, fmt.Errorf("scan notification export: %w", err)
		}
		decoded := decodeDetails(details)
		decoded["channel"] = channel
		decoded["category"] = category
		out = append(out, OperationalExportRecord{
			RecordType: "notification_outbox",
			Source:     source,
			ID:         id,
			Timestamp:  ts,
			Type:       typ,
			Status:     status,
			Actor:      userID,
			Target:     userID,
			Details:    decoded,
		})
	}
	return out, rows.Err()
}

func normalizeOperationalExportFilter(filter OperationalExportFilter) OperationalExportFilter {
	filter.RecordType = strings.TrimSpace(filter.RecordType)
	filter.Source = strings.TrimSpace(filter.Source)
	if filter.Source == "" {
		filter.Source = "hot"
	}
	filter.Type = strings.TrimSpace(filter.Type)
	filter.Status = strings.TrimSpace(filter.Status)
	if filter.Limit <= 0 || filter.Limit > 5000 {
		filter.Limit = 1000
	}
	return filter
}

func sourceTable(source, hot, archive string) string {
	if source == "archive" {
		return archive
	}
	return hot
}

func exportQuery(timeColumn, typeColumn, statusColumn string, filter OperationalExportFilter, selectSQL string) (string, []any) {
	args := []any{}
	where := []string{"true"}
	addArg := func(v any) string {
		args = append(args, v)
		return fmt.Sprintf("$%d", len(args))
	}
	if !filter.From.IsZero() {
		where = append(where, timeColumn+" >= "+addArg(filter.From))
	}
	if !filter.To.IsZero() {
		where = append(where, timeColumn+" <= "+addArg(filter.To))
	}
	if filter.Type != "" {
		where = append(where, typeColumn+" = "+addArg(filter.Type))
	}
	if statusColumn != "" && filter.Status != "" {
		where = append(where, statusColumn+" = "+addArg(filter.Status))
	}
	args = append(args, filter.Limit)
	return selectSQL + " WHERE " + strings.Join(where, " AND ") +
		" ORDER BY " + timeColumn + " DESC, id DESC LIMIT " + fmt.Sprintf("$%d", len(args)), args
}

func decodeDetails(raw string) map[string]any {
	out := map[string]any{}
	if strings.TrimSpace(raw) == "" {
		return out
	}
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		out["raw"] = raw
	}
	return out
}

func joinTarget(targetType, targetID string) string {
	if targetType == "" {
		return targetID
	}
	if targetID == "" {
		return targetType
	}
	return targetType + ":" + targetID
}

func execRows(ctx context.Context, db operationalHistoryDB, sql string, args ...any) (int64, error) {
	tag, err := db.Exec(ctx, sql, args...)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}
