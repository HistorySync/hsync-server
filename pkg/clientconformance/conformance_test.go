//go:build conformance

package clientconformance

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pquerna/otp"
	"github.com/pquerna/otp/totp"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
	"gopkg.in/yaml.v3"

	"github.com/historysync/hsync-server/migrations"
	"github.com/historysync/hsync-server/pkg/apierrors"
	"github.com/historysync/hsync-server/pkg/auth"
	"github.com/historysync/hsync-server/pkg/buildinfo"
	"github.com/historysync/hsync-server/pkg/config"
	"github.com/historysync/hsync-server/pkg/handler"
	"github.com/historysync/hsync-server/pkg/middleware"
	"github.com/historysync/hsync-server/pkg/migrate"
	"github.com/historysync/hsync-server/pkg/model"
	"github.com/historysync/hsync-server/pkg/observability"
	"github.com/historysync/hsync-server/pkg/repository"
	"github.com/historysync/hsync-server/pkg/service"
	"github.com/historysync/hsync-server/pkg/storage"
	"github.com/historysync/hsync-server/pkg/ws"
)

const (
	conformanceTurnstileToken = "conformance-turnstile-ok"
	turnstileModeFake         = "fake"
	turnstileModeSkip         = "skip"
	passkeyModeDisabled       = "disabled"
	passkeyModeFake           = "fake"
	passkeyModeSkip           = "skip"
	twoFactorModeReal         = "real"
	twoFactorModeFake         = "fake"
	twoFactorModeSkip         = "skip"
)

type suiteConfig struct {
	turnstileMode string
	passkeyMode   string
	twoFactorMode string
}

type harness struct {
	baseURL      string
	wsURL        string
	httpClient   *http.Client
	tokenManager *auth.TokenManager
	pool         *pgxpool.Pool
	app          *fiber.App
	listener     net.Listener
	openAPI      openAPIDocument
	catalog      map[string]apierrors.Entry
}

type session struct {
	userID       uuid.UUID
	email        string
	password     string
	accessToken  string
	refreshToken string
	secret       string
	deviceUUID   uuid.UUID
	deviceToken  string
}

type authTokenPair struct {
	User struct {
		ID string `json:"id"`
	} `json:"user"`
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int64  `json:"expires_in"`
}

type login2FAChallenge struct {
	RequiresTwoFactor bool   `json:"requires_2fa"`
	Challenge         string `json:"challenge"`
	ExpiresIn         int64  `json:"expires_in"`
}

type verificationResult struct {
	VerificationToken string `json:"verification_token"`
	ExpiresIn         int64  `json:"expires_in"`
	Method            string `json:"method"`
}

type deviceTokenResponse struct {
	DeviceToken string `json:"device_token"`
	ExpiresIn   int    `json:"expires_in"`
	Device      struct {
		DeviceUUID string `json:"device_uuid"`
	} `json:"device"`
}

type bundleListResponse struct {
	Bundles []model.BundleMeta `json:"bundles"`
}

type revocationListResponse struct {
	Revocations []model.DeviceRevocation `json:"revocations"`
}

type pushMessage struct {
	Type      string         `json:"type"`
	Data      map[string]any `json:"data"`
	Timestamp int64          `json:"ts"`
}

