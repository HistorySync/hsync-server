package service

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/historysync/hsync-server/pkg/model"
	"github.com/historysync/hsync-server/pkg/repository"
)

// Billing/entitlement errors.
var (
	ErrPlanNotFound           = errors.New("plan not found")
	ErrPlanDisabled           = errors.New("plan is not available")
	ErrInsufficientCredits    = errors.New("insufficient ai credits")
	ErrIdempotencyKeyRequired = errors.New("idempotency key is required")
	ErrInvalidCreditAmount    = errors.New("credit amount must be non-zero")
	ErrSubscriptionNotFound   = errors.New("subscription not found")
)

// ── Store interfaces ─────────────────────────────────────────
//
// EntitlementService depends on narrow interfaces (not concrete repositories)
// so unit tests can supply in-memory fakes. The concrete repository types
// satisfy these.

type planStore interface {
	ListEnabled(ctx context.Context) ([]model.Plan, error)
	GetByCode(ctx context.Context, code string) (*model.Plan, error)
	ListPrices(ctx context.Context) ([]model.PlanPrice, error)
}

type entitlementStore interface {
	Get(ctx context.Context, userID uuid.UUID) (*model.UserEntitlement, error)
	UpsertLifetime(ctx context.Context, userID uuid.UUID, tier model.EntitlementTier, writeback bool, sourcePlanCode string) (*model.UserEntitlement, error)
	SetCloudSync(ctx context.Context, userID uuid.UUID, enabled bool) error
}

type subscriptionStore interface {
	Create(ctx context.Context, s *model.UserSubscription) error
	GetByID(ctx context.Context, id uuid.UUID) (*model.UserSubscription, error)
	ListActiveByUser(ctx context.Context, userID uuid.UUID) ([]model.UserSubscription, error)
	UpdatePeriod(ctx context.Context, id uuid.UUID, start, end time.Time) error
	ListDueForPeriodGrant(ctx context.Context, now time.Time, limit int32) ([]model.UserSubscription, error)
	RefreshExpired(ctx context.Context, now time.Time) (int64, error)
}

type creditLedgerStore interface {
	Grant(ctx context.Context, p repository.GrantParams) (repository.GrantResult, error)
	Consume(ctx context.Context, p repository.ConsumeParams) (repository.ConsumeResult, error)
	Balance(ctx context.Context, userID uuid.UUID) (int64, error)
	ListByUser(ctx context.Context, userID uuid.UUID, limit int32) ([]model.AICreditLedgerEntry, error)
	ExpireDue(ctx context.Context, now time.Time, limit int32) (int64, error)
}

type paymentOrderStore interface {
	Upsert(ctx context.Context, o *model.PaymentOrder) error
	Get(ctx context.Context, provider model.PaymentProvider, externalOrderID string) (*model.PaymentOrder, error)
	MarkCompleted(ctx context.Context, id uuid.UUID, completedAt time.Time) error
}

// EntitlementService owns paid-plan grants, cloud subscriptions, and the AI
// credit ledger. It is intentionally separate from the Stripe-shaped
// BillingService and from User.Tier / quota, so adding it does not perturb
// existing flows.
type EntitlementService struct {
	plans         planStore
	entitlements  entitlementStore
	subscriptions subscriptionStore
	credits       creditLedgerStore
	orders        paymentOrderStore
	// now is the clock, injectable for deterministic period/expiry tests.
	now func() time.Time
}

// NewEntitlementService wires an EntitlementService. A nil store leaves the
// corresponding feature unavailable; callers should pass real repositories.
func NewEntitlementService(plans planStore, entitlements entitlementStore, subscriptions subscriptionStore, credits creditLedgerStore, orders paymentOrderStore) *EntitlementService {
	return &EntitlementService{
		plans:         plans,
		entitlements:  entitlements,
		subscriptions: subscriptions,
		credits:       credits,
		orders:        orders,
		now:           time.Now,
	}
}

// ── Plan catalog ─────────────────────────────────────────────

// PlanPriceView is a price entry in a plan catalog response. Amounts are in
// minor units (cents / fen).
type PlanPriceView struct {
	Region          model.BillingRegion `json:"region"`
	Currency        model.Currency      `json:"currency"`
	Amount          int64               `json:"amount"`
	BillingPeriod   model.BillingPeriod `json:"billing_period"`
	EarlyBirdAmount *int64              `json:"early_bird_amount,omitempty"`
}

