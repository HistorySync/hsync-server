package handler

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/google/uuid"

	"github.com/historysync/hsync-server/pkg/middleware"
	"github.com/historysync/hsync-server/pkg/model"
	"github.com/historysync/hsync-server/pkg/provider"
	"github.com/historysync/hsync-server/pkg/service"
)

type stubDeviceTokenUsers struct {
	user *model.User
	err  error
}

func (s stubDeviceTokenUsers) GetByID(_ context.Context, _ uuid.UUID) (*model.User, error) {
	return s.user, s.err
}

type stubDeviceTokenDevices struct {
	device      *model.Device
	getErr      error
	count       int32
	countErr    error
	createErr   error
	updateErr   error
	created     *model.Device
	updatedID   uuid.UUID
	updatedHash []byte
	updatedExp  time.Time
}

func (s *stubDeviceTokenDevices) GetByUserAndUUID(_ context.Context, _, _ uuid.UUID) (*model.Device, error) {
	if s.device == nil {
		return nil, s.getErr
	}
	clone := *s.device
	return &clone, s.getErr
}

func (s *stubDeviceTokenDevices) CountActiveByUser(_ context.Context, _ uuid.UUID) (int32, error) {
	return s.count, s.countErr
}

func (s *stubDeviceTokenDevices) Create(_ context.Context, device *model.Device) error {
	if s.createErr != nil {
		return s.createErr
	}
	clone := *device
	if clone.ID == uuid.Nil {
		clone.ID = uuid.New()
	}
	if clone.CreatedAt.IsZero() {
		clone.CreatedAt = time.Now().UTC()
	}
	device.ID = clone.ID
	device.CreatedAt = clone.CreatedAt
	s.created = &clone
	return nil
}

func (s *stubDeviceTokenDevices) UpdateToken(_ context.Context, id uuid.UUID, hash []byte, expiresAt time.Time) error {
	if s.updateErr != nil {
		return s.updateErr
	}
	s.updatedID = id
	s.updatedHash = append([]byte(nil), hash...)
	s.updatedExp = expiresAt
	return nil
}

type stubDeviceTokenQuota struct {
	info *provider.QuotaLimitsInfo
	err  error
}

func (s stubDeviceTokenQuota) GetLimits(_ string) (*provider.QuotaLimitsInfo, error) {
	if s.info != nil || s.err != nil {
		return s.info, s.err
	}
	return &provider.QuotaLimitsInfo{MaxDevices: 1}, nil
}

type stubLimiter struct {
	result middleware.Result
	err    error
	keys   []string
}

func (s *stubLimiter) Allow(_ context.Context, key string, _ int, _ time.Duration) (middleware.Result, error) {
	s.keys = append(s.keys, key)
	if s.result == (middleware.Result{}) && s.err == nil {
		return middleware.Result{Allowed: true, Limit: 1, Remaining: 0}, nil
	}
	return s.result, s.err
}

func TestDeviceTokenAuditMetadataOnlyIncludesAllowedFields(t *testing.T) {
	deviceUUID := uuid.New()
	metadata := deviceTokenAuditMetadata(deviceUUID, "ios", "quota_max_devices")

	if metadata["device_uuid"] != deviceUUID.String() {
		t.Fatalf("device_uuid = %v, want %s", metadata["device_uuid"], deviceUUID)
	}
	if metadata["platform"] != "ios" {
		t.Fatalf("platform = %v, want ios", metadata["platform"])
	}
	if metadata["reason"] != "quota_max_devices" {
		t.Fatalf("reason = %v, want quota_max_devices", metadata["reason"])
	}
	if len(metadata) != 3 {
		t.Fatalf("metadata len = %d, want 3", len(metadata))
	}
}

func TestGenerateDeviceTokenReturnsServerCheckedExpiry(t *testing.T) {
	now := time.Date(2026, 6, 12, 9, 0, 0, 0, time.UTC)
	token, hash, expiresAt, err := generateDeviceToken(now)
	if err != nil {
		t.Fatalf("generateDeviceToken() error = %v", err)
	}
	if token == "" {
		t.Fatal("token is empty")
	}
	if len(hash) != 32 {
		t.Fatalf("hash len = %d, want 32", len(hash))
	}
	if got, want := expiresAt, now.Add(deviceTokenTTL); !got.Equal(want) {
		t.Fatalf("expiresAt = %s, want %s", got, want)
	}
}

