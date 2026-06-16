//go:build integration

package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/zerolog"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/historysync/hsync-server/migrations"
	"github.com/historysync/hsync-server/pkg/migrate"
	"github.com/historysync/hsync-server/pkg/model"
	"github.com/historysync/hsync-server/pkg/provider"
	"github.com/historysync/hsync-server/pkg/repository"
	"github.com/historysync/hsync-server/pkg/scheduler"
)

var serviceTestPool *pgxpool.Pool

func TestMain(m *testing.M) {
	code, err := runServiceIntegrationTests(m)
	if err != nil {
		fmt.Fprintln(os.Stderr, "service integration test setup failed:", err)
		os.Exit(1)
	}
	os.Exit(code)
}

func runServiceIntegrationTests(m *testing.M) (int, error) {
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
	defer func() { _ = container.Terminate(context.Background()) }()

	dsn, err := container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		return 0, fmt.Errorf("container connection string: %w", err)
	}

	pool, err := repository.NewPGXPool(ctx, dsn)
	if err != nil {
		return 0, fmt.Errorf("connect pool: %w", err)
	}
	defer pool.Close()

	if _, err := migrate.Up(ctx, pool, migrations.FS); err != nil {
		return 0, fmt.Errorf("apply migrations: %w", err)
	}

	serviceTestPool = pool
	return m.Run(), nil
}

func serviceTestContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func resetServiceTestDB(t *testing.T) {
	t.Helper()
	ctx := serviceTestContext(t)
	_, err := serviceTestPool.Exec(ctx, `
		TRUNCATE
			account_erasure_jobs,
			notification_outbox,
			user_notification_preferences,
			audit_logs,
			passkey_challenges,
			passkey_credentials,
			user_two_factor_backup_codes,
			user_two_factor,
			password_resets,
			email_verifications,
			storage_usage,
			quota_limits,
			bundles,
			snapshots,
			device_revocations,
			devices,
			refresh_tokens,
			users
		RESTART IDENTITY CASCADE`)
	if err != nil {
		t.Fatalf("reset db: %v", err)
	}
}

func serviceRepos(t *testing.T) *repository.Repos {
	t.Helper()
	resetServiceTestDB(t)
	return repository.New(serviceTestPool, nil)
}

func seedServiceUser(t *testing.T, repos *repository.Repos, email string) *model.User {
	t.Helper()
	user := &model.User{
		Email:        email,
		PasswordHash: "test-hash",
		DisplayName:  "Test User",
		Tier:         model.TierFree,
		Status:       model.StatusActive,
	}
	if err := repos.Users.Create(serviceTestContext(t), user); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	return user
}

func TestSchedulerRunOnceUsesAdvisoryLockAcrossInstances(t *testing.T) {
	resetServiceTestDB(t)
	var runs atomic.Int32
	started := make(chan struct{}, 1)
	release := make(chan struct{})

	task := scheduler.Task{
		Name:     "ops-dependency-check",
		LockKey:  scheduler.LockOpsDependencyCheck,
		Interval: time.Millisecond,
		Run: func(context.Context) error {
			runs.Add(1)
			select {
			case started <- struct{}{}:
			default:
			}
			<-release
			return nil
		},
	}

	s1 := scheduler.New(serviceTestPool, zerolog.Nop(), task)
	s2 := scheduler.New(serviceTestPool, zerolog.Nop(), task)

	ctx, cancel := context.WithCancel(serviceTestContext(t))
	defer cancel()

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		s1.Run(ctx)
	}()
	go func() {
		defer wg.Done()
		s2.Run(ctx)
	}()

	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("scheduler task never started")
	}

	time.Sleep(150 * time.Millisecond)
	if got := runs.Load(); got != 1 {
		t.Fatalf("runs while first worker holds lock = %d, want 1", got)
	}

	close(release)
	cancel()
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("scheduler workers did not stop")
	}
}

type integrationBlockingOpsRunner struct {
	svc     *OpsService
	started chan struct{}
	release chan struct{}
	calls   atomic.Int32
}

func (r *integrationBlockingOpsRunner) RunScheduledDependencyCheck(ctx context.Context) {
	r.calls.Add(1)
	r.svc.RunScheduledDependencyCheck(ctx)
	select {
	case r.started <- struct{}{}:
	default:
	}
	<-r.release
}

func (r *integrationBlockingOpsRunner) RunScheduledConsistencyCheck(context.Context, int32) {}