// PlanView is one plan in a catalog response. It surfaces the plan's effect
// (tier/credits/flags) parsed from metadata, never the raw metadata blob.
type PlanView struct {
	Code           string                `json:"code"`
	Name           string                `json:"name"`
	Kind           model.PlanKind        `json:"kind"`
	Tier           model.EntitlementTier `json:"tier,omitempty"`
	OneTimeCredits int64                 `json:"one_time_credits,omitempty"`
	PeriodCredits  int64                 `json:"period_credits,omitempty"`
	CloudSync      bool                  `json:"cloud_sync,omitempty"`
	Writeback      bool                  `json:"writeback,omitempty"`
	Prices         []PlanPriceView       `json:"prices"`
}

// GetAvailablePlans returns the enabled plan catalog. When region is non-empty
// (international/china) only that region's prices are included.
func (s *EntitlementService) GetAvailablePlans(ctx context.Context, region model.BillingRegion) ([]PlanView, error) {
	plans, err := s.plans.ListEnabled(ctx)
	if err != nil {
		return nil, fmt.Errorf("list plans: %w", err)
	}
	prices, err := s.plans.ListPrices(ctx)
	if err != nil {
		return nil, fmt.Errorf("list plan prices: %w", err)
	}

	pricesByPlan := make(map[string][]PlanPriceView)
	for _, p := range prices {
		if region != "" && p.Region != region {
			continue
		}
		pricesByPlan[p.PlanCode] = append(pricesByPlan[p.PlanCode], PlanPriceView{
			Region:          p.Region,
			Currency:        p.Currency,
			Amount:          p.Amount,
			BillingPeriod:   p.BillingPeriod,
			EarlyBirdAmount: p.EarlyBirdAmount,
		})
	}

	views := make([]PlanView, 0, len(plans))
	for _, plan := range plans {
		views = append(views, PlanView{
			Code:           plan.Code,
			Name:           plan.Name,
			Kind:           plan.Kind,
			Tier:           plan.Metadata.Tier,
			OneTimeCredits: plan.Metadata.OneTimeCredits,
			PeriodCredits:  plan.Metadata.PeriodCredits,
			CloudSync:      plan.Metadata.CloudSync,
			Writeback:      plan.Metadata.Writeback,
			Prices:         pricesByPlan[plan.Code],
		})
	}
	return views, nil
}

// ── Grants ───────────────────────────────────────────────────

// GrantOptions carries the purchase context for a grant. Provider defaults to
// manual; Region/BillingPeriod feed the recorded order amount and (for
// subscriptions) the active-until horizon.
type GrantOptions struct {
	Provider        model.PaymentProvider
	ExternalOrderID string
	Region          model.BillingRegion
	BillingPeriod   model.BillingPeriod
	RawMetadata     map[string]any
}

// GrantOutcome summarizes what a grant applied.
type GrantOutcome struct {
	PlanCode       string                   `json:"plan_code"`
	Entitlement    model.UserEntitlement    `json:"entitlement"`
	CreditsGranted int64                    `json:"credits_granted"`
	Subscriptions  []model.UserSubscription `json:"subscriptions,omitempty"`
}