type errorEnvelope struct {
	RequestID string `json:"request_id"`
	Error     struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

type openAPIDocument struct {
	Paths      map[string]map[string]any `yaml:"paths"`
	Components struct {
		Responses map[string]any `yaml:"responses"`
	} `yaml:"components"`
}

type fakeTurnstileVerifier struct {
	acceptedToken string
}

type memoryBlobStore struct {
	mu      sync.RWMutex
	objects map[string][]byte
}

func TestCEClientConformanceSuite(t *testing.T) {
	cfg := loadSuiteConfig()
	h := newHarness(t, cfg)

	t.Run("negative_errors", func(t *testing.T) {
		if cfg.turnstileMode != turnstileModeSkip {
			testRegisterRejectsInvalidTurnstile(t, h)
			testLoginRequiresTurnstile(t, h)
			testLoginRejectsInvalidTurnstile(t, h)
		}
		testRefreshRejectsInvalidToken(t, h)
		testBundleUploadRequiresFile(t, h)
		testBundleUploadRejectsUnregisteredDevice(t, h)
		testDeviceTokenRejectsInvalidUUID(t, h)
		testStepUpRejectsInvalidVerificationToken(t, h)
		if cfg.twoFactorMode != twoFactorModeSkip {
			testVerifyRejectsUnsupportedMethod(t, h)
		}
		if cfg.passkeyMode == passkeyModeDisabled {
			testPasskeyLoginDisabled(t, h)
		}
	})

	t.Run("sync_flow", func(t *testing.T) {
		s := registerAndLogin(t, h, "sync")
		assertOpenAPIStatus(t, h, "/api/v1/auth/register", http.MethodPost, http.StatusCreated)
		assertOpenAPIStatus(t, h, "/api/v1/auth/login", http.MethodPost, http.StatusOK)
		testRefreshSucceeds(t, h, s)
		assertOpenAPIStatus(t, h, "/api/v1/auth/refresh", http.MethodPost, http.StatusOK)

		deviceResp := requestDeviceToken(t, h, s)
		assertOpenAPIStatus(t, h, "/api/v1/devices/{uuid}/token", http.MethodPost, http.StatusOK)
		if got, want := deviceResp.ExpiresIn, int((24*time.Hour)/time.Second); got != want {
			t.Fatalf("device token expires_in = %d, want %d", got, want)
		}
		s.deviceToken = deviceResp.DeviceToken

		conn := dialWebSocket(t, h, s.deviceToken)
		defer conn.Close()

		listDevicesAndAssert(t, h, s, s.deviceUUID.String())
		assertOpenAPIStatus(t, h, "/api/v1/devices", http.MethodGet, http.StatusOK)

		bundleBytes := []byte("encrypted bundle bytes")
		uploadBundle(t, h, s, "bundle-1", bundleBytes)
		assertOpenAPIStatus(t, h, "/api/v1/bundles", http.MethodPost, http.StatusCreated)
		assertPushMessage(t, conn, ws.MsgBundleUploaded, map[string]string{
			"bundle_id": "bundle-1",
		})
		listBundlesAndAssert(t, h, s, "bundle-1")
		assertOpenAPIStatus(t, h, "/api/v1/bundles", http.MethodGet, http.StatusOK)
		downloadBundleAndAssert(t, h, s, "bundle-1", bundleBytes)
		assertOpenAPIStatus(t, h, "/api/v1/bundles/{id}", http.MethodGet, http.StatusOK)

		snapshotBytes := []byte("encrypted snapshot bytes")
		uploadSnapshot(t, h, s, "snapshot-1", snapshotBytes)
		assertOpenAPIStatus(t, h, "/api/v1/snapshots", http.MethodPost, http.StatusCreated)
		assertPushMessage(t, conn, ws.MsgSnapshotUploaded, map[string]string{
			"snapshot_id": "snapshot-1",
		})
		getLatestSnapshotAndAssert(t, h, s, "snapshot-1")
		assertOpenAPIStatus(t, h, "/api/v1/snapshots/latest", http.MethodGet, http.StatusOK)
		downloadSnapshotAndAssert(t, h, s, "snapshot-1", snapshotBytes)
		assertOpenAPIStatus(t, h, "/api/v1/snapshots/{id}", http.MethodGet, http.StatusOK)

		stepUpToken := issueStepUpToken(t, h, s, cfg.twoFactorMode)

		expectError(t, h, http.MethodPost, "/api/v1/devices/{uuid}/revoke",
			"/api/v1/devices/"+url.PathEscape(s.deviceUUID.String())+"/revoke",
			map[string]any{}, bearerHeaders(s.accessToken), http.StatusForbidden, apierrors.CodeStepUpRequired)
		assertOpenAPIStatus(t, h, "/api/v1/devices/{uuid}/revoke", http.MethodPost, http.StatusForbidden)

		revokeDevice(t, h, s, stepUpToken)
		assertPushMessage(t, conn, ws.MsgDeviceRevoked, map[string]string{
			"device_uuid": s.deviceUUID.String(),
		})
		listRevocationsAndAssert(t, h, s)
		assertOpenAPIStatus(t, h, "/api/v1/devices/revocations", http.MethodGet, http.StatusOK)
		expectError(t, h, http.MethodPost, "/api/v1/devices/{uuid}/token",
			"/api/v1/devices/"+url.PathEscape(s.deviceUUID.String())+"/token",
			map[string]any{"platform": "desktop"}, bearerHeaders(s.accessToken),
			http.StatusForbidden, apierrors.CodeDeviceRevoked)
	})
}

func loadSuiteConfig() suiteConfig {
	return suiteConfig{
		turnstileMode: normalizeMode(os.Getenv("HSYNC_CONFORMANCE_TURNSTILE_MODE"), turnstileModeFake, turnstileModeFake, turnstileModeSkip),
		passkeyMode:   normalizeMode(os.Getenv("HSYNC_CONFORMANCE_PASSKEY_MODE"), passkeyModeDisabled, passkeyModeDisabled, passkeyModeFake, passkeyModeSkip),
		twoFactorMode: normalizeMode(os.Getenv("HSYNC_CONFORMANCE_2FA_MODE"), twoFactorModeReal, twoFactorModeReal, twoFactorModeFake, twoFactorModeSkip),
	}
}

func normalizeMode(raw, fallback string, allowed ...string) string {
	value := strings.ToLower(strings.TrimSpace(raw))
	if value == "" {
		return fallback
	}
	for _, candidate := range allowed {
		if value == candidate {
			return value
		}
	}
	return fallback
}

func newHarness(t *testing.T, suiteCfg suiteConfig) *harness {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	pool := newPostgresPool(t, ctx)
	applyAllMigrations(t, ctx, pool)

	serverCfg := config.DefaultConfig()
	serverCfg.JWTPrivateKey = base64.StdEncoding.EncodeToString(make([]byte, ed25519.SeedSize))
	serverCfg.SecuritySecret = base64.StdEncoding.EncodeToString(make([]byte, 32))
	serverCfg.StripeDisabled = true
	serverCfg.BackgroundTasksEnabled = false

	repos := repository.New(pool, nil)
	tokenManager, err := auth.NewTokenManager(serverCfg.JWTPrivateKey, auth.TokenConfig{
		AccessTTL:  15 * time.Minute,
		RefreshTTL: 30 * 24 * time.Hour,
	})
	if err != nil {
		t.Fatalf("token manager: %v", err)
	}

	blobStore := &memoryBlobStore{objects: map[string][]byte{}}
	services := service.New(service.Deps{
		Repos:          repos,
		TokenManager:   tokenManager,
		BlobStore:      blobStore,
		StripeDisabled: true,
		SecuritySecret: serverCfg.SecuritySecret,
		Config:         serverCfg,
		DatabasePing:   pool.Ping,
	})
	hub := ws.NewHubWithOptions(repos.Devices, ws.Options{OriginCheckDisabled: true})
	go hub.Run()

	fakeTurnstile := &fakeTurnstileVerifier{acceptedToken: conformanceTurnstileToken}
	h := handler.New(handler.Deps{
		Services:     services,
		TokenManager: tokenManager,
		Hub:          hub,
		DB:           pool,
		BlobStore:    blobStore,
		AdminKey:     "conformance-admin-key",
		BuildInfo:    buildinfo.WithEdition("community"),
		RateLimiter:  middleware.NewMemoryLimiter(),
		Turnstile: middleware.TurnstileConfig{
			Enabled:  suiteCfg.turnstileMode != turnstileModeSkip,
			Verifier: fakeTurnstile,
		},
	})

	app := fiber.New(fiber.Config{
		AppName:      "HistorySync Cloud Server",
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 60 * time.Second,
		IdleTimeout:  120 * time.Second,
		BodyLimit:    55 * 1024 * 1024,
		ErrorHandler: h.ErrorHandler,
	})
	app.Use(middleware.RequestID())
	app.Use(observability.HTTPMetricsMiddleware())
	h.RegisterRoutes(app)

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	errCh := make(chan error, 1)
	go func() {
		errCh <- app.Listener(listener, fiber.ListenConfig{DisableStartupMessage: true})
	}()

	baseURL := "http://" + listener.Addr().String()
	waitForHealth(t, baseURL)

	t.Cleanup(func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = app.ShutdownWithContext(shutdownCtx)
		_ = listener.Close()
		select {
		case err := <-errCh:
			if err != nil && !errors.Is(err, net.ErrClosed) && !strings.Contains(err.Error(), "use of closed network connection") {
				t.Fatalf("fiber listener: %v", err)
			}
		case <-time.After(2 * time.Second):
		}
		pool.Close()
	})

	return &harness{
		baseURL:      baseURL,
		wsURL:        strings.Replace(baseURL, "http://", "ws://", 1) + "/ws/push",
		httpClient:   &http.Client{Timeout: 15 * time.Second},
		tokenManager: tokenManager,
		pool:         pool,
		app:          app,
		listener:     listener,
		openAPI:      loadOpenAPI(t),
		catalog:      loadCatalog(),
	}
}