func TestOpsSchedulerDependencyCheckRunsOnceAcrossInstancesWithRedisUnavailable(t *testing.T) {
	repos := serviceRepos(t)
	store := newFakeOpsBlobStore()
	opsSvc := NewOpsService(OpsDeps{
		Config:           testOpsConfig(),
		Repos:            repos,
		BlobStore:        store,
		DatabasePing:     func(context.Context) error { return nil },
		RedisPing:        func(context.Context) error { return errors.New("redis unavailable") },
		BundleMetadata:   fakeOpsBundleMetadata{},
		SnapshotMetadata: fakeOpsSnapshotMetadata{},
		History:          repos.OpsHistory,
	})
	runner := &integrationBlockingOpsRunner{
		svc:     opsSvc,
		started: make(chan struct{}, 1),
		release: make(chan struct{}),
	}
	tasks := scheduler.OpsTasks(runner, scheduler.OpsTaskConfig{
		DependencyInterval: time.Millisecond,
	})
	if len(tasks) != 2 {
		t.Fatalf("ops tasks = %d, want 2", len(tasks))
	}

	s1 := scheduler.New(serviceTestPool, zerolog.Nop(), tasks...)
	s2 := scheduler.New(serviceTestPool, zerolog.Nop(), tasks...)

	ctx, cancel := context.WithCancel(serviceTestContext(t))
	defer cancel()

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		s1.Run(ctx)
	}()
	go func() {
		defer wg.Done()
		s2.Run(ctx)
	}()

	select {
	case <-runner.started:
	case <-time.After(5 * time.Second):
		t.Fatal("ops dependency scheduler task never started")
	}

	time.Sleep(150 * time.Millisecond)
	if got := runner.calls.Load(); got != 1 {
		t.Fatalf("dependency task calls while lock held = %d, want 1", got)
	}
	if store.puts != 1 || store.gets != 1 || store.deletes != 1 {
		t.Fatalf("storage probe ops puts=%d gets=%d deletes=%d, want 1 each", store.puts, store.gets, store.deletes)
	}

	history, err := repos.OpsHistory.ListRecent(serviceTestContext(t), 10)
	if err != nil {
		t.Fatalf("ListRecent() error = %v", err)
	}
	if len(history) != 1 {
		t.Fatalf("ops history runs = %d, want 1", len(history))
	}
	if history[0].RunType != model.OpsRunTypeDependency || history[0].OverallStatus != OpsStatusDegraded {
		t.Fatalf("ops history run = %+v, want degraded dependency run", history[0])
	}
	if !json.Valid(history[0].ReportJSON) || !bytes.Contains(history[0].ReportJSON, []byte(`"redis"`)) {
		t.Fatalf("ops history report = %s, want persisted redis dependency details", history[0].ReportJSON)
	}

	close(runner.release)
	cancel()
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("ops scheduler workers did not stop")
	}
}

func TestNotificationOutboxRepoClaimDueSplitsWorkAcrossWorkers(t *testing.T) {
	repos := serviceRepos(t)
	user := seedServiceUser(t, repos, "notify@example.com")
	now := time.Now().UTC().Add(-time.Minute)

	for i := 0; i < 3; i++ {
		item := &model.NotificationOutbox{
			UserID:      user.ID,
			Channel:     model.NotificationChannelEmail,
			Category:    "security",
			Type:        fmt.Sprintf("security.%d", i),
			PayloadJSON: json.RawMessage(`{"subject":"Login","message":"Detected","email_kind":"generic"}`),
			NextRetryAt: now,
		}
		if err := repos.NotificationOutbox.Enqueue(serviceTestContext(t), item); err != nil {
			t.Fatalf("enqueue outbox item %d: %v", i, err)
		}
	}

	var first, second []model.NotificationOutbox
	var err1, err2 error
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		first, err1 = repos.NotificationOutbox.ClaimDue(serviceTestContext(t), time.Now().UTC(), 2)
	}()
	go func() {
		defer wg.Done()
		second, err2 = repos.NotificationOutbox.ClaimDue(serviceTestContext(t), time.Now().UTC(), 2)
	}()
	wg.Wait()

	if err1 != nil || err2 != nil {
		t.Fatalf("claim errors: %v / %v", err1, err2)
	}

	claimed := map[uuid.UUID]bool{}
	for _, item := range append(first, second...) {
		if claimed[item.ID] {
			t.Fatalf("duplicate claim for outbox item %s", item.ID)
		}
		claimed[item.ID] = true
		if item.Status != model.NotificationOutboxProcessing {
			t.Fatalf("claimed item status = %q, want processing", item.Status)
		}
	}
	if len(claimed) != 3 {
		t.Fatalf("claimed items = %d, want 3", len(claimed))
	}

	remaining, err := repos.NotificationOutbox.ClaimDue(serviceTestContext(t), time.Now().UTC(), 10)
	if err != nil {
		t.Fatalf("claim remaining: %v", err)
	}
	if len(remaining) != 0 {
		t.Fatalf("remaining claims = %d, want 0", len(remaining))
	}
}

