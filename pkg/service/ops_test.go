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

	"github.com/google/uuid"
	"github.com/rs/zerolog"

	"github.com/historysync/hsync-server/pkg/config"
	"github.com/historysync/hsync-server/pkg/model"
	"github.com/historysync/hsync-server/pkg/provider"
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

func (f *fakeOpsBlobStore) List(_ context.Context, prefix string) ([]storage.ObjectInfo, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	if f.objects == nil {
		objects := make([]storage.ObjectInfo, 0, len(f.data))
		for key, body := range f.data {
			if !strings.HasPrefix(key, prefix) {
				continue
			}
			objects = append(objects, storage.ObjectInfo{Key: key, Size: int64(len(body))})
		}
		return objects, nil
	}
	objects := make([]storage.ObjectInfo, 0, len(f.objects))
	for _, obj := range f.objects {
		if strings.HasPrefix(obj.Key, prefix) {
			objects = append(objects, obj)
		}
	}
	return objects, nil
}

func (f *fakeOpsBlobStore) setObject(key string, size int64) {
	if f.data == nil {
		f.data = map[string][]byte{}
	}
	f.data[key] = bytes.Repeat([]byte("x"), int(size))
}

type fakeOpsBundleMetadata struct {
	rows     []model.BundleMeta
	countErr error
	sumErr   error
	listErr  error
}

func (f fakeOpsBundleMetadata) CountAll(context.Context) (int64, error) {
	if f.countErr != nil {
		return 0, f.countErr
	}
	return int64(len(f.rows)), nil
}

func (f fakeOpsBundleMetadata) SumSizeAll(context.Context) (int64, error) {
	if f.sumErr != nil {
		return 0, f.sumErr
	}
	var total int64
	for _, row := range f.rows {
		total += row.SizeBytes
	}
	return total, nil
}

func (f fakeOpsBundleMetadata) ListForOpsConsistency(_ context.Context, limit int32) ([]model.BundleMeta, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	rows := append([]model.BundleMeta(nil), f.rows...)
	if int(limit) < len(rows) {
		rows = rows[:limit]
	}
	return rows, nil
}

func (f fakeOpsBundleMetadata) ListForOpsRestoreManifest(ctx context.Context, limit int32) ([]model.BundleMeta, error) {
	return f.ListForOpsConsistency(ctx, limit)
}

type fakeOpsSnapshotMetadata struct {
	rows     []model.SnapshotMeta
	countErr error
	sumErr   error
	listErr  error
}

func (f fakeOpsSnapshotMetadata) CountAll(context.Context) (int64, error) {
	if f.countErr != nil {
		return 0, f.countErr
	}
	return int64(len(f.rows)), nil
}

func (f fakeOpsSnapshotMetadata) SumSizeAll(context.Context) (int64, error) {
	if f.sumErr != nil {
		return 0, f.sumErr
	}
	var total int64
	for _, row := range f.rows {
		total += row.SizeBytes
	}
	return total, nil
}

func (f fakeOpsSnapshotMetadata) ListForOpsConsistency(_ context.Context, limit int32) ([]model.SnapshotMeta, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	rows := append([]model.SnapshotMeta(nil), f.rows...)
	if int(limit) < len(rows) {
		rows = rows[:limit]
	}
	return rows, nil
}

func (f fakeOpsSnapshotMetadata) ListForOpsRestoreManifest(ctx context.Context, limit int32) ([]model.SnapshotMeta, error) {
	return f.ListForOpsConsistency(ctx, limit)
}

type fakeOpsHistoryStore struct {
	runs []model.OpsCheckRun
	err  error
}

func (s *fakeOpsHistoryStore) Create(_ context.Context, run *model.OpsCheckRun) error {
	if s.err != nil {
		return s.err
	}
	cp := *run
	if cp.ID == uuid.Nil {
		cp.ID = uuid.New()
	}
	if cp.CreatedAt.IsZero() {
		cp.CreatedAt = cp.FinishedAt
	}
	s.runs = append([]model.OpsCheckRun{cp}, s.runs...)
	return nil
}