// GrantPlanToUser applies a single plan to a user. Bundles are delegated to
// GrantBundleToUser. It records a payment order (sanitized, idempotent on
// provider+external_order_id) and then applies the plan's effect.
func (s *EntitlementService) GrantPlanToUser(ctx context.Context, userID uuid.UUID, planCode string, opts GrantOptions) (*GrantOutcome, error) {
	plan, err := s.loadEnabledPlan(ctx, planCode)
	if err != nil {
		return nil, err
	}
	if plan.Kind == model.PlanKindBundle {
		return s.GrantBundleToUser(ctx, userID, planCode, opts)
	}

	if done, err := s.alreadyFulfilled(ctx, opts.Provider, opts.ExternalOrderID); err != nil {
		return nil, err
	} else if done {
		// A replay of an already-recorded external order is a no-op.
		return s.finalizeOutcome(ctx, userID, &GrantOutcome{PlanCode: plan.Code})
	}

	if err := s.recordOrder(ctx, userID, plan, opts); err != nil {
		return nil, err
	}

	outcome := &GrantOutcome{PlanCode: plan.Code}
	switch plan.Kind {
	case model.PlanKindLifetime:
		credits, err := s.applyLifetime(ctx, userID, plan, opts)
		if err != nil {
			return nil, err
		}
		outcome.CreditsGranted += credits
	case model.PlanKindSubscription:
		sub, credits, err := s.activateSubscription(ctx, userID, plan, opts, s.addBillingPeriod(s.now(), opts.BillingPeriod))
		if err != nil {
			return nil, err
		}
		outcome.CreditsGranted += credits
		if sub != nil {
			outcome.Subscriptions = append(outcome.Subscriptions, *sub)
		}
	default:
		return nil, fmt.Errorf("unsupported plan kind %q", plan.Kind)
	}

	if err := s.markOrderCompleted(ctx, opts.Provider, opts.ExternalOrderID); err != nil {
		return nil, err
	}
	return s.finalizeOutcome(ctx, userID, outcome)
}

// GrantBundleToUser applies a bundle plan by splitting it into its components:
// each lifetime component upgrades the entitlement and grants one-time credits,
// and each subscription component activates a cloud subscription for the
// component's cloud_months and grants its first period. The Max lifetime and the
// Cloud subscription are therefore tracked independently.
func (s *EntitlementService) GrantBundleToUser(ctx context.Context, userID uuid.UUID, planCode string, opts GrantOptions) (*GrantOutcome, error) {
	plan, err := s.loadEnabledPlan(ctx, planCode)
	if err != nil {
		return nil, err
	}

	if done, err := s.alreadyFulfilled(ctx, opts.Provider, opts.ExternalOrderID); err != nil {
		return nil, err
	} else if done {
		return s.finalizeOutcome(ctx, userID, &GrantOutcome{PlanCode: plan.Code})
	}

	if err := s.recordOrder(ctx, userID, plan, opts); err != nil {
		return nil, err
	}

	outcome := &GrantOutcome{PlanCode: plan.Code}

	// A non-bundle plan (or a bundle without components) is applied as itself.
	components := plan.Metadata.Components
	if plan.Kind != model.PlanKindBundle || len(components) == 0 {
		components = []model.PlanComponent{{PlanCode: plan.Code}}
	}

	for _, comp := range components {
		compPlan, err := s.loadEnabledPlan(ctx, comp.PlanCode)
		if err != nil {
			return nil, err
		}
		// Components inherit the order's provider/id but are namespaced per
		// component code so each grant is idempotent on webhook replay.
		compOpts := opts
		switch compPlan.Kind {
		case model.PlanKindLifetime:
			credits, err := s.applyLifetime(ctx, userID, compPlan, compOpts)
			if err != nil {
				return nil, err
			}
			outcome.CreditsGranted += credits
		case model.PlanKindSubscription:
			months := comp.CloudMonths
			if months <= 0 {
				months = 1
			}
			sub, credits, err := s.activateSubscription(ctx, userID, compPlan, compOpts, s.now().AddDate(0, months, 0))
			if err != nil {
				return nil, err
			}
			outcome.CreditsGranted += credits
			if sub != nil {
				outcome.Subscriptions = append(outcome.Subscriptions, *sub)
			}
		default:
			return nil, fmt.Errorf("unsupported component plan kind %q", compPlan.Kind)
		}
	}

	if err := s.markOrderCompleted(ctx, opts.Provider, opts.ExternalOrderID); err != nil {
		return nil, err
	}
	return s.finalizeOutcome(ctx, userID, outcome)
}

