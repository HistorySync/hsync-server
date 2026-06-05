// Package handler provides HTTP request handlers and route registration
// for the HistorySync Cloud Server API.
//
// Dependencies (token manager, services, WebSocket hub, etc.) are injected
// via the Deps struct following explicit dependency injection pattern.
package handler

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"time"

	"github.com/historysync/hsync-server/pkg/apierrors"

	"github.com/gofiber/fiber/v3"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"

	"github.com/historysync/hsync-server/pkg/auth"
	"github.com/historysync/hsync-server/pkg/config"
	"github.com/historysync/hsync-server/pkg/middleware"
	"github.com/historysync/hsync-server/pkg/model"
	"github.com/historysync/hsync-server/pkg/provider"
	"github.com/historysync/hsync-server/pkg/service"
	"github.com/historysync/hsync-server/pkg/storage"
	"github.com/historysync/hsync-server/pkg/ws"
)

// Deps holds all external dependencies for HTTP handlers.
type Deps struct {
	Services     *service.Services
	TokenManager *auth.TokenManager
	Hub          *ws.Hub
	DB           *pgxpool.Pool
	Redis        *redis.Client // may be nil if Redis is unavailable
	BlobStore    storage.BlobStorage
	AdminKey     string
	RateLimiter  middleware.Limiter // may be nil to disable rate limiting
	OptionStore  config.OptionStore // may be nil if dynamic options are disabled
}

// Handlers groups all HTTP handler instances.
type Handlers struct {
	deps Deps
}

// New creates a Handlers instance with the given dependencies.
func New(deps Deps) *Handlers {
	return &Handlers{deps: deps}
}

// Rate limit settings.
const (
	rateLimitWindow = time.Minute
	// publicAuthRPM caps unauthenticated auth requests per client IP per minute.
	publicAuthRPM = 30

	authLoginIPLimit       = 10
	authLoginEmailLimit    = 5
	authRegisterIPLimit    = 5
	authRecoveryIPLimit    = 5
	authRecoveryEmailLimit = 3
	authResetIPLimit       = 10
	authResetTokenLimit    = 5

	authRecoveryWindow = 15 * time.Minute
	authRegisterWindow = time.Hour
)

// perUserRateDecision limits authenticated routes by user ID using the tier's
// MaxRPM from the JWT claim (no DB lookup). It must run after AuthMiddleware.
func perUserRateDecision(c fiber.Ctx) middleware.RateDecision {
	userID, _ := c.Locals("user_id").(string)
	if userID == "" {
		return middleware.RateDecision{Skip: true}
	}
	tier, _ := c.Locals("tier").(string)
	limit := int(model.TierLimits(model.UserTier(tier)).MaxRPM)
	return middleware.RateDecision{Key: "u:" + userID, Limit: limit}
}

// perIPRateDecision limits routes by client IP at a fixed per-minute rate.
func perIPRateDecision(limit int) func(fiber.Ctx) middleware.RateDecision {
	return func(c fiber.Ctx) middleware.RateDecision {
		return middleware.RateDecision{Key: "ip:" + c.IP(), Limit: limit}
	}
}

func authRateLimit(limiter middleware.Limiter, window time.Duration, classify func(fiber.Ctx) middleware.RateDecision) fiber.Handler {
	return middleware.RateLimit(middleware.RateLimitConfig{
		Limiter:  limiter,
		Window:   window,
		Classify: classify,
	})
}

func (h *Handlers) enforceAuthRateLimit(c fiber.Ctx, window time.Duration, decision middleware.RateDecision) (bool, error) {
	return middleware.EnforceRateLimit(c, h.deps.RateLimiter, window, decision)
}