func newPostgresPool(t *testing.T, ctx context.Context) *pgxpool.Pool {
	t.Helper()

	container, err := postgres.Run(ctx,
		"postgres:16-alpine",
		postgres.WithDatabase("hsync"),
		postgres.WithUsername("hsync"),
		postgres.WithPassword("hsync"),
		testcontainers.WithWaitStrategy(
			wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2).
				WithStartupTimeout(60*time.Second),
		),
	)
	if err != nil {
		t.Skipf("client conformance tests require a Docker environment for PostgreSQL: %v", err)
	}
	t.Cleanup(func() {
		_ = container.Terminate(context.Background())
	})

	dsn, err := container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatalf("postgres connection string: %v", err)
	}
	pool, err := repository.NewPGXPool(ctx, dsn)
	if err != nil {
		t.Fatalf("connect postgres: %v", err)
	}
	return pool
}

func applyAllMigrations(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	if _, err := migrate.Up(ctx, pool, migrations.FS); err != nil {
		t.Fatalf("apply migrations: %v", err)
	}
}

func waitForHealth(t *testing.T, baseURL string) {
	t.Helper()

	client := &http.Client{Timeout: 2 * time.Second}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		req, err := http.NewRequest(http.MethodGet, baseURL+"/healthz", nil)
		if err != nil {
			t.Fatalf("new request: %v", err)
		}
		resp, err := client.Do(req)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("server %s did not become healthy", baseURL)
}

func testLoginRequiresTurnstile(t *testing.T, h *harness) {
	expectError(t, h, http.MethodPost, "/api/v1/auth/login", "/api/v1/auth/login", map[string]any{
		"email":    "missing@example.com",
		"password": "password-12345",
	}, nil, http.StatusBadRequest, apierrors.CodeTurnstileRequired)
}

func testRegisterRejectsInvalidTurnstile(t *testing.T, h *harness) {
	expectError(t, h, http.MethodPost, "/api/v1/auth/register", "/api/v1/auth/register", map[string]any{
		"email":           "missing@example.com",
		"password":        "password-12345",
		"display_name":    "Conformance User",
		"turnstile_token": "invalid-turnstile",
	}, nil, http.StatusForbidden, apierrors.CodeTurnstileFailed)
}

func testLoginRejectsInvalidTurnstile(t *testing.T, h *harness) {
	expectError(t, h, http.MethodPost, "/api/v1/auth/login", "/api/v1/auth/login", map[string]any{
		"email":           "missing@example.com",
		"password":        "password-12345",
		"turnstile_token": "invalid-turnstile",
	}, nil, http.StatusForbidden, apierrors.CodeTurnstileFailed)
}

