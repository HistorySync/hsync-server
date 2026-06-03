// Package handler provides HTTP request handlers and route registration
// for the HistorySync Cloud Server API.
//
// Dependencies (token manager, services, WebSocket hub, etc.) are injected
// via the Deps struct following explicit dependency injection pattern.
package handler

import (
	"encoding/json"
	"strconv"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"

	"github.com/historysync/hsync-server/pkg/auth"
	"github.com/historysync/hsync-server/pkg/model"
	"github.com/historysync/hsync-server/pkg/service"
	"github.com/historysync/hsync-server/pkg/ws"
)

// Deps holds all external dependencies for HTTP handlers.
type Deps struct {
	Services     *service.Services
	TokenManager *auth.TokenManager
	Hub          *ws.Hub
	Redis        *redis.Client // may be nil if Redis is unavailable
	AdminKey     string
}

// Handlers groups all HTTP handler instances.
type Handlers struct {
	deps Deps
}

// New creates a Handlers instance with the given dependencies.
func New(deps Deps) *Handlers {
	return &Handlers{deps: deps}
}

// RegisterRoutes mounts all API routes onto the Fiber app.
func (h *Handlers) RegisterRoutes(app *fiber.App) {
	// ── Health (no auth) ─────────────────────────────────
	app.Get("/healthz", h.Healthz)
	app.Get("/readyz", h.Readyz)

	// ── API v1 ───────────────────────────────────────────
	v1 := app.Group("/api/v1")

	// Auth
	authGroup := v1.Group("/auth")
	authGroup.Post("/register", h.Register)
	authGroup.Post("/login", h.Login)
	authGroup.Post("/refresh", h.RefreshToken)
	authGroup.Post("/logout", h.Logout)

	// Bundles (JWT-protected)
	bundles := v1.Group("/bundles", auth.AuthMiddleware(h.deps.TokenManager))
	bundles.Post("/", h.UploadBundle)
	bundles.Get("/", h.ListBundles)
	bundles.Get("/:id", h.DownloadBundle)
	bundles.Delete("/:id", h.DeleteBundle)

	// Snapshots (JWT-protected)
	snapshots := v1.Group("/snapshots", auth.AuthMiddleware(h.deps.TokenManager))
	snapshots.Post("/", h.UploadSnapshot)
	snapshots.Get("/latest", h.GetLatestSnapshot)
	snapshots.Get("/:id", h.DownloadSnapshot)

	// Devices (JWT-protected)
	devices := v1.Group("/devices", auth.AuthMiddleware(h.deps.TokenManager))
	devices.Get("/", h.ListDevices)
	devices.Post("/:uuid/revoke", h.RevokeDevice)
	devices.Get("/revocations", h.ListRevocations)

	// Quota (JWT-protected)
	v1.Get("/quota", h.GetQuota, auth.AuthMiddleware(h.deps.TokenManager))

	// Billing (JWT-protected, except webhook)
	billing := v1.Group("/billing", auth.AuthMiddleware(h.deps.TokenManager))
	billing.Post("/checkout", h.CreateCheckout)
	billing.Post("/portal", h.CreatePortalSession)
	billing.Get("/subscription", h.GetSubscription)
	billing.Get("/invoices", h.ListInvoices)
	// Stripe webhook has its own signature verification
	v1.Post("/billing/webhook", h.StripeWebhook)

	// ── WebSocket ────────────────────────────────────────
	app.Get("/ws/push", h.WebSocketUpgrade)

	// ── Admin ────────────────────────────────────────────
	admin := app.Group("/admin", auth.AdminMiddleware(h.deps.AdminKey))
	admin.Get("/users", h.AdminListUsers)
	admin.Get("/stats", h.AdminStats)
}

// ── Health ───────────────────────────────────────────────────

func (h *Handlers) Healthz(c fiber.Ctx) error {
	return c.JSON(fiber.Map{"status": "ok"})
}

func (h *Handlers) Readyz(c fiber.Ctx) error {
	// TODO: check DB, Redis, S3 connectivity
	return c.JSON(fiber.Map{"status": "ok", "checks": fiber.Map{
		"database": "ok",
		"redis":    "ok",
		"storage":  "ok",
	}})
}