func (s *fakeOpsHistoryStore) ListRecent(_ context.Context, limit int32) ([]model.OpsCheckRun, error) {
	if s.err != nil {
		return nil, s.err
	}
	return limitOpsRuns(s.runs, limit), nil
}

func (s *fakeOpsHistoryStore) ListRecentFailures(_ context.Context, limit int32) ([]model.OpsCheckRun, error) {
	if s.err != nil {
		return nil, s.err
	}
	failures := make([]model.OpsCheckRun, 0)
	for _, run := range s.runs {
		if run.OverallStatus != OpsStatusOK {
			failures = append(failures, run)
		}
	}
	return limitOpsRuns(failures, limit), nil
}

func limitOpsRuns(runs []model.OpsCheckRun, limit int32) []model.OpsCheckRun {
	out := append([]model.OpsCheckRun(nil), runs...)
	if limit > 0 && int(limit) < len(out) {
		out = out[:limit]
	}
	return out
}

type captureOpsWebhookProvider struct {
	sent         int
	webhookURL   string
	secret       string
	notification provider.WebhookNotification
}

func (p *captureOpsWebhookProvider) DeliveryEnabled() bool { return true }

func (p *captureOpsWebhookProvider) Send(_ context.Context, webhookURL, secret string, notification provider.WebhookNotification) error {
	p.sent++
	p.webhookURL = webhookURL
	p.secret = secret
	p.notification = notification
	return nil
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

func TestOpsConsistencyReportsMetadataStorageMatchWithoutReadingBlobs(t *testing.T) {
	user := uuid.New()
	bundle := model.BundleMeta{UserID: user, BundleID: "b1", SizeBytes: 3}
	snapshot := model.SnapshotMeta{UserID: user, SnapshotID: "s1", SizeBytes: 5}
	store := newFakeOpsBlobStore()
	store.setObject(storage.BundleKey(user.String(), bundle.BundleID), bundle.SizeBytes)
	store.setObject(storage.SnapshotKey(user.String(), snapshot.SnapshotID), snapshot.SizeBytes)
	svc := NewOpsService(OpsDeps{
		BlobStore:        store,
		BundleMetadata:   fakeOpsBundleMetadata{rows: []model.BundleMeta{bundle}},
		SnapshotMetadata: fakeOpsSnapshotMetadata{rows: []model.SnapshotMeta{snapshot}},
	})

	report := svc.CheckConsistency(context.Background(), 100)
	if report.Overall != OpsStatusOK {
		t.Fatalf("overall = %q, want ok: %+v", report.Overall, report.Artifacts)
	}
	for _, artifact := range report.Artifacts {
		if artifact.Status != OpsStatusOK || artifact.CheckedMetadata != 1 || artifact.IssueCount != 0 {
			t.Fatalf("artifact = %+v, want ok checked=1 issues=0", artifact)
		}
	}
	if len(report.Issues) != 0 {
		t.Fatalf("issues = %+v, want none", report.Issues)
	}
	if store.gets != 0 {
		t.Fatalf("consistency check read %d blob(s), want 0", store.gets)
	}

	summary := svc.Summary(context.Background())
	if summary.Readiness.LastConsistencyCheck == nil || summary.Readiness.LastConsistencyCheck.Overall != OpsStatusOK {
		t.Fatalf("summary last consistency = %+v, want ok report", summary.Readiness.LastConsistencyCheck)
	}
}

func TestOpsConsistencyFindsMissingAndSizeMismatchedObjects(t *testing.T) {
	user := uuid.New()
	bundles := []model.BundleMeta{
		{UserID: user, BundleID: "ok", SizeBytes: 3},
		{UserID: user, BundleID: "wrong-size", SizeBytes: 5},
	}
	snapshots := []model.SnapshotMeta{
		{UserID: user, SnapshotID: "missing", SizeBytes: 7},
	}
	store := newFakeOpsBlobStore()
	store.setObject(storage.BundleKey(user.String(), "ok"), 3)
	store.setObject(storage.BundleKey(user.String(), "wrong-size"), 4)
	svc := NewOpsService(OpsDeps{
		BlobStore:        store,
		BundleMetadata:   fakeOpsBundleMetadata{rows: bundles},
		SnapshotMetadata: fakeOpsSnapshotMetadata{rows: snapshots},
	})

	report := svc.CheckConsistency(context.Background(), 100)
	if report.Overall != OpsStatusUnhealthy {
		t.Fatalf("overall = %q, want unhealthy: %+v", report.Overall, report.Artifacts)
	}
	artifacts := consistencyArtifactsByKind(report)
	if artifacts["bundle"].SizeMismatches != 1 || artifacts["bundle"].MissingObjects != 0 {
		t.Fatalf("bundle artifact = %+v, want one size mismatch", artifacts["bundle"])
	}
	if artifacts["snapshot"].MissingObjects != 1 {
		t.Fatalf("snapshot artifact = %+v, want one missing object", artifacts["snapshot"])
	}
	if len(report.Issues) != 2 {
		t.Fatalf("issues = %+v, want 2", report.Issues)
	}
	statuses := map[string]bool{}
	for _, issue := range report.Issues {
		statuses[issue.Status] = true
		if issue.Key == "" || issue.Action == "" {
			t.Fatalf("issue missing operator context: %+v", issue)
		}
	}
	if !statuses["size_mismatch"] || !statuses["missing_object"] {
		t.Fatalf("issue statuses = %+v, want size_mismatch and missing_object", statuses)
	}
	if store.gets != 0 {
		t.Fatalf("consistency check read %d blob(s), want 0", store.gets)
	}
}

func TestOpsRestoreBaselineAndVerifyReportsZeroKnowledgeFindings(t *testing.T) {
	user := uuid.New()
	bundles := []model.BundleMeta{
		{UserID: user, BundleID: "ok", SizeBytes: 3},
		{UserID: user, BundleID: "wrong-size", SizeBytes: 5},
	}
	snapshots := []model.SnapshotMeta{
		{UserID: user, SnapshotID: "missing", SizeBytes: 7},
	}
	store := newFakeOpsBlobStore()
	store.setObject(storage.BundleKey(user.String(), "ok"), 3)
	store.setObject(storage.BundleKey(user.String(), "wrong-size"), 5)
	store.setObject(storage.SnapshotKey(user.String(), "missing"), 7)
	svc := NewOpsService(OpsDeps{
		BlobStore:        store,
		DatabasePing:     func(context.Context) error { return nil },
		BundleMetadata:   fakeOpsBundleMetadata{rows: bundles},
		SnapshotMetadata: fakeOpsSnapshotMetadata{rows: snapshots},
	})

	baseline := svc.GenerateRestoreBaseline(context.Background(), 100)
	if baseline.Overall != OpsStatusOK {
		t.Fatalf("baseline overall = %q, want ok: %+v", baseline.Overall, baseline.Findings)
	}
	if baseline.Manifest == nil || len(baseline.Manifest.Objects) != 3 {
		t.Fatalf("baseline manifest = %+v, want 3 objects", baseline.Manifest)
	}
	if store.gets != 0 {
		t.Fatalf("restore baseline read %d blob(s), want 0", store.gets)
	}

	restoreStore := newFakeOpsBlobStore()
	restoreStore.setObject(storage.BundleKey(user.String(), "ok"), 3)
	restoreStore.setObject(storage.BundleKey(user.String(), "wrong-size"), 4)
	restoreStore.setObject(storage.BundleKey(user.String(), "orphan"), 11)
	verifySvc := NewOpsService(OpsDeps{
		BlobStore:        restoreStore,
		DatabasePing:     func(context.Context) error { return nil },
		RedisPing:        func(context.Context) error { return errors.New("redis down") },
		BundleMetadata:   fakeOpsBundleMetadata{rows: []model.BundleMeta{bundles[0]}},
		SnapshotMetadata: fakeOpsSnapshotMetadata{rows: snapshots},
	})

	report := verifySvc.VerifyRestore(context.Background(), *baseline.Manifest, 100)
	if report.Overall != OpsStatusUnhealthy {
		t.Fatalf("verify overall = %q, want unhealthy: %+v", report.Overall, report)
	}
	if report.Summary.MissingObjects != 1 || report.Summary.SizeMismatches != 1 || report.Summary.OrphanObjects != 1 || report.Summary.MetadataMismatches != 1 {
		t.Fatalf("summary = %+v, want missing/size/orphan/metadata all 1", report.Summary)
	}
	statuses := map[string]bool{}
	for _, finding := range report.Findings {
		statuses[finding.Status] = true
		if finding.Action == "" {
			t.Fatalf("finding missing action: %+v", finding)
		}
	}
	for _, want := range []string{"missing_object", "size_mismatch", "orphan_object", "metadata_mismatch"} {
		if !statuses[want] {
			t.Fatalf("finding statuses = %+v, missing %s", statuses, want)
		}
	}
	if restoreStore.gets != 0 {
		t.Fatalf("restore verify read %d blob(s), want 0", restoreStore.gets)
	}
}

func TestOpsCheckHistoryPersistence(t *testing.T) {
	user := uuid.New()
	bundle := model.BundleMeta{UserID: user, BundleID: "missing", SizeBytes: 3}
	store := newFakeOpsBlobStore()
	history := &fakeOpsHistoryStore{}
	svc := NewOpsService(OpsDeps{
		BlobStore:        store,
		DatabasePing:     func(context.Context) error { return nil },
		BundleMetadata:   fakeOpsBundleMetadata{rows: []model.BundleMeta{bundle}},
		SnapshotMetadata: fakeOpsSnapshotMetadata{},
		History:          history,
	})

	dependency := svc.CheckDependencies(context.Background())
	if dependency.Overall != OpsStatusOK {
		t.Fatalf("dependency overall = %q, want ok", dependency.Overall)
	}
	consistency := svc.CheckConsistency(context.Background(), 10)
	if consistency.Overall != OpsStatusUnhealthy {
		t.Fatalf("consistency overall = %q, want unhealthy", consistency.Overall)
	}
	if len(history.runs) != 2 {
		t.Fatalf("history runs = %d, want 2", len(history.runs))
	}
	if history.runs[0].RunType != model.OpsRunTypeConsistency || history.runs[0].OverallStatus != OpsStatusUnhealthy {
		t.Fatalf("latest history = %+v, want unhealthy consistency", history.runs[0])
	}
	if history.runs[1].RunType != model.OpsRunTypeDependency || history.runs[1].OverallStatus != OpsStatusOK {
		t.Fatalf("previous history = %+v, want ok dependency", history.runs[1])
	}
	if !json.Valid(history.runs[0].SummarizedFindings) || !strings.Contains(string(history.runs[0].SummarizedFindings), "missing_object") {
		t.Fatalf("consistency findings = %s, want missing object summary", history.runs[0].SummarizedFindings)
	}
	if !json.Valid(history.runs[0].ArtifactCounts) || !strings.Contains(string(history.runs[0].ArtifactCounts), "missing_objects") {
		t.Fatalf("consistency artifact counts = %s, want missing_objects", history.runs[0].ArtifactCounts)
	}
	if !json.Valid(history.runs[1].ReportJSON) || !strings.Contains(string(history.runs[1].ReportJSON), "postgresql") {
		t.Fatalf("dependency report = %s, want report json", history.runs[1].ReportJSON)
	}

	view, err := svc.History(context.Background(), 10)
	if err != nil {
		t.Fatalf("History: %v", err)
	}
	if len(view.RecentRuns) != 2 || len(view.RecentFailures) != 1 {
		t.Fatalf("history view runs=%d failures=%d, want 2/1", len(view.RecentRuns), len(view.RecentFailures))
	}
}

func TestOpsScheduledFailureAlerts(t *testing.T) {
	user := uuid.New()
	bundle := model.BundleMeta{UserID: user, BundleID: "missing", SizeBytes: 3}
	store := newFakeOpsBlobStore()
	history := &fakeOpsHistoryStore{}
	notifier := &countingNotifier{}
	webhook := &captureOpsWebhookProvider{}
	svc := NewOpsService(OpsDeps{
		BlobStore:        store,
		BundleMetadata:   fakeOpsBundleMetadata{rows: []model.BundleMeta{bundle}},
		SnapshotMetadata: fakeOpsSnapshotMetadata{},
		History:          history,
		Alert: OpsAlertConfig{
			Email:         "ops@example.com",
			WebhookURL:    "https://hooks.example.com/ops",
			WebhookSecret: "hook-secret",
			AppName:       "HistorySync Test",
		},
		Notifier: notifier,
		Webhook:  webhook,
	})

	svc.RunScheduledConsistencyCheck(context.Background(), 10)
	if len(history.runs) != 1 || history.runs[0].RunType != model.OpsRunTypeConsistency || history.runs[0].OverallStatus != OpsStatusUnhealthy {
		t.Fatalf("history = %+v, want unhealthy consistency run", history.runs)
	}
	if notifier.sent != 1 {
		t.Fatalf("email alerts = %d, want 1", notifier.sent)
	}
	if webhook.sent != 1 || webhook.webhookURL != "https://hooks.example.com/ops" || webhook.secret != "hook-secret" {
		t.Fatalf("webhook sent=%d url=%q secret=%q, want configured hook", webhook.sent, webhook.webhookURL, webhook.secret)
	}
	if webhook.notification.Type != "ops.consistency.failure" || webhook.notification.Category != "ops" {
		t.Fatalf("webhook notification = %+v, want ops consistency failure", webhook.notification)
	}
	if got := webhook.notification.Data["run_type"]; got != string(model.OpsRunTypeConsistency) {
		t.Fatalf("webhook run_type = %#v, want consistency", got)
	}
	if store.gets != 0 {
		t.Fatalf("scheduled consistency check read %d blob(s), want 0", store.gets)
	}
}

func TestOpsScheduledHealthyCheckDoesNotAlert(t *testing.T) {
	store := newFakeOpsBlobStore()
	notifier := &countingNotifier{}
	webhook := &captureOpsWebhookProvider{}
	svc := NewOpsService(OpsDeps{
		BlobStore:    store,
		DatabasePing: func(context.Context) error { return nil },
		History:      &fakeOpsHistoryStore{},
		Alert: OpsAlertConfig{
			Email:      "ops@example.com",
			WebhookURL: "https://hooks.example.com/ops",
		},
		Notifier: notifier,
		Webhook:  webhook,
	})

	svc.RunScheduledDependencyCheck(context.Background())
	if notifier.sent != 0 || webhook.sent != 0 {
		t.Fatalf("alerts email=%d webhook=%d, want none for healthy dependency check", notifier.sent, webhook.sent)
	}
}

func statusByName(report OpsDependencyReport) map[string]string {
	out := make(map[string]string, len(report.Checks))
	for _, check := range report.Checks {
		out[check.Name] = check.Status
	}
	return out
}

func consistencyArtifactsByKind(report OpsConsistencyReport) map[string]OpsArtifactConsistency {
	out := make(map[string]OpsArtifactConsistency, len(report.Artifacts))
	for _, artifact := range report.Artifacts {
		out[artifact.Kind] = artifact
	}
	return out
}