func TestRunErasureJobsMultipleWorkersCompleteJobOnce(t *testing.T) {
	repos := serviceRepos(t)
	user := seedServiceUser(t, repos, "erase@example.com")
	if err := repos.Users.SoftDelete(serviceTestContext(t), user.ID); err != nil {
		t.Fatalf("soft delete user: %v", err)
	}
	job := &model.AccountErasureJob{
		UserID:      user.ID,
		RequestedAt: time.Now().UTC().Add(-2 * time.Hour),
		EligibleAt:  time.Now().UTC().Add(-time.Hour),
		Status:      model.AccountErasureJobStatusPending,
		Summary:     json.RawMessage(`{"soft_deleted_bundles":0,"soft_deleted_snapshots":0}`),
	}
	if err := repos.AccountErasureJobs.Create(serviceTestContext(t), job); err != nil {
		t.Fatalf("create erasure job: %v", err)
	}

	retention := &RetentionService{repos: repos}
	var reports [2]AccountErasureRunReport
	var errs [2]error
	var wg sync.WaitGroup
	wg.Add(2)
	for i := 0; i < 2; i++ {
		i := i
		go func() {
			defer wg.Done()
			reports[i], errs[i] = retention.RunErasureJobs(serviceTestContext(t))
		}()
	}
	wg.Wait()

	for _, err := range errs {
		if err != nil {
			t.Fatalf("RunErasureJobs() error = %v", err)
		}
	}

	totalCompleted := reports[0].Completed + reports[1].Completed
	totalChecked := reports[0].Checked + reports[1].Checked
	if totalCompleted != 1 {
		t.Fatalf("completed reports = %+v, want exactly one completion", reports)
	}
	if totalChecked < 1 || totalChecked > 2 {
		t.Fatalf("checked reports = %+v, want one or two checks", reports)
	}

	jobs, err := repos.AccountErasureJobs.ListByUser(serviceTestContext(t), user.ID, 10)
	if err != nil {
		t.Fatalf("list erasure jobs: %v", err)
	}
	if len(jobs) != 1 {
		t.Fatalf("jobs = %+v, want one job", jobs)
	}
	if jobs[0].Status != model.AccountErasureJobStatusCompleted {
		t.Fatalf("job status = %q, want completed", jobs[0].Status)
	}
	if !json.Valid(jobs[0].Summary) {
		t.Fatalf("job summary is not valid json: %s", jobs[0].Summary)
	}
}

func TestRunErasureJobsPostCompletionFailureDoesNotRecompleteJob(t *testing.T) {
	repos := serviceRepos(t)
	user := seedServiceUser(t, repos, "erase-retry@example.com")
	if err := repos.Users.SoftDelete(serviceTestContext(t), user.ID); err != nil {
		t.Fatalf("soft delete user: %v", err)
	}
	job := &model.AccountErasureJob{
		UserID:      user.ID,
		RequestedAt: time.Now().UTC().Add(-2 * time.Hour),
		EligibleAt:  time.Now().UTC().Add(-time.Hour),
		Status:      model.AccountErasureJobStatusPending,
		Summary:     json.RawMessage(`{"soft_deleted_bundles":0,"soft_deleted_snapshots":0}`),
	}
	if err := repos.AccountErasureJobs.Create(serviceTestContext(t), job); err != nil {
		t.Fatalf("create erasure job: %v", err)
	}

	retention := &RetentionService{
		repos: repos,
		auditRecorder: failingAuditRecorder{
			failOn: map[model.AuditEventType]error{
				model.AuditEventAccountErasureJobFinished: errors.New("worker crashed after completion write"),
			},
		},
	}
	report, err := retention.RunErasureJobs(serviceTestContext(t))
	if err != nil {
		t.Fatalf("RunErasureJobs() error = %v", err)
	}
	if report.Completed != 1 || report.Failed != 0 {
		t.Fatalf("report = %+v, want completed=1 failed=0", report)
	}

	jobs, err := repos.AccountErasureJobs.ListByUser(serviceTestContext(t), user.ID, 10)
	if err != nil {
		t.Fatalf("list erasure jobs: %v", err)
	}
	if len(jobs) != 1 || jobs[0].Status != model.AccountErasureJobStatusCompleted {
		t.Fatalf("jobs after first run = %+v, want completed job", jobs)
	}

	second, err := retention.RunErasureJobs(serviceTestContext(t))
	if err != nil {
		t.Fatalf("second RunErasureJobs() error = %v", err)
	}
	if second.Completed != 0 || second.Checked != 0 {
		t.Fatalf("second report = %+v, want no duplicate completion", second)
	}
}

