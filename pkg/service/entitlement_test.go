package service

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/historysync/hsync-server/pkg/model"
	"github.com/historysync/hsync-server/pkg/repository"
)

// ── In-memory fake billing store ─────────────────────────────
//
// fakeBilling implements all five EntitlementService store interfaces with
// in-memory state, faithfully reproducing the repository's credit semantics:
// FIFO-by-expiry consumption, idempotency on the ledger key, no negative
// balance, and lazy expiry. Tests drive a settable clock so period and expiry
// behavior is deterministic.

type testClock struct{ t time.Time }

func (c *testClock) now() time.Time { return c.t }

type fakeBilling struct {
	now    func() time.Time
	plans  map[string]model.Plan
	prices []model.PlanPrice
	ents   map[uuid.UUID]*model.UserEntitlement
	subs   map[uuid.UUID]*model.UserSubscription
	ledger []*model.AICreditLedgerEntry
	orders []*model.PaymentOrder
}

func newFakeBilling(now func() time.Time) *fakeBilling {
	fb := &fakeBilling{
		now:   now,
		plans: map[string]model.Plan{},
		ents:  map[uuid.UUID]*model.UserEntitlement{},
		subs:  map[uuid.UUID]*model.UserSubscription{},
	}
	fb.seedCatalog()
	return fb
}

func newTestEntitlement() (*EntitlementService, *fakeBilling, *testClock) {
	clock := &testClock{t: time.Date(2026, 1, 15, 12, 0, 0, 0, time.UTC)}
	fb := newFakeBilling(clock.now)
	svc := NewEntitlementService(fb, fb, fb, fb, fakeOrderStore{fb})
	svc.now = clock.now
	return svc, fb, clock
}

// fakeOrderStore adapts fakeBilling to paymentOrderStore. It exists only because
// entitlementStore and paymentOrderStore both declare a Get method with
// different signatures, which a single struct cannot satisfy; the production
// repositories are distinct types and have no such clash.
type fakeOrderStore struct{ *fakeBilling }

func (f fakeOrderStore) Get(ctx context.Context, provider model.PaymentProvider, externalOrderID string) (*model.PaymentOrder, error) {
	return f.fakeBilling.GetOrder(ctx, provider, externalOrderID)
}

func (f fakeOrderStore) MarkCompleted(_ context.Context, id uuid.UUID, completedAt time.Time) error {
	for _, o := range f.orders {
		if o.ID == id {
			o.Status = model.PaymentOrderStatusCompleted
			o.CompletedAt = &completedAt
			o.FailedAt = nil
			o.FailedReason = ""
			return nil
		}
	}
	return nil
}

// seedCatalog mirrors the migration-seeded plan effects (and a subset of prices).
func (f *fakeBilling) seedCatalog() {
	f.plans = map[string]model.Plan{
		"free":         {Code: "free", Name: "Free", Kind: model.PlanKindLifetime, Enabled: true, Metadata: model.PlanMetadata{Tier: model.EntitlementTierFree, OneTimeCredits: 50}},
		"pro":          {Code: "pro", Name: "Pro", Kind: model.PlanKindLifetime, Enabled: true, Metadata: model.PlanMetadata{Tier: model.EntitlementTierPro, OneTimeCredits: 200}},
		"max":          {Code: "max", Name: "Max", Kind: model.PlanKindLifetime, Enabled: true, Metadata: model.PlanMetadata{Tier: model.EntitlementTierMax, OneTimeCredits: 600, Writeback: true}},
		"cloud_lite":   {Code: "cloud_lite", Name: "Cloud Lite", Kind: model.PlanKindSubscription, Enabled: true, Metadata: model.PlanMetadata{PeriodCredits: 200, CloudSync: true}},
		"cloud":        {Code: "cloud", Name: "Cloud", Kind: model.PlanKindSubscription, Enabled: true, Metadata: model.PlanMetadata{PeriodCredits: 500, CloudSync: true}},
		"max_cloud_1y": {Code: "max_cloud_1y", Name: "Max + 1y Cloud", Kind: model.PlanKindBundle, Enabled: true, Metadata: model.PlanMetadata{Components: []model.PlanComponent{{PlanCode: "max"}, {PlanCode: "cloud", CloudMonths: 12}}}},
		"max_cloud_2y": {Code: "max_cloud_2y", Name: "Max + 2y Cloud", Kind: model.PlanKindBundle, Enabled: true, Metadata: model.PlanMetadata{Components: []model.PlanComponent{{PlanCode: "max"}, {PlanCode: "cloud", CloudMonths: 24}}}},
	}
	f.prices = []model.PlanPrice{
		{PlanCode: "pro", Region: model.RegionInternational, Currency: model.CurrencyUSD, Amount: 999, BillingPeriod: model.BillingPeriodNone},
		{PlanCode: "pro", Region: model.RegionChina, Currency: model.CurrencyCNY, Amount: 6800, BillingPeriod: model.BillingPeriodNone},
		{PlanCode: "cloud", Region: model.RegionInternational, Currency: model.CurrencyUSD, Amount: 299, BillingPeriod: model.BillingPeriodMonthly},
		{PlanCode: "cloud", Region: model.RegionChina, Currency: model.CurrencyCNY, Amount: 990, BillingPeriod: model.BillingPeriodMonthly},
	}
}

