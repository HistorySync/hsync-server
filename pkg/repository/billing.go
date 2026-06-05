package repository

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/historysync/hsync-server/pkg/model"
)

// ErrInsufficientCredits is returned by CreditLedgerRepo.Consume when the user
// does not have enough live (non-expired) credits to cover the cost. The
// service layer maps it to its own sentinel for the handler.
var ErrInsufficientCredits = errors.New("insufficient ai credits")

// userAdvisoryKey derives a stable int64 from a user ID for per-user
// transaction-scoped advisory locks. Collisions only cause harmless extra
// serialization between two unrelated users, never incorrect accounting.
func userAdvisoryKey(userID uuid.UUID) int64 {
	return int64(binary.BigEndian.Uint64(userID[:8]))
}

// ── PlanRepo ─────────────────────────────────────────────────

// PlanRepo reads the (migration-seeded) plan catalog.
type PlanRepo struct {
	pool *pgxpool.Pool
}

// ListEnabled returns all enabled plans ordered by code.
func (r *PlanRepo) ListEnabled(ctx context.Context) ([]model.Plan, error) {
	const q = `
		SELECT id, code, name, kind, enabled, metadata, created_at, updated_at
		FROM plans WHERE enabled = true ORDER BY code`
	rows, err := r.pool.Query(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("list plans: %w", err)
	}
	defer rows.Close()
	return scanPlans(rows)
}

// GetByCode returns a single plan by code, or nil when no such plan exists.
func (r *PlanRepo) GetByCode(ctx context.Context, code string) (*model.Plan, error) {
	const q = `
		SELECT id, code, name, kind, enabled, metadata, created_at, updated_at
		FROM plans WHERE code = $1`
	var p model.Plan
	var metadata []byte
	err := r.pool.QueryRow(ctx, q, code).Scan(
		&p.ID, &p.Code, &p.Name, &p.Kind, &p.Enabled, &metadata, &p.CreatedAt, &p.UpdatedAt,
	)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get plan: %w", err)
	}
	if p.Metadata, err = model.ParsePlanMetadata(metadata); err != nil {
		return nil, fmt.Errorf("parse plan metadata: %w", err)
	}
	return &p, nil
}

// ListPrices returns every plan price (the catalog is small). The service groups
// them by plan code.
func (r *PlanRepo) ListPrices(ctx context.Context) ([]model.PlanPrice, error) {
	const q = `
		SELECT id, plan_code, region, currency, amount, billing_period, early_bird_amount, created_at, updated_at
		FROM plan_prices ORDER BY plan_code, region, billing_period`
	rows, err := r.pool.Query(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("list plan prices: %w", err)
	}
	defer rows.Close()
	return scanPlanPrices(rows)
}

func scanPlans(rows pgx.Rows) ([]model.Plan, error) {
	var plans []model.Plan
	for rows.Next() {
		var p model.Plan
		var metadata []byte
		if err := rows.Scan(&p.ID, &p.Code, &p.Name, &p.Kind, &p.Enabled, &metadata, &p.CreatedAt, &p.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan plan: %w", err)
		}
		meta, err := model.ParsePlanMetadata(metadata)
		if err != nil {
			return nil, fmt.Errorf("parse plan metadata: %w", err)
		}
		p.Metadata = meta
		plans = append(plans, p)
	}
	return plans, rows.Err()
}

func scanPlanPrices(rows pgx.Rows) ([]model.PlanPrice, error) {
	var prices []model.PlanPrice
	for rows.Next() {
		var p model.PlanPrice
		if err := rows.Scan(&p.ID, &p.PlanCode, &p.Region, &p.Currency, &p.Amount,
			&p.BillingPeriod, &p.EarlyBirdAmount, &p.CreatedAt, &p.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan plan price: %w", err)
		}
		prices = append(prices, p)
	}
	return prices, rows.Err()
}

// ── EntitlementRepo ──────────────────────────────────────────

// EntitlementRepo persists the per-user effective entitlement.
type EntitlementRepo struct {
	pool *pgxpool.Pool
}

// Get returns the user's entitlement, or nil when none has been created yet.
func (r *EntitlementRepo) Get(ctx context.Context, userID uuid.UUID) (*model.UserEntitlement, error) {
	const q = `
		SELECT id, user_id, tier, cloud_sync_enabled, writeback_enabled,
		       source_plan_code, starts_at, ends_at, created_at, updated_at
		FROM user_entitlements WHERE user_id = $1`
	var e model.UserEntitlement
	err := r.pool.QueryRow(ctx, q, userID).Scan(
		&e.ID, &e.UserID, &e.Tier, &e.CloudSyncEnabled, &e.WritebackEnabled,
		&e.SourcePlanCode, &e.StartsAt, &e.EndsAt, &e.CreatedAt, &e.UpdatedAt,
	)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get entitlement: %w", err)
	}
	return &e, nil
}

