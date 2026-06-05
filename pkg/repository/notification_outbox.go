package repository

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/historysync/hsync-server/pkg/model"
)

const (
	defaultNotificationOutboxClaimLimit = int32(50)
	maxNotificationOutboxClaimLimit     = int32(200)
	defaultNotificationFailureLimit     = int32(50)
	maxNotificationFailureLimit         = int32(200)
	maxNotificationErrorLength          = 512
)

type notificationOutboxDB interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

type NotificationOutboxRepo struct {
	db notificationOutboxDB
}

func NewNotificationOutboxRepo(db notificationOutboxDB) *NotificationOutboxRepo {
	return &NotificationOutboxRepo{db: db}
}

func (r *NotificationOutboxRepo) Enqueue(ctx context.Context, item *model.NotificationOutbox) error {
	if r == nil || r.db == nil {
		return fmt.Errorf("notification outbox repository is not configured")
	}
	if item == nil {
		return fmt.Errorf("notification outbox item is required")
	}
	if item.NextRetryAt.IsZero() {
		item.NextRetryAt = time.Now().UTC()
	}
	if len(item.PayloadJSON) == 0 {
		item.PayloadJSON = json.RawMessage(`{}`)
	}
	const q = `
		INSERT INTO notification_outbox (
			user_id, channel, category, type, payload_json, status, next_retry_at
		) VALUES ($1, $2, $3, $4, $5, 'pending', $6)
		RETURNING id, status, attempt_count, last_error, created_at, updated_at`
	if err := r.db.QueryRow(ctx, q,
		item.UserID, string(item.Channel), item.Category, item.Type, item.PayloadJSON, item.NextRetryAt,
	).Scan(&item.ID, &item.Status, &item.AttemptCount, &item.LastError, &item.CreatedAt, &item.UpdatedAt); err != nil {
		return fmt.Errorf("enqueue notification outbox: %w", err)
	}
	return nil
}

func (r *NotificationOutboxRepo) ClaimDue(ctx context.Context, now time.Time, limit int32) ([]model.NotificationOutbox, error) {
	if r == nil || r.db == nil {
		return []model.NotificationOutbox{}, nil
	}
	limit = normalizeNotificationLimit(limit, defaultNotificationOutboxClaimLimit, maxNotificationOutboxClaimLimit)
	const q = `
		WITH due AS (
			SELECT id
			FROM notification_outbox
			WHERE status = 'pending' AND next_retry_at <= $1
			ORDER BY next_retry_at, created_at
			LIMIT $2
			FOR UPDATE SKIP LOCKED
		)
		UPDATE notification_outbox n
		SET status = 'processing',
		    updated_at = now()
		FROM due
		WHERE n.id = due.id
		RETURNING n.id, n.user_id, n.channel, n.category, n.type, n.payload_json,
		          n.status, n.attempt_count, n.next_retry_at, n.last_error,
		          n.created_at, n.updated_at, n.sent_at`
	rows, err := r.db.Query(ctx, q, now, limit)
	if err != nil {
		return nil, fmt.Errorf("claim notification outbox: %w", err)
	}
	defer rows.Close()
	return scanNotificationOutboxRows(rows)
}

func (r *NotificationOutboxRepo) GetByID(ctx context.Context, id uuid.UUID) (*model.NotificationOutbox, error) {
	if r == nil || r.db == nil {
		return nil, nil
	}
	const q = `
		SELECT id, user_id, channel, category, type, payload_json,
		       status, attempt_count, next_retry_at, last_error,
		       created_at, updated_at, sent_at
		FROM notification_outbox
		WHERE id = $1`
	rows, err := r.db.Query(ctx, q, id)
	if err != nil {
		return nil, fmt.Errorf("get notification outbox: %w", err)
	}
	defer rows.Close()
	items, err := scanNotificationOutboxRows(rows)
	if err != nil {
		return nil, err
	}
	if len(items) == 0 {
		return nil, nil
	}
	return &items[0], nil
}