type failingAuditRecorder struct {
	failOn map[model.AuditEventType]error
}

func (r failingAuditRecorder) Record(_ context.Context, input AuditEventInput) error {
	if err := r.failOn[input.EventType]; err != nil {
		return err
	}
	return nil
}

type integrationBlockingNotifier struct {
	started chan struct{}
	release chan struct{}
	calls   atomic.Int32
}

func (n *integrationBlockingNotifier) DeliveryEnabled() bool { return true }
func (n *integrationBlockingNotifier) SendWelcome(context.Context, provider.WelcomeParams) error {
	return nil
}
func (n *integrationBlockingNotifier) SendEmailVerification(context.Context, provider.EmailVerificationParams) error {
	return nil
}
func (n *integrationBlockingNotifier) SendPasswordReset(context.Context, provider.PasswordResetParams) error {
	return nil
}
func (n *integrationBlockingNotifier) SendQuotaWarning(context.Context, provider.QuotaWarningParams) error {
	return n.block()
}
func (n *integrationBlockingNotifier) SendQuotaExhausted(context.Context, provider.QuotaExhaustedParams) error {
	return n.block()
}
func (n *integrationBlockingNotifier) SendQuotaRestored(context.Context, provider.QuotaRestoredParams) error {
	return n.block()
}
func (n *integrationBlockingNotifier) SendNotification(context.Context, provider.NotificationParams) error {
	return n.block()
}

func (n *integrationBlockingNotifier) block() error {
	n.calls.Add(1)
	select {
	case n.started <- struct{}{}:
	default:
	}
	<-n.release
	return nil
}

func TestNotificationServiceProcessOutboxWorkersDoNotDoubleDeliver(t *testing.T) {
	repos := serviceRepos(t)
	user := seedServiceUser(t, repos, "process@example.com")
	if err := repos.NotificationPrefs.Upsert(serviceTestContext(t), &model.NotificationPreferences{
		UserID:        user.ID,
		SecurityEmail: true,
	}); err != nil {
		t.Fatalf("upsert prefs: %v", err)
	}
	item := &model.NotificationOutbox{
		UserID:      user.ID,
		Channel:     model.NotificationChannelEmail,
		Category:    "security",
		Type:        "security.login",
		PayloadJSON: json.RawMessage(`{"subject":"Login","message":"Detected","email_kind":"generic"}`),
		NextRetryAt: time.Now().UTC().Add(-time.Minute),
	}
	if err := repos.NotificationOutbox.Enqueue(serviceTestContext(t), item); err != nil {
		t.Fatalf("enqueue outbox item: %v", err)
	}

	notifier := &integrationBlockingNotifier{
		started: make(chan struct{}, 1),
		release: make(chan struct{}),
	}
	svc := NewNotificationServiceWithStoresAndOutbox(repos.Users, repos.NotificationPrefs, repos.NotificationOutbox, notifier, nil, NotificationConfig{Enabled: true})

	ctx1, cancel1 := context.WithCancel(serviceTestContext(t))
	defer cancel1()
	ctx2, cancel2 := context.WithCancel(serviceTestContext(t))
	defer cancel2()

	resultCh := make(chan NotificationOutboxProcessResult, 2)
	errCh := make(chan error, 2)
	go func() {
		result, err := svc.ProcessOutbox(ctx1, 1)
		resultCh <- result
		errCh <- err
	}()

	select {
	case <-notifier.started:
	case <-time.After(5 * time.Second):
		t.Fatal("first worker never started delivery")
	}

	go func() {
		result, err := svc.ProcessOutbox(ctx2, 1)
		resultCh <- result
		errCh <- err
	}()

	time.Sleep(150 * time.Millisecond)
	close(notifier.release)

	var results []NotificationOutboxProcessResult
	for i := 0; i < 2; i++ {
		results = append(results, <-resultCh)
		if err := <-errCh; err != nil {
			t.Fatalf("ProcessOutbox() error = %v", err)
		}
	}

	if notifier.calls.Load() != 1 {
		t.Fatalf("delivery calls = %d, want 1", notifier.calls.Load())
	}

	var totalSent, totalClaimed int
	for _, result := range results {
		totalSent += result.Sent
		totalClaimed += result.Claimed
	}
	if totalSent != 1 || totalClaimed != 1 {
		t.Fatalf("results = %+v, want exactly one claimed+sent item", results)
	}

	stored, err := repos.NotificationOutbox.GetByID(serviceTestContext(t), item.ID)
	if err != nil {
		t.Fatalf("GetByID() error = %v", err)
	}
	if stored == nil || stored.Status != model.NotificationOutboxSent {
		t.Fatalf("stored item = %+v, want sent", stored)
	}
}

