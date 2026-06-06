package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/rs/zerolog"

	"github.com/historysync/hsync-server/pkg/config"
	"github.com/historysync/hsync-server/pkg/storage"
)

type fakeOpsBlobStore struct {
	listErr      error
	putErr       error
	sizeErr      error
	getErr       error
	deleteErr    error
	readMismatch bool

	objects []storage.ObjectInfo
	data    map[string][]byte
	puts    int
	gets    int
	deletes int
}

func newFakeOpsBlobStore() *fakeOpsBlobStore {
	return &fakeOpsBlobStore{data: map[string][]byte{}}
}

func (f *fakeOpsBlobStore) Put(_ context.Context, key string, reader io.Reader, _ int64, _ string) error {
	if f.putErr != nil {
		return f.putErr
	}
	body, err := io.ReadAll(reader)
	if err != nil {
		return err
	}
	f.puts++
	f.data[key] = body
	return nil
}

func (f *fakeOpsBlobStore) Get(_ context.Context, key string) (io.ReadCloser, error) {
	if f.getErr != nil {
		return nil, f.getErr
	}
	f.gets++
	body := f.data[key]
	if f.readMismatch {
		body = []byte("different")
	}
	return io.NopCloser(bytes.NewReader(body)), nil
}

func (f *fakeOpsBlobStore) Delete(_ context.Context, key string) error {
	if f.deleteErr != nil {
		return f.deleteErr
	}
	f.deletes++
	delete(f.data, key)
	return nil
}

func (f *fakeOpsBlobStore) Exists(_ context.Context, key string) (bool, error) {
	_, ok := f.data[key]
	return ok, nil
}

func (f *fakeOpsBlobStore) Size(_ context.Context, key string) (int64, bool, error) {
	if f.sizeErr != nil {
		return 0, false, f.sizeErr
	}
	body, ok := f.data[key]
	return int64(len(body)), ok, nil
}

func (f *fakeOpsBlobStore) List(context.Context, string) ([]storage.ObjectInfo, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	return f.objects, nil
}

func testOpsConfig() *config.Config {
	return &config.Config{
		ListenAddr:                 ":8080",
		LogLevel:                   zerolog.InfoLevel,
		WebEnabled:                 true,
		WebConsolePath:             "/console",
		WebSupportEmail:            "ops@example.com",
		PublicURL:                  "https://history.example",
		MetricsEnabled:             true,
		MetricsPath:                "/metrics",
		DatabaseURL:                "postgres://hsync:db-secret@example-db:5432/hsync?sslmode=disable",
		RedisURL:                   "redis://:redis-secret@example-redis:6379/0",
		S3Endpoint:                 "minio.example:9000",
		S3Bucket:                   "hsync-bundles",
		S3UseSSL:                   true,
		S3AccessKey:                "access-secret",
		S3SecretKey:                "storage-secret",
		JWTPrivateKey:              "jwt-secret",
		SecuritySecret:             "security-secret",
		StripeSecretKey:            "stripe-secret",
		StripeWebhookSecret:        "webhook-secret",
		AdminKey:                   "admin-secret",
		OIDCClientSecret:           "oidc-secret",
		TurnstileSecret:            "turnstile-secret",
		BackgroundTasksEnabled:     true,
		QuotaReconcileInterval:     time.Hour,
		RetentionCleanupInterval:   2 * time.Hour,
		RetentionGracePeriod:       30 * 24 * time.Hour,
		NotificationOutboxInterval: time.Minute,
		NotificationsEnabled:       true,
		QuotaWarningThreshold:      80,
		QuotaExhaustedThreshold:    95,
		EmailVerificationPath:      "/verify-email",
		PasswordResetPath:          "/reset-password",
		SMTPEnabled:                true,
		SMTPServer:                 "smtp.example",
		SMTPPort:                   587,
		SMTPUsername:               "mailer",
		SMTPPassword:               "smtp-secret",
		SMTPFrom:                   "noreply@example.com",
		SMTPFromName:               "HistorySync",
		SMTPTLSMode:                "starttls",
	}
}