// RegisterRoutes mounts all API routes onto the Fiber app.
func (h *Handlers) RegisterRoutes(app *fiber.App) {
	// ── Health (no auth) ─────────────────────────────────
	app.Get("/healthz", h.Healthz)
	app.Get("/readyz", h.Readyz)

	// ── API v1 ───────────────────────────────────────────
	v1 := app.Group("/api/v1")

	// Rate limiting: per-user (tier MaxRPM) on authenticated groups, and
	// endpoint-specific controls on public auth endpoints. A nil RateLimiter
	// makes these middlewares no-op.
	perUserRL := middleware.RateLimit(middleware.RateLimitConfig{
		Limiter:  h.deps.RateLimiter,
		Window:   rateLimitWindow,
		Classify: perUserRateDecision,
	})
	publicAuthRL := middleware.RateLimit(middleware.RateLimitConfig{
		Limiter:  h.deps.RateLimiter,
		Window:   rateLimitWindow,
		Classify: perIPRateDecision(publicAuthRPM),
	})
	authRL := func(window time.Duration, classify func(fiber.Ctx) middleware.RateDecision) fiber.Handler {
		return authRateLimit(h.deps.RateLimiter, window, classify)
	}

	// Auth (public; endpoint-specific throttles blunt credential-stuffing and
	// email workflow abuse without applying one coarse limit to every route).
	authGroup := v1.Group("/auth")
	authGroup.Post("/register",
		h.Register,
		authRL(authRegisterWindow, middleware.AuthIPRateDecision("auth:register:ip", authRegisterIPLimit)))
	authGroup.Post("/login",
		h.Login,
		authRL(rateLimitWindow, middleware.AuthIPRateDecision("auth:login:ip", authLoginIPLimit)))
	authGroup.Post("/refresh", h.RefreshToken, publicAuthRL)
	authGroup.Post("/logout", h.Logout, publicAuthRL)
	authGroup.Post("/resend-verification",
		h.ResendEmailVerification,
		authRL(authRecoveryWindow, middleware.AuthIPRateDecision("auth:resend:ip", authRecoveryIPLimit)))
	authGroup.Post("/verify-email", h.VerifyEmail, publicAuthRL)
	authGroup.Post("/forgot-password",
		h.ForgotPassword,
		authRL(authRecoveryWindow, middleware.AuthIPRateDecision("auth:forgot:ip", authRecoveryIPLimit)))
	authGroup.Post("/reset-password",
		h.ResetPassword,
		authRL(rateLimitWindow, middleware.AuthIPRateDecision("auth:reset:ip", authResetIPLimit)))

	// Bundles (JWT-protected)
	bundles := v1.Group("/bundles", auth.AuthMiddleware(h.deps.TokenManager), perUserRL)
	bundles.Post("/", h.UploadBundle)
	bundles.Get("/", h.ListBundles)
	bundles.Get("/:id", h.DownloadBundle)
	bundles.Delete("/:id", h.DeleteBundle)

	// Snapshots (JWT-protected)
	snapshots := v1.Group("/snapshots", auth.AuthMiddleware(h.deps.TokenManager), perUserRL)
	snapshots.Post("/", h.UploadSnapshot)
	snapshots.Get("/latest", h.GetLatestSnapshot)
	snapshots.Get("/:id", h.DownloadSnapshot)

	// Devices (JWT-protected)
	devices := v1.Group("/devices", auth.AuthMiddleware(h.deps.TokenManager), perUserRL)
	devices.Get("/", h.ListDevices)
	devices.Post("/:uuid/revoke", h.RevokeDevice)
	devices.Get("/revocations", h.ListRevocations)

	// Quota (JWT-protected)
	v1.Get("/quota", auth.AuthMiddleware(h.deps.TokenManager), perUserRL, h.GetQuota)

	// Billing (JWT-protected, except webhook)
	billing := v1.Group("/billing", auth.AuthMiddleware(h.deps.TokenManager), perUserRL)
	billing.Post("/checkout", h.CreateCheckout)
	billing.Post("/portal", h.CreatePortalSession)
	billing.Get("/subscription", h.GetSubscription)
	billing.Get("/invoices", h.ListInvoices)
	// Stripe webhook has its own signature verification
	v1.Post("/billing/webhook", h.StripeWebhook)

	// ── WebSocket ────────────────────────────────────────
	app.Get("/ws/push", h.WebSocketUpgrade)
	app.Get("/api/meta/overview", h.WebOverview)

	// ── Admin ────────────────────────────────────────────
	admin := app.Group("/admin", auth.AdminMiddleware(h.deps.AdminKey))
	admin.Get("/users", h.AdminListUsers)
	admin.Get("/stats", h.AdminStats)
	admin.Post("/users/:id/recalculate-quota", h.AdminRecalculateQuota)
	admin.Get("/options", h.AdminListOptions)
	admin.Put("/options/:key", h.AdminSetOption)
	admin.Get("/error-codes", h.AdminErrorCodes)
}

// ── Health ───────────────────────────────────────────────────

func (h *Handlers) Healthz(c fiber.Ctx) error {
	return c.JSON(fiber.Map{"status": "ok"})
}

func (h *Handlers) Readyz(c fiber.Ctx) error {
	ctx, cancel := context.WithTimeout(c.Context(), 3*time.Second)
	defer cancel()

	status := "ok"
	checks := fiber.Map{}

	if h.deps.DB == nil {
		status = "degraded"
		checks["database"] = "not_configured"
	} else if err := h.deps.DB.Ping(ctx); err != nil {
		status = "unhealthy"
		checks["database"] = "error: " + err.Error()
	} else {
		checks["database"] = "ok"
	}

	if h.deps.Redis == nil {
		checks["redis"] = "disabled"
	} else if err := h.deps.Redis.Ping(ctx).Err(); err != nil {
		if status == "ok" {
			status = "degraded"
		}
		checks["redis"] = "error: " + err.Error()
	} else {
		checks["redis"] = "ok"
	}

	if h.deps.BlobStore == nil {
		status = "unhealthy"
		checks["storage"] = "not_configured"
	} else if _, err := h.deps.BlobStore.List(ctx, ""); err != nil {
		status = "unhealthy"
		checks["storage"] = "error: " + err.Error()
	} else {
		checks["storage"] = "ok"
	}

	// Merge any provider-contributed checks (Enterprise dependencies). A failing
	// critical check makes readiness unhealthy; a failing non-critical one only
	// degrades it.
	for _, check := range provider.Registry().Readiness.ReadinessChecks(ctx) {
		checks[check.Name] = check.Status
		if check.Healthy {
			continue
		}
		if check.Critical {
			status = "unhealthy"
		} else if status == "ok" {
			status = "degraded"
		}
	}

	code := fiber.StatusOK
	if status == "unhealthy" {
		code = fiber.StatusServiceUnavailable
	}

	return c.Status(code).JSON(fiber.Map{"status": status, "checks": checks})
}

