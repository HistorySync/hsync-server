//go:build integration

// Integration tests for the repository layer. These run real SQL against a
// throwaway PostgreSQL container (via testcontainers) with the project's actual
// migrations applied, so they exercise query correctness, constraints, and
// triggers that pure unit tests cannot reach.
//
// They are gated behind the "integration" build tag and are therefore excluded
// from the default `go test ./...` / `make test` run. Execute them with
// `make test-integration` (a running Docker daemon is required).
package repository

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/historysync/hsync-server/migrations"
	"github.com/historysync/hsync-server/pkg/migrate"
	"github.com/historysync/hsync-server/pkg/model"
)

// testPool is the shared connection pool for this package's integration tests.
// It is initialized once in TestMain against a single throwaway container.
var testPool *pgxpool.Pool

// allTables lists every table the migrations create, used to reset state
// between tests. TRUNCATE ... CASCADE handles foreign-key order, so the listed
// order does not matter. schema_migrations is intentionally excluded.
var allTables = []string{
	"users",
	"refresh_tokens",
	"devices",
	"bundles",
	"snapshots",
	"device_revocations",
	"storage_usage",
	"quota_limits",
	"invoices",
	"email_verifications",
	"password_resets",
	"user_two_factor",
	"user_two_factor_backup_codes",
	"audit_logs",
}

func TestMain(m *testing.M) {
	code, err := runIntegrationTests(m)
	if err != nil {
		fmt.Fprintln(os.Stderr, "integration test setup failed:", err)
		os.Exit(1)
	}
	os.Exit(code)
}

// runIntegrationTests owns the container lifecycle so its deferred cleanup runs
// before TestMain calls os.Exit.
func runIntegrationTests(m *testing.M) (int, error) {
	ctx := context.Background()

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
		return 0, fmt.Errorf("start postgres container: %w", err)
	}
	defer func() {
		// Best-effort teardown; the container is ephemeral regardless.
		_ = container.Terminate(context.Background())
	}()

	dsn, err := container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		return 0, fmt.Errorf("container connection string: %w", err)
	}

	pool, err := NewPGXPool(ctx, dsn)
	if err != nil {
		return 0, fmt.Errorf("connect pool: %w", err)
	}
	defer pool.Close()

	if _, err := migrate.Up(ctx, pool, migrations.FS); err != nil {
		return 0, fmt.Errorf("apply migrations: %w", err)
	}

	testPool = pool
	return m.Run(), nil
}

// testContext returns a per-test context with a generous timeout, cancelled on
// test cleanup so a hung query cannot block the suite indefinitely.
func testContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	return ctx
}

// setupTest resets the database to an empty state and returns repositories bound
// to the shared pool. Redis is nil: the quota SQL methods exercised here do not
// use it.
func setupTest(t *testing.T) *Repos {
	t.Helper()
	resetDB(t)
	return New(testPool, nil)
}

// resetDB truncates all data tables so each test starts from a clean slate.
func resetDB(t *testing.T) {
	t.Helper()
	ctx := testContext(t)
	_, err := testPool.Exec(ctx, "TRUNCATE "+strings.Join(allTables, ", ")+" RESTART IDENTITY CASCADE")
	if err != nil {
		t.Fatalf("reset db: %v", err)
	}
}

// seedUser inserts a minimal active free-tier user and returns it with its
// generated ID and timestamps populated.
func seedUser(t *testing.T, repos *Repos, email string) *model.User {
	t.Helper()
	u := &model.User{
		Email:        email,
		PasswordHash: "test-hash",
		DisplayName:  "Test User",
		Tier:         model.TierFree,
		Status:       model.StatusActive,
	}
	if err := repos.Users.Create(testContext(t), u); err != nil {
		t.Fatalf("seed user %q: %v", email, err)
	}
	return u
}

// seedDevice registers a device for the given user and returns it.
func seedDevice(t *testing.T, repos *Repos, userID uuid.UUID) *model.Device {
	t.Helper()
	d := &model.Device{
		UserID:     userID,
		DeviceUUID: uuid.New(),
		DeviceName: "test-device",
		Platform:   "linux",
		AppVersion: "1.0.0",
	}
	if err := repos.Devices.Create(testContext(t), d); err != nil {
		t.Fatalf("seed device: %v", err)
	}
	return d
}
