package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/historysync/hsync-server/pkg/model"
	"github.com/historysync/hsync-server/pkg/repository"
)

type fakeIdempotencyStore struct {
	records map[string]*model.IdempotencyRecord
	err     error
}

func newFakeIdempotencyService() (*IdempotencyService, *fakeIdempotencyStore) {
	store := &fakeIdempotencyStore{records: map[string]*model.IdempotencyRecord{}}
	return NewIdempotencyService(store), store
}

func (f *fakeIdempotencyStore) Claim(_ context.Context, p repository.IdempotencyClaimParams) (repository.IdempotencyClaimResult, error) {
	if f.err != nil {
		return repository.IdempotencyClaimResult{}, f.err
	}
	if f.records == nil {
		f.records = map[string]*model.IdempotencyRecord{}
	}
	key := p.Scope + "\x00" + p.IdempotencyKeyHash
	if existing := f.records[key]; existing != nil {
		if existing.RequestFingerprint != p.RequestFingerprint {
			return repository.IdempotencyClaimResult{Status: repository.IdempotencyClaimConflict, Record: *existing}, nil
		}
		if existing.Status == model.IdempotencyStatusSucceeded {
			return repository.IdempotencyClaimResult{Status: repository.IdempotencyClaimReplayed, Record: *existing}, nil
		}
		if existing.Status == model.IdempotencyStatusProcessing &&
			existing.LockedUntil != nil && existing.LockedUntil.After(p.Now) {
			return repository.IdempotencyClaimResult{Status: repository.IdempotencyClaimProcessing, Record: *existing}, nil
		}
		existing.Status = model.IdempotencyStatusProcessing
		existing.LockedUntil = &p.LockedUntil
		existing.ExpiresAt = p.ExpiresAt
		existing.ResponseStatus = nil
		existing.ResponseBody = nil
		existing.ErrorReason = ""
		return repository.IdempotencyClaimResult{Status: repository.IdempotencyClaimStarted, Record: *existing}, nil
	}
	record := &model.IdempotencyRecord{
		ID:                 uuid.New(),
		Scope:              p.Scope,
		IdempotencyKeyHash: p.IdempotencyKeyHash,
		RequestFingerprint: p.RequestFingerprint,
		Status:             model.IdempotencyStatusProcessing,
		LockedUntil:        &p.LockedUntil,
		ExpiresAt:          p.ExpiresAt,
		CreatedAt:          p.Now,
		UpdatedAt:          p.Now,
	}
	f.records[key] = record
	return repository.IdempotencyClaimResult{Status: repository.IdempotencyClaimStarted, Record: *record}, nil
}

func (f *fakeIdempotencyStore) MarkSucceeded(_ context.Context, id uuid.UUID, responseStatus int, responseBody []byte, now time.Time) error {
	if f.err != nil {
		return f.err
	}
	for _, record := range f.records {
		if record.ID == id {
			record.Status = model.IdempotencyStatusSucceeded
			record.LockedUntil = nil
			record.ResponseStatus = &responseStatus
			record.ResponseBody = append([]byte(nil), responseBody...)
			record.ErrorReason = ""
			record.UpdatedAt = now
			return nil
		}
	}
	return nil
}

func (f *fakeIdempotencyStore) MarkFailed(_ context.Context, id uuid.UUID, reason string, now time.Time) error {
	if f.err != nil {
		return f.err
	}
	for _, record := range f.records {
		if record.ID == id {
			record.Status = model.IdempotencyStatusFailed
			record.LockedUntil = nil
			record.ErrorReason = reason
			record.UpdatedAt = now
			return nil
		}
	}
	return nil
}

func TestExecuteIdempotentReplaysSucceededResponse(t *testing.T) {
	svc, _ := newFakeIdempotencyService()
	calls := 0
	execute := func(context.Context) (*ConsumeAICreditsResult, int, error) {
		calls++
		return &ConsumeAICreditsResult{BalanceAfter: 25, Charged: true}, 200, nil
	}

	first, err := ExecuteIdempotent[ConsumeAICreditsResult](ctx(), svc, IdempotencyOptions{
		Scope: "test.adjust", IdempotencyKey: "same-key", Payload: map[string]any{"amount": 25}, RequireKey: true,
	}, execute)
	if err != nil {
		t.Fatalf("first ExecuteIdempotent() error = %v", err)
	}
	second, err := ExecuteIdempotent[ConsumeAICreditsResult](ctx(), svc, IdempotencyOptions{
		Scope: "test.adjust", IdempotencyKey: "same-key", Payload: map[string]any{"amount": 25}, RequireKey: true,
	}, execute)
	if err != nil {
		t.Fatalf("second ExecuteIdempotent() error = %v", err)
	}
	if calls != 1 {
		t.Fatalf("execute calls = %d, want 1", calls)
	}
	if !second.Replayed || first.Data.BalanceAfter != second.Data.BalanceAfter {
		t.Fatalf("replay = %+v first = %+v", second, first)
	}
}

func TestExecuteIdempotentRejectsFingerprintConflict(t *testing.T) {
	svc, _ := newFakeIdempotencyService()
	if _, err := ExecuteIdempotent[ConsumeAICreditsResult](ctx(), svc, IdempotencyOptions{
		Scope: "test.adjust", IdempotencyKey: "same-key", Payload: map[string]any{"amount": 25}, RequireKey: true,
	}, func(context.Context) (*ConsumeAICreditsResult, int, error) {
		return &ConsumeAICreditsResult{BalanceAfter: 25, Charged: true}, 200, nil
	}); err != nil {
		t.Fatalf("first ExecuteIdempotent() error = %v", err)
	}

	_, err := ExecuteIdempotent[ConsumeAICreditsResult](ctx(), svc, IdempotencyOptions{
		Scope: "test.adjust", IdempotencyKey: "same-key", Payload: map[string]any{"amount": 50}, RequireKey: true,
	}, func(context.Context) (*ConsumeAICreditsResult, int, error) {
		t.Fatal("execute should not run on fingerprint conflict")
		return nil, 0, nil
	})
	if !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("conflict error = %v, want ErrIdempotencyConflict", err)
	}
}