func (h *Handlers) WebOverview(c fiber.Ctx) error {
	ctx, cancel := context.WithTimeout(c.Context(), 3*time.Second)
	defer cancel()

	status := "ok"
	checks := fiber.Map{}

	if h.deps.DB == nil {
		status = "degraded"
		checks["database"] = "not_configured"
	} else if err := h.deps.DB.Ping(ctx); err != nil {
		status = "unhealthy"
		checks["database"] = "error: " + err.Error()
	} else {
		checks["database"] = "ok"
	}

	if h.deps.Redis == nil {
		checks["redis"] = "disabled"
	} else if err := h.deps.Redis.Ping(ctx).Err(); err != nil {
		if status == "ok" {
			status = "degraded"
		}
		checks["redis"] = "error: " + err.Error()
	} else {
		checks["redis"] = "ok"
	}

	if h.deps.BlobStore == nil {
		status = "unhealthy"
		checks["storage"] = "not_configured"
	} else if _, err := h.deps.BlobStore.List(ctx, ""); err != nil {
		status = "unhealthy"
		checks["storage"] = "error: " + err.Error()
	} else {
		checks["storage"] = "ok"
	}

	bundleCount, err := h.deps.Services.Repos.Bundles.CountAll(ctx)
	if err != nil {
		return apierrors.NewInternal("failed to count bundles")
	}
	snapshotCount, err := h.deps.Services.Repos.Snapshots.CountAll(ctx)
	if err != nil {
		return apierrors.NewInternal("failed to count snapshots")
	}
	totalBundleBytes, err := h.deps.Services.Repos.Bundles.SumSizeAll(ctx)
	if err != nil {
		return apierrors.NewInternal("failed to sum bundle bytes")
	}
	totalSnapshotBytes, err := h.deps.Services.Repos.Snapshots.SumSizeAll(ctx)
	if err != nil {
		return apierrors.NewInternal("failed to sum snapshot bytes")
	}
	activeDevices, err := h.deps.Services.Repos.Devices.CountActive(ctx)
	if err != nil {
		return apierrors.NewInternal("failed to count active devices")
	}
	users, err := h.deps.Services.Repos.Users.List(ctx, 200, 0)
	if err != nil {
		return apierrors.NewInternal("failed to list users")
	}

	tierDistribution := map[string]int{
		"free":       0,
		"pro":        0,
		"team":       0,
		"enterprise": 0,
	}
	statusDistribution := map[string]int{}
	for _, user := range users {
		tierDistribution[string(user.Tier)]++
		statusDistribution[string(user.Status)]++
	}

	return c.JSON(fiber.Map{
		"status": status,
		"checks": checks,
		"summary": fiber.Map{
			"total_users":            len(users),
			"active_devices":         activeDevices,
			"total_bundles":          bundleCount,
			"total_snapshots":        snapshotCount,
			"total_storage_bytes":    totalBundleBytes + totalSnapshotBytes,
			"bundle_storage_bytes":   totalBundleBytes,
			"snapshot_storage_bytes": totalSnapshotBytes,
		},
		"distribution": fiber.Map{
			"tiers":    tierDistribution,
			"statuses": statusDistribution,
		},
		"routes": fiber.Map{
			"health":    "/healthz",
			"readiness": "/readyz",
			"meta":      "/api/meta/web",
			"quota":     "/api/v1/quota",
			"admin":     "/admin/stats",
		},
	})
}

func (h *Handlers) ErrorHandler(c fiber.Ctx, err error) error {
	code := fiber.StatusInternalServerError
	errCode := string(apierrors.CodeInternalError)
	message := err.Error()

	if e, ok := err.(*fiber.Error); ok {
		code = e.Code
	}
	if fe, ok := err.(*apierrors.Error); ok {
		code = fe.HTTPStatus
		errCode = string(fe.Code)
	}

	return c.Status(code).JSON(fiber.Map{
		"request_id": middleware.GetRequestID(c),
		"error": fiber.Map{
			"code":    errCode,
			"message": message,
		},
	})
}

func requiredFormValue(form map[string][]string, name string) (string, error) {
	values := form[name]
	if len(values) == 0 || values[0] == "" {
		return "", apierrors.NewBadRequest("missing '" + name + "' field")
	}
	return values[0], nil
}

func parseFormUUID(form map[string][]string, name string) (uuid.UUID, error) {
	raw, err := requiredFormValue(form, name)
	if err != nil {
		return uuid.Nil, err
	}
	id, err := uuid.Parse(raw)
	if err != nil {
		return uuid.Nil, apierrors.NewBadRequest("invalid '" + name + "' field")
	}
	return id, nil
}