// applyLifetime upgrades the entitlement (tier only moves up, write-back is
// sticky) and grants the plan's one-time, never-expiring credits. It returns the
// number of credits actually granted (0 on idempotent replay).
func (s *EntitlementService) applyLifetime(ctx context.Context, userID uuid.UUID, plan *model.Plan, opts GrantOptions) (int64, error) {
	tier := plan.Metadata.Tier
	if tier == "" {
		tier = model.EntitlementTierFree
	}
	if _, err := s.entitlements.UpsertLifetime(ctx, userID, tier, plan.Metadata.Writeback, plan.Code); err != nil {
		return 0, fmt.Errorf("apply lifetime entitlement: %w", err)
	}

	if plan.Metadata.OneTimeCredits <= 0 {
		return 0, nil
	}
	res, err := s.credits.Grant(ctx, repository.GrantParams{
		UserID:         userID,
		Source:         lifetimeGrantSource(tier),
		Amount:         plan.Metadata.OneTimeCredits,
		IdempotencyKey: s.grantIdemKey(opts, plan.Code, userID),
		ExpiresAt:      nil, // one-time credits never expire
		Metadata:       map[string]any{"plan": plan.Code},
	})
	if err != nil {
		return 0, fmt.Errorf("grant one-time credits: %w", err)
	}
	if !res.Granted {
		return 0, nil
	}
	return plan.Metadata.OneTimeCredits, nil
}

// activateSubscription creates a cloud subscription with the given hard end,
// enables cloud sync, and grants the first period's credits. It returns the
// created subscription and the credits granted.
func (s *EntitlementService) activateSubscription(ctx context.Context, userID uuid.UUID, plan *model.Plan, opts GrantOptions, activeUntil time.Time) (*model.UserSubscription, int64, error) {
	now := s.now()
	provider := opts.Provider
	if provider == "" {
		provider = model.PaymentProviderManual
	}
	sub := &model.UserSubscription{
		UserID:             userID,
		PlanCode:           plan.Code,
		Status:             model.SubscriptionStatusActive,
		CurrentPeriodStart: now,
		CurrentPeriodEnd:   now.AddDate(0, 1, 0), // monthly credit cadence
		ActiveUntil:        activeUntil,
		Provider:           provider,
		ExternalOrderID:    opts.ExternalOrderID,
	}
	if err := s.subscriptions.Create(ctx, sub); err != nil {
		return nil, 0, fmt.Errorf("create subscription: %w", err)
	}
	if err := s.entitlements.SetCloudSync(ctx, userID, true); err != nil {
		return nil, 0, fmt.Errorf("enable cloud sync: %w", err)
	}
	result, err := s.grantPeriod(ctx, sub)
	if err != nil {
		return nil, 0, err
	}
	return sub, result.Credits, nil
}

// finalizeOutcome reloads the effective entitlement into the outcome.
func (s *EntitlementService) finalizeOutcome(ctx context.Context, userID uuid.UUID, outcome *GrantOutcome) (*GrantOutcome, error) {
	ent, err := s.entitlements.Get(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("load entitlement: %w", err)
	}
	if ent == nil {
		def := model.DefaultEntitlement(userID)
		ent = &def
	}
	outcome.Entitlement = *ent
	return outcome, nil
}

// ── User-facing reads ────────────────────────────────────────

// EntitlementView is the user's effective entitlement plus active subscriptions.
type EntitlementView struct {
	Entitlement   model.UserEntitlement `json:"entitlement"`
	Subscriptions []SubscriptionView    `json:"subscriptions"`
}

// SubscriptionView is the non-sensitive summary of an active subscription.
type SubscriptionView struct {
	PlanCode         string                   `json:"plan_code"`
	Status           model.SubscriptionStatus `json:"status"`
	CurrentPeriodEnd time.Time                `json:"current_period_end"`
	ActiveUntil      time.Time                `json:"active_until"`
	Provider         model.PaymentProvider    `json:"provider"`
}

// GetUserEntitlements returns the user's effective entitlement (defaulting to
// free when none exists) and their active subscriptions.
func (s *EntitlementService) GetUserEntitlements(ctx context.Context, userID uuid.UUID) (*EntitlementView, error) {
	ent, err := s.entitlements.Get(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("get entitlement: %w", err)
	}
	if ent == nil {
		def := model.DefaultEntitlement(userID)
		ent = &def
	}
	subs, err := s.subscriptions.ListActiveByUser(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("list subscriptions: %w", err)
	}
	views := make([]SubscriptionView, 0, len(subs))
	for _, sub := range subs {
		views = append(views, SubscriptionView{
			PlanCode:         sub.PlanCode,
			Status:           sub.Status,
			CurrentPeriodEnd: sub.CurrentPeriodEnd,
			ActiveUntil:      sub.ActiveUntil,
			Provider:         sub.Provider,
		})
	}
	return &EntitlementView{Entitlement: *ent, Subscriptions: views}, nil
}