func (h *Handlers) ErrorHandler(c fiber.Ctx, err error) error {
	code := fiber.StatusInternalServerError
	errCode := "INTERNAL_ERROR"
	message := err.Error()

	if e, ok := err.(*fiber.Error); ok {
		code = e.Code
	}
	if fe, ok := err.(*fiberError); ok {
		code = fe.Code
		errCode = fe.ErrCode
	}

	return c.Status(code).JSON(fiber.Map{
		"error": fiber.Map{
			"code":    errCode,
			"message": message,
		},
	})
}

type fiberError struct {
	Code    int
	ErrCode string
	Message string
}

func (e *fiberError) Error() string { return e.Message }

func newError(code int, errCode, message string) *fiberError {
	return &fiberError{Code: code, ErrCode: errCode, Message: message}
}

// ── Auth ────────────────────────────────────────────────────

type registerRequest struct {
	Email       string `json:"email"`
	Password    string `json:"password"`
	DisplayName string `json:"display_name"`
}

func (h *Handlers) Register(c fiber.Ctx) error {
	var req registerRequest
	if err := json.Unmarshal(c.Body(), &req); err != nil {
		return fiber.NewError(fiber.StatusBadRequest, "invalid request body")
	}

	if req.Email == "" || req.Password == "" {
		return fiber.NewError(fiber.StatusBadRequest, "email and password are required")
	}

	result, err := h.deps.Services.Auth.Register(c.Context(), service.RegisterInput{
		Email:       req.Email,
		Password:    req.Password,
		DisplayName: req.DisplayName,
	})
	if err != nil {
		if err == service.ErrEmailTaken {
			return newError(fiber.StatusConflict, "EMAIL_TAKEN", err.Error())
		}
		return fiber.NewError(fiber.StatusInternalServerError, err.Error())
	}

	return c.Status(fiber.StatusCreated).JSON(fiber.Map{
		"user":          result.User,
		"access_token":  result.AccessToken,
		"refresh_token": result.RefreshToken,
		"expires_in":    result.ExpiresIn,
	})
}

type loginRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

func (h *Handlers) Login(c fiber.Ctx) error {
	var req loginRequest
	if err := json.Unmarshal(c.Body(), &req); err != nil {
		return fiber.NewError(fiber.StatusBadRequest, "invalid request body")
	}

	result, err := h.deps.Services.Auth.Login(c.Context(), service.LoginInput{
		Email:    req.Email,
		Password: req.Password,
	})
	if err != nil {
		if err == service.ErrInvalidCredentials {
			return newError(fiber.StatusUnauthorized, "INVALID_CREDENTIALS", err.Error())
		}
		return fiber.NewError(fiber.StatusInternalServerError, err.Error())
	}

	return c.JSON(fiber.Map{
		"user":          result.User,
		"access_token":  result.AccessToken,
		"refresh_token": result.RefreshToken,
		"expires_in":    result.ExpiresIn,
	})
}

type refreshRequest struct {
	RefreshToken string `json:"refresh_token"`
}

func (h *Handlers) RefreshToken(c fiber.Ctx) error {
	var req refreshRequest
	if err := json.Unmarshal(c.Body(), &req); err != nil {
		return fiber.NewError(fiber.StatusBadRequest, "invalid request body")
	}

	accessToken, err := h.deps.Services.Auth.RefreshAccessToken(c.Context(), req.RefreshToken)
	if err != nil {
		return newError(fiber.StatusUnauthorized, "INVALID_REFRESH_TOKEN", err.Error())
	}

	return c.JSON(fiber.Map{
		"access_token": accessToken,
		"expires_in":   900,
	})
}

func (h *Handlers) Logout(c fiber.Ctx) error {
	var req refreshRequest
	if err := json.Unmarshal(c.Body(), &req); err != nil {
		return fiber.NewError(fiber.StatusBadRequest, "invalid request body")
	}

	if err := h.deps.Services.Auth.Logout(c.Context(), req.RefreshToken); err != nil {
		return fiber.NewError(fiber.StatusInternalServerError, err.Error())
	}

	return c.JSON(fiber.Map{"status": "ok"})
}

// ── Bundles ─────────────────────────────────────────────────

