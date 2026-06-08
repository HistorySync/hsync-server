package handler

import (
	"context"
	"encoding/json"
	"io"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gofiber/fiber/v3"

	"github.com/historysync/hsync-server/pkg/model"
	"github.com/historysync/hsync-server/pkg/service"
	"github.com/historysync/hsync-server/pkg/storage"
)

// handlerSettingStore is an in-memory settingStore for handler tests. Its
// method set structurally satisfies the service's unexported store interface.
type handlerSettingStore struct {
	rows map[string]model.SystemSetting
}

func (s *handlerSettingStore) Get(_ context.Context, key string) (*model.SystemSetting, error) {
	if row, ok := s.rows[key]; ok {
		cp := row
		return &cp, nil
	}
	return nil, nil
}

func (s *handlerSettingStore) Upsert(_ context.Context, v *model.SystemSetting) error {
	if s.rows == nil {
		s.rows = make(map[string]model.SystemSetting)
	}
	v.UpdatedAt = time.Unix(1_700_000_000, 0).UTC()
	s.rows[v.Key] = *v
	return nil
}

func newSettingsTestApp(store *handlerSettingStore) *fiber.App {
	defs := []service.SettingDefinition{
		{Key: "feature_enabled", Type: service.SettingTypeBool, Default: "false", Description: "a bool"},
		{Key: "api_token", Type: service.SettingTypeString, Default: "", Description: "a secret", Sensitive: true},
	}
	h := New(Deps{
		Services: &service.Services{Settings: service.NewSettingsService(store, defs)},
		AdminKey: "secret",
	})
	app := fiber.New(fiber.Config{ErrorHandler: h.ErrorHandler})
	h.RegisterRoutes(app)
	return app
}

type handlerBlobStore struct{}

func (handlerBlobStore) Put(context.Context, string, io.Reader, int64, string) error { return nil }
func (handlerBlobStore) Get(context.Context, string) (io.ReadCloser, error)          { return nil, nil }
func (handlerBlobStore) Delete(context.Context, string) error                        { return nil }
func (handlerBlobStore) Exists(context.Context, string) (bool, error)                { return false, nil }
func (handlerBlobStore) Size(context.Context, string) (int64, bool, error)           { return 0, false, nil }
func (handlerBlobStore) List(context.Context, string) ([]storage.ObjectInfo, error)  { return nil, nil }