type integrationFlakyNotifier struct {
	calls atomic.Int32
}

func (n *integrationFlakyNotifier) DeliveryEnabled() bool { return true }
func (n *integrationFlakyNotifier) SendWelcome(context.Context, provider.WelcomeParams) error {
	return nil
}
func (n *integrationFlakyNotifier) SendEmailVerification(context.Context, provider.EmailVerificationParams) error {
	return nil
}
func (n *integrationFlakyNotifier) SendPasswordReset(context.Context, provider.PasswordResetParams) error {
	return nil
}
func (n *integrationFlakyNotifier) SendQuotaWarning(context.Context, provider.QuotaWarningParams) error {
	return n.failOnce()
}
func (n *integrationFlakyNotifier) SendQuotaExhausted(context.Context, provider.QuotaExhaustedParams) error {
	return n.failOnce()
}
func (n *integrationFlakyNotifier) SendQuotaRestored(context.Context, provider.QuotaRestoredParams) error {
	return n.failOnce()
}
func (n *integrationFlakyNotifier) SendNotification(context.Context, provider.NotificationParams) error {
	return n.failOnce()
}

func (n *integrationFlakyNotifier) failOnce() error {
	if n.calls.Add(1) == 1 {
		return errors.New("worker crashed after claiming notification")
	}
	return nil
}

func TestNotificationOutboxFailureCanBeRetriedByAnotherWorker(t *testing.T) {
	repos := serviceRepos(t)
	user := seedServiceUser(t, repos, "retry-notify@example.com")
	if err := repos.NotificationPrefs.Upsert(serviceTestContext(t), &model.NotificationPreferences{
		UserID:        user.ID,
		SecurityEmail: true,
	}); err != nil {
		t.Fatalf("upsert prefs: %v", err)
	}
	item := &model.NotificationOutbox{
		UserID:      user.ID,
		Channel:     model.NotificationChannelEmail,
		Category:    "security",
		Type:        "security.retry",
		PayloadJSON: json.RawMessage(`{"subject":"Retry","message":"Deliver","email_kind":"generic"}`),
		NextRetryAt: time.Now().UTC().Add(-time.Minute),
	}
	if err := repos.NotificationOutbox.Enqueue(serviceTestContext(t), item); err != nil {
		t.Fatalf("enqueue outbox item: %v", err)
	}

	notifier := &integrationFlakyNotifier{}
	svc := NewNotificationServiceWithStoresAndOutbox(repos.Users, repos.NotificationPrefs, repos.NotificationOutbox, notifier, nil, NotificationConfig{Enabled: true})

	first, err := svc.ProcessOutbox(serviceTestContext(t), 1)
	if err != nil {
		t.Fatalf("first ProcessOutbox() error = %v", err)
	}
	if first.Claimed != 1 || first.Retried != 1 || first.Sent != 0 || first.Failed != 0 {
		t.Fatalf("first result = %+v, want one claimed retry", first)
	}

	failedState, err := repos.NotificationOutbox.GetByID(serviceTestContext(t), item.ID)
	if err != nil {
		t.Fatalf("GetByID() after failure error = %v", err)
	}
	if failedState == nil || failedState.Status != model.NotificationOutboxPending || failedState.AttemptCount != 1 {
		t.Fatalf("failed state = %+v, want pending attempt_count=1", failedState)
	}

	if _, err := serviceTestPool.Exec(serviceTestContext(t),
		`UPDATE notification_outbox SET next_retry_at = $2 WHERE id = $1`,
		item.ID, time.Now().UTC().Add(-time.Second)); err != nil {
		t.Fatalf("force retry due: %v", err)
	}

	second, err := svc.ProcessOutbox(serviceTestContext(t), 1)
	if err != nil {
		t.Fatalf("second ProcessOutbox() error = %v", err)
	}
	if second.Claimed != 1 || second.Sent != 1 {
		t.Fatalf("second result = %+v, want one claimed sent retry", second)
	}
	if notifier.calls.Load() != 2 {
		t.Fatalf("delivery calls = %d, want 2 (failed once then retried once)", notifier.calls.Load())
	}

	stored, err := repos.NotificationOutbox.GetByID(serviceTestContext(t), item.ID)
	if err != nil {
		t.Fatalf("GetByID() after retry error = %v", err)
	}
	if stored == nil || stored.Status != model.NotificationOutboxSent || stored.AttemptCount != 1 {
		t.Fatalf("stored item after retry = %+v, want sent attempt_count=1", stored)
	}
}