// UpsertLifetime applies a lifetime entitlement. The tier only ever moves up
// (free -> pro -> max), write-back is sticky (OR), ends_at is cleared (lifetime
// never expires), and starts_at is preserved on an existing row.
func (r *EntitlementRepo) UpsertLifetime(ctx context.Context, userID uuid.UUID, tier model.EntitlementTier, writeback bool, sourcePlanCode string) (*model.UserEntitlement, error) {
	const q = `
		INSERT INTO user_entitlements (user_id, tier, writeback_enabled, source_plan_code, starts_at, ends_at)
		VALUES ($1, $2, $3, $4, now(), NULL)
		ON CONFLICT (user_id) DO UPDATE SET
			tier = CASE
				WHEN (CASE EXCLUDED.tier WHEN 'max' THEN 2 WHEN 'pro' THEN 1 ELSE 0 END)
				   > (CASE user_entitlements.tier WHEN 'max' THEN 2 WHEN 'pro' THEN 1 ELSE 0 END)
				THEN EXCLUDED.tier ELSE user_entitlements.tier END,
			writeback_enabled = user_entitlements.writeback_enabled OR EXCLUDED.writeback_enabled,
			source_plan_code = EXCLUDED.source_plan_code,
			ends_at = NULL
		RETURNING id, user_id, tier, cloud_sync_enabled, writeback_enabled,
		          source_plan_code, starts_at, ends_at, created_at, updated_at`
	var e model.UserEntitlement
	if err := r.pool.QueryRow(ctx, q, userID, string(tier), writeback, sourcePlanCode).Scan(
		&e.ID, &e.UserID, &e.Tier, &e.CloudSyncEnabled, &e.WritebackEnabled,
		&e.SourcePlanCode, &e.StartsAt, &e.EndsAt, &e.CreatedAt, &e.UpdatedAt,
	); err != nil {
		return nil, fmt.Errorf("upsert lifetime entitlement: %w", err)
	}
	return &e, nil
}

// SetCloudSync toggles cloud sync for a user, creating a default free
// entitlement row if none exists yet.
func (r *EntitlementRepo) SetCloudSync(ctx context.Context, userID uuid.UUID, enabled bool) error {
	const q = `
		INSERT INTO user_entitlements (user_id, cloud_sync_enabled)
		VALUES ($1, $2)
		ON CONFLICT (user_id) DO UPDATE SET cloud_sync_enabled = EXCLUDED.cloud_sync_enabled`
	if _, err := r.pool.Exec(ctx, q, userID, enabled); err != nil {
		return fmt.Errorf("set cloud sync: %w", err)
	}
	return nil
}

// ── SubscriptionRepo ─────────────────────────────────────────

// SubscriptionRepo persists cloud subscriptions.
type SubscriptionRepo struct {
	pool *pgxpool.Pool
}

// Create inserts a new subscription.
func (r *SubscriptionRepo) Create(ctx context.Context, s *model.UserSubscription) error {
	const q = `
		INSERT INTO user_subscriptions (user_id, plan_code, status, current_period_start,
		                                current_period_end, active_until, provider, external_order_id)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		ON CONFLICT (provider, external_order_id, plan_code) WHERE external_order_id <> '' DO UPDATE SET
			active_until = GREATEST(user_subscriptions.active_until, EXCLUDED.active_until),
			status = CASE WHEN user_subscriptions.status = 'canceled' THEN user_subscriptions.status ELSE EXCLUDED.status END
		RETURNING id, created_at, updated_at`
	return r.pool.QueryRow(ctx, q,
		s.UserID, s.PlanCode, string(s.Status), s.CurrentPeriodStart,
		s.CurrentPeriodEnd, s.ActiveUntil, string(s.Provider), s.ExternalOrderID,
	).Scan(&s.ID, &s.CreatedAt, &s.UpdatedAt)
}

// GetByID returns a subscription by ID, or nil when not found.
func (r *SubscriptionRepo) GetByID(ctx context.Context, id uuid.UUID) (*model.UserSubscription, error) {
	const q = `
		SELECT id, user_id, plan_code, status, current_period_start, current_period_end,
		       active_until, provider, external_order_id, created_at, updated_at
		FROM user_subscriptions WHERE id = $1`
	s := &model.UserSubscription{}
	err := r.pool.QueryRow(ctx, q, id).Scan(
		&s.ID, &s.UserID, &s.PlanCode, &s.Status, &s.CurrentPeriodStart, &s.CurrentPeriodEnd,
		&s.ActiveUntil, &s.Provider, &s.ExternalOrderID, &s.CreatedAt, &s.UpdatedAt,
	)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get subscription: %w", err)
	}
	return s, nil
}