// GetAICreditBalance returns the user's live (non-expired) AI credit balance.
func (s *EntitlementService) GetAICreditBalance(ctx context.Context, userID uuid.UUID) (int64, error) {
	return s.credits.Balance(ctx, userID)
}

// ListAICreditLedger returns recent ledger entries for a user.
func (s *EntitlementService) ListAICreditLedger(ctx context.Context, userID uuid.UUID, limit int32) ([]model.AICreditLedgerEntry, error) {
	entries, err := s.credits.ListByUser(ctx, userID, limit)
	if err != nil {
		return nil, err
	}
	if entries == nil {
		entries = []model.AICreditLedgerEntry{}
	}
	return entries, nil
}

// ── AI credit consumption ────────────────────────────────────

// ConsumeAICreditsInput describes a credit consumption. IdempotencyKey is
// required so a retried request never double-charges.
type ConsumeAICreditsInput struct {
	UserID         uuid.UUID
	Cost           int64
	IdempotencyKey string
	Reason         string
	Metadata       map[string]any
}

// ConsumeAICreditsResult reports the post-consume balance and whether this call
// actually charged (false on idempotent replay).
type ConsumeAICreditsResult struct {
	BalanceAfter int64 `json:"balance_after"`
	Charged      bool  `json:"charged"`
}

// ConsumeAICredits deducts credits for an AI operation. It requires an
// idempotency key, rejects non-positive costs, fails with ErrInsufficientCredits
// when the balance is too low (never going negative), and is a no-op charge on
// replay of the same key.
func (s *EntitlementService) ConsumeAICredits(ctx context.Context, in ConsumeAICreditsInput) (*ConsumeAICreditsResult, error) {
	if in.IdempotencyKey == "" {
		return nil, ErrIdempotencyKeyRequired
	}
	if in.Cost <= 0 {
		return nil, ErrInvalidCreditAmount
	}
	metadata := sanitizeAuditMetadata(in.Metadata)
	if in.Reason != "" {
		metadata["reason"] = in.Reason
	}
	res, err := s.credits.Consume(ctx, repository.ConsumeParams{
		UserID:         in.UserID,
		Cost:           in.Cost,
		IdempotencyKey: in.IdempotencyKey,
		Source:         model.CreditSourceConsume,
		Metadata:       metadata,
	})
	if err != nil {
		if errors.Is(err, repository.ErrInsufficientCredits) {
			return nil, ErrInsufficientCredits
		}
		return nil, fmt.Errorf("consume credits: %w", err)
	}
	return &ConsumeAICreditsResult{BalanceAfter: res.BalanceAfter, Charged: res.Charged}, nil
}

// AdjustCreditsInput describes a manual credit adjustment by an operator.
// A positive Amount grants non-expiring credits; a negative Amount deducts.
type AdjustCreditsInput struct {
	UserID         uuid.UUID
	Amount         int64
	Reason         string
	IdempotencyKey string
}

// AdjustAICredits applies a manual credit grant or deduction.
func (s *EntitlementService) AdjustAICredits(ctx context.Context, in AdjustCreditsInput) (*ConsumeAICreditsResult, error) {
	if in.Amount == 0 {
		return nil, ErrInvalidCreditAmount
	}
	key := in.IdempotencyKey
	if key == "" {
		key = fmt.Sprintf("manual:%s:%s", in.UserID, uuid.NewString())
	}
	metadata := map[string]any{}
	if in.Reason != "" {
		metadata["reason"] = in.Reason
	}
	if in.Amount > 0 {
		res, err := s.credits.Grant(ctx, repository.GrantParams{
			UserID:         in.UserID,
			Source:         model.CreditSourceManualGrant,
			Amount:         in.Amount,
			IdempotencyKey: key,
			ExpiresAt:      nil,
			Metadata:       metadata,
		})
		if err != nil {
			return nil, fmt.Errorf("manual grant: %w", err)
		}
		return &ConsumeAICreditsResult{BalanceAfter: res.BalanceAfter, Charged: res.Granted}, nil
	}
	res, err := s.credits.Consume(ctx, repository.ConsumeParams{
		UserID:         in.UserID,
		Cost:           -in.Amount,
		IdempotencyKey: key,
		Source:         model.CreditSourceAdjustment,
		Metadata:       metadata,
	})
	if err != nil {
		if errors.Is(err, repository.ErrInsufficientCredits) {
			return nil, ErrInsufficientCredits
		}
		return nil, fmt.Errorf("manual deduction: %w", err)
	}
	return &ConsumeAICreditsResult{BalanceAfter: res.BalanceAfter, Charged: res.Charged}, nil
}