func testRefreshRejectsInvalidToken(t *testing.T, h *harness) {
	expectError(t, h, http.MethodPost, "/api/v1/auth/refresh", "/api/v1/auth/refresh", map[string]any{
		"refresh_token": "not-a-real-token",
	}, nil, http.StatusUnauthorized, apierrors.CodeInvalidRefreshToken)
}

func testBundleUploadRequiresFile(t *testing.T, h *harness) {
	s := registerAndLogin(t, h, "bundle-negative")
	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)
	mustWriteField(t, writer, "bundle_id", "missing-file")
	mustWriteField(t, writer, "device_uuid", uuid.NewString())
	mustWriteField(t, writer, "lamport_lo", "1")
	mustWriteField(t, writer, "lamport_hi", "2")
	mustWriteField(t, writer, "event_count", "1")
	mustWriteField(t, writer, "cipher_id", "1")
	mustWriteField(t, writer, "key_generation", "1")
	if err := writer.Close(); err != nil {
		t.Fatalf("close multipart writer: %v", err)
	}
	headers := bearerHeaders(s.accessToken)
	headers.Set("Content-Type", writer.FormDataContentType())
	expectRawError(t, h, http.MethodPost, "/api/v1/bundles", "/api/v1/bundles", body, headers, http.StatusBadRequest, apierrors.CodeBadRequest)
}

func testBundleUploadRejectsUnregisteredDevice(t *testing.T, h *harness) {
	s := registerAndLogin(t, h, "bundle-device-negative")
	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)
	mustWriteField(t, writer, "bundle_id", "unregistered-device")
	mustWriteField(t, writer, "device_uuid", uuid.NewString())
	mustWriteField(t, writer, "lamport_lo", "1")
	mustWriteField(t, writer, "lamport_hi", "2")
	mustWriteField(t, writer, "event_count", "1")
	mustWriteField(t, writer, "cipher_id", "1")
	mustWriteField(t, writer, "key_generation", "1")
	part, err := writer.CreateFormFile("bundle", "unregistered-device.hsb")
	if err != nil {
		t.Fatalf("create bundle part: %v", err)
	}
	if _, err := part.Write([]byte("encrypted bundle bytes")); err != nil {
		t.Fatalf("write bundle part: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close multipart writer: %v", err)
	}
	headers := bearerHeaders(s.accessToken)
	headers.Set("Content-Type", writer.FormDataContentType())
	expectRawError(t, h, http.MethodPost, "/api/v1/bundles", "/api/v1/bundles", body, headers, http.StatusBadRequest, apierrors.CodeDeviceNotRegistered)
}

func testDeviceTokenRejectsInvalidUUID(t *testing.T, h *harness) {
	s := registerAndLogin(t, h, "device-token-negative")
	expectError(t, h, http.MethodPost, "/api/v1/devices/{uuid}/token", "/api/v1/devices/not-a-uuid/token",
		map[string]any{"platform": "desktop"}, bearerHeaders(s.accessToken), http.StatusBadRequest, apierrors.CodeBadRequest)
}

func testStepUpRejectsInvalidVerificationToken(t *testing.T, h *harness) {
	s := registerAndLogin(t, h, "stepup-negative")
	headers := bearerHeaders(s.accessToken)
	headers.Set(auth.StepUpHeader, "not-a-real-step-up-token")
	expectRawError(t, h, http.MethodPost, "/api/v1/devices/{uuid}/revoke", "/api/v1/devices/"+url.PathEscape(s.deviceUUID.String())+"/revoke",
		bytes.NewReader([]byte(`{}`)), headers, http.StatusForbidden, apierrors.CodeStepUpInvalid)
}

func testVerifyRejectsUnsupportedMethod(t *testing.T, h *harness) {
	s := registerAndLogin(t, h, "verify-negative")
	expectError(t, h, http.MethodPost, "/api/v1/auth/verify", "/api/v1/auth/verify", map[string]any{
		"method": "passkey",
		"code":   "123456",
	}, bearerHeaders(s.accessToken), http.StatusBadRequest, apierrors.CodeBadRequest)
}

func testPasskeyLoginDisabled(t *testing.T, h *harness) {
	expectError(t, h, http.MethodPost, "/api/v1/auth/passkeys/login/begin", "/api/v1/auth/passkeys/login/begin",
		nil, nil, http.StatusForbidden, apierrors.CodePasskeyDisabled)
}

func registerAndLogin(t *testing.T, h *harness, prefix string) *session {
	t.Helper()

	email := fmt.Sprintf("%s-%d@example.com", prefix, time.Now().UnixNano())
	password := "historysync-password-123"

	var registered authTokenPair
	postJSON(t, h, http.MethodPost, "/api/v1/auth/register", map[string]any{
		"email":           email,
		"password":        password,
		"display_name":    "Conformance User",
		"turnstile_token": conformanceTurnstileToken,
	}, nil, http.StatusCreated, &registered)

	login := loginWithPassword(t, h, email, password)
	userID, err := uuid.Parse(login.User.ID)
	if err != nil {
		t.Fatalf("parse user id: %v", err)
	}

	return &session{
		userID:       userID,
		email:        email,
		password:     password,
		accessToken:  login.AccessToken,
		refreshToken: login.RefreshToken,
		deviceUUID:   uuid.New(),
	}
}

