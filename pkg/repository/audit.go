package repository

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/historysync/hsync-server/pkg/model"
)

const (
	defaultAuditListLimit = int32(50)
	maxAuditListLimit     = int32(200)
	maxAuditTimelineLimit = int32(1000)
)

type AuditRepo struct {
	pool *pgxpool.Pool
}

func (r *AuditRepo) Create(ctx context.Context, event *model.AuditLog) error {
	if event == nil {
		return fmt.Errorf("audit event is nil")
	}
	metadata, err := encodeAuditMetadata(event.Metadata)
	if err != nil {
		return err
	}
	const q = `
		INSERT INTO audit_logs (actor_user_id, event_type, target_type, target_id, ip, user_agent, metadata)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		RETURNING id, created_at`
	return r.pool.QueryRow(ctx, q,
		event.ActorUserID,
		string(event.EventType),
		event.TargetType,
		event.TargetID,
		event.IP,
		event.UserAgent,
		metadata,
	).Scan(&event.ID, &event.CreatedAt)
}

func (r *AuditRepo) List(ctx context.Context, filter model.AuditListFilter) ([]model.AuditLog, error) {
	filter = normalizeAuditListFilter(filter)

	var args []any
	var where []string
	addArg := func(v any) string {
		args = append(args, v)
		return fmt.Sprintf("$%d", len(args))
	}
	if filter.ActorUserID != nil {
		where = append(where, "actor_user_id = "+addArg(*filter.ActorUserID))
	}
	if filter.EventType != "" {
		where = append(where, "event_type = "+addArg(string(filter.EventType)))
	}
	if filter.TargetType != "" {
		where = append(where, "target_type = "+addArg(filter.TargetType))
	}
	if filter.TargetID != "" {
		where = append(where, "target_id = "+addArg(filter.TargetID))
	}

	var b strings.Builder
	b.WriteString(`
		SELECT id, actor_user_id, event_type, target_type, target_id, ip, user_agent, metadata, created_at
		FROM audit_logs`)
	if len(where) > 0 {
		b.WriteString(" WHERE ")
		b.WriteString(strings.Join(where, " AND "))
	}
	b.WriteString(" ORDER BY created_at DESC, id DESC LIMIT ")
	b.WriteString(addArg(filter.Limit))
	b.WriteString(" OFFSET ")
	b.WriteString(addArg(filter.Offset))

	rows, err := r.pool.Query(ctx, b.String(), args...)
	if err != nil {
		return nil, fmt.Errorf("list audit logs: %w", err)
	}
	defer rows.Close()

	return scanAuditLogs(rows)
}

func (r *AuditRepo) ListTimeline(ctx context.Context, filter model.AuditListFilter) ([]model.AuditLog, error) {
	filter = normalizeAuditTimelineFilter(filter)

	var args []any
	var where []string
	addArg := func(v any) string {
		args = append(args, v)
		return fmt.Sprintf("$%d", len(args))
	}
	if filter.ActorUserID != nil && filter.Email != "" {
		userArg := addArg(*filter.ActorUserID)
		targetUserArg := addArg(filter.ActorUserID.String())
		emailArg := addArg(strings.ToLower(strings.TrimSpace(filter.Email)))
		where = append(where, "(actor_user_id = "+userArg+" OR (target_type = 'user' AND target_id = "+targetUserArg+") OR lower(coalesce(metadata->>'email', '')) = "+emailArg+")")
	} else if filter.ActorUserID != nil {
		userArg := addArg(*filter.ActorUserID)
		targetUserArg := addArg(filter.ActorUserID.String())
		where = append(where, "(actor_user_id = "+userArg+" OR (target_type = 'user' AND target_id = "+targetUserArg+"))")
	} else if filter.Email != "" {
		where = append(where, "lower(coalesce(metadata->>'email', '')) = "+addArg(strings.ToLower(strings.TrimSpace(filter.Email))))
	}
	if filter.EventType != "" {
		where = append(where, "event_type = "+addArg(string(filter.EventType)))
	}
	if !filter.Since.IsZero() {
		where = append(where, "created_at >= "+addArg(filter.Since))
	}
	if !filter.Until.IsZero() {
		where = append(where, "created_at <= "+addArg(filter.Until))
	}

	var b strings.Builder
	b.WriteString(`
		SELECT id, actor_user_id, event_type, target_type, target_id, ip, user_agent, metadata, created_at
		FROM audit_logs`)
	if len(where) > 0 {
		b.WriteString(" WHERE ")
		b.WriteString(strings.Join(where, " AND "))
	}
	b.WriteString(" ORDER BY created_at DESC, id DESC LIMIT ")
	b.WriteString(addArg(filter.Limit))
	b.WriteString(" OFFSET ")
	b.WriteString(addArg(filter.Offset))

	rows, err := r.pool.Query(ctx, b.String(), args...)
	if err != nil {
		return nil, fmt.Errorf("list timeline audit logs: %w", err)
	}
	defer rows.Close()

	return scanAuditLogs(rows)
}

