package service

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/historysync/hsync-server/pkg/model"
	"github.com/historysync/hsync-server/pkg/provider"
)

type accountMemoryUserStore struct {
	users       map[uuid.UUID]*model.User
	softDeleted bool
	anonymized  bool
}

func (s *accountMemoryUserStore) GetByID(_ context.Context, id uuid.UUID) (*model.User, error) {
	return s.users[id], nil
}

func (s *accountMemoryUserStore) SoftDelete(_ context.Context, id uuid.UUID) error {
	if s.users[id] == nil {
		return ErrUserNotFound
	}
	now := time.Now()
	s.users[id].Status = model.StatusDeleted
	s.users[id].DeletedAt = &now
	s.softDeleted = true
	return nil
}

func (s *accountMemoryUserStore) GetAnyByID(_ context.Context, id uuid.UUID) (*model.User, error) {
	return s.users[id], nil
}

func (s *accountMemoryUserStore) AnonymizeDeletedUser(_ context.Context, id uuid.UUID) (int64, error) {
	if s.users[id] == nil || s.users[id].DeletedAt == nil {
		return 0, nil
	}
	s.users[id].Email = "deleted-" + id.String() + "@erased.local"
	s.users[id].DisplayName = ""
	s.users[id].PasswordHash = ""
	s.anonymized = true
	return 1, nil
}

type accountMemoryDeviceStore struct {
	devices    map[uuid.UUID][]model.Device
	revokedAll bool
	deleted    int64
}

func (s *accountMemoryDeviceStore) ListByUser(_ context.Context, userID uuid.UUID) ([]model.Device, error) {
	return s.devices[userID], nil
}

func (s *accountMemoryDeviceStore) RevokeAllByUser(context.Context, uuid.UUID) error {
	s.revokedAll = true
	return nil
}

func (s *accountMemoryDeviceStore) DeleteByUser(context.Context, uuid.UUID) (int64, error) {
	s.deleted++
	return s.deleted, nil
}

type accountRefreshMemoryStore struct {
	revoked bool
	deleted int64
}

func (s *accountRefreshMemoryStore) RevokeAllUserTokens(context.Context, uuid.UUID) error {
	s.revoked = true
	return nil
}

func (s *accountRefreshMemoryStore) DeleteByUser(context.Context, uuid.UUID) (int64, error) {
	s.deleted++
	return s.deleted, nil
}

type accountTwoFactorMemoryStore struct {
	deleted bool
}

func (s *accountTwoFactorMemoryStore) DeleteByUser(context.Context, uuid.UUID) error {
	s.deleted = true
	return nil
}

type accountPasskeyMemoryStore struct {
	deletedCredentials bool
	expiredChallenges  bool
}

func (s *accountPasskeyMemoryStore) DeleteCredentialsByUser(context.Context, uuid.UUID) error {
	s.deletedCredentials = true
	return nil
}

func (s *accountPasskeyMemoryStore) ExpireChallengesByUser(context.Context, uuid.UUID, time.Time) error {
	s.expiredChallenges = true
	return nil
}

type accountAuditMemoryRecorder struct {
	events []AuditEventInput
}

func (r *accountAuditMemoryRecorder) Record(_ context.Context, input AuditEventInput) error {
	r.events = append(r.events, input)
	return nil
}

type accountBundleMemoryStore struct {
	softDeletedCount int64
	softDeletedBytes int64
	remainingCount   int64
	remainingBytes   int64
}

func (s *accountBundleMemoryStore) ListAllByUser(context.Context, uuid.UUID) ([]model.BundleMeta, error) {
	return []model.BundleMeta{}, nil
}

func (s *accountBundleMemoryStore) SoftDeleteAllByUser(context.Context, uuid.UUID, time.Time) (int64, int64, error) {
	return s.softDeletedCount, s.softDeletedBytes, nil
}

func (s *accountBundleMemoryStore) CountByUserIncludingDeleted(context.Context, uuid.UUID) (int64, int64, error) {
	return s.remainingCount, s.remainingBytes, nil
}

