package handler

import (
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
	"github.com/historysync/hsync-server/pkg/model"
	"github.com/historysync/hsync-server/pkg/provider"
)

type deviceTokenRequest struct {
	DeviceName string `json:"device_name"`
	Platform   string `json:"platform"`
	AppVersion string `json:"app_version"`
}

// RequestDeviceToken registers or refreshes a device token used by /ws/push.
func (h *Handlers) RequestDeviceToken(c fiber.Ctx) error {
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
	user, err := h.deps.Services.Repos.Users.GetByID(c.Context(), userID)
	if err != nil {
		return apierrors.NewInternal("failed to load user")
	}
	if user == nil {
		return apierrors.New(apierrors.CodeInvalidCredentials, "user not found")
	}

	device, err := h.deps.Services.Repos.Devices.GetByUserAndUUID(c.Context(), userID, deviceUUID)
	if err != nil {
		return apierrors.NewInternal("failed to load device")
	}
	if device == nil {
		limits, err := provider.Registry().Quota.GetLimits(userID.String())
		if err != nil {
			return apierrors.New(apierrors.CodeQuotaExceeded, err.Error())
		}
		count, err := h.deps.Services.Repos.Devices.CountActiveByUser(c.Context(), userID)
		if err != nil {
			return apierrors.NewInternal("failed to count devices")
		}
		if count >= limits.MaxDevices {
			return apierrors.New(apierrors.CodeQuotaExceeded, "device limit reached")
		}
	} else if device.RevokedAt != nil {
		return apierrors.New(apierrors.CodeDeviceRevoked, "device has been revoked")
	}

	rawToken, tokenHash, err := generateDeviceToken()
	if err != nil {
		return apierrors.NewInternal("failed to generate device token")
	}
	if device == nil {
		device = &model.Device{
			UserID:     userID,
			DeviceUUID: deviceUUID,
			DeviceName: strings.TrimSpace(req.DeviceName),
			Platform:   strings.TrimSpace(req.Platform),
			AppVersion: strings.TrimSpace(req.AppVersion),
			TokenHash:  tokenHash,
		}
		if err := h.deps.Services.Repos.Devices.Create(c.Context(), device); err != nil {
			return apierrors.NewInternal("failed to register device")
		}
	} else if err := h.deps.Services.Repos.Devices.UpdateTokenHash(c.Context(), device.ID, tokenHash); err != nil {
		return apierrors.NewInternal("failed to update device token")
	}

	return c.JSON(fiber.Map{
		"device_token": rawToken,
		"expires_in":   int((24 * time.Hour).Seconds()),
		"device":       device,
	})
}

func generateDeviceToken() (string, []byte, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", nil, err
	}
	token := base64.RawURLEncoding.EncodeToString(raw)
	sum := sha256.Sum256([]byte(token))
	return token, sum[:], nil
}
