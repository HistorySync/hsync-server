//go:build integration

package repository

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/historysync/hsync-server/pkg/model"
)

func TestNotificationOutboxFailedListUsesRecentIndex(t *testing.T) {
	repos := setupTest(t)
	ctx := testContext(t)
	user := seedUser(t, repos, "outbox-index@example.com")
	now := time.Now().UTC()

	for i := 0; i < 40; i++ {
		item := &model.NotificationOutbox{
			UserID:      user.ID,
			Channel:     model.NotificationChannelEmail,
			Category:    "billing",
			Type:        "quota.warning",
			PayloadJSON: []byte(`{"subject":"Quota"}`),
			NextRetryAt: now.Add(-time.Minute),
		}
		if err := repos.NotificationOutbox.Enqueue(ctx, item); err != nil {
			t.Fatalf("enqueue %d: %v", i, err)
		}
		if i%2 == 0 {
			if err := repos.NotificationOutbox.MarkFailed(ctx, item.ID, "failure"); err != nil {
				t.Fatalf("mark failed %d: %v", i, err)
			}
		}
	}

	plan := explainPlan(t, ctx, `
		SELECT id, user_id, channel, category, type, payload_json,
		       status, attempt_count, next_retry_at, last_error,
		       created_at, updated_at, sent_at
		FROM notification_outbox
		WHERE status = 'failed'
		ORDER BY updated_at DESC, created_at DESC, id DESC
		LIMIT $1 OFFSET $2`, int32(10), int32(0))
	if !strings.Contains(plan, "idx_notification_outbox_failed_recent") {
		t.Fatalf("plan did not use failed notification index:\n%s", plan)
	}
}

func TestOpsHistoryRecentQueriesUseRecentIndexes(t *testing.T) {
	setupTest(t)
	ctx := testContext(t)
	repo := NewOpsHistoryRepo(testPool)
	now := time.Now().UTC()

	for i := 0; i < 40; i++ {
		run := &model.OpsCheckRun{
			RunType:        model.OpsRunTypeDependency,
			OverallStatus:  "ok",
			StartedAt:      now.Add(-time.Duration(i) * time.Minute),
			FinishedAt:     now.Add(-time.Duration(i) * time.Minute).Add(time.Second),
			DurationMillis: int64(i),
		}
		if i%3 == 0 {
			run.OverallStatus = "degraded"
		}
		if err := repo.Create(ctx, run); err != nil {
			t.Fatalf("create run %d: %v", i, err)
		}
	}

	plan := explainPlan(t, ctx, `
		SELECT id, run_type, overall_status, started_at, finished_at, duration_millis,
		       summarized_findings, artifact_counts, report_json, created_at
		FROM ops_check_runs
		ORDER BY started_at DESC, id DESC
		LIMIT $1`, int32(10))
	if !strings.Contains(plan, "idx_ops_check_runs_started_at_recent") {
		t.Fatalf("plan did not use recent ops history index:\n%s", plan)
	}

	failurePlan := explainPlan(t, ctx, `
		SELECT id, run_type, overall_status, started_at, finished_at, duration_millis,
		       summarized_findings, artifact_counts, report_json, created_at
		FROM ops_check_runs
		WHERE overall_status <> 'ok'
		ORDER BY started_at DESC, id DESC
		LIMIT $1`, int32(10))
	if !strings.Contains(failurePlan, "idx_ops_check_runs_failed_recent") {
		t.Fatalf("plan did not use failed ops history index:\n%s", failurePlan)
	}
}