func TestAdminSetAndListSettings(t *testing.T) {
	store := &handlerSettingStore{}
	app := newSettingsTestApp(store)

	// Write a sensitive setting.
	put := httptest.NewRequest("PUT", "/admin/settings/api_token", strings.NewReader(`{"value":"shh"}`))
	put.Header.Set("X-Admin-Key", "secret")
	put.Header.Set("Content-Type", "application/json")
	put.Header.Set("Idempotency-Key", "settings-write")
	resp, err := app.Test(put)
	if err != nil {
		t.Fatalf("app.Test(PUT): %v", err)
	}
	if resp.StatusCode != fiber.StatusOK {
		t.Fatalf("PUT status = %d, want %d", resp.StatusCode, fiber.StatusOK)
	}

	// List should mask the sensitive value but report it as set.
	get := httptest.NewRequest("GET", "/admin/settings", nil)
	get.Header.Set("X-Admin-Key", "secret")
	resp, err = app.Test(get)
	if err != nil {
		t.Fatalf("app.Test(GET): %v", err)
	}
	if resp.StatusCode != fiber.StatusOK {
		t.Fatalf("GET status = %d, want %d", resp.StatusCode, fiber.StatusOK)
	}
	var body struct {
		Settings []service.SettingView `json:"settings"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	byKey := make(map[string]service.SettingView, len(body.Settings))
	for _, v := range body.Settings {
		byKey[v.Key] = v
	}
	token, ok := byKey["api_token"]
	if !ok {
		t.Fatal("api_token missing from list")
	}
	if !token.Sensitive || token.Value != "" || !token.IsSet {
		t.Fatalf("api_token view = %+v, want masked value, sensitive, IsSet", token)
	}
	if feature := byKey["feature_enabled"]; feature.Value != "false" || feature.IsSet {
		t.Fatalf("feature_enabled view = %+v, want default false, IsSet false", feature)
	}
}

func TestMaintenanceModeBlocksNormalWritesAndKeepsOpsEntrypoints(t *testing.T) {
	store := &handlerSettingStore{rows: map[string]model.SystemSetting{
		service.SettingKeyMaintenanceMode: {Key: service.SettingKeyMaintenanceMode, Value: "true"},
	}}
	h := New(Deps{
		Services: &service.Services{
			Settings: service.NewSettingsService(store, []service.SettingDefinition{
				{
					Key:         service.SettingKeyMaintenanceMode,
					Type:        service.SettingTypeBool,
					Default:     "false",
					Description: "maintenance",
					Group:       service.SettingGroupOperations,
				},
			}),
		},
		AdminKey:  "secret",
		BlobStore: handlerBlobStore{},
	})
	app := fiber.New(fiber.Config{ErrorHandler: h.ErrorHandler})
	h.RegisterRoutes(app)

	login := httptest.NewRequest("POST", "/api/v1/auth/login", strings.NewReader(`{"email":"u@example.com","password":"password"}`))
	login.Header.Set("Content-Type", "application/json")
	resp, err := app.Test(login)
	if err != nil {
		t.Fatalf("app.Test(login): %v", err)
	}
	if resp.StatusCode != fiber.StatusServiceUnavailable {
		t.Fatalf("login status = %d, want %d", resp.StatusCode, fiber.StatusServiceUnavailable)
	}
	if code := decodeErrorCode(t, resp.Body); code != "MAINTENANCE_MODE" {
		t.Fatalf("login error code = %q, want MAINTENANCE_MODE", code)
	}

	health := httptest.NewRequest("GET", "/healthz", nil)
	resp, err = app.Test(health)
	if err != nil {
		t.Fatalf("app.Test(healthz): %v", err)
	}
	if resp.StatusCode != fiber.StatusOK {
		t.Fatalf("healthz status = %d, want %d", resp.StatusCode, fiber.StatusOK)
	}

	ready := httptest.NewRequest("GET", "/readyz", nil)
	resp, err = app.Test(ready)
	if err != nil {
		t.Fatalf("app.Test(readyz): %v", err)
	}
	if resp.StatusCode != fiber.StatusServiceUnavailable {
		t.Fatalf("readyz status = %d, want %d", resp.StatusCode, fiber.StatusServiceUnavailable)
	}
	var readyBody struct {
		Status string            `json:"status"`
		Checks map[string]string `json:"checks"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&readyBody); err != nil {
		t.Fatalf("decode readyz: %v", err)
	}
	if readyBody.Status != "unhealthy" || readyBody.Checks["maintenance_mode"] != "enabled" {
		t.Fatalf("readyz body = %+v, want unhealthy maintenance_mode enabled", readyBody)
	}

	put := httptest.NewRequest("PUT", "/admin/settings/maintenance_mode", strings.NewReader(`{"value":"false"}`))
	put.Header.Set("X-Admin-Key", "secret")
	put.Header.Set("Content-Type", "application/json")
	put.Header.Set("Idempotency-Key", "maintenance-write")
	resp, err = app.Test(put)
	if err != nil {
		t.Fatalf("app.Test(admin setting): %v", err)
	}
	if resp.StatusCode != fiber.StatusOK {
		t.Fatalf("admin setting status = %d, want %d", resp.StatusCode, fiber.StatusOK)
	}
}

func TestAdminSetSettingUnknownKey(t *testing.T) {
	app := newSettingsTestApp(&handlerSettingStore{})

	req := httptest.NewRequest("PUT", "/admin/settings/nope", strings.NewReader(`{"value":"x"}`))
	req.Header.Set("X-Admin-Key", "secret")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", "unknown-setting")
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("app.Test: %v", err)
	}
	if resp.StatusCode != fiber.StatusBadRequest {
		t.Fatalf("status = %d, want %d", resp.StatusCode, fiber.StatusBadRequest)
	}
	if code := decodeErrorCode(t, resp.Body); code != "UNKNOWN_SETTING" {
		t.Fatalf("error code = %q, want UNKNOWN_SETTING", code)
	}
}

func TestAdminSetSettingInvalidValue(t *testing.T) {
	app := newSettingsTestApp(&handlerSettingStore{})

	req := httptest.NewRequest("PUT", "/admin/settings/feature_enabled", strings.NewReader(`{"value":"maybe"}`))
	req.Header.Set("X-Admin-Key", "secret")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", "invalid-setting")
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("app.Test: %v", err)
	}
	if resp.StatusCode != fiber.StatusBadRequest {
		t.Fatalf("status = %d, want %d", resp.StatusCode, fiber.StatusBadRequest)
	}
	if code := decodeErrorCode(t, resp.Body); code != "INVALID_SETTING_VALUE" {
		t.Fatalf("error code = %q, want INVALID_SETTING_VALUE", code)
	}
}

func decodeErrorCode(t *testing.T, r interface{ Read([]byte) (int, error) }) string {
	t.Helper()
	var body struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.NewDecoder(r).Decode(&body); err != nil {
		t.Fatalf("decode error body: %v", err)
	}
	return body.Error.Code
}