func (r *AuditRepo) SecurityEventCounts(ctx context.Context, since24h, since7d, until time.Time) ([]model.SecurityEventWindowCount, error) {
	const q = `
		SELECT event_type,
		       COUNT(*) FILTER (WHERE created_at >= $2),
		       COUNT(*)
		FROM audit_logs
		WHERE created_at >= $1 AND created_at < $3
		GROUP BY event_type
		ORDER BY event_type`
	rows, err := r.pool.Query(ctx, q, since7d, since24h, until)
	if err != nil {
		return nil, fmt.Errorf("count security audit events: %w", err)
	}
	defer rows.Close()

	var counts []model.SecurityEventWindowCount
	for rows.Next() {
		var count model.SecurityEventWindowCount
		if err := rows.Scan(&count.EventType, &count.Last24h, &count.Last7d); err != nil {
			return nil, fmt.Errorf("scan security audit event count: %w", err)
		}
		counts = append(counts, count)
	}
	return counts, rows.Err()
}

func (r *AuditRepo) ListVisibleByUser(ctx context.Context, userID uuid.UUID, limit int32) ([]model.AuditLog, error) {
	if limit <= 0 || limit > maxAuditListLimit {
		limit = defaultAuditListLimit
	}
	const q = `
		SELECT id, actor_user_id, event_type, target_type, target_id, ip, user_agent, metadata, created_at
		FROM audit_logs
		WHERE actor_user_id = $1 OR (target_type = 'user' AND target_id = $2)
		ORDER BY created_at DESC, id DESC
		LIMIT $3`
	rows, err := r.pool.Query(ctx, q, userID, userID.String(), limit)
	if err != nil {
		return nil, fmt.Errorf("list visible audit logs: %w", err)
	}
	defer rows.Close()

	return scanAuditLogs(rows)
}

func normalizeAuditListFilter(filter model.AuditListFilter) model.AuditListFilter {
	if filter.Limit <= 0 || filter.Limit > maxAuditListLimit {
		filter.Limit = defaultAuditListLimit
	}
	if filter.Offset < 0 {
		filter.Offset = 0
	}
	return filter
}

func normalizeAuditTimelineFilter(filter model.AuditListFilter) model.AuditListFilter {
	if filter.Limit <= 0 || filter.Limit > maxAuditTimelineLimit {
		filter.Limit = maxAuditTimelineLimit
	}
	if filter.Offset < 0 {
		filter.Offset = 0
	}
	filter.Email = strings.ToLower(strings.TrimSpace(filter.Email))
	return filter
}

func encodeAuditMetadata(metadata map[string]any) ([]byte, error) {
	if metadata == nil {
		metadata = map[string]any{}
	}
	data, err := json.Marshal(metadata)
	if err != nil {
		return nil, fmt.Errorf("encode audit metadata: %w", err)
	}
	return data, nil
}

func scanAuditLogs(rows pgx.Rows) ([]model.AuditLog, error) {
	var logs []model.AuditLog
	for rows.Next() {
		var log model.AuditLog
		var actor pgtype.UUID
		var metadata []byte
		if err := rows.Scan(
			&log.ID,
			&actor,
			&log.EventType,
			&log.TargetType,
			&log.TargetID,
			&log.IP,
			&log.UserAgent,
			&metadata,
			&log.CreatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan audit log: %w", err)
		}
		if actor.Valid {
			id, err := uuid.FromBytes(actor.Bytes[:])
			if err != nil {
				return nil, fmt.Errorf("scan audit actor: %w", err)
			}
			log.ActorUserID = &id
		}
		if len(metadata) > 0 {
			if err := json.Unmarshal(metadata, &log.Metadata); err != nil {
				return nil, fmt.Errorf("decode audit metadata: %w", err)
			}
		}
		if log.Metadata == nil {
			log.Metadata = map[string]any{}
		}
		logs = append(logs, log)
	}
	return logs, rows.Err()
}