func TestAccountErasureJobsListByUserUsesRecentIndex(t *testing.T) {
	repos := setupTest(t)
	ctx := testContext(t)
	repo := &AccountErasureJobRepo{pool: testPool}
	user := seedUser(t, repos, "erasure-index@example.com")
	now := time.Now().UTC()

	for i := 0; i < 30; i++ {
		job := &model.AccountErasureJob{
			UserID:      user.ID,
			RequestedAt: now.Add(-time.Duration(i) * time.Hour),
			EligibleAt:  now.Add(-time.Duration(i) * time.Hour),
			Status:      model.AccountErasureJobStatusPending,
		}
		if err := repo.Create(ctx, job); err != nil {
			t.Fatalf("create erasure job %d: %v", i, err)
		}
	}

	plan := explainPlan(t, ctx, `
		SELECT id, user_id, requested_at, eligible_at, status, summary, last_error,
		       started_at, finished_at, created_at, updated_at
		FROM account_erasure_jobs
		WHERE user_id = $1
		ORDER BY requested_at DESC, id DESC
		LIMIT $2`, user.ID, int32(10))
	if !strings.Contains(plan, "idx_account_erasure_jobs_user_recent") {
		t.Fatalf("plan did not use account erasure index:\n%s", plan)
	}
}

func TestOperationalHistoryApplyRetentionProcessesOneBatch(t *testing.T) {
	setupTest(t)
	ctx := testContext(t)
	history := NewOperationalHistoryRepo(testPool)
	now := time.Now().UTC()
	hotCutoff := now.Add(-time.Hour)
	archiveCutoff := now.Add(-24 * time.Hour)

	const total = 700
	for i := 0; i < total; i++ {
		run := &model.OpsCheckRun{
			RunType:        model.OpsRunTypeDependency,
			OverallStatus:  "ok",
			StartedAt:      now.Add(-2 * time.Hour).Add(-time.Duration(i) * time.Second),
			FinishedAt:     now.Add(-2 * time.Hour).Add(-time.Duration(i) * time.Second).Add(time.Second),
			DurationMillis: int64(i),
		}
		if err := NewOpsHistoryRepo(testPool).Create(ctx, run); err != nil {
			t.Fatalf("seed run %d: %v", i, err)
		}
	}

	counts, err := history.ApplyRetention(ctx, hotCutoff, archiveCutoff)
	if err != nil {
		t.Fatalf("ApplyRetention: %v", err)
	}
	if counts.ArchivedOpsCheckRuns != int64(operationalHistoryRetentionBatchSize) {
		t.Fatalf("ArchivedOpsCheckRuns = %d, want %d", counts.ArchivedOpsCheckRuns, operationalHistoryRetentionBatchSize)
	}

	var hotRemaining, archiveRows int64
	if err := testPool.QueryRow(ctx, `SELECT COUNT(*) FROM ops_check_runs WHERE started_at < $1`, hotCutoff).Scan(&hotRemaining); err != nil {
		t.Fatalf("count hot rows: %v", err)
	}
	if err := testPool.QueryRow(ctx, `SELECT COUNT(*) FROM ops_check_runs_archive WHERE started_at < $1`, hotCutoff).Scan(&archiveRows); err != nil {
		t.Fatalf("count archive rows: %v", err)
	}
	if hotRemaining != total-int64(operationalHistoryRetentionBatchSize) {
		t.Fatalf("hotRemaining = %d, want %d", hotRemaining, total-int64(operationalHistoryRetentionBatchSize))
	}
	if archiveRows != int64(operationalHistoryRetentionBatchSize) {
		t.Fatalf("archiveRows = %d, want %d", archiveRows, operationalHistoryRetentionBatchSize)
	}
}

func explainPlan(t *testing.T, ctx context.Context, query string, args ...any) string {
	t.Helper()
	conn, err := testPool.Acquire(ctx)
	if err != nil {
		t.Fatalf("acquire connection: %v", err)
	}
	defer conn.Release()

	if _, err := conn.Exec(ctx, "BEGIN"); err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() {
		_, _ = conn.Exec(context.Background(), "ROLLBACK")
	}()
	if _, err := conn.Exec(ctx, "SET LOCAL enable_seqscan = off"); err != nil {
		t.Fatalf("set local: %v", err)
	}
	rows, err := conn.Query(ctx, "EXPLAIN (COSTS OFF) "+query, args...)
	if err != nil {
		t.Fatalf("explain query: %v", err)
	}
	defer rows.Close()

	lines := make([]string, 0, 8)
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			t.Fatalf("scan explain: %v", err)
		}
		lines = append(lines, line)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("explain rows: %v", err)
	}
	return strings.Join(lines, "\n")
}
