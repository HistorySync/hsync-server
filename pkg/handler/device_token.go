package handler

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"strings"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/google/uuid"

	"github.com/historysync/hsync-server/pkg/apierrors"
	"github.com/historysync/hsync-server/pkg/auth"
	"github.com/historysync/hsync-server/pkg/middleware"
	"github.com/historysync/hsync-server/pkg/model"
	"github.com/historysync/hsync-server/pkg/provider"
	"github.com/historysync/hsync-server/pkg/service"
)

const deviceTokenTTL = 24 * time.Hour

type deviceTokenRepoUserStore interface {
	GetByID(ctx context.Context, id uuid.UUID) (*model.User, error)
}

type deviceTokenDeviceStore interface {
	GetByUserAndUUID(ctx context.Context, userID, deviceUUID uuid.UUID) (*model.Device, error)
	CountActiveByUser(ctx context.Context, userID uuid.UUID) (int32, error)
	Create(ctx context.Context, device *model.Device) error
	UpdateToken(ctx context.Context, id uuid.UUID, hash []byte, expiresAt time.Time) error
}

type deviceTokenQuotaProvider interface {
	GetLimits(userID string) (*provider.QuotaLimitsInfo, error)
}

type deviceTokenDeps struct {
	users   deviceTokenRepoUserStore
	devices deviceTokenDeviceStore
	quota   deviceTokenQuotaProvider
}

type deviceTokenRequest struct {
	DeviceName string `json:"device_name"`
	Platform   string `json:"platform"`
	AppVersion string `json:"app_version"`
}

// RequestDeviceToken registers or refreshes a device token used by /ws/push.
func (h *Handlers) RequestDeviceToken(c fiber.Ctx) error {
	return h.requestDeviceToken(c, h.deviceTokenDeps())
}

func (h *Handlers) requestDeviceToken(c fiber.Ctx, deps deviceTokenDeps) error {
	deviceUUID, err := uuid.Parse(strings.TrimSpace(c.Params("uuid")))
	if err != nil {
		return apierrors.NewBadRequest("invalid device uuid")
	}

	var req deviceTokenRequest
	if len(c.Body()) > 0 {
		if err := json.Unmarshal(c.Body(), &req); err != nil {
			return apierrors.New(apierrors.CodeInvalidJSON, "request body must be a JSON object")
		}
	}

	userID := auth.UserID(c)
	platform := strings.TrimSpace(req.Platform)
	if ok, err := h.enforceDeviceTokenRateLimit(c, userID, deviceUUID, platform); err != nil {
		return err
	} else if !ok {
		return nil
	}

	user, err := deps.users.GetByID(c.Context(), userID)
	if err != nil {
		h.auditDeviceTokenRejected(c, userID, deviceUUID, platform, "user_lookup_failed")
		return apierrors.NewInternal("failed to load user")
	}
	if user == nil {
		h.auditDeviceTokenRejected(c, userID, deviceUUID, platform, "user_not_found")
		return apierrors.New(apierrors.CodeInvalidCredentials, "user not found")
	}

	device, err := deps.devices.GetByUserAndUUID(c.Context(), userID, deviceUUID)
	if err != nil {
		h.auditDeviceTokenRejected(c, userID, deviceUUID, platform, "device_lookup_failed")
		return apierrors.NewInternal("failed to load device")
	}
	if device == nil {
		limits, err := deps.quota.GetLimits(userID.String())
		if err != nil {
			h.auditDeviceTokenRejected(c, userID, deviceUUID, platform, "quota_lookup_failed")
			return apierrors.New(apierrors.CodeQuotaExceeded, err.Error())
		}
		count, err := deps.devices.CountActiveByUser(c.Context(), userID)
		if err != nil {
			h.auditDeviceTokenRejected(c, userID, deviceUUID, platform, "device_count_failed")
			return apierrors.NewInternal("failed to count devices")
		}
		if count >= limits.MaxDevices {
			h.auditDeviceTokenRejected(c, userID, deviceUUID, platform, "quota_max_devices")
			return apierrors.New(apierrors.CodeQuotaExceeded, "device limit reached")
		}
	} else if device.RevokedAt != nil {
		h.auditDeviceTokenRejected(c, userID, deviceUUID, coalescePlatform(platform, device.Platform), "device_revoked")
		return apierrors.New(apierrors.CodeDeviceRevoked, "device has been revoked")
	}

	rawToken, tokenHash, expiresAt, err := generateDeviceToken(time.Now())
	if err != nil {
		h.auditDeviceTokenRejected(c, userID, deviceUUID, coalescePlatform(platform, platformFromDevice(device)), "token_generation_failed")
		return apierrors.NewInternal("failed to generate device token")
	}

	if device == nil {
		device = &model.Device{
			UserID:         userID,
			DeviceUUID:     deviceUUID,
			DeviceName:     strings.TrimSpace(req.DeviceName),
			Platform:       platform,
			AppVersion:     strings.TrimSpace(req.AppVersion),
			TokenHash:      tokenHash,
			TokenExpiresAt: timePtr(expiresAt),
		}
		if err := deps.devices.Create(c.Context(), device); err != nil {
			h.auditDeviceTokenRejected(c, userID, deviceUUID, platform, "device_create_failed")
			return apierrors.NewInternal("failed to register device")
		}
		h.auditDeviceTokenEvent(c, userID, model.AuditEventDeviceTokenIssued, deviceUUID, platform, "")
	} else {
		if err := deps.devices.UpdateToken(c.Context(), device.ID, tokenHash, expiresAt); err != nil {
			h.auditDeviceTokenRejected(c, userID, deviceUUID, coalescePlatform(platform, device.Platform), "device_token_update_failed")
			return apierrors.NewInternal("failed to update device token")
		}
		device.TokenHash = tokenHash
		device.TokenExpiresAt = timePtr(expiresAt)
		if device.DeviceName == "" && strings.TrimSpace(req.DeviceName) != "" {
			device.DeviceName = strings.TrimSpace(req.DeviceName)
		}
		if platform != "" {
			device.Platform = platform
		}
		if strings.TrimSpace(req.AppVersion) != "" {
			device.AppVersion = strings.TrimSpace(req.AppVersion)
		}
		h.auditDeviceTokenEvent(c, userID, model.AuditEventDeviceTokenRotated, deviceUUID, coalescePlatform(platform, device.Platform), "")
	}

	return c.JSON(fiber.Map{
		"device_token": rawToken,
		"expires_in":   int(deviceTokenTTL / time.Second),
		"device":       device,
	})
}