// ListActiveByUser returns the user's active subscriptions.
func (r *SubscriptionRepo) ListActiveByUser(ctx context.Context, userID uuid.UUID) ([]model.UserSubscription, error) {
	const q = `
		SELECT id, user_id, plan_code, status, current_period_start, current_period_end,
		       active_until, provider, external_order_id, created_at, updated_at
		FROM user_subscriptions WHERE user_id = $1 AND status = 'active'
		ORDER BY created_at`
	rows, err := r.pool.Query(ctx, q, userID)
	if err != nil {
		return nil, fmt.Errorf("list active subscriptions: %w", err)
	}
	defer rows.Close()
	return scanSubscriptions(rows)
}

// UpdatePeriod advances the monthly credit-grant window of a subscription.
func (r *SubscriptionRepo) UpdatePeriod(ctx context.Context, id uuid.UUID, start, end time.Time) error {
	const q = `UPDATE user_subscriptions SET current_period_start = $2, current_period_end = $3 WHERE id = $1`
	_, err := r.pool.Exec(ctx, q, id, start, end)
	return err
}

// ListDueForPeriodGrant returns active subscriptions whose current monthly
// window has elapsed (current_period_end <= now) while their hard end has not
// (active_until > now): those needing a period roll + grant. It is bounded by
// limit; granting rolls each subscription's window past now so a re-query no
// longer returns it, letting the scheduler drain due subscriptions in batches.
func (r *SubscriptionRepo) ListDueForPeriodGrant(ctx context.Context, now time.Time, limit int32) ([]model.UserSubscription, error) {
	if limit <= 0 || limit > 1000 {
		limit = 500
	}
	const q = `
		SELECT id, user_id, plan_code, status, current_period_start, current_period_end,
		       active_until, provider, external_order_id, created_at, updated_at
		FROM user_subscriptions
		WHERE status = 'active' AND active_until > $1 AND current_period_end <= $1
		ORDER BY id
		LIMIT $2`
	rows, err := r.pool.Query(ctx, q, now, limit)
	if err != nil {
		return nil, fmt.Errorf("list due subscriptions: %w", err)
	}
	defer rows.Close()
	return scanSubscriptions(rows)
}

// RefreshExpired marks all active subscriptions whose active_until has passed as
// expired, then recomputes cloud_sync_enabled for the affected users (true iff
// they still have another active subscription). It deliberately does not touch
// tier or writeback, so a Max lifetime entitlement survives Cloud lapsing. It
// returns the number of subscriptions expired.
func (r *SubscriptionRepo) RefreshExpired(ctx context.Context, now time.Time) (int64, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("begin refresh: %w", err)
	}
	defer tx.Rollback(ctx)

	rows, err := tx.Query(ctx,
		`UPDATE user_subscriptions SET status = 'expired'
		 WHERE status = 'active' AND active_until <= $1
		 RETURNING user_id`, now)
	if err != nil {
		return 0, fmt.Errorf("mark expired subscriptions: %w", err)
	}
	var userIDs []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return 0, fmt.Errorf("scan expired user: %w", err)
		}
		userIDs = append(userIDs, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, fmt.Errorf("iterate expired users: %w", err)
	}

	expired := int64(len(userIDs))
	if expired > 0 {
		if _, err := tx.Exec(ctx,
			`UPDATE user_entitlements e SET cloud_sync_enabled = EXISTS (
				SELECT 1 FROM user_subscriptions s
				WHERE s.user_id = e.user_id AND s.status = 'active' AND s.active_until > $1)
			 WHERE e.user_id = ANY($2)`, now, userIDs); err != nil {
			return 0, fmt.Errorf("recompute cloud sync: %w", err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("commit refresh: %w", err)
	}
	return expired, nil
}

func scanSubscriptions(rows pgx.Rows) ([]model.UserSubscription, error) {
	var subs []model.UserSubscription
	for rows.Next() {
		var s model.UserSubscription
		if err := rows.Scan(&s.ID, &s.UserID, &s.PlanCode, &s.Status, &s.CurrentPeriodStart,
			&s.CurrentPeriodEnd, &s.ActiveUntil, &s.Provider, &s.ExternalOrderID,
			&s.CreatedAt, &s.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan subscription: %w", err)
		}
		subs = append(subs, s)
	}
	return subs, rows.Err()
}

// ── CreditLedgerRepo ─────────────────────────────────────────

// CreditLedgerRepo owns the append-only AI credit ledger. The ledger is the
// source of truth: grant rows carry a mutable remaining_amount (a consumable
// "lot") and an optional expiry; consumption draws lots down oldest-expiry-first.
// Every per-user mutation takes a transaction-scoped advisory lock so concurrent
// grants and consumes for the same user serialize, which keeps the balance
// correct and prevents negative balances without weakening idempotency.
type CreditLedgerRepo struct {
	pool *pgxpool.Pool
}

// GrantParams describes a credit grant.
type GrantParams struct {
	UserID         uuid.UUID
	Source         model.CreditSource
	Amount         int64 // must be > 0
	IdempotencyKey string
	ExpiresAt      *time.Time // nil = never expires
	Metadata       map[string]any
}

// GrantResult reports the outcome of a grant.
type GrantResult struct {
	Entry        model.AICreditLedgerEntry
	BalanceAfter int64
	Granted      bool // false when the idempotency key was already used (replay)
}

// Grant credits to a user as a new lot. It is idempotent on IdempotencyKey: a
// replay returns the original entry without granting again.
func (r *CreditLedgerRepo) Grant(ctx context.Context, p GrantParams) (GrantResult, error) {
	if p.Amount <= 0 {
		return GrantResult{}, fmt.Errorf("grant amount must be positive")
	}
	metadata, err := encodeAuditMetadata(p.Metadata)
	if err != nil {
		return GrantResult{}, err
	}

	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return GrantResult{}, fmt.Errorf("begin grant: %w", err)
	}
	defer tx.Rollback(ctx)

	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, userAdvisoryKey(p.UserID)); err != nil {
		return GrantResult{}, fmt.Errorf("lock user credits: %w", err)
	}

	if existing, err := findLedgerByIdempotencyKey(ctx, tx, p.IdempotencyKey); err != nil {
		return GrantResult{}, err
	} else if existing != nil {
		if err := tx.Commit(ctx); err != nil {
			return GrantResult{}, fmt.Errorf("commit grant: %w", err)
		}
		return GrantResult{Entry: *existing, BalanceAfter: existing.BalanceAfter, Granted: false}, nil
	}

	current, err := liveBalanceTx(ctx, tx, p.UserID)
	if err != nil {
		return GrantResult{}, err
	}
	balanceAfter := current + p.Amount

	entry := model.AICreditLedgerEntry{
		UserID:          p.UserID,
		Source:          p.Source,
		Amount:          p.Amount,
		BalanceAfter:    balanceAfter,
		RemainingAmount: &p.Amount,
		ExpiresAt:       p.ExpiresAt,
	}
	const q = `
		INSERT INTO ai_credit_ledger (user_id, source, amount, balance_after, remaining_amount,
		                              idempotency_key, expires_at, metadata)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		RETURNING id, created_at`
	if err := tx.QueryRow(ctx, q,
		p.UserID, string(p.Source), p.Amount, balanceAfter, p.Amount,
		nullableKey(p.IdempotencyKey), p.ExpiresAt, metadata,
	).Scan(&entry.ID, &entry.CreatedAt); err != nil {
		return GrantResult{}, fmt.Errorf("insert grant: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return GrantResult{}, fmt.Errorf("commit grant: %w", err)
	}
	return GrantResult{Entry: entry, BalanceAfter: balanceAfter, Granted: true}, nil
}

