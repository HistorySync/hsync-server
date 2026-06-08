package service

import (
	"context"
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

type accountMemoryDeviceStore struct {
	devices    map[uuid.UUID][]model.Device
	revokedAll bool
}

func (s *accountMemoryDeviceStore) ListByUser(_ context.Context, userID uuid.UUID) ([]model.Device, error) {
	return s.devices[userID], nil
}

func (s *accountMemoryDeviceStore) RevokeAllByUser(context.Context, uuid.UUID) error {
	s.revokedAll = true
	return nil
}

type accountRefreshMemoryStore struct {
	revoked bool
}

func (s *accountRefreshMemoryStore) RevokeAllUserTokens(context.Context, uuid.UUID) error {
	s.revoked = true
	return nil
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
	refresh := &accountRefreshMemoryStore{}
	twoFactor := &accountTwoFactorMemoryStore{}
	passkeys := &accountPasskeyMemoryStore{}
	audit := &accountAuditMemoryRecorder{}
	svc := NewAccountService(AccountDeps{
		Users:                users,
		Devices:              devices,
		RefreshTokens:        refresh,
		TwoFactor:            twoFactor,
		Passkeys:             passkeys,
		AuditRecorder:        audit,
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
	if !users.softDeleted || !refresh.revoked || !devices.revokedAll || !twoFactor.deleted || !passkeys.deletedCredentials || !passkeys.expiredChallenges {
		t.Fatalf("state cleanup users=%v refresh=%v devices=%v 2fa=%v passkeys=%v challenges=%v",
			users.softDeleted, refresh.revoked, devices.revokedAll, twoFactor.deleted, passkeys.deletedCredentials, passkeys.expiredChallenges)
	}
	if len(audit.events) != 2 {
		t.Fatalf("audit events = %d, want request and result", len(audit.events))
	}
	if audit.events[0].EventType != model.AuditEventAccountDeletionRequest || audit.events[1].EventType != model.AuditEventAccountDeletionResult {
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