// ── Subscription credit cycle ────────────────────────────────

// PeriodGrantResult reports a period-grant outcome.
type PeriodGrantResult struct {
	Granted bool  `json:"granted"`
	Credits int64 `json:"credits"`
	Expired bool  `json:"expired"`
}

// GrantCloudPeriodCredits grants the subscription's current-period credits,
// idempotently per period (a re-run for the same period never double-grants).
// It rolls the monthly window forward to the period containing now before
// granting, so each monthly scheduler tick grants that month's credits. This is
// the per-tick entry point for a scheduler.
func (s *EntitlementService) GrantCloudPeriodCredits(ctx context.Context, userID, subscriptionID uuid.UUID) (*PeriodGrantResult, error) {
	sub, err := s.subscriptions.GetByID(ctx, subscriptionID)
	if err != nil {
		return nil, fmt.Errorf("get subscription: %w", err)
	}
	if sub == nil || sub.UserID != userID {
		return nil, ErrSubscriptionNotFound
	}
	return s.grantPeriod(ctx, sub)
}

// grantPeriod is the shared period-grant logic used by both activation and the
// scheduler tick. It first rolls the monthly window forward to the period that
// contains "now" (catching up after a scheduler gap, never past the hard end),
// then grants that period. The grant's idempotency key is derived from the
// subscription and the current period start and the credits expire at the period
// end, so a re-run within a period never double-grants; intervening periods
// after a long scheduler gap are intentionally skipped (credits are monthly
// use-it-or-lose-it).
func (s *EntitlementService) grantPeriod(ctx context.Context, sub *model.UserSubscription) (*PeriodGrantResult, error) {
	now := s.now()
	if sub.Status != model.SubscriptionStatusActive {
		return &PeriodGrantResult{}, nil
	}
	if !now.Before(sub.ActiveUntil) {
		return &PeriodGrantResult{Expired: true}, nil
	}

	// Roll forward to the period containing now, bounded by the hard end. The
	// guard caps iterations against pathological data.
	changed := false
	for guard := 0; !now.Before(sub.CurrentPeriodEnd) && guard < 1200; guard++ {
		nextStart := sub.CurrentPeriodEnd
		if !nextStart.Before(sub.ActiveUntil) {
			break
		}
		sub.CurrentPeriodStart = nextStart
		sub.CurrentPeriodEnd = nextStart.AddDate(0, 1, 0)
		changed = true
	}
	if changed {
		if err := s.subscriptions.UpdatePeriod(ctx, sub.ID, sub.CurrentPeriodStart, sub.CurrentPeriodEnd); err != nil {
			return nil, fmt.Errorf("advance subscription period: %w", err)
		}
	}

	plan, err := s.plans.GetByCode(ctx, sub.PlanCode)
	if err != nil {
		return nil, fmt.Errorf("get subscription plan: %w", err)
	}
	result := &PeriodGrantResult{}
	if plan != nil && plan.Metadata.PeriodCredits > 0 {
		periodEnd := sub.CurrentPeriodEnd
		res, err := s.credits.Grant(ctx, repository.GrantParams{
			UserID:         sub.UserID,
			Source:         model.CreditSourceCloudPeriodGrant,
			Amount:         plan.Metadata.PeriodCredits,
			IdempotencyKey: fmt.Sprintf("cloud_period:%s:%d", sub.ID, sub.CurrentPeriodStart.Unix()),
			ExpiresAt:      &periodEnd,
			Metadata:       map[string]any{"plan": sub.PlanCode, "subscription_id": sub.ID.String()},
		})
		if err != nil {
			return nil, fmt.Errorf("grant period credits: %w", err)
		}
		if res.Granted {
			result.Granted = true
			result.Credits = plan.Metadata.PeriodCredits
		}
	}

	return result, nil
}