func loginWithPassword(t *testing.T, h *harness, email, password string) authTokenPair {
	t.Helper()

	var login authTokenPair
	postJSON(t, h, http.MethodPost, "/api/v1/auth/login", map[string]any{
		"email":           email,
		"password":        password,
		"turnstile_token": conformanceTurnstileToken,
	}, nil, http.StatusOK, &login)
	return login
}

func testRefreshSucceeds(t *testing.T, h *harness, s *session) {
	t.Helper()
	var resp struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int64  `json:"expires_in"`
	}
	postJSON(t, h, http.MethodPost, "/api/v1/auth/refresh", map[string]any{
		"refresh_token": s.refreshToken,
	}, nil, http.StatusOK, &resp)
	if resp.AccessToken == "" {
		t.Fatal("refresh access token is empty")
	}
	if resp.ExpiresIn <= 0 {
		t.Fatalf("refresh expires_in = %d, want > 0", resp.ExpiresIn)
	}
	s.accessToken = resp.AccessToken
}

func requestDeviceToken(t *testing.T, h *harness, s *session) deviceTokenResponse {
	t.Helper()

	var resp deviceTokenResponse
	postJSON(t, h, http.MethodPost, "/api/v1/devices/"+url.PathEscape(s.deviceUUID.String())+"/token", map[string]any{
		"device_name": "Conformance Desktop",
		"platform":    "desktop",
		"app_version": "test",
	}, bearerHeaders(s.accessToken), http.StatusOK, &resp)
	if resp.DeviceToken == "" {
		t.Fatal("device token is empty")
	}
	if resp.Device.DeviceUUID != s.deviceUUID.String() {
		t.Fatalf("device uuid = %q, want %q", resp.Device.DeviceUUID, s.deviceUUID)
	}
	return resp
}

func uploadBundle(t *testing.T, h *harness, s *session, bundleID string, payload []byte) {
	t.Helper()

	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)
	mustWriteField(t, writer, "bundle_id", bundleID)
	mustWriteField(t, writer, "device_uuid", s.deviceUUID.String())
	mustWriteField(t, writer, "lamport_lo", "1")
	mustWriteField(t, writer, "lamport_hi", "2")
	mustWriteField(t, writer, "event_count", "1")
	mustWriteField(t, writer, "cipher_id", "1")
	mustWriteField(t, writer, "key_generation", "1")
	part, err := writer.CreateFormFile("bundle", bundleID+".hsb")
	if err != nil {
		t.Fatalf("create bundle part: %v", err)
	}
	if _, err := part.Write(payload); err != nil {
		t.Fatalf("write bundle part: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close bundle multipart writer: %v", err)
	}

	headers := bearerHeaders(s.accessToken)
	headers.Set("Content-Type", writer.FormDataContentType())

	var meta model.BundleMeta
	doRequest(t, h, http.MethodPost, "/api/v1/bundles", body, headers, http.StatusCreated, &meta)
	if meta.BundleID != bundleID {
		t.Fatalf("bundle id = %q, want %q", meta.BundleID, bundleID)
	}
}

func listBundlesAndAssert(t *testing.T, h *harness, s *session, wantBundleID string) {
	t.Helper()
	var list bundleListResponse
	doRequest(t, h, http.MethodGet, "/api/v1/bundles?limit=50", nil, bearerHeaders(s.accessToken), http.StatusOK, &list)
	if len(list.Bundles) == 0 {
		t.Fatal("bundle list is empty")
	}
	if list.Bundles[0].BundleID != wantBundleID {
		t.Fatalf("first bundle id = %q, want %q", list.Bundles[0].BundleID, wantBundleID)
	}
}

func listDevicesAndAssert(t *testing.T, h *harness, s *session, wantDeviceUUID string) {
	t.Helper()
	var resp struct {
		Devices []model.Device `json:"devices"`
	}
	doRequest(t, h, http.MethodGet, "/api/v1/devices", nil, bearerHeaders(s.accessToken), http.StatusOK, &resp)
	if len(resp.Devices) == 0 {
		t.Fatal("device list is empty")
	}
	if resp.Devices[0].DeviceUUID.String() != wantDeviceUUID {
		t.Fatalf("first device uuid = %q, want %q", resp.Devices[0].DeviceUUID, wantDeviceUUID)
	}
}

func downloadBundleAndAssert(t *testing.T, h *harness, s *session, bundleID string, want []byte) {
	t.Helper()
	body := doRawRequest(t, h, http.MethodGet, "/api/v1/bundles/"+url.PathEscape(bundleID), nil, bearerHeaders(s.accessToken), http.StatusOK)
	if !bytes.Equal(body, want) {
		t.Fatalf("downloaded bundle bytes = %q, want %q", string(body), string(want))
	}
}

func uploadSnapshot(t *testing.T, h *harness, s *session, snapshotID string, payload []byte) {
	t.Helper()

	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)
	mustWriteField(t, writer, "snapshot_id", snapshotID)
	mustWriteField(t, writer, "base_hlc", "42")
	mustWriteField(t, writer, "cipher_id", "1")
	mustWriteField(t, writer, "key_generation", "1")
	part, err := writer.CreateFormFile("snapshot", snapshotID+".hsb")
	if err != nil {
		t.Fatalf("create snapshot part: %v", err)
	}
	if _, err := part.Write(payload); err != nil {
		t.Fatalf("write snapshot part: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close snapshot multipart writer: %v", err)
	}

	headers := bearerHeaders(s.accessToken)
	headers.Set("Content-Type", writer.FormDataContentType())

	var meta model.SnapshotMeta
	doRequest(t, h, http.MethodPost, "/api/v1/snapshots", body, headers, http.StatusCreated, &meta)
	if meta.SnapshotID != snapshotID {
		t.Fatalf("snapshot id = %q, want %q", meta.SnapshotID, snapshotID)
	}
}