// ConsumeParams describes a credit consumption.
type ConsumeParams struct {
	UserID         uuid.UUID
	Cost           int64 // must be > 0
	IdempotencyKey string
	Source         model.CreditSource // consume or adjustment
	Metadata       map[string]any
}

// ConsumeResult reports the outcome of a consumption.
type ConsumeResult struct {
	Entry        model.AICreditLedgerEntry
	BalanceAfter int64
	Charged      bool // false when the idempotency key was already used (replay)
}

// Consume deducts cost from the user's live credits, drawing lots down
// oldest-expiry-first. It is idempotent on IdempotencyKey (a replay returns the
// original entry without charging again) and never lets the balance go negative
// (insufficient funds returns ErrInsufficientCredits and changes nothing).
func (r *CreditLedgerRepo) Consume(ctx context.Context, p ConsumeParams) (ConsumeResult, error) {
	if p.Cost <= 0 {
		return ConsumeResult{}, fmt.Errorf("consume cost must be positive")
	}
	if p.Source == "" {
		p.Source = model.CreditSourceConsume
	}
	metadata, err := encodeAuditMetadata(p.Metadata)
	if err != nil {
		return ConsumeResult{}, err
	}

	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return ConsumeResult{}, fmt.Errorf("begin consume: %w", err)
	}
	defer tx.Rollback(ctx)

	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, userAdvisoryKey(p.UserID)); err != nil {
		return ConsumeResult{}, fmt.Errorf("lock user credits: %w", err)
	}

	if existing, err := findLedgerByIdempotencyKey(ctx, tx, p.IdempotencyKey); err != nil {
		return ConsumeResult{}, err
	} else if existing != nil {
		if err := tx.Commit(ctx); err != nil {
			return ConsumeResult{}, fmt.Errorf("commit consume: %w", err)
		}
		return ConsumeResult{Entry: *existing, BalanceAfter: existing.BalanceAfter, Charged: false}, nil
	}

	// Live lots, oldest-expiry-first (never-expiring last). The advisory lock
	// above already serializes per-user mutations, so no row lock is needed.
	lotRows, err := tx.Query(ctx, `
		SELECT id, remaining_amount FROM ai_credit_ledger
		WHERE user_id = $1 AND remaining_amount > 0 AND (expires_at IS NULL OR expires_at > now())
		ORDER BY expires_at ASC NULLS LAST, created_at ASC, id ASC`, p.UserID)
	if err != nil {
		return ConsumeResult{}, fmt.Errorf("select lots: %w", err)
	}
	type lot struct {
		id        uuid.UUID
		remaining int64
	}
	var lots []lot
	var total int64
	for lotRows.Next() {
		var l lot
		if err := lotRows.Scan(&l.id, &l.remaining); err != nil {
			lotRows.Close()
			return ConsumeResult{}, fmt.Errorf("scan lot: %w", err)
		}
		lots = append(lots, l)
		total += l.remaining
	}
	lotRows.Close()
	if err := lotRows.Err(); err != nil {
		return ConsumeResult{}, fmt.Errorf("iterate lots: %w", err)
	}

	if total < p.Cost {
		// Nothing has changed; rollback leaves the balance untouched.
		return ConsumeResult{}, ErrInsufficientCredits
	}

	left := p.Cost
	for _, l := range lots {
		if left == 0 {
			break
		}
		take := l.remaining
		if take > left {
			take = left
		}
		if _, err := tx.Exec(ctx,
			`UPDATE ai_credit_ledger SET remaining_amount = remaining_amount - $2 WHERE id = $1`,
			l.id, take); err != nil {
			return ConsumeResult{}, fmt.Errorf("draw down lot: %w", err)
		}
		left -= take
	}

	balanceAfter := total - p.Cost
	entry := model.AICreditLedgerEntry{
		UserID:       p.UserID,
		Source:       p.Source,
		Amount:       -p.Cost,
		BalanceAfter: balanceAfter,
	}
	const q = `
		INSERT INTO ai_credit_ledger (user_id, source, amount, balance_after, remaining_amount,
		                              idempotency_key, expires_at, metadata)
		VALUES ($1, $2, $3, $4, NULL, $5, NULL, $6)
		RETURNING id, created_at`
	if err := tx.QueryRow(ctx, q,
		p.UserID, string(p.Source), -p.Cost, balanceAfter, nullableKey(p.IdempotencyKey), metadata,
	).Scan(&entry.ID, &entry.CreatedAt); err != nil {
		return ConsumeResult{}, fmt.Errorf("insert consume: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return ConsumeResult{}, fmt.Errorf("commit consume: %w", err)
	}
	return ConsumeResult{Entry: entry, BalanceAfter: balanceAfter, Charged: true}, nil
}