func (r *NotificationOutboxRepo) ClaimFailedByID(ctx context.Context, id uuid.UUID) (*model.NotificationOutbox, error) {
	if r == nil || r.db == nil {
		return nil, nil
	}
	const q = `
		WITH target AS (
			SELECT id
			FROM notification_outbox
			WHERE id = $1 AND status = 'failed'
			FOR UPDATE SKIP LOCKED
		)
		UPDATE notification_outbox n
		SET status = 'processing',
		    updated_at = now()
		FROM target
		WHERE n.id = target.id
		RETURNING n.id, n.user_id, n.channel, n.category, n.type, n.payload_json,
		          n.status, n.attempt_count, n.next_retry_at, n.last_error,
		          n.created_at, n.updated_at, n.sent_at`
	rows, err := r.db.Query(ctx, q, id)
	if err != nil {
		return nil, fmt.Errorf("claim failed notification outbox: %w", err)
	}
	defer rows.Close()
	items, err := scanNotificationOutboxRows(rows)
	if err != nil {
		return nil, err
	}
	if len(items) == 0 {
		return nil, nil
	}
	return &items[0], nil
}

func (r *NotificationOutboxRepo) ClaimFailed(ctx context.Context, limit int32) ([]model.NotificationOutbox, error) {
	if r == nil || r.db == nil {
		return []model.NotificationOutbox{}, nil
	}
	limit = normalizeNotificationLimit(limit, defaultNotificationFailureLimit, maxNotificationFailureLimit)
	const q = `
		WITH failed AS (
			SELECT id
			FROM notification_outbox
			WHERE status = 'failed'
			ORDER BY updated_at DESC, created_at
			LIMIT $1
			FOR UPDATE SKIP LOCKED
		)
		UPDATE notification_outbox n
		SET status = 'processing',
		    updated_at = now()
		FROM failed
		WHERE n.id = failed.id
		RETURNING n.id, n.user_id, n.channel, n.category, n.type, n.payload_json,
		          n.status, n.attempt_count, n.next_retry_at, n.last_error,
		          n.created_at, n.updated_at, n.sent_at`
	rows, err := r.db.Query(ctx, q, limit)
	if err != nil {
		return nil, fmt.Errorf("claim failed notification outbox batch: %w", err)
	}
	defer rows.Close()
	return scanNotificationOutboxRows(rows)
}

func (r *NotificationOutboxRepo) MarkSent(ctx context.Context, id uuid.UUID, sentAt time.Time) error {
	if r == nil || r.db == nil {
		return fmt.Errorf("notification outbox repository is not configured")
	}
	const q = `
		UPDATE notification_outbox
		SET status = 'sent',
		    sent_at = $2,
		    last_error = '',
		    updated_at = now()
		WHERE id = $1`
	if _, err := r.db.Exec(ctx, q, id, sentAt); err != nil {
		return fmt.Errorf("mark notification outbox sent: %w", err)
	}
	return nil
}

func (r *NotificationOutboxRepo) MarkRetry(ctx context.Context, id uuid.UUID, nextRetryAt time.Time, errText string) error {
	if r == nil || r.db == nil {
		return fmt.Errorf("notification outbox repository is not configured")
	}
	const q = `
		UPDATE notification_outbox
		SET status = 'pending',
		    attempt_count = attempt_count + 1,
		    next_retry_at = $2,
		    last_error = $3,
		    updated_at = now()
		WHERE id = $1`
	if _, err := r.db.Exec(ctx, q, id, nextRetryAt, truncateNotificationError(errText)); err != nil {
		return fmt.Errorf("mark notification outbox retry: %w", err)
	}
	return nil
}