func getLatestSnapshotAndAssert(t *testing.T, h *harness, s *session, wantSnapshotID string) {
	t.Helper()
	var meta model.SnapshotMeta
	doRequest(t, h, http.MethodGet, "/api/v1/snapshots/latest", nil, bearerHeaders(s.accessToken), http.StatusOK, &meta)
	if meta.SnapshotID != wantSnapshotID {
		t.Fatalf("latest snapshot id = %q, want %q", meta.SnapshotID, wantSnapshotID)
	}
}

func downloadSnapshotAndAssert(t *testing.T, h *harness, s *session, snapshotID string, want []byte) {
	t.Helper()
	body := doRawRequest(t, h, http.MethodGet, "/api/v1/snapshots/"+url.PathEscape(snapshotID), nil, bearerHeaders(s.accessToken), http.StatusOK)
	if !bytes.Equal(body, want) {
		t.Fatalf("downloaded snapshot bytes = %q, want %q", string(body), string(want))
	}
}

func issueStepUpToken(t *testing.T, h *harness, s *session, mode string) string {
	t.Helper()
	if mode == twoFactorModeSkip {
		token, _, err := h.tokenManager.IssueStepUpToken(s.userID, auth.StepUpMethodTOTP)
		if err != nil {
			t.Fatalf("issue fake step-up token: %v", err)
		}
		return token
	}

	setupTwoFactor(t, h, s)
	challenge := loginTwoFactorChallenge(t, h, s.email, s.password)
	code := generateCurrentTOTP(t, s.secret)
	completeTwoFactorLogin(t, h, s, challenge, code)

	var result verificationResult
	postJSON(t, h, http.MethodPost, "/api/v1/auth/verify", map[string]any{
		"method": "totp",
		"code":   generateCurrentTOTP(t, s.secret),
	}, bearerHeaders(s.accessToken), http.StatusOK, &result)
	if result.VerificationToken == "" {
		t.Fatal("step-up verification token is empty")
	}
	if result.Method != auth.StepUpMethodTOTP {
		t.Fatalf("step-up method = %q, want %q", result.Method, auth.StepUpMethodTOTP)
	}
	return result.VerificationToken
}

func setupTwoFactor(t *testing.T, h *harness, s *session) {
	t.Helper()
	if s.secret != "" {
		return
	}

	var setup struct {
		Secret string `json:"secret"`
	}
	postJSON(t, h, http.MethodPost, "/api/v1/me/2fa/setup", map[string]any{}, bearerHeaders(s.accessToken), http.StatusOK, &setup)
	if setup.Secret == "" {
		t.Fatal("2FA secret is empty")
	}
	s.secret = setup.Secret

	postJSON(t, h, http.MethodPost, "/api/v1/me/2fa/enable", map[string]any{
		"code": generateCurrentTOTP(t, s.secret),
	}, bearerHeaders(s.accessToken), http.StatusOK, &map[string]any{})
}

func loginTwoFactorChallenge(t *testing.T, h *harness, email, password string) string {
	t.Helper()

	var resp login2FAChallenge
	postJSON(t, h, http.MethodPost, "/api/v1/auth/login", map[string]any{
		"email":           email,
		"password":        password,
		"turnstile_token": conformanceTurnstileToken,
	}, nil, http.StatusOK, &resp)
	if !resp.RequiresTwoFactor {
		t.Fatal("login did not require 2FA after enabling it")
	}
	if resp.Challenge == "" {
		t.Fatal("2FA challenge is empty")
	}
	return resp.Challenge
}

func completeTwoFactorLogin(t *testing.T, h *harness, s *session, challenge, code string) {
	t.Helper()

	var login authTokenPair
	postJSON(t, h, http.MethodPost, "/api/v1/auth/login/2fa", map[string]any{
		"challenge": challenge,
		"code":      code,
	}, nil, http.StatusOK, &login)
	s.accessToken = login.AccessToken
	s.refreshToken = login.RefreshToken
}

func revokeDevice(t *testing.T, h *harness, s *session, stepUpToken string) {
	t.Helper()
	headers := bearerHeaders(s.accessToken)
	headers.Set(auth.StepUpHeader, stepUpToken)
	postJSON(t, h, http.MethodPost, "/api/v1/devices/"+url.PathEscape(s.deviceUUID.String())+"/revoke", map[string]any{}, headers, http.StatusOK, &map[string]any{})
}

func listRevocationsAndAssert(t *testing.T, h *harness, s *session) {
	t.Helper()
	var resp revocationListResponse
	doRequest(t, h, http.MethodGet, "/api/v1/devices/revocations", nil, bearerHeaders(s.accessToken), http.StatusOK, &resp)
	if len(resp.Revocations) == 0 {
		t.Fatal("revocations list is empty")
	}
	if resp.Revocations[0].DeviceUUID != s.deviceUUID {
		t.Fatalf("revoked device uuid = %s, want %s", resp.Revocations[0].DeviceUUID, s.deviceUUID)
	}
}

