//go:build integration

package repository

import (
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/historysync/hsync-server/pkg/model"
)

// These integration tests exercise the billing repositories against a real
// PostgreSQL instance, validating the SQL-level guarantees that unit tests with
// in-memory fakes cannot: atomic no-negative consumption, the idempotency_key
// UNIQUE constraint, FIFO-by-expiry draw-down, lazy expiry filtering, the
// tier-only-upgrade upsert, and the bulk subscription refresh.

func TestPlanCatalogIsSeeded(t *testing.T) {
	repos := setupTest(t)
	ctx := testContext(t)

	plans, err := repos.Plans.ListEnabled(ctx)
	if err != nil {
		t.Fatalf("ListEnabled() error = %v", err)
	}
	if len(plans) != 7 {
		t.Fatalf("seeded plans = %d, want 7", len(plans))
	}

	max, err := repos.Plans.GetByCode(ctx, model.PlanCodeMax)
	if err != nil || max == nil {
		t.Fatalf("GetByCode(max) = %v, %v", max, err)
	}
	if max.Metadata.Tier != model.EntitlementTierMax || max.Metadata.OneTimeCredits != 600 || !max.Metadata.Writeback {
		t.Fatalf("max metadata = %+v, want max/600/writeback", max.Metadata)
	}

	bundle, err := repos.Plans.GetByCode(ctx, model.PlanCodeMaxCloud1Y)
	if err != nil || bundle == nil {
		t.Fatalf("GetByCode(max_cloud_1y) = %v, %v", bundle, err)
	}
	if len(bundle.Metadata.Components) != 2 {
		t.Fatalf("bundle components = %d, want 2", len(bundle.Metadata.Components))
	}

	prices, err := repos.Plans.ListPrices(ctx)
	if err != nil {
		t.Fatalf("ListPrices() error = %v", err)
	}
	var earlyBirdFound bool
	for _, p := range prices {
		if p.PlanCode == model.PlanCodeMaxCloud1Y && p.Region == model.RegionInternational {
			if p.EarlyBirdAmount == nil || *p.EarlyBirdAmount != 2999 {
				t.Fatalf("max_cloud_1y intl early bird = %v, want 2999", p.EarlyBirdAmount)
			}
			earlyBirdFound = true
		}
	}
	if !earlyBirdFound {
		t.Fatal("did not find max_cloud_1y international price")
	}
}

func TestCreditGrantAndBalance(t *testing.T) {
	repos := setupTest(t)
	ctx := testContext(t)
	u := seedUser(t, repos, "credits@example.com")

	res, err := repos.CreditLedger.Grant(ctx, GrantParams{
		UserID: u.ID, Source: model.CreditSourceMaxGrant, Amount: 600, IdempotencyKey: "grant-1",
	})
	if err != nil {
		t.Fatalf("Grant() error = %v", err)
	}
	if !res.Granted || res.BalanceAfter != 600 {
		t.Fatalf("Grant() = %+v, want granted with balance 600", res)
	}

	// Replaying the same idempotency key must not grant again.
	replay, err := repos.CreditLedger.Grant(ctx, GrantParams{
		UserID: u.ID, Source: model.CreditSourceMaxGrant, Amount: 600, IdempotencyKey: "grant-1",
	})
	if err != nil {
		t.Fatalf("Grant() replay error = %v", err)
	}
	if replay.Granted {
		t.Fatal("replayed grant with same key granted again")
	}

	bal, err := repos.CreditLedger.Balance(ctx, u.ID)
	if err != nil {
		t.Fatalf("Balance() error = %v", err)
	}
	if bal != 600 {
		t.Fatalf("balance = %d, want 600", bal)
	}
}