func TestRequestDeviceTokenRegistersNewDeviceAndAudits(t *testing.T) {
	userID := uuid.New()
	deviceUUID := uuid.New()
	auditStore := &handlerAuditStore{}
	devices := &stubDeviceTokenDevices{}
	h := New(Deps{
		Services: &service.Services{
			Audit: service.NewAuditService(auditStore),
		},
	})
	h.deps.Services.Repos = nil

	app := fiber.New(fiber.Config{ErrorHandler: h.ErrorHandler})
	app.Post("/devices/:uuid/token", func(c fiber.Ctx) error {
		c.Locals("user_id", userID.String())
		return h.requestDeviceToken(c, deviceTokenDeps{
			users:   stubDeviceTokenUsers{user: &model.User{ID: userID}},
			devices: devices,
			quota: stubDeviceTokenQuota{
				info: &provider.QuotaLimitsInfo{MaxDevices: 2},
			},
		})
	})

	req := httptest.NewRequest("POST", "/devices/"+deviceUUID.String()+"/token", strings.NewReader(`{"device_name":"MacBook","platform":"macos","app_version":"1.2.3"}`))
	req.Header.Set("Content-Type", "application/json")
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("app.Test() error = %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != fiber.StatusOK {
		t.Fatalf("status = %d, want %d", resp.StatusCode, fiber.StatusOK)
	}

	var body struct {
		DeviceToken string       `json:"device_token"`
		ExpiresIn   int          `json:"expires_in"`
		Device      model.Device `json:"device"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.DeviceToken == "" {
		t.Fatal("device_token is empty")
	}
	if body.ExpiresIn != int(deviceTokenTTL/time.Second) {
		t.Fatalf("expires_in = %d, want %d", body.ExpiresIn, int(deviceTokenTTL/time.Second))
	}
	if body.Device.DeviceUUID != deviceUUID {
		t.Fatalf("device_uuid = %s, want %s", body.Device.DeviceUUID, deviceUUID)
	}
	if devices.created == nil || devices.created.TokenExpiresAt == nil {
		t.Fatalf("created device = %+v, want token expiry set", devices.created)
	}
	if len(auditStore.created) != 1 {
		t.Fatalf("audit events = %d, want 1", len(auditStore.created))
	}
	if auditStore.created[0].EventType != model.AuditEventDeviceTokenIssued {
		t.Fatalf("event type = %q, want %q", auditStore.created[0].EventType, model.AuditEventDeviceTokenIssued)
	}
}

func TestRequestDeviceTokenRejectsRevokedDeviceAndAuditsReason(t *testing.T) {
	userID := uuid.New()
	deviceUUID := uuid.New()
	now := time.Now().UTC()
	auditStore := &handlerAuditStore{}
	h := New(Deps{
		Services: &service.Services{
			Audit: service.NewAuditService(auditStore),
		},
	})

	app := fiber.New(fiber.Config{ErrorHandler: h.ErrorHandler})
	app.Post("/devices/:uuid/token", func(c fiber.Ctx) error {
		c.Locals("user_id", userID.String())
		return h.requestDeviceToken(c, deviceTokenDeps{
			users: stubDeviceTokenUsers{user: &model.User{ID: userID}},
			devices: &stubDeviceTokenDevices{
				device: &model.Device{
					ID:         uuid.New(),
					UserID:     userID,
					DeviceUUID: deviceUUID,
					Platform:   "android",
					RevokedAt:  &now,
				},
			},
			quota: stubDeviceTokenQuota{info: &provider.QuotaLimitsInfo{MaxDevices: 2}},
		})
	})

	req := httptest.NewRequest("POST", "/devices/"+deviceUUID.String()+"/token", strings.NewReader(`{"platform":"android"}`))
	req.Header.Set("Content-Type", "application/json")
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("app.Test() error = %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != fiber.StatusForbidden {
		t.Fatalf("status = %d, want %d", resp.StatusCode, fiber.StatusForbidden)
	}
	if len(auditStore.created) != 1 {
		t.Fatalf("audit events = %d, want 1", len(auditStore.created))
	}
	event := auditStore.created[0]
	if event.EventType != model.AuditEventDeviceTokenRejected {
		t.Fatalf("event type = %q, want %q", event.EventType, model.AuditEventDeviceTokenRejected)
	}
	if event.Metadata["reason"] != "device_revoked" {
		t.Fatalf("reason = %v, want device_revoked", event.Metadata["reason"])
	}
}

func TestRequestDeviceTokenRateLimitedWritesRejectedAudit(t *testing.T) {
	userID := uuid.New()
	deviceUUID := uuid.New()
	auditStore := &handlerAuditStore{}
	limiter := &stubLimiter{
		result: middleware.Result{Allowed: false, Limit: 10, Remaining: 0, RetryAfter: time.Minute},
	}
	h := New(Deps{
		Services: &service.Services{
			Audit: service.NewAuditService(auditStore),
		},
		RateLimiter: limiter,
	})

	app := fiber.New(fiber.Config{ErrorHandler: h.ErrorHandler})
	app.Post("/devices/:uuid/token", func(c fiber.Ctx) error {
		c.Locals("user_id", userID.String())
		return h.requestDeviceToken(c, deviceTokenDeps{
			users:   stubDeviceTokenUsers{user: &model.User{ID: userID}},
			devices: &stubDeviceTokenDevices{},
			quota:   stubDeviceTokenQuota{info: &provider.QuotaLimitsInfo{MaxDevices: 2}},
		})
	})

	req := httptest.NewRequest("POST", "/devices/"+deviceUUID.String()+"/token", strings.NewReader(`{"platform":"web"}`))
	req.Header.Set("Content-Type", "application/json")
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("app.Test() error = %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != fiber.StatusTooManyRequests {
		t.Fatalf("status = %d, want %d", resp.StatusCode, fiber.StatusTooManyRequests)
	}
	if len(limiter.keys) != 1 || limiter.keys[0] != "device:token:user:"+userID.String() {
		t.Fatalf("limiter keys = %#v, want user scoped key", limiter.keys)
	}
	if len(auditStore.created) != 1 {
		t.Fatalf("audit events = %d, want 1", len(auditStore.created))
	}
	if auditStore.created[0].Metadata["reason"] != "rate_limited" {
		t.Fatalf("reason = %v, want rate_limited", auditStore.created[0].Metadata["reason"])
	}
}