// Balance returns the user's live credit balance (sum of non-expired lots).
func (r *CreditLedgerRepo) Balance(ctx context.Context, userID uuid.UUID) (int64, error) {
	const q = `
		SELECT COALESCE(SUM(remaining_amount), 0) FROM ai_credit_ledger
		WHERE user_id = $1 AND remaining_amount > 0 AND (expires_at IS NULL OR expires_at > now())`
	var balance int64
	if err := r.pool.QueryRow(ctx, q, userID).Scan(&balance); err != nil {
		return 0, fmt.Errorf("get credit balance: %w", err)
	}
	return balance, nil
}

// ListByUser returns recent ledger entries for a user, newest first.
func (r *CreditLedgerRepo) ListByUser(ctx context.Context, userID uuid.UUID, limit int32) ([]model.AICreditLedgerEntry, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	const q = `
		SELECT id, user_id, source, amount, balance_after, remaining_amount,
		       idempotency_key, expires_at, metadata, created_at
		FROM ai_credit_ledger WHERE user_id = $1
		ORDER BY created_at DESC, id DESC LIMIT $2`
	rows, err := r.pool.Query(ctx, q, userID, limit)
	if err != nil {
		return nil, fmt.Errorf("list credit ledger: %w", err)
	}
	defer rows.Close()
	return scanLedgerEntries(rows)
}