type integrationIdempotentResult struct {
	Value string `json:"value"`
}

func TestPostgresIdempotencyHandlesInProgressReplayAndConflict(t *testing.T) {
	repos := serviceRepos(t)
	svc := NewIdempotencyService(repos.Idempotency)
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	var executions atomic.Int32

	firstResult := make(chan *IdempotencyExecution[integrationIdempotentResult], 1)
	firstErr := make(chan error, 1)
	go func() {
		result, err := ExecuteIdempotent(serviceTestContext(t), svc, IdempotencyOptions{
			Scope:          "admin.notification.retry",
			IdempotencyKey: "same-key",
			Payload:        map[string]any{"notification_id": "n-1"},
			RequireKey:     true,
		}, func(context.Context) (*integrationIdempotentResult, int, error) {
			executions.Add(1)
			select {
			case started <- struct{}{}:
			default:
			}
			<-release
			return &integrationIdempotentResult{Value: "ok"}, 202, nil
		})
		firstResult <- result
		firstErr <- err
	}()

	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("first idempotent execution never started")
	}

	if _, err := ExecuteIdempotent(serviceTestContext(t), svc, IdempotencyOptions{
		Scope:          "admin.notification.retry",
		IdempotencyKey: "same-key",
		Payload:        map[string]any{"notification_id": "n-1"},
		RequireKey:     true,
	}, func(context.Context) (*integrationIdempotentResult, int, error) {
		t.Fatal("duplicate in-progress execution should not run")
		return nil, 0, nil
	}); !errors.Is(err, ErrIdempotencyInProgress) {
		t.Fatalf("concurrent execution error = %v, want ErrIdempotencyInProgress", err)
	}

	close(release)
	first := <-firstResult
	if err := <-firstErr; err != nil {
		t.Fatalf("first ExecuteIdempotent() error = %v", err)
	}
	if first == nil || first.Data == nil || first.Data.Value != "ok" || first.ResponseStatus != 202 || first.Replayed {
		t.Fatalf("first execution = %+v, want fresh 202 ok", first)
	}

	replayed, err := ExecuteIdempotent(serviceTestContext(t), svc, IdempotencyOptions{
		Scope:          "admin.notification.retry",
		IdempotencyKey: "same-key",
		Payload:        map[string]any{"notification_id": "n-1"},
		RequireKey:     true,
	}, func(context.Context) (*integrationIdempotentResult, int, error) {
		t.Fatal("replay execution should not re-run")
		return nil, 0, nil
	})
	if err != nil {
		t.Fatalf("replay ExecuteIdempotent() error = %v", err)
	}
	if replayed == nil || replayed.Data == nil || replayed.Data.Value != "ok" || !replayed.Replayed || replayed.ResponseStatus != 202 {
		t.Fatalf("replay execution = %+v, want replayed 202 ok", replayed)
	}
	if executions.Load() != 1 {
		t.Fatalf("fresh executions = %d, want 1", executions.Load())
	}

	if _, err := ExecuteIdempotent(serviceTestContext(t), svc, IdempotencyOptions{
		Scope:          "admin.notification.retry",
		IdempotencyKey: "same-key",
		Payload:        map[string]any{"notification_id": "n-2"},
		RequireKey:     true,
	}, func(context.Context) (*integrationIdempotentResult, int, error) {
		t.Fatal("conflicting execution should not run")
		return nil, 0, nil
	}); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("conflicting execution error = %v, want ErrIdempotencyConflict", err)
	}
}