func parseFormInt(form map[string][]string, name string, bitSize int) (int64, error) {
	raw, err := requiredFormValue(form, name)
	if err != nil {
		return 0, err
	}
	value, err := strconv.ParseInt(raw, 10, bitSize)
	if err != nil {
		return 0, apierrors.NewBadRequest("invalid '" + name + "' field")
	}
	return value, nil
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
		return apierrors.NewBadRequest("invalid request body")
	}

	if req.Email == "" || req.Password == "" {
		return apierrors.NewBadRequest("email and password are required")
	}

	result, err := h.deps.Services.Auth.Register(c.Context(), service.RegisterInput{
		Email:       req.Email,
		Password:    req.Password,
		DisplayName: req.DisplayName,
	})
	if err != nil {
		switch err {
		case service.ErrEmailTaken:
			return apierrors.New(apierrors.CodeEmailTaken, err.Error())
		case service.ErrInvalidEmail, service.ErrWeakPassword:
			return apierrors.NewBadRequest(err.Error())
		}
		return apierrors.NewInternal(err.Error())
	}

	resp := fiber.Map{
		"user":          result.User,
		"access_token":  result.AccessToken,
		"refresh_token": result.RefreshToken,
		"expires_in":    result.ExpiresIn,
	}
	if result.EmailVerificationToken != "" {
		resp["email_verification_token"] = result.EmailVerificationToken
	}
	return c.Status(fiber.StatusCreated).JSON(resp)
}

type loginRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