func TestOpsSummaryMasksSensitiveConfig(t *testing.T) {
	svc := NewOpsService(OpsDeps{Config: testOpsConfig()})

	summary := svc.Summary(context.Background())
	body, err := json.Marshal(summary)
	if err != nil {
		t.Fatalf("marshal summary: %v", err)
	}
	text := string(body)
	for _, secret := range []string{
		"db-secret",
		"redis-secret",
		"access-secret",
		"storage-secret",
		"jwt-secret",
		"security-secret",
		"stripe-secret",
		"webhook-secret",
		"admin-secret",
		"oidc-secret",
		"turnstile-secret",
		"smtp-secret",
	} {
		if strings.Contains(text, secret) {
			t.Fatalf("summary leaked secret %q: %s", secret, text)
		}
	}
	if !strings.Contains(text, "postgres://hsync:redacted@example-db:5432/hsync") {
		t.Fatalf("database URL was not usefully redacted: %s", text)
	}
	if !strings.Contains(text, `"s3_bucket":"hsync-bundles"`) {
		t.Fatalf("summary missing non-sensitive S3 bucket: %s", text)
	}

	key, ok := summary.Config.Storage["s3_access_key"].(MaskedValue)
	if !ok || !key.Set || key.Value != "********" {
		t.Fatalf("s3_access_key mask = %#v, want set ********", summary.Config.Storage["s3_access_key"])
	}
}

func TestOpsCheckDependenciesHealthyAndStoresLastReport(t *testing.T) {
	store := newFakeOpsBlobStore()
	svc := NewOpsService(OpsDeps{
		Config:       testOpsConfig(),
		BlobStore:    store,
		DatabasePing: func(context.Context) error { return nil },
		RedisPing:    func(context.Context) error { return nil },
	})

	report := svc.CheckDependencies(context.Background())
	if report.Overall != OpsStatusOK {
		t.Fatalf("overall = %q, want ok: %+v", report.Overall, report.Checks)
	}
	if statusByName(report)["postgresql"] != OpsStatusOK ||
		statusByName(report)["redis"] != OpsStatusOK ||
		statusByName(report)["storage"] != OpsStatusOK ||
		statusByName(report)["storage_probe"] != OpsStatusOK {
		t.Fatalf("checks = %+v, want all ok", report.Checks)
	}
	if store.puts != 1 || store.gets != 1 || store.deletes != 1 || len(store.data) != 0 {
		t.Fatalf("probe operations puts=%d gets=%d deletes=%d leftover=%d", store.puts, store.gets, store.deletes, len(store.data))
	}

	summary := svc.Summary(context.Background())
	if summary.Readiness.LastDependencyCheck == nil || summary.Readiness.LastDependencyCheck.Overall != OpsStatusOK {
		t.Fatalf("summary last dependency check = %+v, want ok report", summary.Readiness.LastDependencyCheck)
	}
	if got := summary.Backup.Components[0].LastCheckStatus; got != OpsStatusOK {
		t.Fatalf("postgres backup component status = %q, want ok", got)
	}
}

func TestOpsCheckDependenciesReportsActionableFailures(t *testing.T) {
	rawDBErr := errors.New("dial tcp 10.0.0.5:5432: password raw-db-secret rejected")
	rawStorageErr := errors.New("s3 list hsync-bundles: raw-storage-secret denied")
	store := newFakeOpsBlobStore()
	store.listErr = rawStorageErr
	svc := NewOpsService(OpsDeps{
		BlobStore:    store,
		DatabasePing: func(context.Context) error { return rawDBErr },
	})

	report := svc.CheckDependencies(context.Background())
	statuses := statusByName(report)
	if report.Overall != OpsStatusUnhealthy {
		t.Fatalf("overall = %q, want unhealthy: %+v", report.Overall, report.Checks)
	}
	if statuses["postgresql"] != OpsStatusUnhealthy {
		t.Fatalf("postgres status = %q, want unhealthy", statuses["postgresql"])
	}
	if statuses["redis"] != OpsStatusDisabled {
		t.Fatalf("redis status = %q, want disabled", statuses["redis"])
	}
	if statuses["storage"] != OpsStatusUnhealthy {
		t.Fatalf("storage status = %q, want unhealthy", statuses["storage"])
	}
	if statuses["storage_probe"] != OpsStatusSkipped {
		t.Fatalf("storage_probe status = %q, want skipped", statuses["storage_probe"])
	}

	body, err := json.Marshal(report)
	if err != nil {
		t.Fatalf("marshal report: %v", err)
	}
	text := string(body)
	for _, leaked := range []string{"raw-db-secret", "raw-storage-secret", "dial tcp 10.0.0.5", "s3 list hsync-bundles"} {
		if strings.Contains(text, leaked) {
			t.Fatalf("dependency report leaked raw error detail %q: %s", leaked, text)
		}
	}
	if !strings.Contains(text, "Verify database_url") || !strings.Contains(text, "Verify the S3 endpoint") {
		t.Fatalf("dependency report missing operator actions: %s", text)
	}
}

func statusByName(report OpsDependencyReport) map[string]string {
	out := make(map[string]string, len(report.Checks))
	for _, check := range report.Checks {
		out[check.Name] = check.Status
	}
	return out
}