// RefreshExpiredSubscriptions expires subscriptions past their hard end and
// recomputes cloud sync for affected users, leaving lifetime tier/write-back
// untouched. It returns the number of subscriptions expired.
func (s *EntitlementService) RefreshExpiredSubscriptions(ctx context.Context) (int64, error) {
	return s.subscriptions.RefreshExpired(ctx, s.now())
}

// GrantDuePeriodCredits grants cloud period credits for every active
// subscription whose monthly window has rolled over, in bounded batches. Each
// grant rolls that subscription's window past now so it is no longer due, which
// lets this drain all due subscriptions across batches. It returns the number of
// period grants made and backs the periodic billing-maintenance task.
func (s *EntitlementService) GrantDuePeriodCredits(ctx context.Context) (int, error) {
	const batchSize = 500
	const maxBatches = 1000 // backstop against a non-converging loop
	now := s.now()
	granted := 0
	for batch := 0; batch < maxBatches; batch++ {
		subs, err := s.subscriptions.ListDueForPeriodGrant(ctx, now, batchSize)
		if err != nil {
			return granted, fmt.Errorf("list due subscriptions: %w", err)
		}
		if len(subs) == 0 {
			return granted, nil
		}
		for i := range subs {
			res, err := s.grantPeriod(ctx, &subs[i])
			if err != nil {
				return granted, err
			}
			if res.Granted {
				granted++
			}
		}
	}
	return granted, nil
}

// ExpireAICredits records the expiry of any due credit lots in the ledger.
// Balance and consumption already ignore expired lots, so this is for ledger
// completeness; it processes a bounded batch per call. Returns lots expired.
func (s *EntitlementService) ExpireAICredits(ctx context.Context) (int64, error) {
	return s.credits.ExpireDue(ctx, s.now(), 500)
}

// ── AI credit cost guidance ──────────────────────────────────

// AICreditOperation categorizes an AI operation by cost weight. Future AI
// features call AICreditCost to translate an operation class into a credit cost.
type AICreditOperation string

const (
	AICreditOpLight       AICreditOperation = "light"        // lightweight operation
	AICreditOpLongContext AICreditOperation = "long_context" // long-context analysis
	AICreditOpWriteback   AICreditOperation = "writeback"    // write-back / complex reasoning
	AICreditOpHighCost    AICreditOperation = "high_cost"    // high-cost model / very long context
)

// AICreditCost returns the credit cost for an operation class, following the
// product's cost tiers (light 1, long-context 3, write-back 5, high-cost 10).
func AICreditCost(op AICreditOperation) int64 {
	switch op {
	case AICreditOpLongContext:
		return 3
	case AICreditOpWriteback:
		return 5
	case AICreditOpHighCost:
		return 10
	case AICreditOpLight:
		return 1
	default:
		return 1
	}
}

// ── Internal helpers ─────────────────────────────────────────

func (s *EntitlementService) loadEnabledPlan(ctx context.Context, code string) (*model.Plan, error) {
	plan, err := s.plans.GetByCode(ctx, code)
	if err != nil {
		return nil, fmt.Errorf("get plan: %w", err)
	}
	if plan == nil {
		return nil, ErrPlanNotFound
	}
	if !plan.Enabled {
		return nil, ErrPlanDisabled
	}
	return plan, nil
}

// alreadyFulfilled reports whether an order with this provider+external id has
// already completed fulfillment. Paid/failed orders remain retryable.
func (s *EntitlementService) alreadyFulfilled(ctx context.Context, provider model.PaymentProvider, externalOrderID string) (bool, error) {
	if externalOrderID == "" {
		return false, nil
	}
	if provider == "" {
		provider = model.PaymentProviderManual
	}
	existing, err := s.orders.Get(ctx, provider, externalOrderID)
	if err != nil {
		return false, fmt.Errorf("check existing order: %w", err)
	}
	return existing != nil && existing.Status == model.PaymentOrderStatusCompleted, nil
}