func (h *Handlers) Login(c fiber.Ctx) error {
	var req loginRequest
	if err := json.Unmarshal(c.Body(), &req); err != nil {
		return apierrors.NewBadRequest("invalid request body")
	}
	if ok, err := h.enforceAuthRateLimit(c, rateLimitWindow,
		middleware.AuthEmailRateDecisionForValue("auth:login:email", authLoginEmailLimit, req.Email)); err != nil || !ok {
		return err
	}

	result, err := h.deps.Services.Auth.Login(c.Context(), service.LoginInput{
		Email:    req.Email,
		Password: req.Password,
	})
	if err != nil {
		if err == service.ErrInvalidCredentials {
			return apierrors.New(apierrors.CodeInvalidCredentials, err.Error())
		}
		return apierrors.NewInternal(err.Error())
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

type forgotPasswordRequest struct {
	Email string `json:"email"`
}

type resendVerificationRequest struct {
	Email string `json:"email"`
}

type verifyEmailRequest struct {
	Token string `json:"token"`
}

type resetPasswordRequest struct {
	Token       string `json:"token"`
	NewPassword string `json:"new_password"`
}

func (h *Handlers) RefreshToken(c fiber.Ctx) error {
	var req refreshRequest
	if err := json.Unmarshal(c.Body(), &req); err != nil {
		return apierrors.NewBadRequest("invalid request body")
	}

	accessToken, err := h.deps.Services.Auth.RefreshAccessToken(c.Context(), req.RefreshToken)
	if err != nil {
		return apierrors.New(apierrors.CodeInvalidRefreshToken, err.Error())
	}

	return c.JSON(fiber.Map{
		"access_token": accessToken,
		"expires_in":   900,
	})
}

func (h *Handlers) Logout(c fiber.Ctx) error {
	var req refreshRequest
	if err := json.Unmarshal(c.Body(), &req); err != nil {
		return apierrors.NewBadRequest("invalid request body")
	}

	if err := h.deps.Services.Auth.Logout(c.Context(), req.RefreshToken); err != nil {
		return apierrors.NewInternal(err.Error())
	}

	return c.JSON(fiber.Map{"status": "ok"})
}

func (h *Handlers) ResendEmailVerification(c fiber.Ctx) error {
	var req resendVerificationRequest
	if err := json.Unmarshal(c.Body(), &req); err != nil {
		return apierrors.NewBadRequest("invalid request body")
	}
	if ok, err := h.enforceAuthRateLimit(c, authRecoveryWindow,
		middleware.AuthEmailRateDecisionForValue("auth:resend:email", authRecoveryEmailLimit, req.Email)); err != nil || !ok {
		return err
	}
	if req.Email == "" {
		return apierrors.NewBadRequest("email is required")
	}

	verificationToken, err := h.deps.Services.Auth.StartEmailVerification(c.Context(), req.Email)
	if err != nil {
		return apierrors.NewInternal(err.Error())
	}

	resp := fiber.Map{"status": "ok"}
	if verificationToken != "" {
		resp["email_verification_token"] = verificationToken
	}
	return c.JSON(resp)
}

func (h *Handlers) VerifyEmail(c fiber.Ctx) error {
	var req verifyEmailRequest
	if err := json.Unmarshal(c.Body(), &req); err != nil {
		return apierrors.NewBadRequest("invalid request body")
	}

	if err := h.deps.Services.Auth.VerifyEmail(c.Context(), req.Token); err != nil {
		switch err {
		case service.ErrVerifyTokenRequired:
			return apierrors.NewBadRequest(err.Error())
		case service.ErrEmailVerifyInvalid, service.ErrUserInactive:
			return apierrors.New(apierrors.CodeInvalidVerificationToken, err.Error())
		default:
			return apierrors.NewInternal(err.Error())
		}
	}

	return c.JSON(fiber.Map{"status": "ok"})
}

func (h *Handlers) ForgotPassword(c fiber.Ctx) error {
	var req forgotPasswordRequest
	if err := json.Unmarshal(c.Body(), &req); err != nil {
		return apierrors.NewBadRequest("invalid request body")
	}
	if ok, err := h.enforceAuthRateLimit(c, authRecoveryWindow,
		middleware.AuthEmailRateDecisionForValue("auth:forgot:email", authRecoveryEmailLimit, req.Email)); err != nil || !ok {
		return err
	}
	if req.Email == "" {
		return apierrors.NewBadRequest("email is required")
	}

	resetToken, err := h.deps.Services.Auth.StartPasswordReset(c.Context(), req.Email)
	if err != nil {
		return apierrors.NewInternal(err.Error())
	}

	resp := fiber.Map{"status": "ok"}
	if resetToken != "" {
		resp["reset_token"] = resetToken
	}
	return c.JSON(resp)
}

func (h *Handlers) ResetPassword(c fiber.Ctx) error {
	var req resetPasswordRequest
	if err := json.Unmarshal(c.Body(), &req); err != nil {
		return apierrors.NewBadRequest("invalid request body")
	}
	if ok, err := h.enforceAuthRateLimit(c, rateLimitWindow,
		middleware.AuthTokenRateDecisionForValue("auth:reset:token", authResetTokenLimit, req.Token)); err != nil || !ok {
		return err
	}

	if err := h.deps.Services.Auth.ResetPassword(c.Context(), service.ResetPasswordInput{
		Token:       req.Token,
		NewPassword: req.NewPassword,
	}); err != nil {
		switch err {
		case service.ErrResetTokenRequired, service.ErrNewPasswordRequired, service.ErrWeakPassword:
			return apierrors.NewBadRequest(err.Error())
		case service.ErrPasswordResetInvalid, service.ErrUserInactive:
			return apierrors.New(apierrors.CodeInvalidResetToken, err.Error())
		default:
			return apierrors.NewInternal(err.Error())
		}
	}

	return c.JSON(fiber.Map{"status": "ok"})
}

// ── Bundles ─────────────────────────────────────────────────

func (h *Handlers) UploadBundle(c fiber.Ctx) error {
	userID := auth.UserID(c)

	// Parse multipart form
	form, err := c.MultipartForm()
	if err != nil {
		return apierrors.NewBadRequest("invalid multipart form")
	}

	// Get the file
	files := form.File["bundle"]
	if len(files) == 0 {
		return apierrors.NewBadRequest("missing 'bundle' file field")
	}
	file := files[0]

	src, err := file.Open()
	if err != nil {
		return apierrors.NewInternal("failed to open uploaded file")
	}
	defer src.Close()

	// Parse metadata fields
	bundleID, err := requiredFormValue(form.Value, "bundle_id")
	if err != nil {
		return err
	}
	deviceUUID, err := parseFormUUID(form.Value, "device_uuid")
	if err != nil {
		return err
	}
	lamportLo, err := parseFormInt(form.Value, "lamport_lo", 64)
	if err != nil {
		return err
	}
	lamportHi, err := parseFormInt(form.Value, "lamport_hi", 64)
	if err != nil {
		return err
	}
	eventCount, err := parseFormInt(form.Value, "event_count", 32)
	if err != nil {
		return err
	}
	cipherID, err := parseFormInt(form.Value, "cipher_id", 16)
	if err != nil {
		return err
	}
	keyGen, err := parseFormInt(form.Value, "key_generation", 16)
	if err != nil {
		return err
	}

	meta, err := h.deps.Services.Bundle.UploadBundle(c.Context(), userID, service.UploadInput{
		BundleID:      bundleID,
		DeviceUUID:    deviceUUID,
		LamportLo:     lamportLo,
		LamportHi:     lamportHi,
		EventCount:    int32(eventCount),
		SizeBytes:     file.Size,
		CipherID:      int16(cipherID),
		KeyGeneration: int16(keyGen),
		Reader:        src,
		ContentType:   file.Header.Get("Content-Type"),
		RequestID:     middleware.GetRequestID(c),
		TeamID:        strings.TrimSpace(c.Get("X-Team-ID")),
	})
	if err != nil {
		switch err {
		case service.ErrBundleExists:
			return apierrors.New(apierrors.CodeConflict, err.Error())
		case service.ErrQuotaExceeded:
			return apierrors.New(apierrors.CodeQuotaExceeded, err.Error())
		case service.ErrReservationDenied:
			return apierrors.New(apierrors.CodeReservationDenied, err.Error())
		case service.ErrDeviceRevoked:
			return apierrors.New(apierrors.CodeDeviceRevoked, err.Error())
		case service.ErrDeviceNotRegistered:
			return apierrors.New(apierrors.CodeDeviceNotRegistered, err.Error())
		}
		return apierrors.NewInternal(err.Error())
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
		return apierrors.NewInternal(err.Error())
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
		if err == service.ErrBundleNotFound {
			return apierrors.New(apierrors.CodeNotFound, "bundle not found")
		}
		return apierrors.NewInternal(err.Error())
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
		if err == service.ErrBundleNotFound {
			return apierrors.New(apierrors.CodeNotFound, err.Error())
		}
		return apierrors.NewInternal(err.Error())
	}

	return c.JSON(fiber.Map{"status": "deleted"})
}

// ── Snapshots ───────────────────────────────────────────────

func (h *Handlers) UploadSnapshot(c fiber.Ctx) error {
	userID := auth.UserID(c)

	form, err := c.MultipartForm()
	if err != nil {
		return apierrors.NewBadRequest("invalid multipart form")
	}
	files := form.File["snapshot"]
	if len(files) == 0 {
		return apierrors.NewBadRequest("missing 'snapshot' file field")
	}
	file := files[0]
	if len(form.Value["snapshot_id"]) == 0 || len(form.Value["base_hlc"]) == 0 || len(form.Value["cipher_id"]) == 0 || len(form.Value["key_generation"]) == 0 {
		return apierrors.NewBadRequest("missing snapshot metadata fields")
	}

	src, err := file.Open()
	if err != nil {
		return apierrors.NewInternal("failed to open uploaded file")
	}
	defer src.Close()

	snapshotID, err := requiredFormValue(form.Value, "snapshot_id")
	if err != nil {
		return err
	}
	baseHLC, err := parseFormInt(form.Value, "base_hlc", 64)
	if err != nil {
		return err
	}
	cipherID, err := parseFormInt(form.Value, "cipher_id", 16)
	if err != nil {
		return err
	}
	keyGen, err := parseFormInt(form.Value, "key_generation", 16)
	if err != nil {
		return err
	}

	meta, err := h.deps.Services.Snapshot.UploadSnapshot(c.Context(), userID, service.UploadSnapshotInput{
		SnapshotID:    snapshotID,
		BaseHLC:       baseHLC,
		SizeBytes:     file.Size,
		CipherID:      int16(cipherID),
		KeyGeneration: int16(keyGen),
		Reader:        src,
		ContentType:   file.Header.Get("Content-Type"),
		RequestID:     middleware.GetRequestID(c),
		TeamID:        strings.TrimSpace(c.Get("X-Team-ID")),
	})
	if err != nil {
		if err == service.ErrReservationDenied {
			return apierrors.New(apierrors.CodeReservationDenied, err.Error())
		}
		if err == service.ErrQuotaExceeded {
			return apierrors.New(apierrors.CodeQuotaExceeded, err.Error())
		}
		if err == service.ErrUserNotFound {
			return apierrors.New(apierrors.CodeNotFound, err.Error())
		}
		return apierrors.NewInternal(err.Error())
	}

	msg := ws.PushMessage{
		Type:      ws.MsgSnapshotUploaded,
		Timestamp: time.Now().Unix(),
		Data: fiber.Map{
			"snapshot_id": meta.SnapshotID,
			"base_hlc":    meta.BaseHLC,
		},
	}
	if data, err := json.Marshal(msg); err == nil {
		h.deps.Hub.PushToUser(userID, data)
	}

	return c.Status(fiber.StatusCreated).JSON(meta)
}

func (h *Handlers) GetLatestSnapshot(c fiber.Ctx) error {
	userID := auth.UserID(c)
	snapshot, err := h.deps.Services.Repos.Snapshots.GetLatest(c.Context(), userID)
	if err != nil {
		return apierrors.NewInternal(err.Error())
	}
	if snapshot == nil {
		return apierrors.New(apierrors.CodeNotFound, "no snapshot found")
	}
	return c.JSON(snapshot)
}

func (h *Handlers) DownloadSnapshot(c fiber.Ctx) error {
	userID := auth.UserID(c)
	snapshotID := c.Params("id")

	reader, meta, err := h.deps.Services.Snapshot.DownloadSnapshot(c.Context(), userID, snapshotID)
	if err != nil {
		if err == service.ErrSnapshotNotFound {
			return apierrors.New(apierrors.CodeNotFound, "snapshot not found")
		}
		return apierrors.NewInternal(err.Error())
	}
	defer reader.Close()

	c.Set("Content-Type", "application/octet-stream")
	c.Set("Content-Disposition", "attachment; filename=\""+meta.SnapshotID+".hsb\"")
	c.Set("Content-Length", strconv.FormatInt(meta.SizeBytes, 10))
	return c.SendStream(reader)
}

// ── Devices ─────────────────────────────────────────────────

func (h *Handlers) ListDevices(c fiber.Ctx) error {
	userID := auth.UserID(c)
	devices, err := h.deps.Services.Repos.Devices.ListByUser(c.Context(), userID)
	if err != nil {
		return apierrors.NewInternal(err.Error())
	}
	if devices == nil {
		devices = []model.Device{}
	}
	return c.JSON(fiber.Map{"devices": devices})
}

func (h *Handlers) RevokeDevice(c fiber.Ctx) error {
	userID := auth.UserID(c)
	deviceUUID, err := uuid.Parse(c.Params("uuid"))
	if err != nil {
		return apierrors.NewBadRequest("invalid device uuid")
	}

	if err := h.deps.Services.Auth.RevokeDevice(c.Context(), userID, deviceUUID); err != nil {
		switch err {
		case service.ErrDeviceNotFound:
			return apierrors.New(apierrors.CodeNotFound, err.Error())
		case service.ErrDeviceAlreadyRevoked:
			return apierrors.New(apierrors.CodeDeviceRevoked, err.Error())
		default:
			return apierrors.NewInternal(err.Error())
		}
	}

	msg := ws.PushMessage{
		Type:      ws.MsgDeviceRevoked,
		Timestamp: time.Now().Unix(),
		Data: fiber.Map{
			"device_uuid": deviceUUID,
		},
	}
	if data, err := json.Marshal(msg); err == nil {
		h.deps.Hub.PushToUser(userID, data)
	}

	return c.JSON(fiber.Map{"status": "revoked"})
}

func (h *Handlers) ListRevocations(c fiber.Ctx) error {
	userID := auth.UserID(c)
	revs, err := h.deps.Services.Auth.ListRevocations(c.Context(), userID)
	if err != nil {
		return apierrors.NewInternal(err.Error())
	}
	return c.JSON(fiber.Map{"revocations": revs})
}

// ── Quota ───────────────────────────────────────────────────

func (h *Handlers) GetQuota(c fiber.Ctx) error {
	userID := auth.UserID(c)
	tierStr := c.Locals("tier").(string)

	info, err := h.deps.Services.Quota.GetQuota(c.Context(), userID, model.UserTier(tierStr))
	if err != nil {
		return apierrors.NewInternal(err.Error())
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

func (h *Handlers) CreateCheckout(c fiber.Ctx) error {
	userID := auth.UserID(c)

	var req struct {
		PriceID string `json:"price_id"`
	}
	if err := json.Unmarshal(c.Body(), &req); err != nil {
		return apierrors.NewBadRequest("invalid request body")
	}
	if req.PriceID == "" {
		return apierrors.NewBadRequest("price_id is required")
	}

	url, err := h.deps.Services.Billing.CreateCheckoutSession(c.Context(), userID, req.PriceID)
	if err != nil {
		if err == service.ErrStripeDisabled {
			return apierrors.New(apierrors.CodeBillingDisabled, err.Error())
		}
		if err == service.ErrBillingNotSupported {
			return apierrors.New(apierrors.CodeNotImplemented, err.Error())
		}
		return apierrors.NewInternal(err.Error())
	}

	return c.JSON(fiber.Map{"checkout_url": url})
}

func (h *Handlers) CreatePortalSession(c fiber.Ctx) error {
	userID := auth.UserID(c)
	url, err := h.deps.Services.Billing.CreatePortalSession(c.Context(), userID)
	if err != nil {
		if err == service.ErrStripeDisabled {
			return apierrors.New(apierrors.CodeBillingDisabled, err.Error())
		}
		if err == service.ErrBillingNotSupported {
			return apierrors.New(apierrors.CodeNotImplemented, err.Error())
		}
		return apierrors.NewInternal(err.Error())
	}
	return c.JSON(fiber.Map{"portal_url": url})
}

func (h *Handlers) GetSubscription(c fiber.Ctx) error {
	userID := auth.UserID(c)
	result, err := h.deps.Services.Billing.GetSubscription(c.Context(), userID)
	if err != nil {
		if err == service.ErrStripeDisabled {
			return apierrors.New(apierrors.CodeBillingDisabled, err.Error())
		}
		return apierrors.NewInternal(err.Error())
	}
	return c.JSON(result)
}

func (h *Handlers) ListInvoices(c fiber.Ctx) error {
	userID := auth.UserID(c)
	invoices, err := h.deps.Services.Billing.ListInvoices(c.Context(), userID)
	if err != nil {
		if err == service.ErrStripeDisabled {
			return apierrors.New(apierrors.CodeBillingDisabled, err.Error())
		}
		if err == service.ErrBillingNotSupported {
			return apierrors.New(apierrors.CodeNotImplemented, err.Error())
		}
		return apierrors.NewInternal(err.Error())
	}
	return c.JSON(fiber.Map{"invoices": invoices})
}

func (h *Handlers) StripeWebhook(c fiber.Ctx) error {
	if err := h.deps.Services.Billing.HandleWebhook(c.Context(), c.Body(), c.Get("Stripe-Signature")); err != nil {
		if err == service.ErrStripeDisabled {
			return apierrors.New(apierrors.CodeBillingDisabled, err.Error())
		}
		if err == service.ErrBillingNotSupported {
			return apierrors.New(apierrors.CodeNotImplemented, err.Error())
		}
		return apierrors.NewBadRequest(err.Error())
	}
	return c.JSON(fiber.Map{"status": "ok"})
}

// ── WebSocket ───────────────────────────────────────────────

func (h *Handlers) WebSocketUpgrade(c fiber.Ctx) error {
	return h.deps.Hub.UpgradeHandler(c)
}

// ── Admin ───────────────────────────────────────────────────

func (h *Handlers) AdminListUsers(c fiber.Ctx) error {
	limit := int32(50)
	if l, err := strconv.Atoi(c.Query("limit", "50")); err == nil && l > 0 && l <= 200 {
		limit = int32(l)
	}
	offset := int32(0)
	if o, err := strconv.Atoi(c.Query("offset", "0")); err == nil && o > 0 {
		offset = int32(o)
	}

	users, err := h.deps.Services.Repos.Users.List(c.Context(), limit, offset)
	if err != nil {
		return apierrors.NewInternal(err.Error())
	}
	if users == nil {
		users = []model.User{}
	}
	total, err := h.deps.Services.Repos.Users.Count(c.Context())
	if err != nil {
		return apierrors.NewInternal(err.Error())
	}

	return c.JSON(fiber.Map{
		"users":  users,
		"total":  total,
		"limit":  limit,
		"offset": offset,
	})
}

func (h *Handlers) AdminStats(c fiber.Ctx) error {
	userCount, err := h.deps.Services.Repos.Users.Count(c.Context())
	if err != nil {
		return apierrors.NewInternal(err.Error())
	}
	usersByStatus, err := h.deps.Services.Repos.Users.CountByStatus(c.Context())
	if err != nil {
		return apierrors.NewInternal(err.Error())
	}
	deviceCount, err := h.deps.Services.Repos.Devices.CountActive(c.Context())
	if err != nil {
		return apierrors.NewInternal(err.Error())
	}
	bundleCount, err := h.deps.Services.Repos.Bundles.CountAll(c.Context())
	if err != nil {
		return apierrors.NewInternal(err.Error())
	}
	bundleBytes, err := h.deps.Services.Repos.Bundles.SumSizeAll(c.Context())
	if err != nil {
		return apierrors.NewInternal(err.Error())
	}
	snapshotCount, err := h.deps.Services.Repos.Snapshots.CountAll(c.Context())
	if err != nil {
		return apierrors.NewInternal(err.Error())
	}
	snapshotBytes, err := h.deps.Services.Repos.Snapshots.SumSizeAll(c.Context())
	if err != nil {
		return apierrors.NewInternal(err.Error())
	}

	return c.JSON(fiber.Map{
		"users": fiber.Map{
			"total":     userCount,
			"by_status": usersByStatus,
		},
		"devices": fiber.Map{
			"active": deviceCount,
		},
		"storage": fiber.Map{
			"total_bytes":    bundleBytes + snapshotBytes,
			"bundle_bytes":   bundleBytes,
			"snapshot_bytes": snapshotBytes,
		},
		"bundles": fiber.Map{
			"total": bundleCount,
		},
		"snapshots": fiber.Map{
			"total": snapshotCount,
		},
		"websocket": fiber.Map{
			"active_users":       h.deps.Hub.ActiveUserCount(),
			"active_connections": h.deps.Hub.ActiveConnectionCount(),
		},
	})
}

// AdminRecalculateQuota recomputes a user's storage usage counters from their
// authoritative bundle and snapshot rows, correcting any drift. It returns the
// usage before and after so an operator can see the size of the correction.
func (h *Handlers) AdminRecalculateQuota(c fiber.Ctx) error {
	userID, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return apierrors.New(apierrors.CodeInvalidUserID, "invalid user id")
	}

	result, err := h.deps.Services.Quota.RecalculateUsage(c.Context(), userID)
	if err != nil {
		if err == service.ErrUserNotFound {
			return apierrors.New(apierrors.CodeUserNotFound, "user not found")
		}
		return apierrors.NewInternal(err.Error())
	}

	return c.JSON(fiber.Map{
		"user_id": userID,
		"before":  result.Before,
		"after":   result.After,
	})
}

// ── Dynamic Options ──────────────────────────────────────────

// AdminListOptions returns all runtime-configurable key-value pairs. When the
// OptionStore is not configured the response is an empty object.
func (h *Handlers) AdminListOptions(c fiber.Ctx) error {
	if h.deps.OptionStore == nil {
		return c.JSON(fiber.Map{"options": fiber.Map{}})
	}
	return c.JSON(fiber.Map{"options": h.deps.OptionStore.All()})
}

type setOptionRequest struct {
	Value string `json:"value"`
}

// AdminSetOption writes a runtime-configurable option. When the OptionStore is
// not configured it returns 501 Not Implemented.
func (h *Handlers) AdminSetOption(c fiber.Ctx) error {
	if h.deps.OptionStore == nil {
		return apierrors.New(apierrors.CodeOptionsDisabled, "runtime options are not configured; set options_file in config")
	}
	key := c.Params("key")
	if key == "" {
		return apierrors.New(apierrors.CodeMissingKey, "option key is required")
	}

	var req setOptionRequest
	if err := json.Unmarshal(c.Body(), &req); err != nil {
		return apierrors.New(apierrors.CodeInvalidJSON, "request body must be a JSON object with a \"value\" field")
	}

	if err := h.deps.OptionStore.Set(key, req.Value); err != nil {
		return apierrors.NewInternal(err.Error())
	}
	return c.JSON(fiber.Map{"key": key, "value": req.Value})
}

// AdminErrorCodes returns the full catalog of registered API error codes as a
// reference document for client developers. Each entry includes the code string,
// default HTTP status, and the English fallback message.
func (h *Handlers) AdminErrorCodes(c fiber.Ctx) error {
	return c.JSON(fiber.Map{"error_codes": apierrors.All()})
}