type accountSnapshotMemoryStore struct {
	softDeletedCount int64
	softDeletedBytes int64
	remainingCount   int64
	remainingBytes   int64
}

func (s *accountSnapshotMemoryStore) ListAllByUser(context.Context, uuid.UUID) ([]model.SnapshotMeta, error) {
	return []model.SnapshotMeta{}, nil
}

func (s *accountSnapshotMemoryStore) SoftDeleteAllByUser(context.Context, uuid.UUID, time.Time) (int64, int64, error) {
	return s.softDeletedCount, s.softDeletedBytes, nil
}

func (s *accountSnapshotMemoryStore) CountByUserIncludingDeleted(context.Context, uuid.UUID) (int64, int64, error) {
	return s.remainingCount, s.remainingBytes, nil
}

type accountQuotaMemoryStore struct {
	bundleBytesRemoved   int64
	snapshotBytesRemoved int64
}

func (s *accountQuotaMemoryStore) GetUsage(context.Context, uuid.UUID) (*model.QuotaUsage, error) {
	return &model.QuotaUsage{}, nil
}

func (s *accountQuotaMemoryStore) RemoveBundleUsage(_ context.Context, _ uuid.UUID, bytes int64) error {
	s.bundleBytesRemoved += bytes
	return nil
}

func (s *accountQuotaMemoryStore) RemoveSnapshotUsage(_ context.Context, _ uuid.UUID, bytes int64) error {
	s.snapshotBytesRemoved += bytes
	return nil
}

func (s *accountQuotaMemoryStore) DeleteUsageByUser(context.Context, uuid.UUID) (int64, error) {
	return 1, nil
}

func (s *accountQuotaMemoryStore) DeleteLimitsByUser(context.Context, uuid.UUID) (int64, error) {
	return 1, nil
}

type accountErasureJobMemoryStore struct {
	jobs []model.AccountErasureJob
}

func (s *accountErasureJobMemoryStore) Create(_ context.Context, job *model.AccountErasureJob) error {
	job.ID = uuid.New()
	job.CreatedAt = job.RequestedAt
	job.UpdatedAt = job.RequestedAt
	s.jobs = append(s.jobs, *job)
	return nil
}

func (s *accountErasureJobMemoryStore) UpdateSummary(_ context.Context, id uuid.UUID, summary json.RawMessage) error {
	for i := range s.jobs {
		if s.jobs[i].ID == id {
			s.jobs[i].Summary = summary
		}
	}
	return nil
}

type blockingDeletionPolicy struct{}

func (p blockingDeletionPolicy) EvaluateAccountDeletion(context.Context, provider.AccountDeletionRequest) (*provider.AccountDeletionDecision, error) {
	return &provider.AccountDeletionDecision{
		Allowed: false,
		Reasons: []provider.AccountDeletionPolicyReason{{
			Code:    "active_subscription",
			Message: "active subscription must be resolved first",
		}},
	}, nil
}