func TestCreditConsumeInsufficientLeavesBalanceUnchanged(t *testing.T) {
	repos := setupTest(t)
	ctx := testContext(t)
	u := seedUser(t, repos, "insufficient@example.com")

	if _, err := repos.CreditLedger.Grant(ctx, GrantParams{
		UserID: u.ID, Source: model.CreditSourceFreeGrant, Amount: 50, IdempotencyKey: "g",
	}); err != nil {
		t.Fatalf("Grant() error = %v", err)
	}

	_, err := repos.CreditLedger.Consume(ctx, ConsumeParams{
		UserID: u.ID, Cost: 60, IdempotencyKey: "c", Source: model.CreditSourceConsume,
	})
	if err != ErrInsufficientCredits {
		t.Fatalf("Consume() error = %v, want ErrInsufficientCredits", err)
	}
	bal, _ := repos.CreditLedger.Balance(ctx, u.ID)
	if bal != 50 {
		t.Fatalf("balance = %d after failed consume, want 50 (unchanged)", bal)
	}
}

func TestCreditConsumeIdempotent(t *testing.T) {
	repos := setupTest(t)
	ctx := testContext(t)
	u := seedUser(t, repos, "idem@example.com")

	if _, err := repos.CreditLedger.Grant(ctx, GrantParams{
		UserID: u.ID, Source: model.CreditSourceProGrant, Amount: 200, IdempotencyKey: "g",
	}); err != nil {
		t.Fatalf("Grant() error = %v", err)
	}

	first, err := repos.CreditLedger.Consume(ctx, ConsumeParams{
		UserID: u.ID, Cost: 30, IdempotencyKey: "op-1", Source: model.CreditSourceConsume,
	})
	if err != nil || !first.Charged || first.BalanceAfter != 170 {
		t.Fatalf("first consume = %+v, err = %v", first, err)
	}
	second, err := repos.CreditLedger.Consume(ctx, ConsumeParams{
		UserID: u.ID, Cost: 30, IdempotencyKey: "op-1", Source: model.CreditSourceConsume,
	})
	if err != nil {
		t.Fatalf("replay consume error = %v", err)
	}
	if second.Charged {
		t.Fatal("replayed consume charged again")
	}
	bal, _ := repos.CreditLedger.Balance(ctx, u.ID)
	if bal != 170 {
		t.Fatalf("balance = %d, want 170 (charged once)", bal)
	}
}

func TestCreditConsumeDrawsExpiringLotsFirst(t *testing.T) {
	repos := setupTest(t)
	ctx := testContext(t)
	u := seedUser(t, repos, "fifo@example.com")

	soon := time.Now().Add(time.Hour)
	// Expiring lot first, then a never-expiring lot.
	if _, err := repos.CreditLedger.Grant(ctx, GrantParams{
		UserID: u.ID, Source: model.CreditSourceCloudPeriodGrant, Amount: 30, IdempotencyKey: "expiring", ExpiresAt: &soon,
	}); err != nil {
		t.Fatalf("grant expiring: %v", err)
	}
	if _, err := repos.CreditLedger.Grant(ctx, GrantParams{
		UserID: u.ID, Source: model.CreditSourceMaxGrant, Amount: 70, IdempotencyKey: "permanent",
	}); err != nil {
		t.Fatalf("grant permanent: %v", err)
	}

	if _, err := repos.CreditLedger.Consume(ctx, ConsumeParams{
		UserID: u.ID, Cost: 50, IdempotencyKey: "c", Source: model.CreditSourceConsume,
	}); err != nil {
		t.Fatalf("consume: %v", err)
	}

	entries, err := repos.CreditLedger.ListByUser(ctx, u.ID, 50)
	if err != nil {
		t.Fatalf("ListByUser: %v", err)
	}
	for _, e := range entries {
		if e.RemainingAmount == nil {
			continue
		}
		switch e.Source {
		case model.CreditSourceCloudPeriodGrant:
			if *e.RemainingAmount != 0 {
				t.Fatalf("expiring lot remaining = %d, want 0 (drawn first)", *e.RemainingAmount)
			}
		case model.CreditSourceMaxGrant:
			if *e.RemainingAmount != 50 {
				t.Fatalf("permanent lot remaining = %d, want 50", *e.RemainingAmount)
			}
		}
	}
}