func (h *Handlers) deviceTokenDeps() deviceTokenDeps {
	return deviceTokenDeps{
		users:   h.deps.Services.Repos.Users,
		devices: h.deps.Services.Repos.Devices,
		quota:   provider.Registry().Quota,
	}
}

func (h *Handlers) enforceDeviceTokenRateLimit(c fiber.Ctx, userID, deviceUUID uuid.UUID, platform string) (bool, error) {
	if userID == uuid.Nil {
		return true, nil
	}
	decision := middleware.RateDecision{
		Key:      "device:token:user:" + userID.String(),
		Limit:    deviceTokenRPM,
		Policy:   "device_token",
		FailMode: h.deps.RateLimit.DefaultFailMode(),
	}
	allowed, err := middleware.EnforceRateLimit(c, h.deps.RateLimiter, rateLimitWindow, decision)
	if err != nil {
		h.auditDeviceTokenRejected(c, userID, deviceUUID, platform, "rate_limit_error")
		return false, err
	}
	if !allowed {
		h.auditDeviceTokenRejected(c, userID, deviceUUID, platform, "rate_limited")
	}
	return allowed, nil
}

func (h *Handlers) auditDeviceTokenEvent(c fiber.Ctx, userID uuid.UUID, eventType model.AuditEventType, deviceUUID uuid.UUID, platform, reason string) {
	metadata := deviceTokenAuditMetadata(deviceUUID, platform, reason)
	h.recordAudit(c, service.AuditEventInput{
		ActorUserID: auditActor(userID),
		EventType:   eventType,
		TargetType:  "device",
		TargetID:    deviceUUID.String(),
		Metadata:    metadata,
	})
}

func (h *Handlers) auditDeviceTokenRejected(c fiber.Ctx, userID uuid.UUID, deviceUUID uuid.UUID, platform, reason string) {
	h.auditDeviceTokenEvent(c, userID, model.AuditEventDeviceTokenRejected, deviceUUID, platform, reason)
}

func deviceTokenAuditMetadata(deviceUUID uuid.UUID, platform, reason string) map[string]any {
	metadata := map[string]any{
		"device_uuid": deviceUUID.String(),
	}
	if platform = strings.TrimSpace(platform); platform != "" {
		metadata["platform"] = platform
	}
	if reason = strings.TrimSpace(reason); reason != "" {
		metadata["reason"] = reason
	}
	return metadata
}

func generateDeviceToken(now time.Time) (string, []byte, time.Time, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", nil, time.Time{}, err
	}
	token := base64.RawURLEncoding.EncodeToString(raw)
	sum := sha256.Sum256([]byte(token))
	return token, sum[:], now.UTC().Add(deviceTokenTTL), nil
}

func timePtr(value time.Time) *time.Time {
	v := value.UTC()
	return &v
}

func coalescePlatform(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func platformFromDevice(device *model.Device) string {
	if device == nil {
		return ""
	}
	return device.Platform
}
