//go:build smoke

package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/historysync/hsync-server/migrations"
	"github.com/historysync/hsync-server/pkg/auth"
	"github.com/historysync/hsync-server/pkg/buildinfo"
	"github.com/historysync/hsync-server/pkg/config"
	"github.com/historysync/hsync-server/pkg/handler"
	"github.com/historysync/hsync-server/pkg/middleware"
	"github.com/historysync/hsync-server/pkg/migrate"
	"github.com/historysync/hsync-server/pkg/observability"
	"github.com/historysync/hsync-server/pkg/preflight"
	"github.com/historysync/hsync-server/pkg/repository"
	"github.com/historysync/hsync-server/pkg/service"
	"github.com/historysync/hsync-server/pkg/storage"
	"github.com/historysync/hsync-server/pkg/web"
	"github.com/historysync/hsync-server/pkg/ws"
)

func TestCEProductionReadinessSmoke(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	pool := newSmokePostgresPool(t, ctx)
	allMigrations, err := migrate.Parse(migrations.FS)
	if err != nil {
		t.Fatalf("parse migrations: %v", err)
	}
	applied, err := migrate.Up(ctx, pool, migrations.FS)
	if err != nil {
		t.Fatalf("CE migrate up: %v", err)
	}
	if len(applied) != len(allMigrations) {
		t.Fatalf("applied migrations = %d, want %d", len(applied), len(allMigrations))
	}
	assertSmokeMigrationRows(t, ctx, pool, len(allMigrations))
	assertSmokeMigrationStatus(t, ctx, pool)

	app := newCESmokeApp(t, pool)

	for _, tt := range []struct {
		name   string
		path   string
		assert func(*testing.T, int, string)
	}{
		{name: "health", path: "/healthz", assert: assertSmokeJSONField("status", "ok")},
		{name: "readiness", path: "/readyz", assert: assertSmokeReadyz},
		{name: "version", path: "/api/meta/version", assert: assertSmokeVersion},
		{name: "overview", path: "/api/meta/overview", assert: assertSmokeOverview},
		{name: "console", path: "/console", assert: assertSmokeBodyContains("Build info")},
		{name: "metrics", path: "/metrics", assert: assertSmokeBodyContains("hsync_http_requests_total")},
	} {
		t.Run(tt.name, func(t *testing.T) {
			resp, body := smokeRequest(t, app, fiber.MethodGet, tt.path, nil)
			tt.assert(t, resp.StatusCode, body)
		})
	}
}

func newCESmokeApp(t *testing.T, pool *pgxpool.Pool) *fiber.App {
	t.Helper()

	cfg := config.DefaultConfig()
	cfg.JWTPrivateKey = base64.StdEncoding.EncodeToString(make([]byte, ed25519.SeedSize))
	cfg.SecuritySecret = "0123456789abcdef0123456789abcdef"
	cfg.StripeDisabled = true
	cfg.BackgroundTasksEnabled = false
	cfg.WebEnabled = true
	cfg.WebAppName = "HistorySync CE"
	cfg.WebConsolePath = "/console"
	cfg.MetricsEnabled = true
	cfg.MetricsPath = "/metrics"
	cfg.MetricsAllowedCIDRs = nil

	repos := repository.New(pool, nil)
	tokenManager, err := auth.NewTokenManager(cfg.JWTPrivateKey, auth.TokenConfig{
		AccessTTL:  15 * time.Minute,
		RefreshTTL: 30 * 24 * time.Hour,
	})
	if err != nil {
		t.Fatalf("token manager: %v", err)
	}
	blobStore := &smokeBlobStore{objects: map[string][]byte{}}
	services := service.New(service.Deps{
		Repos:          repos,
		TokenManager:   tokenManager,
		BlobStore:      blobStore,
		StripeDisabled: true,
		SecuritySecret: cfg.SecuritySecret,
		Config:         cfg,
		DatabasePing:   pool.Ping,
	})
	hub := ws.NewHub(repos.Devices)

	h := handler.New(handler.Deps{
		Services:     services,
		TokenManager: tokenManager,
		Hub:          hub,
		DB:           pool,
		BlobStore:    blobStore,
		AdminKey:     "smoke-admin-key",
		BuildInfo:    buildinfo.WithEdition("community"),
		RateLimiter:  middleware.NewMemoryLimiter(),
		Metrics: handler.MetricsConfig{
			Enabled: true,
			Path:    "/metrics",
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
	web.Register(app, web.Options{
		Enabled:     true,
		AppName:     "HistorySync CE",
		ConsolePath: "/console",
		Edition:     "community",
		BuildInfo:   buildinfo.WithEdition("community"),
		APIPrefix:   "/api/v1",
		AdminPath:   "/admin",
	})
	return app
}

func assertSmokeVersion(t *testing.T, status int, body string) {
	t.Helper()
	if status != fiber.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", status, body)
	}
	var payload struct {
		BuildInfo buildinfo.Info `json:"build_info"`
	}
	if err := json.Unmarshal([]byte(body), &payload); err != nil {
		t.Fatalf("unmarshal version: %v; body=%s", err, body)
	}
	if payload.BuildInfo.Edition != "community" {
		t.Fatalf("edition = %q, want community; body=%s", payload.BuildInfo.Edition, body)
	}
	if payload.BuildInfo.SchemaVersion == 0 {
		t.Fatalf("schema_version = 0; body=%s", body)
	}
}

func newSmokePostgresPool(t *testing.T, ctx context.Context) *pgxpool.Pool {
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
		t.Fatalf("start postgres container: %v", err)
	}
	t.Cleanup(func() {
		_ = container.Terminate(context.Background())
	})

	dsn, err := container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatalf("container connection string: %v", err)
	}
	pool, err := repository.NewPGXPool(ctx, dsn)
	if err != nil {
		t.Fatalf("connect pool: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func assertSmokeMigrationRows(t *testing.T, ctx context.Context, pool *pgxpool.Pool, want int) {
	t.Helper()
	var got int
	if err := pool.QueryRow(ctx, "SELECT COUNT(*) FROM schema_migrations").Scan(&got); err != nil {
		t.Fatalf("count schema_migrations: %v", err)
	}
	if got != want {
		t.Fatalf("schema_migrations rows = %d, want %d", got, want)
	}
}

func assertSmokeMigrationStatus(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	status, err := migrate.Status(ctx, pool, migrations.FS, "schema_migrations", "community")
	if err != nil {
		t.Fatalf("migrate status: %v", err)
	}
	if !status.Consistent || len(status.Pending) != 0 || len(status.Applied) == 0 {
		t.Fatalf("unexpected migration status: %#v", status)
	}
	findings, err := migrate.Drift(ctx, pool, preflight.CEDriftRequirements())
	if err != nil {
		t.Fatalf("schema drift: %v", err)
	}
	if len(findings) != 0 {
		t.Fatalf("schema drift findings: %#v", findings)
	}
}

func smokeRequest(t *testing.T, app *fiber.App, method, path string, body io.Reader) (*http.Response, string) {
	t.Helper()
	req := httptest.NewRequest(method, path, body)
	req.RemoteAddr = "127.0.0.1:12345"
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read %s %s body: %v", method, path, err)
	}
	return resp, string(data)
}

func assertSmokeJSONField(field, want string) func(*testing.T, int, string) {
	return func(t *testing.T, status int, body string) {
		t.Helper()
		if status != fiber.StatusOK {
			t.Fatalf("status = %d, want 200; body=%s", status, body)
		}
		var payload map[string]any
		if err := json.Unmarshal([]byte(body), &payload); err != nil {
			t.Fatalf("unmarshal body: %v; body=%s", err, body)
		}
		if got, _ := payload[field].(string); got != want {
			t.Fatalf("%s = %q, want %q; body=%s", field, got, want, body)
		}
	}
}

func assertSmokeReadyz(t *testing.T, status int, body string) {
	t.Helper()
	if status != fiber.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", status, body)
	}
	var payload struct {
		Status string            `json:"status"`
		Checks map[string]string `json:"checks"`
	}
	if err := json.Unmarshal([]byte(body), &payload); err != nil {
		t.Fatalf("unmarshal readyz: %v; body=%s", err, body)
	}
	if payload.Status != "ok" {
		t.Fatalf("readyz status = %q, want ok; body=%s", payload.Status, body)
	}
	for key, want := range map[string]string{"database": "ok", "redis": "disabled", "storage": "ok"} {
		if got := payload.Checks[key]; got != want {
			t.Fatalf("readyz check %s = %q, want %q; body=%s", key, got, want, body)
		}
	}
}

