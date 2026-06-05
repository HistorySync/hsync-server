package repository

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/historysync/hsync-server/pkg/model"
)

type fakeNotificationPreferenceDB struct {
	row fakeNotificationPreferenceRow
}

func (db *fakeNotificationPreferenceDB) QueryRow(context.Context, string, ...any) pgx.Row {
	return db.row
}

func (db *fakeNotificationPreferenceDB) Exec(context.Context, string, ...any) (pgconn.CommandTag, error) {
	return pgconn.CommandTag{}, nil
}

type fakeNotificationPreferenceRow struct {
	values []any
	err    error
}

func (r fakeNotificationPreferenceRow) Scan(dest ...any) error {
	if r.err != nil {
		return r.err
	}
	for i, value := range r.values {
		switch d := dest[i].(type) {
		case *uuid.UUID:
			*d = value.(uuid.UUID)
		case *bool:
			*d = value.(bool)
		case *string:
			*d = value.(string)
		case *time.Time:
			*d = value.(time.Time)
		default:
			return errors.New("unsupported scan destination")
		}
	}
	return nil
}

func TestNotificationPreferenceRepoGetDefault(t *testing.T) {
	userID := uuid.New()
	repo := NewNotificationPreferenceRepo(&fakeNotificationPreferenceDB{
		row: fakeNotificationPreferenceRow{err: pgx.ErrNoRows},
	})

	prefs, err := repo.GetByUserID(context.Background(), userID)
	if err != nil {
		t.Fatalf("GetByUserID: %v", err)
	}
	if prefs.UserID != userID || !prefs.SecurityEmail || !prefs.BillingEmail || prefs.SecurityWebhook || prefs.BillingWebhook {
		t.Fatalf("default prefs = %+v", prefs)
	}
}

func TestNotificationPreferenceRepoUpsertScansTimestamps(t *testing.T) {
	now := time.Now().UTC()
	userID := uuid.New()
	repo := NewNotificationPreferenceRepo(&fakeNotificationPreferenceDB{
		row: fakeNotificationPreferenceRow{values: []any{now, now}},
	})
	prefs := &model.NotificationPreferences{
		UserID:          userID,
		SecurityEmail:   true,
		SecurityWebhook: true,
		BillingEmail:    false,
		BillingWebhook:  true,
		WebhookURL:      "https://example.com/hook",
	}

	if err := repo.Upsert(context.Background(), prefs); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	if !prefs.CreatedAt.Equal(now) || !prefs.UpdatedAt.Equal(now) {
		t.Fatalf("timestamps = %s/%s, want %s", prefs.CreatedAt, prefs.UpdatedAt, now)
	}
}
