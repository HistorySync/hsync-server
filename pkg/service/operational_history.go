package service

import (
	"context"
	"fmt"
	"time"

	"github.com/historysync/hsync-server/pkg/repository"
)

// OperationalHistoryRetentionPolicy describes the lifecycle for operational
// history tables:
//   - hot data remains in the source table and powers existing list/recent APIs
//   - archival data is moved into archive tables for export and audit review
//   - purgeable data is archive data older than the total retention window
type OperationalHistoryRetentionPolicy struct {
	HotRetention     time.Duration
	ArchiveRetention time.Duration
	DryRun           bool
}

type OperationalHistoryRetentionResult struct {
	HotCutoff     time.Time `json:"hot_cutoff"`
	ArchiveCutoff time.Time `json:"archive_cutoff"`
	DryRun        bool      `json:"dry_run"`

	repository.OperationalHistoryRetentionCounts
}

type operationalHistoryRetentionStore interface {
	ReportRetention(ctx context.Context, hotCutoff, archiveCutoff time.Time) (repository.OperationalHistoryRetentionCounts, error)
	ApplyRetention(ctx context.Context, hotCutoff, archiveCutoff time.Time) (repository.OperationalHistoryRetentionCounts, error)
	Export(ctx context.Context, filter repository.OperationalExportFilter) ([]repository.OperationalExportRecord, error)
}

type OperationalHistoryRetentionService struct {
	store operationalHistoryRetentionStore
	now   func() time.Time
}

func NewOperationalHistoryRetentionService(repos *repository.Repos) *OperationalHistoryRetentionService {
	var store operationalHistoryRetentionStore
	if repos != nil {
		store = repos.OperationalHistory
	}
	return &OperationalHistoryRetentionService{store: store, now: time.Now}
}

func (s *OperationalHistoryRetentionService) Run(ctx context.Context, policy OperationalHistoryRetentionPolicy) (OperationalHistoryRetentionResult, error) {
	if s == nil {
		return runOperationalHistoryRetention(ctx, nil, time.Now(), policy)
	}
	now := time.Now
	if s.now != nil {
		now = s.now
	}
	return runOperationalHistoryRetention(ctx, s.store, now(), policy)
}

func (s *OperationalHistoryRetentionService) Export(ctx context.Context, filter repository.OperationalExportFilter) ([]repository.OperationalExportRecord, error) {
	if s == nil || s.store == nil {
		return []repository.OperationalExportRecord{}, nil
	}
	return s.store.Export(ctx, filter)
}

func runOperationalHistoryRetention(ctx context.Context, store operationalHistoryRetentionStore, now time.Time, policy OperationalHistoryRetentionPolicy) (OperationalHistoryRetentionResult, error) {
	hotCutoff, archiveCutoff, err := operationalHistoryCutoffs(now, policy)
	if err != nil {
		return OperationalHistoryRetentionResult{}, err
	}
	result := OperationalHistoryRetentionResult{
		HotCutoff:     hotCutoff,
		ArchiveCutoff: archiveCutoff,
		DryRun:        policy.DryRun,
	}
	if store == nil {
		return result, nil
	}
	if policy.DryRun {
		counts, err := store.ReportRetention(ctx, hotCutoff, archiveCutoff)
		if err != nil {
			return result, err
		}
		result.OperationalHistoryRetentionCounts = counts
		return result, nil
	}
	counts, err := store.ApplyRetention(ctx, hotCutoff, archiveCutoff)
	if err != nil {
		return result, err
	}
	result.OperationalHistoryRetentionCounts = counts
	return result, nil
}

func operationalHistoryCutoffs(now time.Time, policy OperationalHistoryRetentionPolicy) (time.Time, time.Time, error) {
	if policy.HotRetention <= 0 {
		return time.Time{}, time.Time{}, fmt.Errorf("history hot retention must be greater than 0")
	}
	if policy.ArchiveRetention <= policy.HotRetention {
		return time.Time{}, time.Time{}, fmt.Errorf("history archive retention must be greater than hot retention")
	}
	return now.Add(-policy.HotRetention), now.Add(-policy.ArchiveRetention), nil
}
