package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/historysync/hsync-server/pkg/repository"
)

type fakeOperationalHistoryStore struct {
	reportCalls int
	applyCalls  int
	hotCutoff   time.Time
	purgeCutoff time.Time
	report      repository.OperationalHistoryRetentionCounts
	apply       repository.OperationalHistoryRetentionCounts
	err         error
}

func (f *fakeOperationalHistoryStore) ReportRetention(ctx context.Context, hotCutoff, archiveCutoff time.Time) (repository.OperationalHistoryRetentionCounts, error) {
	f.reportCalls++
	f.hotCutoff = hotCutoff
	f.purgeCutoff = archiveCutoff
	if f.err != nil {
		return repository.OperationalHistoryRetentionCounts{}, f.err
	}
	return f.report, nil
}

func (f *fakeOperationalHistoryStore) ApplyRetention(ctx context.Context, hotCutoff, archiveCutoff time.Time) (repository.OperationalHistoryRetentionCounts, error) {
	f.applyCalls++
	f.hotCutoff = hotCutoff
	f.purgeCutoff = archiveCutoff
	if f.err != nil {
		return repository.OperationalHistoryRetentionCounts{}, f.err
	}
	return f.apply, nil
}

func (f *fakeOperationalHistoryStore) Export(ctx context.Context, filter repository.OperationalExportFilter) ([]repository.OperationalExportRecord, error) {
	return []repository.OperationalExportRecord{}, f.err
}

func TestOperationalHistoryRetentionCutoffs(t *testing.T) {
	now := time.Date(2026, 6, 6, 12, 0, 0, 0, time.UTC)
	hot, archive, err := operationalHistoryCutoffs(now, OperationalHistoryRetentionPolicy{
		HotRetention:     30 * 24 * time.Hour,
		ArchiveRetention: 365 * 24 * time.Hour,
	})
	if err != nil {
		t.Fatalf("operationalHistoryCutoffs() error = %v", err)
	}
	if want := now.Add(-30 * 24 * time.Hour); !hot.Equal(want) {
		t.Fatalf("hot cutoff = %s, want %s", hot, want)
	}
	if want := now.Add(-365 * 24 * time.Hour); !archive.Equal(want) {
		t.Fatalf("archive cutoff = %s, want %s", archive, want)
	}
}

func TestOperationalHistoryRetentionRejectsInvalidPolicy(t *testing.T) {
	now := time.Date(2026, 6, 6, 12, 0, 0, 0, time.UTC)
	if _, _, err := operationalHistoryCutoffs(now, OperationalHistoryRetentionPolicy{}); err == nil {
		t.Fatal("operationalHistoryCutoffs() error = nil, want invalid hot retention error")
	}
	if _, _, err := operationalHistoryCutoffs(now, OperationalHistoryRetentionPolicy{
		HotRetention:     90 * 24 * time.Hour,
		ArchiveRetention: 30 * 24 * time.Hour,
	}); err == nil {
		t.Fatal("operationalHistoryCutoffs() error = nil, want archive retention error")
	}
}

func TestOperationalHistoryRetentionDryRunReportsOnly(t *testing.T) {
	now := time.Date(2026, 6, 6, 12, 0, 0, 0, time.UTC)
	store := &fakeOperationalHistoryStore{
		report: repository.OperationalHistoryRetentionCounts{
			ArchivedAuditLogs:              3,
			ArchivedOpsCheckRuns:           2,
			ArchivedNotificationOutboxRows: 1,
			PurgedAuditLogArchives:         4,
		},
	}
	result, err := runOperationalHistoryRetention(context.Background(), store, now, OperationalHistoryRetentionPolicy{
		HotRetention:     90 * 24 * time.Hour,
		ArchiveRetention: 365 * 24 * time.Hour,
		DryRun:           true,
	})
	if err != nil {
		t.Fatalf("runOperationalHistoryRetention() error = %v", err)
	}
	if store.reportCalls != 1 || store.applyCalls != 0 {
		t.Fatalf("calls report=%d apply=%d, want report=1 apply=0", store.reportCalls, store.applyCalls)
	}
	if !store.hotCutoff.Equal(now.Add(-90*24*time.Hour)) || !store.purgeCutoff.Equal(now.Add(-365*24*time.Hour)) {
		t.Fatalf("cutoffs hot=%s purge=%s", store.hotCutoff, store.purgeCutoff)
	}
	if !result.DryRun || result.ArchivedAuditLogs != 3 || result.PurgedAuditLogArchives != 4 {
		t.Fatalf("result = %+v, want dry-run report counts", result)
	}
}

func TestOperationalHistoryRetentionApplyPurgesTargetCounts(t *testing.T) {
	now := time.Date(2026, 6, 6, 12, 0, 0, 0, time.UTC)
	store := &fakeOperationalHistoryStore{
		apply: repository.OperationalHistoryRetentionCounts{
			ArchivedAuditLogs:              5,
			ArchivedOpsCheckRuns:           4,
			ArchivedNotificationOutboxRows: 3,
			PurgedAuditLogArchives:         2,
			PurgedOpsCheckRunArchives:      1,
			PurgedNotificationArchives:     6,
		},
	}
	result, err := runOperationalHistoryRetention(context.Background(), store, now, OperationalHistoryRetentionPolicy{
		HotRetention:     30 * 24 * time.Hour,
		ArchiveRetention: 180 * 24 * time.Hour,
	})
	if err != nil {
		t.Fatalf("runOperationalHistoryRetention() error = %v", err)
	}
	if store.reportCalls != 0 || store.applyCalls != 1 {
		t.Fatalf("calls report=%d apply=%d, want report=0 apply=1", store.reportCalls, store.applyCalls)
	}
	if result.PurgedAuditLogArchives != 2 || result.PurgedOpsCheckRunArchives != 1 || result.PurgedNotificationArchives != 6 {
		t.Fatalf("purge counts = %+v, want only archive purge counters populated from apply", result.OperationalHistoryRetentionCounts)
	}
}

func TestOperationalHistoryRetentionPropagatesStoreError(t *testing.T) {
	want := errors.New("store unavailable")
	_, err := runOperationalHistoryRetention(context.Background(), &fakeOperationalHistoryStore{err: want}, time.Now(), OperationalHistoryRetentionPolicy{
		HotRetention:     time.Hour,
		ArchiveRetention: 2 * time.Hour,
		DryRun:           true,
	})
	if !errors.Is(err, want) {
		t.Fatalf("error = %v, want %v", err, want)
	}
}