func dialWebSocket(t *testing.T, h *harness, deviceToken string) *websocket.Conn {
	t.Helper()
	header := http.Header{}
	header.Set("Authorization", "Bearer "+deviceToken)
	conn, resp, err := websocket.DefaultDialer.Dial(h.wsURL, header)
	if err != nil {
		if resp != nil && resp.Body != nil {
			defer resp.Body.Close()
			body, _ := io.ReadAll(resp.Body)
			t.Fatalf("dial websocket: %v; status=%d; body=%s", err, resp.StatusCode, string(body))
		}
		t.Fatalf("dial websocket: %v", err)
	}
	return conn
}

func assertPushMessage(t *testing.T, conn *websocket.Conn, wantType string, wantFields map[string]string) {
	t.Helper()
	if err := conn.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatalf("set websocket deadline: %v", err)
	}
	var msg pushMessage
	if err := conn.ReadJSON(&msg); err != nil {
		t.Fatalf("read websocket message: %v", err)
	}
	if msg.Type != wantType {
		t.Fatalf("push type = %q, want %q", msg.Type, wantType)
	}
	for key, want := range wantFields {
		if got := stringifyJSONValue(msg.Data[key]); got != want {
			t.Fatalf("push field %s = %q, want %q", key, got, want)
		}
	}
}

func stringifyJSONValue(value any) string {
	switch v := value.(type) {
	case string:
		return v
	case float64:
		return strconv.FormatInt(int64(v), 10)
	default:
		return fmt.Sprint(v)
	}
}

func generateCurrentTOTP(t *testing.T, secret string) string {
	t.Helper()
	code, err := totp.GenerateCodeCustom(secret, time.Now(), totp.ValidateOpts{
		Period:    30,
		Skew:      1,
		Digits:    otp.DigitsSix,
		Algorithm: otp.AlgorithmSHA1,
	})
	if err != nil {
		t.Fatalf("generate totp code: %v", err)
	}
	return code
}

func expectError(t *testing.T, h *harness, method, pathTemplate, path string, payload any, headers http.Header, wantStatus int, wantCode apierrors.Code) {
	t.Helper()
	var body io.Reader
	if payload != nil {
		raw, err := json.Marshal(payload)
		if err != nil {
			t.Fatalf("marshal payload: %v", err)
		}
		body = bytes.NewReader(raw)
		if headers == nil {
			headers = http.Header{}
		}
		headers.Set("Content-Type", "application/json")
	}
	expectRawError(t, h, method, pathTemplate, path, body, headers, wantStatus, wantCode)
}

func expectRawError(t *testing.T, h *harness, method, pathTemplate, path string, body io.Reader, headers http.Header, wantStatus int, wantCode apierrors.Code) {
	t.Helper()
	respStatus, raw := doRawErrorRequest(t, h, method, path, body, headers)
	if respStatus != wantStatus {
		t.Fatalf("%s %s status = %d, want %d; body=%s", method, path, respStatus, wantStatus, string(raw))
	}

	var env errorEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("decode error envelope: %v; body=%s", err, string(raw))
	}
	if env.RequestID == "" {
		t.Fatalf("%s %s request_id is empty", method, path)
	}
	if got := apierrors.Code(env.Error.Code); got != wantCode {
		t.Fatalf("%s %s error.code = %q, want %q", method, path, got, wantCode)
	}

	entry, ok := h.catalog[string(wantCode)]
	if !ok {
		t.Fatalf("error code %q not found in apierrors catalog", wantCode)
	}
	if entry.HTTPStatus != wantStatus {
		t.Fatalf("catalog status for %q = %d, want %d", wantCode, entry.HTTPStatus, wantStatus)
	}
	if !h.openAPI.allowsErrorStatus(pathTemplate, method, wantStatus) {
		t.Fatalf("OpenAPI does not declare %s %s error status %d", method, pathTemplate, wantStatus)
	}
}

func assertOpenAPIStatus(t *testing.T, h *harness, path, method string, status int) {
	t.Helper()
	if !h.openAPI.allowsStatus(path, method, status) {
		t.Fatalf("OpenAPI does not declare %s %s response %d", method, path, status)
	}
}

func postJSON(t *testing.T, h *harness, method, path string, payload any, headers http.Header, wantStatus int, out any) {
	t.Helper()
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal JSON payload: %v", err)
	}
	if headers == nil {
		headers = http.Header{}
	}
	headers.Set("Content-Type", "application/json")
	doRequest(t, h, method, path, bytes.NewReader(raw), headers, wantStatus, out)
}

func doRequest(t *testing.T, h *harness, method, path string, body io.Reader, headers http.Header, wantStatus int, out any) {
	t.Helper()
	respBody := doRawRequest(t, h, method, path, body, headers, wantStatus)
	if out != nil {
		if err := json.Unmarshal(respBody, out); err != nil {
			t.Fatalf("decode %s %s response: %v; body=%s", method, path, err, string(respBody))
		}
	}
}

func doRawRequest(t *testing.T, h *harness, method, path string, body io.Reader, headers http.Header, wantStatus int) []byte {
	t.Helper()
	req, err := http.NewRequest(method, h.baseURL+path, body)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	copyHeaders(req.Header, headers)
	resp, err := h.httpClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read %s %s body: %v", method, path, err)
	}
	if resp.StatusCode != wantStatus {
		t.Fatalf("%s %s status = %d, want %d; body=%s", method, path, resp.StatusCode, wantStatus, string(respBody))
	}
	return respBody
}