func (h *Handlers) UploadBundle(c fiber.Ctx) error {
	userID := auth.UserID(c)

	// Parse multipart form
	form, err := c.MultipartForm()
	if err != nil {
		return fiber.NewError(fiber.StatusBadRequest, "invalid multipart form")
	}

	// Get the file
	files := form.File["bundle"]
	if len(files) == 0 {
		return fiber.NewError(fiber.StatusBadRequest, "missing 'bundle' file field")
	}
	file := files[0]

	src, err := file.Open()
	if err != nil {
		return fiber.NewError(fiber.StatusInternalServerError, "failed to open uploaded file")
	}
	defer src.Close()

	// Parse metadata fields
	deviceUUID, _ := uuid.Parse(form.Value["device_uuid"][0])
	lamportLo, _ := strconv.ParseInt(form.Value["lamport_lo"][0], 10, 64)
	lamportHi, _ := strconv.ParseInt(form.Value["lamport_hi"][0], 10, 64)
	eventCount, _ := strconv.ParseInt(form.Value["event_count"][0], 10, 32)
	cipherID, _ := strconv.ParseInt(form.Value["cipher_id"][0], 10, 16)
	keyGen, _ := strconv.ParseInt(form.Value["key_generation"][0], 10, 16)

	meta, err := h.deps.Services.Bundle.UploadBundle(c.Context(), userID, service.UploadInput{
		BundleID:      form.Value["bundle_id"][0],
		DeviceUUID:    deviceUUID,
		LamportLo:     lamportLo,
		LamportHi:     lamportHi,
		EventCount:    int32(eventCount),
		SizeBytes:     file.Size,
		CipherID:      int16(cipherID),
		KeyGeneration: int16(keyGen),
		Reader:        src,
		ContentType:   file.Header.Get("Content-Type"),
	})
	if err != nil {
		switch err {
		case service.ErrBundleExists:
			return newError(fiber.StatusConflict, "CONFLICT", err.Error())
		case service.ErrQuotaExceeded:
			return newError(507, "QUOTA_EXCEEDED", err.Error())
		case service.ErrDeviceRevoked:
			return newError(fiber.StatusForbidden, "DEVICE_REVOKED", err.Error())
		}
		return fiber.NewError(fiber.StatusInternalServerError, err.Error())
	}

	// Broadcast to user's other devices via WebSocket
	msg := ws.PushMessage{
		Type:      ws.MsgBundleUploaded,
		Timestamp: time.Now().Unix(),
		Data: fiber.Map{
			"device_uuid": meta.UploaderDeviceUUID,
			"bundle_id":   meta.BundleID,
			"lamport_lo":  meta.LamportLo,
			"lamport_hi":  meta.LamportHi,
		},
	}
	if data, err := json.Marshal(msg); err == nil {
		h.deps.Hub.PushToUser(userID, data)
	}

	return c.Status(fiber.StatusCreated).JSON(meta)
}

func (h *Handlers) ListBundles(c fiber.Ctx) error {
	userID := auth.UserID(c)

	limit := int32(50)
	if l, err := strconv.Atoi(c.Query("limit", "50")); err == nil && l > 0 && l <= 200 {
		limit = int32(l)
	}

	var deviceUUID *uuid.UUID
	if raw := c.Query("device_uuid", ""); raw != "" {
		if id, err := uuid.Parse(raw); err == nil {
			deviceUUID = &id
		}
	}

	afterLamport, _ := strconv.ParseInt(c.Query("after_lamport", "0"), 10, 64)
	cursor := c.Query("cursor", "")

	bundles, err := h.deps.Services.Bundle.ListBundles(c.Context(), userID, deviceUUID, afterLamport, cursor, limit)
	if err != nil {
		return fiber.NewError(fiber.StatusInternalServerError, err.Error())
	}

	nextCursor := ""
	if len(bundles) == int(limit) {
		nextCursor = bundles[len(bundles)-1].BundleID
	}

	return c.JSON(fiber.Map{
		"bundles":     bundles,
		"next_cursor": nextCursor,
	})
}

func (h *Handlers) DownloadBundle(c fiber.Ctx) error {
	userID := auth.UserID(c)
	bundleID := c.Params("id")

	reader, meta, err := h.deps.Services.Bundle.DownloadBundle(c.Context(), userID, bundleID)
	if err != nil {
		if err.Error() == "bundle not found" {
			return newError(fiber.StatusNotFound, "NOT_FOUND", "bundle not found")
		}
		return fiber.NewError(fiber.StatusInternalServerError, err.Error())
	}
	defer reader.Close()

	c.Set("Content-Type", "application/octet-stream")
	c.Set("Content-Disposition", "attachment; filename=\""+meta.BundleID+".hsb\"")
	c.Set("Content-Length", strconv.FormatInt(meta.SizeBytes, 10))

	return c.SendStream(reader)
}