func TestCreditExpiredLotsExcludedAndExpireDueRecords(t *testing.T) {
	repos := setupTest(t)
	ctx := testContext(t)
	u := seedUser(t, repos, "expire@example.com")

	past := time.Now().Add(-time.Hour)
	if _, err := repos.CreditLedger.Grant(ctx, GrantParams{
		UserID: u.ID, Source: model.CreditSourceCloudPeriodGrant, Amount: 40, IdempotencyKey: "old", ExpiresAt: &past,
	}); err != nil {
		t.Fatalf("grant expired: %v", err)
	}

	bal, _ := repos.CreditLedger.Balance(ctx, u.ID)
	if bal != 0 {
		t.Fatalf("balance = %d, want 0 (lot already expired)", bal)
	}

	n, err := repos.CreditLedger.ExpireDue(ctx, time.Now(), 100)
	if err != nil {
		t.Fatalf("ExpireDue() error = %v", err)
	}
	if n != 1 {
		t.Fatalf("expired lots = %d, want 1", n)
	}

	entries, _ := repos.CreditLedger.ListByUser(ctx, u.ID, 50)
	var foundExpire bool
	for _, e := range entries {
		if e.Source == model.CreditSourceExpire && e.Amount == -40 {
			foundExpire = true
		}
	}
	if !foundExpire {
		t.Fatal("expected an expire ledger row of -40")
	}

	// Re-running ExpireDue is a no-op (the lot is already zeroed).
	if n, err := repos.CreditLedger.ExpireDue(ctx, time.Now(), 100); err != nil || n != 0 {
		t.Fatalf("second ExpireDue = %d, %v, want 0, nil", n, err)
	}
}

func TestEntitlementTierOnlyUpgrades(t *testing.T) {
	repos := setupTest(t)
	ctx := testContext(t)
	u := seedUser(t, repos, "tier@example.com")

	if _, err := repos.Entitlements.UpsertLifetime(ctx, u.ID, model.EntitlementTierPro, false, "pro"); err != nil {
		t.Fatalf("upsert pro: %v", err)
	}
	if _, err := repos.Entitlements.UpsertLifetime(ctx, u.ID, model.EntitlementTierMax, true, "max"); err != nil {
		t.Fatalf("upsert max: %v", err)
	}
	// A subsequent lower-tier grant must not downgrade tier or clear writeback.
	if _, err := repos.Entitlements.UpsertLifetime(ctx, u.ID, model.EntitlementTierPro, false, "pro"); err != nil {
		t.Fatalf("upsert pro again: %v", err)
	}

	ent, err := repos.Entitlements.Get(ctx, u.ID)
	if err != nil || ent == nil {
		t.Fatalf("Get() = %v, %v", ent, err)
	}
	if ent.Tier != model.EntitlementTierMax {
		t.Fatalf("tier = %q, want max (no downgrade)", ent.Tier)
	}
	if !ent.WritebackEnabled {
		t.Fatal("writeback cleared by later grant")
	}
}

func TestRefreshExpiredKeepsLifetimeButDisablesCloud(t *testing.T) {
	repos := setupTest(t)
	ctx := testContext(t)
	u := seedUser(t, repos, "refresh@example.com")

	if _, err := repos.Entitlements.UpsertLifetime(ctx, u.ID, model.EntitlementTierMax, true, "max"); err != nil {
		t.Fatalf("upsert max: %v", err)
	}
	if err := repos.Entitlements.SetCloudSync(ctx, u.ID, true); err != nil {
		t.Fatalf("set cloud sync: %v", err)
	}
	now := time.Now()
	sub := &model.UserSubscription{
		UserID:             u.ID,
		PlanCode:           model.PlanCodeCloud,
		Status:             model.SubscriptionStatusActive,
		CurrentPeriodStart: now.Add(-2 * time.Hour),
		CurrentPeriodEnd:   now.Add(-time.Hour),
		ActiveUntil:        now.Add(-time.Hour), // already past
		Provider:           model.PaymentProviderManual,
	}
	if err := repos.Subscriptions.Create(ctx, sub); err != nil {
		t.Fatalf("create subscription: %v", err)
	}

	n, err := repos.Subscriptions.RefreshExpired(ctx, time.Now())
	if err != nil {
		t.Fatalf("RefreshExpired() error = %v", err)
	}
	if n != 1 {
		t.Fatalf("expired subscriptions = %d, want 1", n)
	}

	ent, _ := repos.Entitlements.Get(ctx, u.ID)
	if ent.Tier != model.EntitlementTierMax || !ent.WritebackEnabled {
		t.Fatalf("entitlement = %+v, want max + writeback preserved", ent)
	}
	if ent.CloudSyncEnabled {
		t.Fatal("cloud sync still enabled after expiry")
	}
	active, _ := repos.Subscriptions.ListActiveByUser(ctx, u.ID)
	if len(active) != 0 {
		t.Fatalf("active subscriptions = %d, want 0", len(active))
	}
}