func doRawErrorRequest(t *testing.T, h *harness, method, path string, body io.Reader, headers http.Header) (int, []byte) {
	t.Helper()
	req, err := http.NewRequest(method, h.baseURL+path, body)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	copyHeaders(req.Header, headers)
	resp, err := h.httpClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read %s %s body: %v", method, path, err)
	}
	return resp.StatusCode, respBody
}

func bearerHeaders(token string) http.Header {
	headers := http.Header{}
	headers.Set("Authorization", "Bearer "+token)
	return headers
}

func copyHeaders(dst, src http.Header) {
	for key, values := range src {
		for _, value := range values {
			dst.Add(key, value)
		}
	}
}

func mustWriteField(t *testing.T, writer *multipart.Writer, name, value string) {
	t.Helper()
	if err := writer.WriteField(name, value); err != nil {
		t.Fatalf("write multipart field %s: %v", name, err)
	}
}

func loadCatalog() map[string]apierrors.Entry {
	catalog := make(map[string]apierrors.Entry, len(apierrors.All()))
	for _, entry := range apierrors.All() {
		catalog[string(entry.Code)] = entry
	}
	return catalog
}

func loadOpenAPI(t *testing.T) openAPIDocument {
	t.Helper()

	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime caller unavailable")
	}
	path := filepath.Join(filepath.Dir(currentFile), "..", "..", "docs", "api", "openapi.ce.yaml")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read OpenAPI document %s: %v", path, err)
	}

	var doc openAPIDocument
	if err := yaml.Unmarshal(data, &doc); err != nil {
		t.Fatalf("unmarshal OpenAPI document: %v", err)
	}
	return doc
}

func (d openAPIDocument) allowsErrorStatus(path, method string, status int) bool {
	responses, ok := d.responsesFor(path, method)
	if !ok {
		return false
	}
	response, ok := responses[strconv.Itoa(status)]
	if !ok {
		return false
	}
	resolved := d.resolveResponse(response)
	content := asMap(resolved)["content"]
	media := asMap(content)
	jsonContent := asMap(media["application/json"])
	schema := asMap(jsonContent["schema"])
	if ref, _ := schema["$ref"].(string); ref == "#/components/schemas/ErrorEnvelope" {
		return true
	}
	ref, _ := asMap(response)["$ref"].(string)
	return ref == "#/components/responses/ErrorResponse"
}

func (d openAPIDocument) allowsStatus(path, method string, status int) bool {
	responses, ok := d.responsesFor(path, method)
	if !ok {
		return false
	}
	_, ok = responses[strconv.Itoa(status)]
	return ok
}

func (d openAPIDocument) responsesFor(path, method string) (map[string]any, bool) {
	operations, ok := d.Paths[path]
	if !ok {
		return nil, false
	}
	operation, ok := operations[strings.ToLower(method)]
	if !ok {
		return nil, false
	}
	return asMap(asMap(operation)["responses"]), true
}

func (d openAPIDocument) resolveResponse(response any) any {
	responseMap := asMap(response)
	ref, _ := responseMap["$ref"].(string)
	if ref == "" {
		return response
	}
	const prefix = "#/components/responses/"
	if !strings.HasPrefix(ref, prefix) {
		return response
	}
	name := strings.TrimPrefix(ref, prefix)
	if resolved, ok := d.Components.Responses[name]; ok {
		return resolved
	}
	return response
}

func asMap(value any) map[string]any {
	if mapped, ok := value.(map[string]any); ok {
		return mapped
	}
	return map[string]any{}
}

func (v *fakeTurnstileVerifier) Verify(_ context.Context, token string, _ string) error {
	if strings.TrimSpace(token) != v.acceptedToken {
		return middleware.ErrTurnstileFailed
	}
	return nil
}

func (s *memoryBlobStore) Put(_ context.Context, key string, reader io.Reader, _ int64, _ string) error {
	data, err := io.ReadAll(reader)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.objects[key] = append([]byte(nil), data...)
	return nil
}

func (s *memoryBlobStore) Get(_ context.Context, key string) (io.ReadCloser, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	data, ok := s.objects[key]
	if !ok {
		return nil, fmt.Errorf("object %q not found", key)
	}
	return io.NopCloser(bytes.NewReader(append([]byte(nil), data...))), nil
}

func (s *memoryBlobStore) Delete(_ context.Context, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.objects, key)
	return nil
}

func (s *memoryBlobStore) Exists(_ context.Context, key string) (bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	_, ok := s.objects[key]
	return ok, nil
}

func (s *memoryBlobStore) Size(_ context.Context, key string) (int64, bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	data, ok := s.objects[key]
	if !ok {
		return 0, false, nil
	}
	return int64(len(data)), true, nil
}

func (s *memoryBlobStore) List(_ context.Context, prefix string) ([]storage.ObjectInfo, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	objects := make([]storage.ObjectInfo, 0)
	for key, data := range s.objects {
		if strings.HasPrefix(key, prefix) {
			objects = append(objects, storage.ObjectInfo{
				Key:          key,
				Size:         int64(len(data)),
				LastModified: time.Now(),
			})
		}
	}
	return objects, nil
}