func (h *Handlers) DeleteBundle(c fiber.Ctx) error {
	userID := auth.UserID(c)
	bundleID := c.Params("id")

	if err := h.deps.Services.Bundle.DeleteBundle(c.Context(), userID, bundleID); err != nil {
		return fiber.NewError(fiber.StatusInternalServerError, err.Error())
	}

	return c.JSON(fiber.Map{"status": "deleted"})
}

// ── Snapshots ───────────────────────────────────────────────

func (h *Handlers) UploadSnapshot(c fiber.Ctx) error { return c.SendStatus(501) }

func (h *Handlers) GetLatestSnapshot(c fiber.Ctx) error {
	userID := auth.UserID(c)
	snapshot, err := h.deps.Services.Repos.Snapshots.GetLatest(c.Context(), userID)
	if err != nil {
		return fiber.NewError(fiber.StatusInternalServerError, err.Error())
	}
	if snapshot == nil {
		return newError(fiber.StatusNotFound, "NOT_FOUND", "no snapshot found")
	}
	return c.JSON(snapshot)
}

func (h *Handlers) DownloadSnapshot(c fiber.Ctx) error { return c.SendStatus(501) }

// ── Devices ─────────────────────────────────────────────────

func (h *Handlers) ListDevices(c fiber.Ctx) error {
	userID := auth.UserID(c)
	devices, err := h.deps.Services.Repos.Devices.ListByUser(c.Context(), userID)
	if err != nil {
		return fiber.NewError(fiber.StatusInternalServerError, err.Error())
	}
	if devices == nil {
		devices = []model.Device{}
	}
	return c.JSON(fiber.Map{"devices": devices})
}

func (h *Handlers) RevokeDevice(c fiber.Ctx) error { return c.SendStatus(501) }
func (h *Handlers) ListRevocations(c fiber.Ctx) error { return c.SendStatus(501) }

// ── Quota ───────────────────────────────────────────────────

func (h *Handlers) GetQuota(c fiber.Ctx) error {
	userID := auth.UserID(c)
	tierStr := c.Locals("tier").(string)

	info, err := h.deps.Services.Quota.GetQuota(c.Context(), userID, model.UserTier(tierStr))
	if err != nil {
		return fiber.NewError(fiber.StatusInternalServerError, err.Error())
	}

	storageUsagePercent := 0.0
	if info.Limits.StorageLimitBytes > 0 {
		storageUsagePercent = float64(info.Storage.TotalBytes) / float64(info.Limits.StorageLimitBytes) * 100
	}

	deviceCount, _ := h.deps.Services.Repos.Devices.CountActiveByUser(c.Context(), userID)

	return c.JSON(fiber.Map{
		"storage": fiber.Map{
			"used_bytes":    info.Storage.TotalBytes,
			"limit_bytes":   info.Limits.StorageLimitBytes,
			"usage_percent": storageUsagePercent,
		},
		"bundles": fiber.Map{
			"count": info.Storage.BundleCount,
			"limit": 10000,
		},
		"devices": fiber.Map{
			"count": deviceCount,
			"limit": info.Limits.MaxDevices,
		},
	})
}

// ── Billing ─────────────────────────────────────────────────

func (h *Handlers) CreateCheckout(c fiber.Ctx) error       { return c.SendStatus(501) }
func (h *Handlers) CreatePortalSession(c fiber.Ctx) error  { return c.SendStatus(501) }
func (h *Handlers) GetSubscription(c fiber.Ctx) error      { return c.SendStatus(501) }
func (h *Handlers) ListInvoices(c fiber.Ctx) error         { return c.SendStatus(501) }
func (h *Handlers) StripeWebhook(c fiber.Ctx) error        { return c.SendStatus(501) }

// ── WebSocket ───────────────────────────────────────────────

func (h *Handlers) WebSocketUpgrade(c fiber.Ctx) error {
	return h.deps.Hub.UpgradeHandler(c)
}

// ── Admin ───────────────────────────────────────────────────

func (h *Handlers) AdminListUsers(c fiber.Ctx) error { return c.SendStatus(501) }
func (h *Handlers) AdminStats(c fiber.Ctx) error     { return c.SendStatus(501) }