func TestPaymentOrderUpsertIsIdempotent(t *testing.T) {
	repos := setupTest(t)
	ctx := testContext(t)
	u := seedUser(t, repos, "orders@example.com")

	order := &model.PaymentOrder{
		UserID: u.ID, Provider: model.PaymentProviderGumroad, ExternalOrderID: "o-1",
		PlanCode: model.PlanCodePro, Currency: "USD", Amount: 999, Status: model.PaymentOrderStatusPaid,
		RawMetadata: map[string]any{"note": "ok"},
	}
	if err := repos.PaymentOrders.Upsert(ctx, order); err != nil {
		t.Fatalf("first upsert: %v", err)
	}
	firstID := order.ID

	// Same provider+external id updates the existing row rather than inserting.
	order2 := &model.PaymentOrder{
		UserID: u.ID, Provider: model.PaymentProviderGumroad, ExternalOrderID: "o-1",
		PlanCode: model.PlanCodePro, Currency: "USD", Amount: 999, Status: model.PaymentOrderStatusRefunded,
	}
	if err := repos.PaymentOrders.Upsert(ctx, order2); err != nil {
		t.Fatalf("second upsert: %v", err)
	}
	if order2.ID != firstID {
		t.Fatalf("upsert created a new row (%s) instead of updating (%s)", order2.ID, firstID)
	}

	got, err := repos.PaymentOrders.Get(ctx, model.PaymentProviderGumroad, "o-1")
	if err != nil || got == nil {
		t.Fatalf("Get() = %v, %v", got, err)
	}
	if got.Status != model.PaymentOrderStatusRefunded {
		t.Fatalf("status = %q, want refunded", got.Status)
	}

	// Orders without an external id are always inserted as new rows.
	for i := 0; i < 2; i++ {
		manual := &model.PaymentOrder{
			UserID: u.ID, Provider: model.PaymentProviderManual, ExternalOrderID: "",
			PlanCode: model.PlanCodeMax, Status: model.PaymentOrderStatusPaid,
		}
		if err := repos.PaymentOrders.Upsert(ctx, manual); err != nil {
			t.Fatalf("manual upsert %d: %v", i, err)
		}
	}
}

func TestCreditConsumeConcurrentDoesNotOversell(t *testing.T) {
	repos := setupTest(t)
	ctx := testContext(t)
	u := seedUser(t, repos, "race@example.com")

	if _, err := repos.CreditLedger.Grant(ctx, GrantParams{
		UserID: u.ID, Source: model.CreditSourceMaxGrant, Amount: 100, IdempotencyKey: "g",
	}); err != nil {
		t.Fatalf("grant: %v", err)
	}

	// Fire concurrent consumes that together exceed the balance; the advisory
	// lock + conditional draw-down must let at most 100 worth succeed and never
	// drive the balance negative.
	const workers = 10
	done := make(chan struct{}, workers)
	for i := 0; i < workers; i++ {
		go func(i int) {
			_, _ = repos.CreditLedger.Consume(ctx, ConsumeParams{
				UserID: u.ID, Cost: 20, IdempotencyKey: uuid.NewString(), Source: model.CreditSourceConsume,
			})
			done <- struct{}{}
		}(i)
	}
	for i := 0; i < workers; i++ {
		<-done
	}

	bal, err := repos.CreditLedger.Balance(ctx, u.ID)
	if err != nil {
		t.Fatalf("Balance() error = %v", err)
	}
	if bal < 0 {
		t.Fatalf("balance = %d, went negative", bal)
	}
	if bal != 0 {
		t.Fatalf("balance = %d, want 0 (exactly 5 of 10 consumes of 20 succeed)", bal)
	}
}
