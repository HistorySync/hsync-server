package repository

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/historysync/hsync-server/pkg/model"
)

type notificationPreferenceDB interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

// NotificationPreferenceRepo handles per-user notification preference storage.
type NotificationPreferenceRepo struct {
	db notificationPreferenceDB
}

func NewNotificationPreferenceRepo(db notificationPreferenceDB) *NotificationPreferenceRepo {
	return &NotificationPreferenceRepo{db: db}
}

// GetByUserID fetches a user's preferences. A missing row returns the model
// defaults so callers can treat preferences as always present.
func (r *NotificationPreferenceRepo) GetByUserID(ctx context.Context, userID uuid.UUID) (*model.NotificationPreferences, error) {
	if r == nil || r.db == nil {
		defaults := model.DefaultNotificationPreferences(userID)
		return &defaults, nil
	}
	const q = `
		SELECT user_id, security_email, security_webhook, billing_email, billing_webhook,
		       webhook_url, created_at, updated_at
		FROM user_notification_preferences
		WHERE user_id = $1`

	prefs := &model.NotificationPreferences{}
	err := r.db.QueryRow(ctx, q, userID).Scan(
		&prefs.UserID, &prefs.SecurityEmail, &prefs.SecurityWebhook,
		&prefs.BillingEmail, &prefs.BillingWebhook, &prefs.WebhookURL,
		&prefs.CreatedAt, &prefs.UpdatedAt,
	)
	if err == pgx.ErrNoRows {
		defaults := model.DefaultNotificationPreferences(userID)
		return &defaults, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get notification preferences: %w", err)
	}
	return prefs, nil
}

// Upsert saves the complete preference row for a user.
func (r *NotificationPreferenceRepo) Upsert(ctx context.Context, prefs *model.NotificationPreferences) error {
	if r == nil || r.db == nil {
		return fmt.Errorf("notification preference repository is not configured")
	}
	const q = `
		INSERT INTO user_notification_preferences (
			user_id, security_email, security_webhook, billing_email, billing_webhook, webhook_url
		) VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (user_id) DO UPDATE SET
			security_email = EXCLUDED.security_email,
			security_webhook = EXCLUDED.security_webhook,
			billing_email = EXCLUDED.billing_email,
			billing_webhook = EXCLUDED.billing_webhook,
			webhook_url = EXCLUDED.webhook_url,
			updated_at = now()
		RETURNING created_at, updated_at`

	if err := r.db.QueryRow(ctx, q,
		prefs.UserID, prefs.SecurityEmail, prefs.SecurityWebhook,
		prefs.BillingEmail, prefs.BillingWebhook, prefs.WebhookURL,
	).Scan(&prefs.CreatedAt, &prefs.UpdatedAt); err != nil {
		return fmt.Errorf("upsert notification preferences: %w", err)
	}
	return nil
}