func TestAccountDeleteAccountRevokesSecurityStateAndAudits(t *testing.T) {
	userID := uuid.New()
	users := &accountMemoryUserStore{users: map[uuid.UUID]*model.User{
		userID: {ID: userID, Email: "user@example.com", Tier: model.TierFree, Status: model.StatusActive},
	}}
	devices := &accountMemoryDeviceStore{}
	bundles := &accountBundleMemoryStore{softDeletedCount: 2, softDeletedBytes: 300}
	snapshots := &accountSnapshotMemoryStore{softDeletedCount: 1, softDeletedBytes: 700}
	quota := &accountQuotaMemoryStore{}
	refresh := &accountRefreshMemoryStore{}
	twoFactor := &accountTwoFactorMemoryStore{}
	passkeys := &accountPasskeyMemoryStore{}
	audit := &accountAuditMemoryRecorder{}
	jobs := &accountErasureJobMemoryStore{}
	svc := NewAccountService(AccountDeps{
		Users:                users,
		Devices:              devices,
		Bundles:              bundles,
		Snapshots:            snapshots,
		Quota:                quota,
		RefreshTokens:        refresh,
		TwoFactor:            twoFactor,
		Passkeys:             passkeys,
		AuditRecorder:        audit,
		ErasureJobs:          jobs,
		RetentionGracePeriod: 24 * time.Hour,
	})

	result, err := svc.DeleteAccount(context.Background(), AccountDeletionInput{
		UserID:    userID,
		RequestID: "req-1",
		IP:        "127.0.0.1",
		UserAgent: "test",
	})
	if err != nil {
		t.Fatalf("DeleteAccount() error = %v", err)
	}
	if result.Status != "deleted" || result.DeletedAt == nil {
		t.Fatalf("result = %+v, want deleted", result)
	}
	if result.ErasureJobID == uuid.Nil || result.ErasureEligibleAt == nil || len(jobs.jobs) != 1 {
		t.Fatalf("erasure job result=%+v jobs=%+v, want created job", result, jobs.jobs)
	}
	if result.SoftDeletedBundles != 2 || result.SoftDeletedBundleBytes != 300 || quota.bundleBytesRemoved != 300 {
		t.Fatalf("bundle cleanup result=%+v quota=%+v", result, quota)
	}
	if result.SoftDeletedSnapshots != 1 || result.SoftDeletedSnapshotBytes != 700 || quota.snapshotBytesRemoved != 700 {
		t.Fatalf("snapshot cleanup result=%+v quota=%+v", result, quota)
	}
	if !users.softDeleted || !refresh.revoked || !devices.revokedAll || !twoFactor.deleted || !passkeys.deletedCredentials || !passkeys.expiredChallenges {
		t.Fatalf("state cleanup users=%v refresh=%v devices=%v 2fa=%v passkeys=%v challenges=%v",
			users.softDeleted, refresh.revoked, devices.revokedAll, twoFactor.deleted, passkeys.deletedCredentials, passkeys.expiredChallenges)
	}
	if len(audit.events) != 3 {
		t.Fatalf("audit events = %d, want request, job, and result", len(audit.events))
	}
	if audit.events[0].EventType != model.AuditEventAccountDeletionRequest ||
		audit.events[1].EventType != model.AuditEventAccountErasureJobCreated ||
		audit.events[2].EventType != model.AuditEventAccountDeletionResult {
		t.Fatalf("audit events = %+v", audit.events)
	}
}

func TestAccountDeleteAccountPolicyBlockAuditsAndSkipsMutation(t *testing.T) {
	userID := uuid.New()
	users := &accountMemoryUserStore{users: map[uuid.UUID]*model.User{
		userID: {ID: userID, Email: "user@example.com", Tier: model.TierPro, Status: model.StatusActive},
	}}
	refresh := &accountRefreshMemoryStore{}
	audit := &accountAuditMemoryRecorder{}
	svc := NewAccountService(AccountDeps{
		Users:          users,
		RefreshTokens:  refresh,
		AuditRecorder:  audit,
		DeletionPolicy: blockingDeletionPolicy{},
	})

	result, err := svc.DeleteAccount(context.Background(), AccountDeletionInput{UserID: userID, RequestID: "req-block"})
	if !errors.Is(err, ErrAccountDeletionBlocked) {
		t.Fatalf("DeleteAccount() error = %v, want ErrAccountDeletionBlocked", err)
	}
	if result == nil || result.Status != "blocked" || len(result.Policy.Reasons) != 1 {
		t.Fatalf("result = %+v, want blocked with reason", result)
	}
	if users.softDeleted || refresh.revoked {
		t.Fatalf("mutation happened despite policy block: softDeleted=%v revoked=%v", users.softDeleted, refresh.revoked)
	}
	if len(audit.events) != 2 || audit.events[1].Metadata["status"] != "blocked" {
		t.Fatalf("audit events = %+v", audit.events)
	}
}
