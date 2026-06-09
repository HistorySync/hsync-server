package service

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/historysync/hsync-server/pkg/model"
	"github.com/historysync/hsync-server/pkg/provider"
)

const erasureJobBatchSize = int32(50)

type AccountErasureRunReport struct {
	Checked   int64 `json:"checked"`
	Completed int64 `json:"completed"`
	Failed    int64 `json:"failed"`
	Skipped   int64 `json:"skipped"`
}

type AccountErasureCertificate struct {
	Version               int                            `json:"version"`
	JobID                 uuid.UUID                      `json:"job_id"`
	UserID                uuid.UUID                      `json:"user_id"`
	RequestedAt           time.Time                      `json:"requested_at"`
	EligibleAt            time.Time                      `json:"eligible_at"`
	CompletedAt           time.Time                      `json:"completed_at"`
	Status                string                         `json:"status"`
	ZeroKnowledgeBoundary ZeroKnowledgeBoundaryStatement `json:"zero_knowledge_boundary"`
	Deleted               []ErasureCertificateItem       `json:"deleted"`
	Retained              []ErasureCertificateItem       `json:"retained"`
}

type ZeroKnowledgeBoundaryStatement struct {
	BlobContentsParsed    bool   `json:"blob_contents_parsed"`
	BlobContentsDecrypted bool   `json:"blob_contents_decrypted"`
	Statement             string `json:"statement"`
}

type ErasureCertificateItem struct {
	Category    string         `json:"category"`
	Count       int64          `json:"count"`
	Bytes       int64          `json:"bytes,omitempty"`
	Reason      string         `json:"reason,omitempty"`
	RetainedFor string         `json:"retained_for,omitempty"`
	CompletedAt time.Time      `json:"completed_at,omitempty"`
	Metadata    map[string]any `json:"metadata,omitempty"`
}

type erasureInitialSummary struct {
	SoftDeletedBundles       int64 `json:"soft_deleted_bundles"`
	SoftDeletedBundleBytes   int64 `json:"soft_deleted_bundle_bytes"`
	SoftDeletedSnapshots     int64 `json:"soft_deleted_snapshots"`
	SoftDeletedSnapshotBytes int64 `json:"soft_deleted_snapshot_bytes"`
}

func (s *RetentionService) RunErasureJobs(ctx context.Context) (AccountErasureRunReport, error) {
	report := AccountErasureRunReport{}
	if s == nil || s.repos == nil || s.repos.AccountErasureJobs == nil {
		return report, nil
	}
	now := time.Now().UTC()
	jobs, err := s.repos.AccountErasureJobs.ListEligible(ctx, now, erasureJobBatchSize)
	if err != nil {
		return report, err
	}
	for _, job := range jobs {
		if err := ctx.Err(); err != nil {
			return report, err
		}
		report.Checked++
		claimed, err := s.repos.AccountErasureJobs.MarkRunning(ctx, job.ID, now)
		if err != nil {
			return report, err
		}
		if !claimed {
			report.Skipped++
			continue
		}
		s.recordErasureAudit(ctx, job, model.AuditEventAccountErasureJobStarted, map[string]any{
			"status": "running",
		})
		if err := s.completeErasureJob(ctx, job, now); err != nil {
			report.Failed++
			lastError := err.Error()
			_ = s.repos.AccountErasureJobs.MarkFailed(ctx, job.ID, lastError, time.Now().UTC())
			s.recordErasureAudit(ctx, job, model.AuditEventAccountErasureJobFailed, map[string]any{
				"status":     "failed",
				"last_error": lastError,
			})
			continue
		}
		report.Completed++
	}
	return report, nil
}

