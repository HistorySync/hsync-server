package service

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/historysync/hsync-server/pkg/model"
	"github.com/historysync/hsync-server/pkg/provider"
)

type memoryNotificationPreferenceStore struct {
	prefs map[uuid.UUID]model.NotificationPreferences
}

func (s *memoryNotificationPreferenceStore) GetByUserID(_ context.Context, userID uuid.UUID) (*model.NotificationPreferences, error) {
	if s.prefs == nil {
		s.prefs = map[uuid.UUID]model.NotificationPreferences{}
	}
	if prefs, ok := s.prefs[userID]; ok {
		return &prefs, nil
	}
	defaults := model.DefaultNotificationPreferences(userID)
	return &defaults, nil
}

func (s *memoryNotificationPreferenceStore) Upsert(_ context.Context, prefs *model.NotificationPreferences) error {
	if s.prefs == nil {
		s.prefs = map[uuid.UUID]model.NotificationPreferences{}
	}
	s.prefs[prefs.UserID] = *prefs
	return nil
}

type notificationUserMemoryStore struct {
	users map[uuid.UUID]*model.User
}

func (s *notificationUserMemoryStore) GetByID(_ context.Context, id uuid.UUID) (*model.User, error) {
	return s.users[id], nil
}

type failingWebhookProvider struct {
	err error
}

func (p failingWebhookProvider) DeliveryEnabled() bool { return true }

func (p failingWebhookProvider) Send(context.Context, string, provider.WebhookNotification) error {
	return p.err
}

func TestUpdateNotificationPreferences(t *testing.T) {
	userID := uuid.New()
	store := &memoryNotificationPreferenceStore{}
	svc := NewNotificationServiceWithStores(nil, store, provider.NewLogNotifier(), nil, NotificationConfig{})

	enableWebhook := true
	_, err := svc.UpdatePreferences(context.Background(), userID, NotificationPreferenceUpdate{
		SecurityWebhook: &enableWebhook,
	})
	if !errors.Is(err, ErrWebhookURLRequired) {
		t.Fatalf("UpdatePreferences without URL error = %v, want ErrWebhookURLRequired", err)
	}

	badURL := "ftp://example.com/hook"
	_, err = svc.UpdatePreferences(context.Background(), userID, NotificationPreferenceUpdate{
		WebhookURL: &badURL,
	})
	if !errors.Is(err, ErrInvalidWebhookURL) {
		t.Fatalf("UpdatePreferences invalid URL error = %v, want ErrInvalidWebhookURL", err)
	}

	goodURL := "https://example.com/hook?token=secret"
	prefs, err := svc.UpdatePreferences(context.Background(), userID, NotificationPreferenceUpdate{
		SecurityWebhook: &enableWebhook,
		WebhookURL:      &goodURL,
	})
	if err != nil {
		t.Fatalf("UpdatePreferences valid URL: %v", err)
	}
	if !prefs.SecurityWebhook || prefs.WebhookURL != goodURL {
		t.Fatalf("saved prefs = %+v, want security webhook with URL", prefs)
	}
	if !prefs.SecurityEmail || !prefs.BillingEmail {
		t.Fatalf("default email prefs not preserved: %+v", prefs)
	}
}

func TestSendNotificationWebhookFailureModes(t *testing.T) {
	userID := uuid.New()
	store := &memoryNotificationPreferenceStore{
		prefs: map[uuid.UUID]model.NotificationPreferences{
			userID: {
				UserID:          userID,
				SecurityEmail:   false,
				SecurityWebhook: true,
				WebhookURL:      "https://example.com/hook?token=secret",
			},
		},
	}
	users := &notificationUserMemoryStore{
		users: map[uuid.UUID]*model.User{
			userID: {ID: userID, Email: "user@example.com", DisplayName: "User"},
		},
	}
	webhookErr := errors.New("webhook request returned status 500")
	svc := NewNotificationServiceWithStores(users, store, provider.NewLogNotifier(), failingWebhookProvider{err: webhookErr}, NotificationConfig{
		Enabled: true,
	})

	err := svc.SendNotification(context.Background(), NotificationInput{
		UserID:   userID,
		Category: NotificationCategorySecurity,
		Type:     "security.login",
		Subject:  "New login",
		Message:  "A login was detected.",
	})
	if err != nil {
		t.Fatalf("best-effort SendNotification error = %v, want nil", err)
	}

	err = svc.SendNotification(context.Background(), NotificationInput{
		UserID:          userID,
		Category:        NotificationCategorySecurity,
		Type:            "security.login",
		Subject:         "New login",
		Message:         "A login was detected.",
		RequireDelivery: true,
	})
	if err == nil || !errors.Is(err, webhookErr) {
		t.Fatalf("required SendNotification error = %v, want webhook error", err)
	}
}
