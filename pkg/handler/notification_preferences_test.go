package handler

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/google/uuid"

	"github.com/historysync/hsync-server/pkg/auth"
	"github.com/historysync/hsync-server/pkg/model"
	"github.com/historysync/hsync-server/pkg/provider"
	"github.com/historysync/hsync-server/pkg/service"
)

type handlerNotificationPreferenceStore struct {
	prefs map[uuid.UUID]model.NotificationPreferences
}

func (s *handlerNotificationPreferenceStore) GetByUserID(_ context.Context, userID uuid.UUID) (*model.NotificationPreferences, error) {
	if s.prefs == nil {
		s.prefs = map[uuid.UUID]model.NotificationPreferences{}
	}
	if prefs, ok := s.prefs[userID]; ok {
		return &prefs, nil
	}
	defaults := model.DefaultNotificationPreferences(userID)
	return &defaults, nil
}

func (s *handlerNotificationPreferenceStore) Upsert(_ context.Context, prefs *model.NotificationPreferences) error {
	if s.prefs == nil {
		s.prefs = map[uuid.UUID]model.NotificationPreferences{}
	}
	s.prefs[prefs.UserID] = *prefs
	return nil
}

type handlerNotificationUserStore struct {
	users map[uuid.UUID]*model.User
}

func (s *handlerNotificationUserStore) GetByID(_ context.Context, id uuid.UUID) (*model.User, error) {
	return s.users[id], nil
}

func newHandlerTestTokenManager(t *testing.T) *auth.TokenManager {
	t.Helper()
	seed := make([]byte, ed25519.SeedSize)
	for i := range seed {
		seed[i] = byte(i + 1)
	}
	tm, err := auth.NewTokenManager(base64.StdEncoding.EncodeToString(seed), auth.TokenConfig{
		AccessTTL:  15 * time.Minute,
		RefreshTTL: 30 * 24 * time.Hour,
	})
	if err != nil {
		t.Fatalf("NewTokenManager() error = %v", err)
	}
	return tm
}

func newNotificationPreferenceTestApp(t *testing.T, userID uuid.UUID, store *handlerNotificationPreferenceStore) (*fiber.App, string) {
	t.Helper()
	tm := newHandlerTestTokenManager(t)
	token, err := tm.IssueAccessToken(userID, string(model.TierFree))
	if err != nil {
		t.Fatalf("IssueAccessToken: %v", err)
	}
	users := &handlerNotificationUserStore{
		users: map[uuid.UUID]*model.User{
			userID: {ID: userID, Email: "user@example.com", DisplayName: "User", Tier: model.TierFree, Status: model.StatusActive},
		},
	}
	notif := service.NewNotificationServiceWithStores(users, store, provider.NewLogNotifier(), nil, service.NotificationConfig{})
	h := New(Deps{
		Services: &service.Services{
			Notification: notif,
		},
		TokenManager: tm,
	})
	app := fiber.New(fiber.Config{ErrorHandler: h.ErrorHandler})
	h.RegisterRoutes(app)
	return app, token
}

func TestGetNotificationPreferencesReturnsDefaults(t *testing.T) {
	userID := uuid.New()
	app, token := newNotificationPreferenceTestApp(t, userID, &handlerNotificationPreferenceStore{})

	req := httptest.NewRequest("GET", "/api/v1/me/notification-preferences", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("app.Test() error = %v", err)
	}
	if resp.StatusCode != fiber.StatusOK {
		t.Fatalf("status = %d, want %d", resp.StatusCode, fiber.StatusOK)
	}
	var prefs model.NotificationPreferences
	if err := json.NewDecoder(resp.Body).Decode(&prefs); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if prefs.UserID != userID || !prefs.SecurityEmail || !prefs.BillingEmail || prefs.SecurityWebhook || prefs.BillingWebhook {
		t.Fatalf("prefs = %+v, want defaults", prefs)
	}
}

func TestUpdateNotificationPreferencesRejectsInvalidWebhookURL(t *testing.T) {
	userID := uuid.New()
	app, token := newNotificationPreferenceTestApp(t, userID, &handlerNotificationPreferenceStore{})

	req := httptest.NewRequest("PUT", "/api/v1/me/notification-preferences", bytes.NewBufferString(`{"security_webhook":true,"webhook_url":"ftp://example.com/hook"}`))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("app.Test() error = %v", err)
	}
	if resp.StatusCode != fiber.StatusBadRequest {
		t.Fatalf("status = %d, want %d", resp.StatusCode, fiber.StatusBadRequest)
	}
}

func TestUpdateNotificationPreferencesSavesValidPayload(t *testing.T) {
	userID := uuid.New()
	store := &handlerNotificationPreferenceStore{}
	app, token := newNotificationPreferenceTestApp(t, userID, store)

	req := httptest.NewRequest("PUT", "/api/v1/me/notification-preferences", bytes.NewBufferString(`{"security_webhook":true,"billing_email":false,"webhook_url":"https://example.com/hook?token=secret"}`))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("app.Test() error = %v", err)
	}
	if resp.StatusCode != fiber.StatusOK {
		t.Fatalf("status = %d, want %d", resp.StatusCode, fiber.StatusOK)
	}
	saved := store.prefs[userID]
	if !saved.SecurityWebhook || saved.BillingEmail || saved.WebhookURL == "" {
		t.Fatalf("saved prefs = %+v", saved)
	}
}