// recordOrder persists a payment order for the purchase. raw_metadata is
// sanitized so payment secrets are never stored. The order is idempotent on
// provider+external_order_id when an external id is present.
func (s *EntitlementService) recordOrder(ctx context.Context, userID uuid.UUID, plan *model.Plan, opts GrantOptions) error {
	provider := opts.Provider
	if provider == "" {
		provider = model.PaymentProviderManual
	}
	amount, currency := s.resolveOrderAmount(ctx, plan.Code, opts.Region, opts.BillingPeriod, plan.Kind)
	order := &model.PaymentOrder{
		UserID:          userID,
		Provider:        provider,
		ExternalOrderID: opts.ExternalOrderID,
		PlanCode:        plan.Code,
		Currency:        currency,
		Amount:          amount,
		Status:          model.PaymentOrderStatusPaid,
		RawMetadata:     sanitizeAuditMetadata(opts.RawMetadata),
	}
	now := s.now()
	order.PaidAt = &now
	if err := s.orders.Upsert(ctx, order); err != nil {
		return fmt.Errorf("record payment order: %w", err)
	}
	return nil
}

func (s *EntitlementService) markOrderCompleted(ctx context.Context, provider model.PaymentProvider, externalOrderID string) error {
	if externalOrderID == "" {
		return nil
	}
	if provider == "" {
		provider = model.PaymentProviderManual
	}
	order, err := s.orders.Get(ctx, provider, externalOrderID)
	if err != nil {
		return fmt.Errorf("load payment order: %w", err)
	}
	if order == nil || order.Status == model.PaymentOrderStatusCompleted {
		return nil
	}
	return s.orders.MarkCompleted(ctx, order.ID, s.now())
}

// resolveOrderAmount looks up the price for an order's region/period. It is
// best-effort: an unmatched lookup yields a zero amount and empty currency
// rather than failing the grant.
func (s *EntitlementService) resolveOrderAmount(ctx context.Context, planCode string, region model.BillingRegion, period model.BillingPeriod, kind model.PlanKind) (int64, string) {
	prices, err := s.plans.ListPrices(ctx)
	if err != nil {
		return 0, ""
	}
	if region == "" {
		region = model.RegionInternational
	}
	if period == "" {
		if kind == model.PlanKindSubscription {
			period = model.BillingPeriodMonthly
		} else {
			period = model.BillingPeriodNone
		}
	}
	var fallback *model.PlanPrice
	for i := range prices {
		p := prices[i]
		if p.PlanCode != planCode || p.Region != region {
			continue
		}
		if p.BillingPeriod == period {
			return p.Amount, string(p.Currency)
		}
		if fallback == nil {
			fb := p
			fallback = &fb
		}
	}
	if fallback != nil {
		return fallback.Amount, string(fallback.Currency)
	}
	return 0, ""
}

// addBillingPeriod returns the subscription hard end for a payment cadence.
// An unspecified or "none" period defaults to monthly.
func (s *EntitlementService) addBillingPeriod(from time.Time, period model.BillingPeriod) time.Time {
	switch period {
	case model.BillingPeriodYearly:
		return from.AddDate(1, 0, 0)
	default:
		return from.AddDate(0, 1, 0)
	}
}

// grantIdemKey derives an idempotency key for a one-time grant. When an external
// order id is present the key is stable (so webhook replays do not double-grant);
// otherwise a unique key is generated per call.
func (s *EntitlementService) grantIdemKey(opts GrantOptions, planCode string, userID uuid.UUID) string {
	provider := opts.Provider
	if provider == "" {
		provider = model.PaymentProviderManual
	}
	if opts.ExternalOrderID != "" {
		return fmt.Sprintf("plan_grant:%s:%s:%s", provider, opts.ExternalOrderID, planCode)
	}
	return fmt.Sprintf("plan_grant:%s:%s:%s", planCode, userID, uuid.NewString())
}

func lifetimeGrantSource(tier model.EntitlementTier) model.CreditSource {
	switch tier {
	case model.EntitlementTierMax:
		return model.CreditSourceMaxGrant
	case model.EntitlementTierPro:
		return model.CreditSourceProGrant
	default:
		return model.CreditSourceFreeGrant
	}
}