// planStore
func (f *fakeBilling) ListEnabled(_ context.Context) ([]model.Plan, error) {
	var out []model.Plan
	for _, p := range f.plans {
		if p.Enabled {
			out = append(out, p)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Code < out[j].Code })
	return out, nil
}

func (f *fakeBilling) GetByCode(_ context.Context, code string) (*model.Plan, error) {
	if p, ok := f.plans[code]; ok {
		cp := p
		return &cp, nil
	}
	return nil, nil
}

func (f *fakeBilling) ListPrices(_ context.Context) ([]model.PlanPrice, error) {
	return append([]model.PlanPrice(nil), f.prices...), nil
}

// entitlementStore
func (f *fakeBilling) Get(_ context.Context, userID uuid.UUID) (*model.UserEntitlement, error) {
	if e, ok := f.ents[userID]; ok {
		cp := *e
		return &cp, nil
	}
	return nil, nil
}

func (f *fakeBilling) ensureEnt(userID uuid.UUID) *model.UserEntitlement {
	e, ok := f.ents[userID]
	if !ok {
		e = &model.UserEntitlement{ID: uuid.New(), UserID: userID, Tier: model.EntitlementTierFree, StartsAt: f.now()}
		f.ents[userID] = e
	}
	return e
}

func (f *fakeBilling) UpsertLifetime(_ context.Context, userID uuid.UUID, tier model.EntitlementTier, writeback bool, sourcePlanCode string) (*model.UserEntitlement, error) {
	e := f.ensureEnt(userID)
	if tier.Rank() > e.Tier.Rank() {
		e.Tier = tier
	}
	if writeback {
		e.WritebackEnabled = true
	}
	e.SourcePlanCode = sourcePlanCode
	e.EndsAt = nil
	cp := *e
	return &cp, nil
}

func (f *fakeBilling) SetCloudSync(_ context.Context, userID uuid.UUID, enabled bool) error {
	f.ensureEnt(userID).CloudSyncEnabled = enabled
	return nil
}

// subscriptionStore
func (f *fakeBilling) Create(_ context.Context, s *model.UserSubscription) error {
	s.ID = uuid.New()
	s.CreatedAt = f.now()
	s.UpdatedAt = f.now()
	cp := *s
	f.subs[s.ID] = &cp
	return nil
}

func (f *fakeBilling) GetByID(_ context.Context, id uuid.UUID) (*model.UserSubscription, error) {
	if s, ok := f.subs[id]; ok {
		cp := *s
		return &cp, nil
	}
	return nil, nil
}