func assertSmokeOverview(t *testing.T, status int, body string) {
	t.Helper()
	if status != fiber.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", status, body)
	}
	var payload struct {
		Status  string            `json:"status"`
		Checks  map[string]string `json:"checks"`
		Summary map[string]any    `json:"summary"`
		Routes  map[string]string `json:"routes"`
	}
	if err := json.Unmarshal([]byte(body), &payload); err != nil {
		t.Fatalf("unmarshal overview: %v; body=%s", err, body)
	}
	if payload.Status != "ok" || payload.Checks["database"] != "ok" || payload.Checks["storage"] != "ok" {
		t.Fatalf("overview readiness is not ok; body=%s", body)
	}
	if payload.Routes["admin"] != "/admin/stats" || payload.Routes["quota"] != "/api/v1/quota" {
		t.Fatalf("overview routes missing admin/quota references; body=%s", body)
	}
	if totalUsers, _ := payload.Summary["total_users"].(float64); totalUsers != 0 {
		t.Fatalf("overview total_users = %v, want 0; body=%s", payload.Summary["total_users"], body)
	}
}

func assertSmokeBodyContains(fragment string) func(*testing.T, int, string) {
	return func(t *testing.T, status int, body string) {
		t.Helper()
		if status != fiber.StatusOK {
			t.Fatalf("status = %d, want 200; body=%s", status, body)
		}
		if !strings.Contains(body, fragment) {
			t.Fatalf("body missing %q:\n%s", fragment, body)
		}
	}
}

type smokeBlobStore struct {
	objects map[string][]byte
}

func (s *smokeBlobStore) Put(_ context.Context, key string, reader io.Reader, _ int64, _ string) error {
	data, err := io.ReadAll(reader)
	if err != nil {
		return err
	}
	s.objects[key] = data
	return nil
}

func (s *smokeBlobStore) Get(_ context.Context, key string) (io.ReadCloser, error) {
	data, ok := s.objects[key]
	if !ok {
		return nil, fmt.Errorf("object %q not found", key)
	}
	return io.NopCloser(bytes.NewReader(data)), nil
}

func (s *smokeBlobStore) Delete(_ context.Context, key string) error {
	delete(s.objects, key)
	return nil
}

func (s *smokeBlobStore) Exists(_ context.Context, key string) (bool, error) {
	_, ok := s.objects[key]
	return ok, nil
}

func (s *smokeBlobStore) Size(_ context.Context, key string) (int64, bool, error) {
	data, ok := s.objects[key]
	if !ok {
		return 0, false, nil
	}
	return int64(len(data)), true, nil
}

func (s *smokeBlobStore) List(_ context.Context, prefix string) ([]storage.ObjectInfo, error) {
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