func TestExecuteIdempotentStoreUnavailableFailsClosed(t *testing.T) {
	_, err := ExecuteIdempotent[ConsumeAICreditsResult](ctx(), nil, IdempotencyOptions{
		Scope: "test.adjust", IdempotencyKey: "same-key", Payload: map[string]any{"amount": 25}, RequireKey: true,
	}, func(context.Context) (*ConsumeAICreditsResult, int, error) {
		t.Fatal("execute should not run when the idempotency store is unavailable")
		return nil, 0, nil
	})
	if !errors.Is(err, ErrIdempotencyStoreUnavailable) {
		t.Fatalf("error = %v, want ErrIdempotencyStoreUnavailable", err)
	}
}

func TestExecuteIdempotentProtectsAdminCreditAdjust(t *testing.T) {
	svc, _ := newFakeIdempotencyService()
	ent, fb, _ := newTestEntitlement()
	uid := uuid.New()

	executeAdjust := func(amount int64) func(context.Context) (*ConsumeAICreditsResult, int, error) {
		return func(ctx context.Context) (*ConsumeAICreditsResult, int, error) {
			result, err := ent.AdjustAICredits(ctx, AdjustCreditsInput{
				UserID:         uid,
				Amount:         amount,
				Reason:         "ops",
				IdempotencyKey: "admin_adjust:" + HashIdempotencyKey("credit-key"),
			})
			return result, 200, err
		}
	}

	first, err := ExecuteIdempotent[ConsumeAICreditsResult](ctx(), svc, IdempotencyOptions{
		Scope: "admin.billing.adjust_credits", IdempotencyKey: "credit-key",
		Payload: map[string]any{"user_id": uid.String(), "amount": int64(40), "reason": "ops"}, RequireKey: true,
	}, executeAdjust(40))
	if err != nil {
		t.Fatalf("first adjust error = %v", err)
	}
	second, err := ExecuteIdempotent[ConsumeAICreditsResult](ctx(), svc, IdempotencyOptions{
		Scope: "admin.billing.adjust_credits", IdempotencyKey: "credit-key",
		Payload: map[string]any{"user_id": uid.String(), "amount": int64(40), "reason": "ops"}, RequireKey: true,
	}, executeAdjust(40))
	if err != nil {
		t.Fatalf("second adjust error = %v", err)
	}
	if !second.Replayed || second.Data.Charged != first.Data.Charged {
		t.Fatalf("second result = %+v first = %+v", second, first)
	}
	if bal := fb.liveBalance(uid); bal != 40 {
		t.Fatalf("balance = %d, want 40", bal)
	}
	_, err = ExecuteIdempotent[ConsumeAICreditsResult](ctx(), svc, IdempotencyOptions{
		Scope: "admin.billing.adjust_credits", IdempotencyKey: "credit-key",
		Payload: map[string]any{"user_id": uid.String(), "amount": int64(80), "reason": "ops"}, RequireKey: true,
	}, executeAdjust(80))
	if !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("conflict error = %v, want ErrIdempotencyConflict", err)
	}
}

func TestExecuteIdempotentProtectsAdminGrantPlan(t *testing.T) {
	svc, _ := newFakeIdempotencyService()
	ent, fb, _ := newTestEntitlement()
	uid := uuid.New()
	calls := 0
	executeGrant := func(planCode string) func(context.Context) (*GrantOutcome, int, error) {
		return func(ctx context.Context) (*GrantOutcome, int, error) {
			calls++
			result, err := ent.GrantPlanToUser(ctx, uid, planCode, GrantOptions{})
			return result, 200, err
		}
	}

	first, err := ExecuteIdempotent[GrantOutcome](ctx(), svc, IdempotencyOptions{
		Scope: "admin.billing.grant_plan", IdempotencyKey: "grant-key",
		Payload: map[string]any{"user_id": uid.String(), "plan_code": model.PlanCodePro}, RequireKey: true,
	}, executeGrant(model.PlanCodePro))
	if err != nil {
		t.Fatalf("first grant error = %v", err)
	}
	second, err := ExecuteIdempotent[GrantOutcome](ctx(), svc, IdempotencyOptions{
		Scope: "admin.billing.grant_plan", IdempotencyKey: "grant-key",
		Payload: map[string]any{"user_id": uid.String(), "plan_code": model.PlanCodePro}, RequireKey: true,
	}, executeGrant(model.PlanCodePro))
	if err != nil {
		t.Fatalf("second grant error = %v", err)
	}
	if calls != 1 || !second.Replayed || first.Data.CreditsGranted != second.Data.CreditsGranted {
		t.Fatalf("calls=%d first=%+v second=%+v, want replay without execution", calls, first, second)
	}
	if bal := fb.liveBalance(uid); bal != 200 {
		t.Fatalf("balance = %d, want 200", bal)
	}
	_, err = ExecuteIdempotent[GrantOutcome](ctx(), svc, IdempotencyOptions{
		Scope: "admin.billing.grant_plan", IdempotencyKey: "grant-key",
		Payload: map[string]any{"user_id": uid.String(), "plan_code": model.PlanCodeMax}, RequireKey: true,
	}, executeGrant(model.PlanCodeMax))
	if !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("conflict error = %v, want ErrIdempotencyConflict", err)
	}
}