// ExpireDue zeroes any live lots whose expiry has passed and records an 'expire'
// ledger row for each, so the ledger reflects the expiry. Balance/Consume
// already ignore expired lots lazily, so this is audit hygiene; it processes at
// most limit lots per call. It returns the number of lots expired.
func (r *CreditLedgerRepo) ExpireDue(ctx context.Context, now time.Time, limit int32) (int64, error) {
	if limit <= 0 || limit > 1000 {
		limit = 500
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("begin expire: %w", err)
	}
	defer tx.Rollback(ctx)

	rows, err := tx.Query(ctx, `
		SELECT id, user_id, remaining_amount FROM ai_credit_ledger
		WHERE remaining_amount > 0 AND expires_at IS NOT NULL AND expires_at <= $1
		ORDER BY expires_at ASC LIMIT $2
		FOR UPDATE`, now, limit)
	if err != nil {
		return 0, fmt.Errorf("select due lots: %w", err)
	}
	type dueLot struct {
		id     uuid.UUID
		userID uuid.UUID
		amount int64
	}
	var due []dueLot
	for rows.Next() {
		var d dueLot
		if err := rows.Scan(&d.id, &d.userID, &d.amount); err != nil {
			rows.Close()
			return 0, fmt.Errorf("scan due lot: %w", err)
		}
		due = append(due, d)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, fmt.Errorf("iterate due lots: %w", err)
	}

	for _, d := range due {
		if _, err := tx.Exec(ctx,
			`UPDATE ai_credit_ledger SET remaining_amount = 0 WHERE id = $1`, d.id); err != nil {
			return 0, fmt.Errorf("zero expired lot: %w", err)
		}
		balanceAfter, err := liveBalanceTx(ctx, tx, d.userID)
		if err != nil {
			return 0, err
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO ai_credit_ledger (user_id, source, amount, balance_after, remaining_amount,
			                              idempotency_key, expires_at, metadata)
			VALUES ($1, 'expire', $2, $3, NULL, $4, NULL, '{}'::jsonb)`,
			d.userID, -d.amount, balanceAfter, fmt.Sprintf("expire:%s", d.id)); err != nil {
			return 0, fmt.Errorf("insert expire row: %w", err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("commit expire: %w", err)
	}
	return int64(len(due)), nil
}

// liveBalanceTx computes the user's live balance within an open transaction.
func liveBalanceTx(ctx context.Context, tx pgx.Tx, userID uuid.UUID) (int64, error) {
	const q = `
		SELECT COALESCE(SUM(remaining_amount), 0) FROM ai_credit_ledger
		WHERE user_id = $1 AND remaining_amount > 0 AND (expires_at IS NULL OR expires_at > now())`
	var balance int64
	if err := tx.QueryRow(ctx, q, userID).Scan(&balance); err != nil {
		return 0, fmt.Errorf("compute live balance: %w", err)
	}
	return balance, nil
}

// findLedgerByIdempotencyKey returns the existing ledger entry for key, or nil.
// An empty key never matches (so keyless rows are not deduplicated).
func findLedgerByIdempotencyKey(ctx context.Context, tx pgx.Tx, key string) (*model.AICreditLedgerEntry, error) {
	if key == "" {
		return nil, nil
	}
	const q = `
		SELECT id, user_id, source, amount, balance_after, remaining_amount,
		       idempotency_key, expires_at, metadata, created_at
		FROM ai_credit_ledger WHERE idempotency_key = $1`
	rows, err := tx.Query(ctx, q, key)
	if err != nil {
		return nil, fmt.Errorf("lookup idempotency key: %w", err)
	}
	defer rows.Close()
	entries, err := scanLedgerEntries(rows)
	if err != nil {
		return nil, err
	}
	if len(entries) == 0 {
		return nil, nil
	}
	return &entries[0], nil
}

func nullableKey(key string) any {
	if key == "" {
		return nil
	}
	return key
}

func scanLedgerEntries(rows pgx.Rows) ([]model.AICreditLedgerEntry, error) {
	var entries []model.AICreditLedgerEntry
	for rows.Next() {
		var e model.AICreditLedgerEntry
		var metadata []byte
		if err := rows.Scan(&e.ID, &e.UserID, &e.Source, &e.Amount, &e.BalanceAfter,
			&e.RemainingAmount, &e.IdempotencyKey, &e.ExpiresAt, &metadata, &e.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan ledger entry: %w", err)
		}
		if len(metadata) > 0 {
			if err := json.Unmarshal(metadata, &e.Metadata); err != nil {
				return nil, fmt.Errorf("decode ledger metadata: %w", err)
			}
		}
		entries = append(entries, e)
	}
	return entries, rows.Err()
}

// ── PaymentOrderRepo ─────────────────────────────────────────

// PaymentOrderRepo records payment orders. Orders carrying an external_order_id
// are deduplicated on (provider, external_order_id); orders without one are
// always inserted as new rows.
type PaymentOrderRepo struct {
	pool *pgxpool.Pool
}

// Upsert records a payment order. raw_metadata must already be sanitized by the
// service before it reaches here.
func (r *PaymentOrderRepo) Upsert(ctx context.Context, o *model.PaymentOrder) error {
	rawMetadata, err := encodeAuditMetadata(o.RawMetadata)
	if err != nil {
		return err
	}
	if o.ExternalOrderID == "" {
		const q = `
			INSERT INTO payment_orders (user_id, provider, external_order_id, plan_code,
			                            currency, amount, status, raw_metadata,
			                            paid_at, completed_at, failed_at, failed_reason,
			                            retry_count, last_retry_at)
			VALUES ($1, $2, '', $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
			RETURNING id, created_at, updated_at`
		return r.pool.QueryRow(ctx, q,
			o.UserID, string(o.Provider), o.PlanCode, o.Currency, o.Amount, string(o.Status), rawMetadata,
			o.PaidAt, o.CompletedAt, o.FailedAt, o.FailedReason, o.RetryCount, o.LastRetryAt,
		).Scan(&o.ID, &o.CreatedAt, &o.UpdatedAt)
	}
	const q = `
		INSERT INTO payment_orders (user_id, provider, external_order_id, plan_code,
		                            currency, amount, status, raw_metadata,
		                            paid_at, completed_at, failed_at, failed_reason,
		                            retry_count, last_retry_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)
		ON CONFLICT (provider, external_order_id) WHERE external_order_id <> '' DO UPDATE SET
			user_id = EXCLUDED.user_id,
			plan_code = EXCLUDED.plan_code,
			currency = EXCLUDED.currency,
			amount = EXCLUDED.amount,
			status = CASE
				WHEN payment_orders.status = 'completed' THEN payment_orders.status
				ELSE EXCLUDED.status
			END,
			raw_metadata = EXCLUDED.raw_metadata,
			paid_at = COALESCE(payment_orders.paid_at, EXCLUDED.paid_at),
			completed_at = COALESCE(payment_orders.completed_at, EXCLUDED.completed_at),
			failed_at = CASE
				WHEN payment_orders.status = 'completed' THEN payment_orders.failed_at
				ELSE EXCLUDED.failed_at
			END,
			failed_reason = CASE
				WHEN payment_orders.status = 'completed' THEN payment_orders.failed_reason
				ELSE EXCLUDED.failed_reason
			END,
			retry_count = payment_orders.retry_count,
			last_retry_at = payment_orders.last_retry_at
		RETURNING id, created_at, updated_at`
	return r.pool.QueryRow(ctx, q,
		o.UserID, string(o.Provider), o.ExternalOrderID, o.PlanCode,
		o.Currency, o.Amount, string(o.Status), rawMetadata,
		o.PaidAt, o.CompletedAt, o.FailedAt, o.FailedReason, o.RetryCount, o.LastRetryAt,
	).Scan(&o.ID, &o.CreatedAt, &o.UpdatedAt)
}

// Get returns a recorded order by provider + external order id, or nil.
func (r *PaymentOrderRepo) Get(ctx context.Context, provider model.PaymentProvider, externalOrderID string) (*model.PaymentOrder, error) {
	return r.GetPaymentOrderByExternalID(ctx, provider, externalOrderID)
}

// GetPaymentOrderByExternalID returns a recorded order by provider + external
// order id, or nil when it is not found.
func (r *PaymentOrderRepo) GetPaymentOrderByExternalID(ctx context.Context, provider model.PaymentProvider, externalOrderID string) (*model.PaymentOrder, error) {
	const q = `
		SELECT id, user_id, provider, external_order_id, plan_code, currency, amount,
		       status, raw_metadata, paid_at, completed_at, failed_at, failed_reason,
		       retry_count, last_retry_at, created_at, updated_at
		FROM payment_orders WHERE provider = $1 AND external_order_id = $2`
	o := &model.PaymentOrder{}
	var rawMetadata []byte
	err := r.pool.QueryRow(ctx, q, string(provider), externalOrderID).Scan(
		&o.ID, &o.UserID, &o.Provider, &o.ExternalOrderID, &o.PlanCode, &o.Currency,
		&o.Amount, &o.Status, &rawMetadata, &o.PaidAt, &o.CompletedAt, &o.FailedAt,
		&o.FailedReason, &o.RetryCount, &o.LastRetryAt, &o.CreatedAt, &o.UpdatedAt,
	)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get payment order: %w", err)
	}
	if len(rawMetadata) > 0 {
		if err := json.Unmarshal(rawMetadata, &o.RawMetadata); err != nil {
			return nil, fmt.Errorf("decode order metadata: %w", err)
		}
	}
	return o, nil
}

// GetByID returns an order by its internal ID, or nil.
func (r *PaymentOrderRepo) GetByID(ctx context.Context, id uuid.UUID) (*model.PaymentOrder, error) {
	const q = `
		SELECT id, user_id, provider, external_order_id, plan_code, currency, amount,
		       status, raw_metadata, paid_at, completed_at, failed_at, failed_reason,
		       retry_count, last_retry_at, created_at, updated_at
		FROM payment_orders WHERE id = $1`
	return r.scanOne(ctx, q, id)
}

// List returns payment orders for admin troubleshooting.
func (r *PaymentOrderRepo) List(ctx context.Context, filter model.PaymentOrderListFilter) ([]model.PaymentOrder, error) {
	return r.ListPaymentOrders(ctx, filter)
}

// ListPaymentOrders returns payment orders for admin troubleshooting.
func (r *PaymentOrderRepo) ListPaymentOrders(ctx context.Context, filter model.PaymentOrderListFilter) ([]model.PaymentOrder, error) {
	if filter.Limit <= 0 || filter.Limit > 200 {
		filter.Limit = 50
	}
	if filter.Offset < 0 {
		filter.Offset = 0
	}
	where := "WHERE true"
	args := []any{}
	addArg := func(v any) string {
		args = append(args, v)
		return fmt.Sprintf("$%d", len(args))
	}
	if filter.Provider != "" {
		where += " AND provider = " + addArg(string(filter.Provider))
	}
	if filter.Status != "" {
		where += " AND status = " + addArg(string(filter.Status))
	}
	if filter.ExternalOrderID != "" {
		where += " AND external_order_id = " + addArg(filter.ExternalOrderID)
	}
	args = append(args, filter.Limit, filter.Offset)
	q := fmt.Sprintf(`
		SELECT id, user_id, provider, external_order_id, plan_code, currency, amount,
		       status, raw_metadata, paid_at, completed_at, failed_at, failed_reason,
		       retry_count, last_retry_at, created_at, updated_at
		FROM payment_orders %s
		ORDER BY created_at DESC, id DESC
		LIMIT $%d OFFSET $%d`, where, len(args)-1, len(args))
	rows, err := r.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("list payment orders: %w", err)
	}
	defer rows.Close()
	return scanPaymentOrders(rows)
}

// MarkPaid records a verified successful provider payment.
func (r *PaymentOrderRepo) MarkPaid(ctx context.Context, id uuid.UUID, paidAt time.Time) error {
	const q = `
		UPDATE payment_orders
		SET status = 'paid', paid_at = COALESCE(paid_at, $2), completed_at = NULL,
		    failed_at = NULL, failed_reason = ''
		WHERE id = $1 AND status <> 'completed'`
	_, err := r.pool.Exec(ctx, q, id, paidAt)
	if err != nil {
		return fmt.Errorf("mark order paid: %w", err)
	}
	return nil
}

// MarkCompleted records successful fulfillment. It is safe to call repeatedly.
func (r *PaymentOrderRepo) MarkCompleted(ctx context.Context, id uuid.UUID, completedAt time.Time) error {
	const q = `
		UPDATE payment_orders
		SET status = 'completed', completed_at = COALESCE(completed_at, $2),
		    failed_at = NULL, failed_reason = ''
		WHERE id = $1`
	_, err := r.pool.Exec(ctx, q, id, completedAt)
	if err != nil {
		return fmt.Errorf("mark order completed: %w", err)
	}
	return nil
}

// MarkFailed records a retryable fulfillment failure after payment was verified.
func (r *PaymentOrderRepo) MarkFailed(ctx context.Context, id uuid.UUID, failedAt time.Time, reason string) error {
	const q = `
		UPDATE payment_orders
		SET status = 'failed', failed_at = $2, failed_reason = $3
		WHERE id = $1 AND status <> 'completed'`
	_, err := r.pool.Exec(ctx, q, id, failedAt, trimFailureReason(reason))
	if err != nil {
		return fmt.Errorf("mark order failed: %w", err)
	}
	return nil
}

// MarkRetryAttempt records an operator-triggered retry attempt. Completed
// orders are never touched, so accidental retries remain no-ops.
func (r *PaymentOrderRepo) MarkRetryAttempt(ctx context.Context, id uuid.UUID, retriedAt time.Time) error {
	const q = `
		UPDATE payment_orders
		SET retry_count = retry_count + 1, last_retry_at = $2
		WHERE id = $1 AND status IN ('paid', 'failed')`
	_, err := r.pool.Exec(ctx, q, id, retriedAt)
	if err != nil {
		return fmt.Errorf("mark order retry attempt: %w", err)
	}
	return nil
}

func (r *PaymentOrderRepo) scanOne(ctx context.Context, query string, args ...any) (*model.PaymentOrder, error) {
	rows, err := r.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("query payment order: %w", err)
	}
	defer rows.Close()
	orders, err := scanPaymentOrders(rows)
	if err != nil {
		return nil, err
	}
	if len(orders) == 0 {
		return nil, nil
	}
	return &orders[0], nil
}

func scanPaymentOrders(rows pgx.Rows) ([]model.PaymentOrder, error) {
	var orders []model.PaymentOrder
	for rows.Next() {
		var o model.PaymentOrder
		var rawMetadata []byte
		if err := rows.Scan(
			&o.ID, &o.UserID, &o.Provider, &o.ExternalOrderID, &o.PlanCode, &o.Currency,
			&o.Amount, &o.Status, &rawMetadata, &o.PaidAt, &o.CompletedAt, &o.FailedAt,
			&o.FailedReason, &o.RetryCount, &o.LastRetryAt, &o.CreatedAt, &o.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan payment order: %w", err)
		}
		if len(rawMetadata) > 0 {
			if err := json.Unmarshal(rawMetadata, &o.RawMetadata); err != nil {
				return nil, fmt.Errorf("decode order metadata: %w", err)
			}
		}
		orders = append(orders, o)
	}
	return orders, rows.Err()
}

func trimFailureReason(reason string) string {
	if len(reason) <= 500 {
		return reason
	}
	return reason[:500]
}