func (f *fakeBilling) ListActiveByUser(_ context.Context, userID uuid.UUID) ([]model.UserSubscription, error) {
	var out []model.UserSubscription
	for _, s := range f.subs {
		if s.UserID == userID && s.Status == model.SubscriptionStatusActive {
			out = append(out, *s)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out, nil
}

func (f *fakeBilling) UpdatePeriod(_ context.Context, id uuid.UUID, start, end time.Time) error {
	if s, ok := f.subs[id]; ok {
		s.CurrentPeriodStart = start
		s.CurrentPeriodEnd = end
	}
	return nil
}

func (f *fakeBilling) ListDueForPeriodGrant(_ context.Context, now time.Time, limit int32) ([]model.UserSubscription, error) {
	var out []model.UserSubscription
	for _, s := range f.subs {
		if s.Status == model.SubscriptionStatusActive && s.ActiveUntil.After(now) && !s.CurrentPeriodEnd.After(now) {
			out = append(out, *s)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID.String() < out[j].ID.String() })
	if limit > 0 && int32(len(out)) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (f *fakeBilling) RefreshExpired(_ context.Context, now time.Time) (int64, error) {
	var count int64
	affected := map[uuid.UUID]bool{}
	for _, s := range f.subs {
		if s.Status == model.SubscriptionStatusActive && !now.Before(s.ActiveUntil) {
			s.Status = model.SubscriptionStatusExpired
			count++
			affected[s.UserID] = true
		}
	}
	for uid := range affected {
		e, ok := f.ents[uid]
		if !ok {
			continue
		}
		hasActive := false
		for _, s := range f.subs {
			if s.UserID == uid && s.Status == model.SubscriptionStatusActive && s.ActiveUntil.After(now) {
				hasActive = true
				break
			}
		}
		e.CloudSyncEnabled = hasActive
	}
	return count, nil
}

// creditLedgerStore
func (f *fakeBilling) findByIdem(key string) *model.AICreditLedgerEntry {
	if key == "" {
		return nil
	}
	for _, e := range f.ledger {
		if e.IdempotencyKey != nil && *e.IdempotencyKey == key {
			return e
		}
	}
	return nil
}

func (f *fakeBilling) liveBalance(userID uuid.UUID) int64 {
	now := f.now()
	var total int64
	for _, e := range f.ledger {
		if e.UserID != userID || e.RemainingAmount == nil || *e.RemainingAmount <= 0 {
			continue
		}
		if e.ExpiresAt != nil && !e.ExpiresAt.After(now) {
			continue
		}
		total += *e.RemainingAmount
	}
	return total
}

func (f *fakeBilling) Grant(_ context.Context, p repository.GrantParams) (repository.GrantResult, error) {
	if p.Amount <= 0 {
		return repository.GrantResult{}, fmt.Errorf("grant amount must be positive")
	}
	if e := f.findByIdem(p.IdempotencyKey); e != nil {
		return repository.GrantResult{Entry: *e, BalanceAfter: e.BalanceAfter, Granted: false}, nil
	}
	balanceAfter := f.liveBalance(p.UserID) + p.Amount
	amt := p.Amount
	entry := &model.AICreditLedgerEntry{
		ID: uuid.New(), UserID: p.UserID, Source: p.Source, Amount: p.Amount,
		BalanceAfter: balanceAfter, RemainingAmount: &amt, ExpiresAt: p.ExpiresAt,
		IdempotencyKey: keyPtr(p.IdempotencyKey), Metadata: p.Metadata, CreatedAt: f.now(),
	}
	f.ledger = append(f.ledger, entry)
	return repository.GrantResult{Entry: *entry, BalanceAfter: balanceAfter, Granted: true}, nil
}

func (f *fakeBilling) Consume(_ context.Context, p repository.ConsumeParams) (repository.ConsumeResult, error) {
	if p.Cost <= 0 {
		return repository.ConsumeResult{}, fmt.Errorf("consume cost must be positive")
	}
	if e := f.findByIdem(p.IdempotencyKey); e != nil {
		return repository.ConsumeResult{Entry: *e, BalanceAfter: e.BalanceAfter, Charged: false}, nil
	}
	now := f.now()
	var lots []*model.AICreditLedgerEntry
	for _, e := range f.ledger {
		if e.UserID != p.UserID || e.RemainingAmount == nil || *e.RemainingAmount <= 0 {
			continue
		}
		if e.ExpiresAt != nil && !e.ExpiresAt.After(now) {
			continue
		}
		lots = append(lots, e)
	}
	// FIFO by expiry: soonest expiry first, never-expiring last; stable for ties.
	sort.SliceStable(lots, func(i, j int) bool {
		ei, ej := lots[i].ExpiresAt, lots[j].ExpiresAt
		switch {
		case ei == nil && ej == nil:
			return false
		case ei == nil:
			return false
		case ej == nil:
			return true
		default:
			return ei.Before(*ej)
		}
	})
	var total int64
	for _, e := range lots {
		total += *e.RemainingAmount
	}
	if total < p.Cost {
		return repository.ConsumeResult{}, repository.ErrInsufficientCredits
	}
	left := p.Cost
	for _, e := range lots {
		if left == 0 {
			break
		}
		take := *e.RemainingAmount
		if take > left {
			take = left
		}
		*e.RemainingAmount -= take
		left -= take
	}
	balanceAfter := total - p.Cost
	src := p.Source
	if src == "" {
		src = model.CreditSourceConsume
	}
	entry := &model.AICreditLedgerEntry{
		ID: uuid.New(), UserID: p.UserID, Source: src, Amount: -p.Cost,
		BalanceAfter: balanceAfter, IdempotencyKey: keyPtr(p.IdempotencyKey),
		Metadata: p.Metadata, CreatedAt: f.now(),
	}
	f.ledger = append(f.ledger, entry)
	return repository.ConsumeResult{Entry: *entry, BalanceAfter: balanceAfter, Charged: true}, nil
}

func (f *fakeBilling) Balance(_ context.Context, userID uuid.UUID) (int64, error) {
	return f.liveBalance(userID), nil
}

func (f *fakeBilling) ListByUser(_ context.Context, userID uuid.UUID, limit int32) ([]model.AICreditLedgerEntry, error) {
	var out []model.AICreditLedgerEntry
	for i := len(f.ledger) - 1; i >= 0; i-- {
		if f.ledger[i].UserID != userID {
			continue
		}
		out = append(out, *f.ledger[i])
		if int32(len(out)) >= limit {
			break
		}
	}
	return out, nil
}

func (f *fakeBilling) ExpireDue(_ context.Context, now time.Time, _ int32) (int64, error) {
	var due []*model.AICreditLedgerEntry
	for _, e := range f.ledger {
		if e.RemainingAmount != nil && *e.RemainingAmount > 0 && e.ExpiresAt != nil && !e.ExpiresAt.After(now) {
			due = append(due, e)
		}
	}
	for _, e := range due {
		amount := *e.RemainingAmount
		*e.RemainingAmount = 0
		key := fmt.Sprintf("expire:%s", e.ID)
		f.ledger = append(f.ledger, &model.AICreditLedgerEntry{
			ID: uuid.New(), UserID: e.UserID, Source: model.CreditSourceExpire,
			Amount: -amount, BalanceAfter: f.liveBalance(e.UserID),
			IdempotencyKey: keyPtr(key), CreatedAt: now,
		})
	}
	return int64(len(due)), nil
}

// paymentOrderStore
func (f *fakeBilling) Upsert(_ context.Context, o *model.PaymentOrder) error {
	if o.ExternalOrderID != "" {
		for _, existing := range f.orders {
			if existing.Provider == o.Provider && existing.ExternalOrderID == o.ExternalOrderID {
				existing.UserID = o.UserID
				existing.PlanCode = o.PlanCode
				existing.Currency = o.Currency
				existing.Amount = o.Amount
				if existing.Status != model.PaymentOrderStatusCompleted {
					existing.Status = o.Status
					existing.FailedAt = o.FailedAt
					existing.FailedReason = o.FailedReason
				}
				existing.RawMetadata = o.RawMetadata
				if existing.PaidAt == nil {
					existing.PaidAt = o.PaidAt
				}
				o.ID = existing.ID
				o.CreatedAt = existing.CreatedAt
				o.UpdatedAt = f.now()
				existing.UpdatedAt = o.UpdatedAt
				return nil
			}
		}
	}
	o.ID = uuid.New()
	o.CreatedAt = f.now()
	o.UpdatedAt = f.now()
	cp := *o
	f.orders = append(f.orders, &cp)
	return nil
}

func (f *fakeBilling) GetOrder(_ context.Context, provider model.PaymentProvider, externalOrderID string) (*model.PaymentOrder, error) {
	for _, o := range f.orders {
		if o.Provider == provider && o.ExternalOrderID == externalOrderID {
			cp := *o
			return &cp, nil
		}
	}
	return nil, nil
}

func (f *fakeBilling) GetPaymentOrderByExternalID(ctx context.Context, provider model.PaymentProvider, externalOrderID string) (*model.PaymentOrder, error) {
	return f.GetOrder(ctx, provider, externalOrderID)
}

func keyPtr(key string) *string {
	if key == "" {
		return nil
	}
	return &key
}

func (f *fakeBilling) ledgerHasSource(userID uuid.UUID, source model.CreditSource) bool {
	for _, e := range f.ledger {
		if e.UserID == userID && e.Source == source {
			return true
		}
	}
	return false
}

// ── Tests ────────────────────────────────────────────────────

func ctx() context.Context { return context.Background() }

func TestGrantFreeGrants50OneTimeCredits(t *testing.T) {
	svc, fb, _ := newTestEntitlement()
	uid := uuid.New()

	out, err := svc.GrantPlanToUser(ctx(), uid, model.PlanCodeFree, GrantOptions{})
	if err != nil {
		t.Fatalf("GrantPlanToUser() error = %v", err)
	}
	if out.CreditsGranted != 50 {
		t.Fatalf("CreditsGranted = %d, want 50", out.CreditsGranted)
	}
	if bal := fb.liveBalance(uid); bal != 50 {
		t.Fatalf("balance = %d, want 50", bal)
	}
	if out.Entitlement.Tier != model.EntitlementTierFree {
		t.Fatalf("tier = %q, want free", out.Entitlement.Tier)
	}
	if !fb.ledgerHasSource(uid, model.CreditSourceFreeGrant) {
		t.Fatal("expected a free_grant ledger row")
	}
}

func TestGrantProGrants200OneTimeCredits(t *testing.T) {
	svc, fb, _ := newTestEntitlement()
	uid := uuid.New()

	out, err := svc.GrantPlanToUser(ctx(), uid, model.PlanCodePro, GrantOptions{})
	if err != nil {
		t.Fatalf("GrantPlanToUser() error = %v", err)
	}
	if out.CreditsGranted != 200 || fb.liveBalance(uid) != 200 {
		t.Fatalf("credits = %d / balance = %d, want 200/200", out.CreditsGranted, fb.liveBalance(uid))
	}
	if out.Entitlement.Tier != model.EntitlementTierPro {
		t.Fatalf("tier = %q, want pro", out.Entitlement.Tier)
	}
}

func TestGrantMaxGrants600CreditsAndEnablesWriteback(t *testing.T) {
	svc, fb, _ := newTestEntitlement()
	uid := uuid.New()

	out, err := svc.GrantPlanToUser(ctx(), uid, model.PlanCodeMax, GrantOptions{})
	if err != nil {
		t.Fatalf("GrantPlanToUser() error = %v", err)
	}
	if out.CreditsGranted != 600 || fb.liveBalance(uid) != 600 {
		t.Fatalf("credits = %d / balance = %d, want 600/600", out.CreditsGranted, fb.liveBalance(uid))
	}
	if out.Entitlement.Tier != model.EntitlementTierMax {
		t.Fatalf("tier = %q, want max", out.Entitlement.Tier)
	}
	if !out.Entitlement.WritebackEnabled {
		t.Fatal("writeback not enabled for max")
	}
	if !fb.ledgerHasSource(uid, model.CreditSourceMaxGrant) {
		t.Fatal("expected a max_grant ledger row")
	}
}

func TestCloudLiteGrants200PeriodCredits(t *testing.T) {
	svc, fb, _ := newTestEntitlement()
	uid := uuid.New()

	out, err := svc.GrantPlanToUser(ctx(), uid, model.PlanCodeCloudLite, GrantOptions{BillingPeriod: model.BillingPeriodMonthly})
	if err != nil {
		t.Fatalf("GrantPlanToUser() error = %v", err)
	}
	if out.CreditsGranted != 200 || fb.liveBalance(uid) != 200 {
		t.Fatalf("credits = %d / balance = %d, want 200/200", out.CreditsGranted, fb.liveBalance(uid))
	}
	if !out.Entitlement.CloudSyncEnabled {
		t.Fatal("cloud sync not enabled")
	}
	if !fb.ledgerHasSource(uid, model.CreditSourceCloudPeriodGrant) {
		t.Fatal("expected a cloud_period_grant ledger row")
	}
}

func TestCloudGrants500PeriodCredits(t *testing.T) {
	svc, fb, _ := newTestEntitlement()
	uid := uuid.New()

	out, err := svc.GrantPlanToUser(ctx(), uid, model.PlanCodeCloud, GrantOptions{BillingPeriod: model.BillingPeriodMonthly})
	if err != nil {
		t.Fatalf("GrantPlanToUser() error = %v", err)
	}
	if out.CreditsGranted != 500 || fb.liveBalance(uid) != 500 {
		t.Fatalf("credits = %d / balance = %d, want 500/500", out.CreditsGranted, fb.liveBalance(uid))
	}
}

func TestMaxCloud1YBundleSplitsEntitlementAndSubscription(t *testing.T) {
	svc, fb, clock := newTestEntitlement()
	uid := uuid.New()

	out, err := svc.GrantPlanToUser(ctx(), uid, model.PlanCodeMaxCloud1Y, GrantOptions{})
	if err != nil {
		t.Fatalf("GrantPlanToUser() error = %v", err)
	}
	if out.Entitlement.Tier != model.EntitlementTierMax || !out.Entitlement.WritebackEnabled {
		t.Fatalf("entitlement = %+v, want max + writeback", out.Entitlement)
	}
	if !out.Entitlement.CloudSyncEnabled {
		t.Fatal("cloud sync not enabled by bundle")
	}
	if bal := fb.liveBalance(uid); bal != 1100 { // 600 max one-time + 500 first cloud period
		t.Fatalf("balance = %d, want 1100", bal)
	}
	subs, _ := fb.ListActiveByUser(ctx(), uid)
	if len(subs) != 1 || subs[0].PlanCode != model.PlanCodeCloud {
		t.Fatalf("subscriptions = %+v, want one cloud subscription", subs)
	}
	wantUntil := clock.t.AddDate(0, 12, 0)
	if !subs[0].ActiveUntil.Equal(wantUntil) {
		t.Fatalf("active_until = %v, want %v", subs[0].ActiveUntil, wantUntil)
	}
}

func TestMaxCloud2YBundleGivesTwoYearsCloud(t *testing.T) {
	svc, fb, clock := newTestEntitlement()
	uid := uuid.New()

	out, err := svc.GrantPlanToUser(ctx(), uid, model.PlanCodeMaxCloud2Y, GrantOptions{})
	if err != nil {
		t.Fatalf("GrantPlanToUser() error = %v", err)
	}
	if out.Entitlement.Tier != model.EntitlementTierMax {
		t.Fatalf("tier = %q, want max", out.Entitlement.Tier)
	}
	subs, _ := fb.ListActiveByUser(ctx(), uid)
	if len(subs) != 1 {
		t.Fatalf("subscriptions = %d, want 1", len(subs))
	}
	wantUntil := clock.t.AddDate(0, 24, 0)
	if !subs[0].ActiveUntil.Equal(wantUntil) {
		t.Fatalf("active_until = %v, want %v (2 years)", subs[0].ActiveUntil, wantUntil)
	}
}

func TestCloudExpiryPreservesMaxEntitlement(t *testing.T) {
	svc, fb, clock := newTestEntitlement()
	uid := uuid.New()

	if _, err := svc.GrantPlanToUser(ctx(), uid, model.PlanCodeMaxCloud1Y, GrantOptions{}); err != nil {
		t.Fatalf("grant bundle: %v", err)
	}

	// Advance past the 1-year cloud horizon and refresh.
	clock.t = clock.t.AddDate(0, 13, 0)
	if _, err := svc.RefreshExpiredSubscriptions(ctx()); err != nil {
		t.Fatalf("RefreshExpiredSubscriptions() error = %v", err)
	}

	ent, _ := fb.Get(ctx(), uid)
	if ent.Tier != model.EntitlementTierMax {
		t.Fatalf("tier = %q after cloud expiry, want max (unchanged)", ent.Tier)
	}
	if !ent.WritebackEnabled {
		t.Fatal("writeback lost after cloud expiry")
	}
	if ent.CloudSyncEnabled {
		t.Fatal("cloud sync still enabled after expiry")
	}
}

func TestEntitlementGuardCloudSyncRequiresActiveCloud(t *testing.T) {
	svc, _, clock := newTestEntitlement()
	uid := uuid.New()

	if err := svc.RequireCloudSync(ctx(), uid); !errors.Is(err, ErrFeatureNotEnabled) {
		t.Fatalf("RequireCloudSync(free) error = %v, want ErrFeatureNotEnabled", err)
	}

	if _, err := svc.GrantPlanToUser(ctx(), uid, model.PlanCodeCloud, GrantOptions{BillingPeriod: model.BillingPeriodMonthly}); err != nil {
		t.Fatalf("grant cloud: %v", err)
	}
	if err := svc.RequireCloudSync(ctx(), uid); err != nil {
		t.Fatalf("RequireCloudSync(active cloud) error = %v, want nil", err)
	}

	clock.t = clock.t.AddDate(0, 2, 0)
	if err := svc.RequireCloudSync(ctx(), uid); !errors.Is(err, ErrFeatureNotEnabled) {
		t.Fatalf("RequireCloudSync(expired cloud) error = %v, want ErrFeatureNotEnabled", err)
	}
}

func TestEntitlementGuardMinimumTierProAndMax(t *testing.T) {
	svc, _, _ := newTestEntitlement()
	uid := uuid.New()

	if err := svc.RequireMinimumTier(ctx(), uid, model.EntitlementTierPro); !errors.Is(err, ErrEntitlementRequired) {
		t.Fatalf("RequireMinimumTier(free, pro) error = %v, want ErrEntitlementRequired", err)
	}
	if _, err := svc.GrantPlanToUser(ctx(), uid, model.PlanCodePro, GrantOptions{}); err != nil {
		t.Fatalf("grant pro: %v", err)
	}
	if err := svc.RequireMinimumTier(ctx(), uid, model.EntitlementTierPro); err != nil {
		t.Fatalf("RequireMinimumTier(pro, pro) error = %v, want nil", err)
	}
	if err := svc.RequireMinimumTier(ctx(), uid, model.EntitlementTierMax); !errors.Is(err, ErrEntitlementRequired) {
		t.Fatalf("RequireMinimumTier(pro, max) error = %v, want ErrEntitlementRequired", err)
	}
	if _, err := svc.GrantPlanToUser(ctx(), uid, model.PlanCodeMax, GrantOptions{}); err != nil {
		t.Fatalf("grant max: %v", err)
	}
	if err := svc.RequireMinimumTier(ctx(), uid, model.EntitlementTierMax); err != nil {
		t.Fatalf("RequireMinimumTier(max, max) error = %v, want nil", err)
	}
}

func TestEntitlementGuardMaxLifetimeFeaturesSurviveCloudExpiry(t *testing.T) {
	svc, _, clock := newTestEntitlement()
	uid := uuid.New()

	if _, err := svc.GrantPlanToUser(ctx(), uid, model.PlanCodeMaxCloud1Y, GrantOptions{}); err != nil {
		t.Fatalf("grant bundle: %v", err)
	}
	clock.t = clock.t.AddDate(0, 13, 0)
	if _, err := svc.RefreshExpiredSubscriptions(ctx()); err != nil {
		t.Fatalf("RefreshExpiredSubscriptions() error = %v", err)
	}

	if err := svc.RequireCloudSync(ctx(), uid); !errors.Is(err, ErrFeatureNotEnabled) {
		t.Fatalf("RequireCloudSync(expired bundle cloud) error = %v, want ErrFeatureNotEnabled", err)
	}
	if err := svc.RequireWriteback(ctx(), uid); err != nil {
		t.Fatalf("RequireWriteback(max lifetime) error = %v, want nil", err)
	}
	if err := svc.RequireMinimumTier(ctx(), uid, model.EntitlementTierMax); err != nil {
		t.Fatalf("RequireMinimumTier(max lifetime) error = %v, want nil", err)
	}
}

func TestConsumeInsufficientFailsAndNeverGoesNegative(t *testing.T) {
	svc, fb, _ := newTestEntitlement()
	uid := uuid.New()
	if _, err := svc.GrantPlanToUser(ctx(), uid, model.PlanCodeFree, GrantOptions{}); err != nil {
		t.Fatalf("grant free: %v", err)
	}

	_, err := svc.ConsumeAICredits(ctx(), ConsumeAICreditsInput{UserID: uid, Cost: 60, IdempotencyKey: "op-1"})
	if err != ErrInsufficientCredits {
		t.Fatalf("ConsumeAICredits() error = %v, want ErrInsufficientCredits", err)
	}
	if bal := fb.liveBalance(uid); bal != 50 {
		t.Fatalf("balance = %d after failed consume, want 50 (unchanged, not negative)", bal)
	}
}

func TestConsumeIdempotentRetryDoesNotDoubleCharge(t *testing.T) {
	svc, fb, _ := newTestEntitlement()
	uid := uuid.New()
	if _, err := svc.GrantPlanToUser(ctx(), uid, model.PlanCodePro, GrantOptions{}); err != nil {
		t.Fatalf("grant pro: %v", err)
	}

	first, err := svc.ConsumeAICredits(ctx(), ConsumeAICreditsInput{UserID: uid, Cost: 30, IdempotencyKey: "op-1"})
	if err != nil {
		t.Fatalf("first consume: %v", err)
	}
	if !first.Charged || first.BalanceAfter != 170 {
		t.Fatalf("first consume = %+v, want charged with balance 170", first)
	}

	second, err := svc.ConsumeAICredits(ctx(), ConsumeAICreditsInput{UserID: uid, Cost: 30, IdempotencyKey: "op-1"})
	if err != nil {
		t.Fatalf("retry consume: %v", err)
	}
	if second.Charged {
		t.Fatal("retry with same idempotency key charged again")
	}
	if bal := fb.liveBalance(uid); bal != 170 {
		t.Fatalf("balance = %d after retry, want 170 (charged once)", bal)
	}
}

func TestConsumeRequiresIdempotencyKey(t *testing.T) {
	svc, _, _ := newTestEntitlement()
	_, err := svc.ConsumeAICredits(ctx(), ConsumeAICreditsInput{UserID: uuid.New(), Cost: 1})
	if err != ErrIdempotencyKeyRequired {
		t.Fatalf("ConsumeAICredits() error = %v, want ErrIdempotencyKeyRequired", err)
	}
}

func TestGrantCloudPeriodCreditsIdempotentPerPeriod(t *testing.T) {
	svc, fb, _ := newTestEntitlement()
	uid := uuid.New()

	out, err := svc.GrantPlanToUser(ctx(), uid, model.PlanCodeCloud, GrantOptions{BillingPeriod: model.BillingPeriodMonthly})
	if err != nil {
		t.Fatalf("grant cloud: %v", err)
	}
	subID := out.Subscriptions[0].ID

	// Re-running the same period must not grant again.
	res, err := svc.GrantCloudPeriodCredits(ctx(), uid, subID)
	if err != nil {
		t.Fatalf("GrantCloudPeriodCredits() error = %v", err)
	}
	if res.Granted {
		t.Fatal("period credits granted twice for the same period")
	}
	if bal := fb.liveBalance(uid); bal != 500 {
		t.Fatalf("balance = %d, want 500 (granted once)", bal)
	}
}

func TestGrantCloudPeriodCreditsGrantsEachPeriodAndPriorExpires(t *testing.T) {
	svc, fb, clock := newTestEntitlement()
	uid := uuid.New()

	// Yearly payment keeps the subscription active for a year, but credits are
	// granted monthly and expire at each month boundary.
	out, err := svc.GrantPlanToUser(ctx(), uid, model.PlanCodeCloud, GrantOptions{BillingPeriod: model.BillingPeriodYearly})
	if err != nil {
		t.Fatalf("grant cloud yearly: %v", err)
	}
	subID := out.Subscriptions[0].ID
	if bal := fb.liveBalance(uid); bal != 500 {
		t.Fatalf("balance = %d after first period, want 500", bal)
	}

	// Move into the second month: the first period's credits expire and a new
	// period is granted.
	clock.t = clock.t.AddDate(0, 1, 0).Add(24 * time.Hour)
	res, err := svc.GrantCloudPeriodCredits(ctx(), uid, subID)
	if err != nil {
		t.Fatalf("GrantCloudPeriodCredits() error = %v", err)
	}
	if !res.Granted || res.Credits != 500 {
		t.Fatalf("second period grant = %+v, want granted 500", res)
	}
	if bal := fb.liveBalance(uid); bal != 500 {
		t.Fatalf("balance = %d, want 500 (prior period expired, new period granted)", bal)
	}
}

func TestSubscriptionCreditsExpireButOneTimeCreditsDoNot(t *testing.T) {
	svc, fb, clock := newTestEntitlement()
	uid := uuid.New()

	if _, err := svc.GrantPlanToUser(ctx(), uid, model.PlanCodeMax, GrantOptions{}); err != nil {
		t.Fatalf("grant max: %v", err)
	}
	if _, err := svc.GrantPlanToUser(ctx(), uid, model.PlanCodeCloud, GrantOptions{BillingPeriod: model.BillingPeriodYearly}); err != nil {
		t.Fatalf("grant cloud: %v", err)
	}
	if bal := fb.liveBalance(uid); bal != 1100 {
		t.Fatalf("balance = %d, want 1100", bal)
	}

	// After the first cloud period ends, the 500 period credits expire; the 600
	// one-time Max credits remain.
	clock.t = clock.t.AddDate(0, 1, 0).Add(24 * time.Hour)
	bal, err := svc.GetAICreditBalance(ctx(), uid)
	if err != nil {
		t.Fatalf("GetAICreditBalance() error = %v", err)
	}
	if bal != 600 {
		t.Fatalf("balance = %d after period expiry, want 600 (one-time only)", bal)
	}

	// ExpireAICredits records the expiry in the ledger.
	expired, err := svc.ExpireAICredits(ctx())
	if err != nil {
		t.Fatalf("ExpireAICredits() error = %v", err)
	}
	if expired != 1 {
		t.Fatalf("expired lots = %d, want 1", expired)
	}
	if !fb.ledgerHasSource(uid, model.CreditSourceExpire) {
		t.Fatal("expected an expire ledger row")
	}

	// One-time credits remain consumable; nothing beyond them is available.
	if _, err := svc.ConsumeAICredits(ctx(), ConsumeAICreditsInput{UserID: uid, Cost: 600, IdempotencyKey: "spend-all"}); err != nil {
		t.Fatalf("consume 600: %v", err)
	}
	if _, err := svc.ConsumeAICredits(ctx(), ConsumeAICreditsInput{UserID: uid, Cost: 1, IdempotencyKey: "spend-more"}); err != ErrInsufficientCredits {
		t.Fatalf("consume beyond balance error = %v, want ErrInsufficientCredits", err)
	}
}

func TestGrantPlanIdempotentOnExternalOrder(t *testing.T) {
	svc, fb, _ := newTestEntitlement()
	uid := uuid.New()
	opts := GrantOptions{Provider: model.PaymentProviderGumroad, ExternalOrderID: "ord-1"}

	if _, err := svc.GrantPlanToUser(ctx(), uid, model.PlanCodePro, opts); err != nil {
		t.Fatalf("first grant: %v", err)
	}
	// A webhook replay with the same order must not double-grant credits.
	out, err := svc.GrantPlanToUser(ctx(), uid, model.PlanCodePro, opts)
	if err != nil {
		t.Fatalf("replay grant: %v", err)
	}
	if out.CreditsGranted != 0 {
		t.Fatalf("replay CreditsGranted = %d, want 0", out.CreditsGranted)
	}
	if bal := fb.liveBalance(uid); bal != 200 {
		t.Fatalf("balance = %d after replay, want 200 (granted once)", bal)
	}
}

func TestGrantBundleIdempotentOnExternalOrder(t *testing.T) {
	svc, fb, _ := newTestEntitlement()
	uid := uuid.New()
	opts := GrantOptions{Provider: model.PaymentProviderGumroad, ExternalOrderID: "bundle-1"}

	if _, err := svc.GrantPlanToUser(ctx(), uid, model.PlanCodeMaxCloud1Y, opts); err != nil {
		t.Fatalf("first bundle grant: %v", err)
	}
	// A replay of the same order must not create a second subscription or
	// re-grant credits.
	out, err := svc.GrantPlanToUser(ctx(), uid, model.PlanCodeMaxCloud1Y, opts)
	if err != nil {
		t.Fatalf("replay bundle grant: %v", err)
	}
	if out.CreditsGranted != 0 {
		t.Fatalf("replay CreditsGranted = %d, want 0", out.CreditsGranted)
	}
	if bal := fb.liveBalance(uid); bal != 1100 {
		t.Fatalf("balance = %d after replay, want 1100 (granted once)", bal)
	}
	subs, _ := fb.ListActiveByUser(ctx(), uid)
	if len(subs) != 1 {
		t.Fatalf("active subscriptions = %d after replay, want 1", len(subs))
	}
}

func TestRecordOrderStripsSensitiveMetadata(t *testing.T) {
	svc, fb, _ := newTestEntitlement()
	uid := uuid.New()

	_, err := svc.GrantPlanToUser(ctx(), uid, model.PlanCodePro, GrantOptions{
		Provider:        model.PaymentProviderGumroad,
		ExternalOrderID: "ord-secret",
		RawMetadata: map[string]any{
			"access_token": "should-be-stripped",
			"note":         "keep-me",
		},
	})
	if err != nil {
		t.Fatalf("grant: %v", err)
	}
	if len(fb.orders) != 1 {
		t.Fatalf("orders = %d, want 1", len(fb.orders))
	}
	meta := fb.orders[0].RawMetadata
	if _, ok := meta["access_token"]; ok {
		t.Fatal("payment token was not stripped from raw_metadata")
	}
	if meta["note"] != "keep-me" {
		t.Fatalf("note = %v, want keep-me", meta["note"])
	}
}

func TestGetAvailablePlansFiltersByRegion(t *testing.T) {
	svc, _, _ := newTestEntitlement()

	plans, err := svc.GetAvailablePlans(ctx(), model.RegionChina)
	if err != nil {
		t.Fatalf("GetAvailablePlans() error = %v", err)
	}
	for _, p := range plans {
		for _, price := range p.Prices {
			if price.Region != model.RegionChina {
				t.Fatalf("plan %q has non-china price region %q", p.Code, price.Region)
			}
			if price.Currency != model.CurrencyCNY {
				t.Fatalf("china price currency = %q, want CNY", price.Currency)
			}
		}
	}
}

func TestGrantDuePeriodCreditsGrantsRolledOverSubscriptions(t *testing.T) {
	svc, fb, clock := newTestEntitlement()
	uid := uuid.New()

	if _, err := svc.GrantPlanToUser(ctx(), uid, model.PlanCodeCloud, GrantOptions{BillingPeriod: model.BillingPeriodYearly}); err != nil {
		t.Fatalf("grant cloud yearly: %v", err)
	}
	if bal := fb.liveBalance(uid); bal != 500 {
		t.Fatalf("balance = %d after activation, want 500", bal)
	}

	// Within the first period nothing is due.
	if n, err := svc.GrantDuePeriodCredits(ctx()); err != nil || n != 0 {
		t.Fatalf("GrantDuePeriodCredits before rollover = %d, %v, want 0, nil", n, err)
	}

	// After the month boundary the subscription is due; granting rolls it and
	// the prior period's credits expire.
	clock.t = clock.t.AddDate(0, 1, 0).Add(24 * time.Hour)
	n, err := svc.GrantDuePeriodCredits(ctx())
	if err != nil {
		t.Fatalf("GrantDuePeriodCredits() error = %v", err)
	}
	if n != 1 {
		t.Fatalf("period grants = %d, want 1", n)
	}
	if bal := fb.liveBalance(uid); bal != 500 {
		t.Fatalf("balance = %d, want 500 (prior expired, new period granted)", bal)
	}

	// Re-running within the new period grants nothing more (idempotent, no longer due).
	if n, err := svc.GrantDuePeriodCredits(ctx()); err != nil || n != 0 {
		t.Fatalf("second GrantDuePeriodCredits = %d, %v, want 0, nil", n, err)
	}
}

func TestAICreditCostTiers(t *testing.T) {
	cases := map[AICreditOperation]int64{
		AICreditOpLight:       1,
		AICreditOpLongContext: 3,
		AICreditOpWriteback:   5,
		AICreditOpHighCost:    10,
	}
	for op, want := range cases {
		if got := AICreditCost(op); got != want {
			t.Fatalf("AICreditCost(%q) = %d, want %d", op, got, want)
		}
	}
}