func (s *RetentionService) completeErasureJob(ctx context.Context, job model.AccountErasureJob, now time.Time) error {
	if s.repos.Users == nil {
		return fmt.Errorf("user repository is not configured")
	}
	user, err := s.repos.Users.GetAnyByID(ctx, job.UserID)
	if err != nil {
		return fmt.Errorf("get erasure user: %w", err)
	}
	if user == nil {
		return fmt.Errorf("user tombstone is missing")
	}
	if user.DeletedAt == nil || user.Status != model.StatusDeleted {
		return fmt.Errorf("user is not soft-deleted")
	}
	if user.DeletedAt.After(job.EligibleAt) {
		return fmt.Errorf("user deletion timestamp is after erasure eligibility")
	}

	deleted := []ErasureCertificateItem{}
	retained := []ErasureCertificateItem{}
	initial := erasureInitialSummary{}
	if len(job.Summary) > 0 {
		_ = json.Unmarshal(job.Summary, &initial)
	}
	addDeleted := func(category string, count, bytes int64, metadata map[string]any) {
		deleted = append(deleted, ErasureCertificateItem{
			Category:    category,
			Count:       count,
			Bytes:       bytes,
			CompletedAt: now,
			Metadata:    metadata,
		})
	}

	if s.repos.RefreshTokens != nil {
		count, err := s.repos.RefreshTokens.DeleteByUser(ctx, job.UserID)
		if err != nil {
			return err
		}
		addDeleted("refresh_tokens", count, 0, nil)
	}
	if s.repos.Devices != nil {
		count, err := s.repos.Devices.DeleteByUser(ctx, job.UserID)
		if err != nil {
			return err
		}
		addDeleted("devices", count, 0, nil)
	}
	if s.repos.TwoFactor != nil {
		if err := s.repos.TwoFactor.DeleteByUser(ctx, job.UserID); err != nil {
			return err
		}
		addDeleted("two_factor_state_and_backup_codes", 1, 0, map[string]any{"idempotent": true})
	}
	if s.repos.Passkeys != nil {
		if err := s.repos.Passkeys.DeleteCredentialsByUser(ctx, job.UserID); err != nil {
			return err
		}
		if err := s.repos.Passkeys.ExpireChallengesByUser(ctx, job.UserID, now); err != nil {
			return err
		}
		addDeleted("passkey_credentials_and_challenges", 1, 0, map[string]any{"idempotent": true})
	}
	if s.repos.NotificationPrefs != nil {
		count, err := s.repos.NotificationPrefs.DeleteByUser(ctx, job.UserID)
		if err != nil {
			return err
		}
		addDeleted("notification_preferences", count, 0, nil)
	}
	if s.repos.Quota != nil {
		usageRows, err := s.repos.Quota.DeleteUsageByUser(ctx, job.UserID)
		if err != nil {
			return err
		}
		limitRows, err := s.repos.Quota.DeleteLimitsByUser(ctx, job.UserID)
		if err != nil {
			return err
		}
		addDeleted("quota_usage_and_overrides", usageRows+limitRows, 0, map[string]any{
			"storage_usage_rows": usageRows,
			"quota_limit_rows":   limitRows,
		})
	}

	bundleRows, bundleBytes, err := s.repos.Bundles.CountByUserIncludingDeleted(ctx, job.UserID)
	if err != nil {
		return err
	}
	if bundleRows > 0 {
		retained = append(retained, ErasureCertificateItem{
			Category: "bundle_metadata_and_blob_objects",
			Count:    bundleRows,
			Bytes:    bundleBytes,
			Reason:   "retention purge has not yet removed every eligible bundle row and blob object; retry after retention cleanup succeeds",
			Metadata: map[string]any{"object_key_pattern": "bundles/{user_id}/{bundle_id}"},
		})
	}
	snapshotRows, snapshotBytes, err := s.repos.Snapshots.CountByUserIncludingDeleted(ctx, job.UserID)
	if err != nil {
		return err
	}
	if snapshotRows > 0 {
		retained = append(retained, ErasureCertificateItem{
			Category: "snapshot_metadata_and_blob_objects",
			Count:    snapshotRows,
			Bytes:    snapshotBytes,
			Reason:   "retention purge has not yet removed every eligible snapshot row and blob object; retry after retention cleanup succeeds",
			Metadata: map[string]any{"object_key_pattern": "snapshots/{user_id}/{snapshot_id}"},
		})
	}
	if len(retained) > 0 {
		return fmt.Errorf("erasure has retained CE sync artifacts pending blob/metadata purge")
	}
	addDeleted("bundle_metadata_and_blob_objects", initial.SoftDeletedBundles, initial.SoftDeletedBundleBytes, map[string]any{"object_key_pattern": "bundles/{user_id}/{bundle_id}"})
	addDeleted("snapshot_metadata_and_blob_objects", initial.SoftDeletedSnapshots, initial.SoftDeletedSnapshotBytes, map[string]any{"object_key_pattern": "snapshots/{user_id}/{snapshot_id}"})

	if rows, err := s.repos.Users.AnonymizeDeletedUser(ctx, job.UserID); err != nil {
		return err
	} else {
		addDeleted("user_personal_identifiers", rows, 0, map[string]any{
			"retained_tombstone_fields": []string{"id", "status", "created_at", "updated_at", "deleted_at"},
		})
	}

	if s.erasureReporter != nil {
		edition, err := s.erasureReporter.DescribeAccountErasure(ctx, provider.AccountErasureReportRequest{UserID: job.UserID.String()})
		if err != nil {
			return fmt.Errorf("describe edition erasure retention: %w", err)
		}
		for _, item := range edition.Retained {
			retained = append(retained, ErasureCertificateItem{
				Category:    item.Category,
				Count:       item.Count,
				Reason:      item.Reason,
				RetainedFor: item.RetainedFor,
				Metadata:    item.Metadata,
			})
		}
	}

	certificate := AccountErasureCertificate{
		Version:     1,
		JobID:       job.ID,
		UserID:      job.UserID,
		RequestedAt: job.RequestedAt,
		EligibleAt:  job.EligibleAt,
		CompletedAt: now,
		Status:      string(model.AccountErasureJobStatusCompleted),
		ZeroKnowledgeBoundary: ZeroKnowledgeBoundaryStatement{
			BlobContentsParsed:    false,
			BlobContentsDecrypted: false,
			Statement:             "HistorySync server erasure verifies metadata rows and opaque blob object deletion by key only; it does not parse or decrypt bundle or snapshot contents.",
		},
		Deleted:  deleted,
		Retained: retained,
	}
	data, err := json.Marshal(certificate)
	if err != nil {
		return fmt.Errorf("encode erasure certificate: %w", err)
	}
	if err := s.repos.AccountErasureJobs.MarkCompleted(ctx, job.ID, data, now); err != nil {
		return err
	}
	s.recordErasureAudit(ctx, job, model.AuditEventAccountErasureJobFinished, map[string]any{
		"status":          "completed",
		"deleted_items":   len(deleted),
		"retained_items":  len(retained),
		"certificate_ver": certificate.Version,
	})
	return nil
}

func (s *RetentionService) recordErasureAudit(ctx context.Context, job model.AccountErasureJob, eventType model.AuditEventType, metadata map[string]any) {
	if s == nil || s.auditRecorder == nil {
		return
	}
	if metadata == nil {
		metadata = map[string]any{}
	}
	metadata["job_id"] = job.ID.String()
	metadata["eligible_at"] = job.EligibleAt
	userID := job.UserID
	_ = s.auditRecorder.Record(ctx, AuditEventInput{
		ActorUserID: &userID,
		EventType:   eventType,
		TargetType:  "account_erasure_job",
		TargetID:    job.ID.String(),
		Metadata:    metadata,
	})
}