func (r *NotificationOutboxRepo) MarkFailed(ctx context.Context, id uuid.UUID, errText string) error {
	if r == nil || r.db == nil {
		return fmt.Errorf("notification outbox repository is not configured")
	}
	const q = `
		UPDATE notification_outbox
		SET status = 'failed',
		    attempt_count = attempt_count + 1,
		    last_error = $2,
		    updated_at = now()
		WHERE id = $1`
	if _, err := r.db.Exec(ctx, q, id, truncateNotificationError(errText)); err != nil {
		return fmt.Errorf("mark notification outbox failed: %w", err)
	}
	return nil
}

func (r *NotificationOutboxRepo) RequeueFailed(ctx context.Context, id uuid.UUID, nextRetryAt time.Time) (bool, error) {
	if r == nil || r.db == nil {
		return false, fmt.Errorf("notification outbox repository is not configured")
	}
	const q = `
		UPDATE notification_outbox
		SET status = 'pending',
		    attempt_count = 0,
		    next_retry_at = $2,
		    last_error = '',
		    sent_at = NULL,
		    updated_at = now()
		WHERE id = $1 AND status = 'failed'`
	tag, err := r.db.Exec(ctx, q, id, nextRetryAt)
	if err != nil {
		return false, fmt.Errorf("requeue notification outbox failure: %w", err)
	}
	return tag.RowsAffected() > 0, nil
}

func (r *NotificationOutboxRepo) MarkDiscarded(ctx context.Context, id uuid.UUID) (bool, error) {
	if r == nil || r.db == nil {
		return false, fmt.Errorf("notification outbox repository is not configured")
	}
	const q = `
		UPDATE notification_outbox
		SET status = 'discarded',
		    updated_at = now()
		WHERE id = $1 AND status = 'failed'`
	tag, err := r.db.Exec(ctx, q, id)
	if err != nil {
		return false, fmt.Errorf("mark notification outbox discarded: %w", err)
	}
	return tag.RowsAffected() > 0, nil
}

func (r *NotificationOutboxRepo) ListFailures(ctx context.Context, limit, offset int32) ([]model.NotificationOutbox, error) {
	if r == nil || r.db == nil {
		return []model.NotificationOutbox{}, nil
	}
	limit = normalizeNotificationLimit(limit, defaultNotificationFailureLimit, maxNotificationFailureLimit)
	if offset < 0 {
		offset = 0
	}
	const q = `
		SELECT id, user_id, channel, category, type, payload_json,
		       status, attempt_count, next_retry_at, last_error,
		       created_at, updated_at, sent_at
		FROM notification_outbox
		WHERE status = 'failed'
		ORDER BY updated_at DESC
		LIMIT $1 OFFSET $2`
	rows, err := r.db.Query(ctx, q, limit, offset)
	if err != nil {
		return nil, fmt.Errorf("list failed notification outbox: %w", err)
	}
	defer rows.Close()
	return scanNotificationOutboxRows(rows)
}

func scanNotificationOutboxRows(rows pgx.Rows) ([]model.NotificationOutbox, error) {
	var items []model.NotificationOutbox
	for rows.Next() {
		var item model.NotificationOutbox
		var channel string
		var status string
		if err := rows.Scan(
			&item.ID, &item.UserID, &channel, &item.Category, &item.Type, &item.PayloadJSON,
			&status, &item.AttemptCount, &item.NextRetryAt, &item.LastError,
			&item.CreatedAt, &item.UpdatedAt, &item.SentAt,
		); err != nil {
			return nil, fmt.Errorf("scan notification outbox: %w", err)
		}
		item.Channel = model.NotificationChannel(channel)
		item.Status = model.NotificationOutboxStatus(status)
		items = append(items, item)
	}
	return items, rows.Err()
}

func normalizeNotificationLimit(limit, fallback, max int32) int32 {
	if limit <= 0 || limit > max {
		return fallback
	}
	return limit
}

func truncateNotificationError(errText string) string {
	errText = strings.TrimSpace(errText)
	if len(errText) <= maxNotificationErrorLength {
		return errText
	}
	return errText[:maxNotificationErrorLength]
}
