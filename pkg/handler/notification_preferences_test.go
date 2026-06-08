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

type handlerAccountDeviceStore struct {
	devices map[uuid.UUID][]model.Device
}

func (s *handlerAccountDeviceStore) ListByUser(_ context.Context, userID uuid.UUID) ([]model.Device, error) {
	return s.devices[userID], nil
}

type handlerAccountBundleStore struct {
	bundles map[uuid.UUID][]model.BundleMeta
}

func (s *handlerAccountBundleStore) ListAllByUser(_ context.Context, userID uuid.UUID) ([]model.BundleMeta, error) {
	return s.bundles[userID], nil
}

type handlerAccountSnapshotStore struct {
	snapshots map[uuid.UUID][]model.SnapshotMeta
}

func (s *handlerAccountSnapshotStore) ListAllByUser(_ context.Context, userID uuid.UUID) ([]model.SnapshotMeta, error) {
	return s.snapshots[userID], nil
}

type handlerAccountQuotaStore struct {
	usage map[uuid.UUID]*model.QuotaUsage
}

func (s *handlerAccountQuotaStore) GetUsage(_ context.Context, userID uuid.UUID) (*model.QuotaUsage, error) {
	return s.usage[userID], nil
}

type handlerAccountAuditStore struct {
	logs map[uuid.UUID][]model.AuditLog
}

func (s *handlerAccountAuditStore) ListVisibleByUser(_ context.Context, userID uuid.UUID, limit int32) ([]model.AuditLog, error) {
	logs := append([]model.AuditLog(nil), s.logs[userID]...)
	if len(logs) > int(limit) {
		logs = logs[:limit]
	}
	return logs, nil
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

func TestExportPrivacyMetadataReturnsSanitizedMetadata(t *testing.T) {
	userID := uuid.New()
	store := &handlerNotificationPreferenceStore{
		prefs: map[uuid.UUID]model.NotificationPreferences{
			userID: {
				UserID:          userID,
				SecurityEmail:   true,
				SecurityWebhook: true,
				BillingEmail:    false,
				BillingWebhook:  true,
				WebhookURL:      "https://example.com/hook?token=secret",
			},
		},
	}
	tm := newHandlerTestTokenManager(t)
	token, err := tm.IssueAccessToken(userID, string(model.TierPro))
	if err != nil {
		t.Fatalf("IssueAccessToken: %v", err)
	}
	users := &handlerNotificationUserStore{
		users: map[uuid.UUID]*model.User{
			userID: {
				ID:            userID,
				Email:         "user@example.com",
				DisplayName:   "User",
				Tier:          model.TierPro,
				Status:        model.StatusActive,
				EmailVerified: true,
				CreatedAt:     time.Unix(100, 0).UTC(),
				UpdatedAt:     time.Unix(200, 0).UTC(),
			},
		},
	}
	notif := service.NewNotificationServiceWithStores(users, store, provider.NewLogNotifier(), nil, service.NotificationConfig{})
	account := service.NewAccountService(service.AccountDeps{
		Users: users,
		Devices: &handlerAccountDeviceStore{
			devices: map[uuid.UUID][]model.Device{
				userID: {{
					UserID:     userID,
					DeviceUUID: uuid.MustParse("11111111-1111-1111-1111-111111111111"),
					DeviceName: "Laptop",
					Platform:   "windows",
					AppVersion: "1.2.3",
					CreatedAt:  time.Unix(300, 0).UTC(),
				}},
			},
		},
		Bundles: &handlerAccountBundleStore{
			bundles: map[uuid.UUID][]model.BundleMeta{
				userID: {{
					BundleID:           "bundle-1",
					UserID:             userID,
					UploaderDeviceUUID: uuid.MustParse("11111111-1111-1111-1111-111111111111"),
					LamportLo:          10,
					LamportHi:          11,
					EventCount:         7,
					SizeBytes:          2048,
					CipherID:           1,
					KeyGeneration:      2,
					UploadedAt:         time.Unix(400, 0).UTC(),
				}},
			},
		},
		Snapshots: &handlerAccountSnapshotStore{
			snapshots: map[uuid.UUID][]model.SnapshotMeta{
				userID: {{
					SnapshotID:    "snap-1",
					UserID:        userID,
					BaseHLC:       99,
					SizeBytes:     4096,
					CipherID:      3,
					KeyGeneration: 4,
					CreatedAt:     time.Unix(500, 0).UTC(),
				}},
			},
		},
		Quota: &handlerAccountQuotaStore{
			usage: map[uuid.UUID]*model.QuotaUsage{
				userID: {
					UserID:      userID,
					TotalBytes:  6144,
					BundleCount: 1,
					SnapCount:   1,
					UpdatedAt:   time.Unix(600, 0).UTC(),
				},
			},
		},
		Notifications: notif,
		Audit: &handlerAccountAuditStore{
			logs: map[uuid.UUID][]model.AuditLog{
				userID: {{
					EventType:  model.AuditEventLoginSuccess,
					TargetType: "user",
					TargetID:   userID.String(),
					CreatedAt:  time.Unix(700, 0).UTC(),
					Metadata: map[string]any{
						"method": "password",
					},
				}},
			},
		},
		RetentionGracePeriod: 30 * 24 * time.Hour,
	})
	h := New(Deps{
		Services: &service.Services{
			Account:      account,
			Notification: notif,
		},
		TokenManager: tm,
	})
	app := fiber.New(fiber.Config{ErrorHandler: h.ErrorHandler})
	h.RegisterRoutes(app)

	req := httptest.NewRequest("GET", "/api/v1/me/privacy-export", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("app.Test() error = %v", err)
	}
	if resp.StatusCode != fiber.StatusOK {
		t.Fatalf("status = %d, want %d", resp.StatusCode, fiber.StatusOK)
	}
	var exported service.PrivacyExport
	if err := json.NewDecoder(resp.Body).Decode(&exported); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if exported.IncludesBlobContents {
		t.Fatal("IncludesBlobContents = true, want false")
	}
	if exported.ParsesBundleOrSnapshots {
		t.Fatal("ParsesBundleOrSnapshots = true, want false")
	}
	if exported.Account.Email != "user@example.com" || exported.Account.Tier != model.TierPro {
		t.Fatalf("account = %+v", exported.Account)
	}
	if len(exported.Devices) != 1 || exported.Devices[0].DeviceName != "Laptop" {
		t.Fatalf("devices = %+v", exported.Devices)
	}
	if len(exported.Bundles) != 1 || exported.Bundles[0].BundleID != "bundle-1" {
		t.Fatalf("bundles = %+v", exported.Bundles)
	}
	if len(exported.Snapshots) != 1 || exported.Snapshots[0].SnapshotID != "snap-1" {
		t.Fatalf("snapshots = %+v", exported.Snapshots)
	}
	if exported.NotificationPreferences.WebhookURL != "https://example.com/..." {
		t.Fatalf("webhook url = %q, want masked host", exported.NotificationPreferences.WebhookURL)
	}
	if !exported.NotificationPreferences.WebhookURLSet {
		t.Fatal("WebhookURLSet = false, want true")
	}
	if len(exported.Audit.Entries) != 1 || exported.Audit.Entries[0].EventType != model.AuditEventLoginSuccess {
		t.Fatalf("audit = %+v", exported.Audit)
	}
	if exported.Retention.GracePeriodSeconds != int64((30*24*time.Hour)/time.Second) {
		t.Fatalf("retention = %+v", exported.Retention)
	}
}
