package model

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

// ── Billing: plans, entitlements, subscriptions, AI credits ──
//
// Billing entitlements are intentionally separate from User.Tier and the quota
// system. User.Tier (free/pro/team/enterprise) drives storage quota, while the
// billing tier below (free/pro/max) drives features, write-back, cloud sync,
// and AI credits. Money is always expressed in BIGINT minor units (USD cents /
// CNY fen) to avoid floating-point rounding.

// PlanKind categorizes how a plan is fulfilled.
type PlanKind string

const (
	// PlanKindLifetime is a one-time buyout entitlement (Free/Pro/Max).
	PlanKindLifetime PlanKind = "lifetime"
	// PlanKindSubscription is a recurring cloud subscription (Cloud Lite/Cloud).
	PlanKindSubscription PlanKind = "subscription"
	// PlanKindBundle combines lifetime + subscription components in one purchase.
	PlanKindBundle PlanKind = "bundle"
)

// EntitlementTier is the billing feature tier (distinct from User.Tier).
type EntitlementTier string

const (
	EntitlementTierFree EntitlementTier = "free"
	EntitlementTierPro  EntitlementTier = "pro"
	EntitlementTierMax  EntitlementTier = "max"
)

// Rank returns an ordinal for the tier so a lifetime grant can move a user's
// tier up but never down.
func (t EntitlementTier) Rank() int {
	switch t {
	case EntitlementTierMax:
		return 2
	case EntitlementTierPro:
		return 1
	default:
		return 0
	}
}

// BillingRegion selects the pricing region.
type BillingRegion string

const (
	RegionInternational BillingRegion = "international"
	RegionChina         BillingRegion = "china"
)

// Currency is the billing currency.
type Currency string

const (
	CurrencyUSD Currency = "USD"
	CurrencyCNY Currency = "CNY"
)

// BillingPeriod is the payment cadence of a price.
type BillingPeriod string

const (
	BillingPeriodNone    BillingPeriod = "none"
	BillingPeriodMonthly BillingPeriod = "monthly"
	BillingPeriodYearly  BillingPeriod = "yearly"
)

// CreditSource labels every AI credit ledger row.
type CreditSource string

const (
	CreditSourceFreeGrant        CreditSource = "free_grant"
	CreditSourceProGrant         CreditSource = "pro_grant"
	CreditSourceMaxGrant         CreditSource = "max_grant"
	CreditSourceCloudPeriodGrant CreditSource = "cloud_period_grant"
	CreditSourceManualGrant      CreditSource = "manual_grant"
	CreditSourceConsume          CreditSource = "consume"
	CreditSourceAdjustment       CreditSource = "adjustment"
	CreditSourceExpire           CreditSource = "expire"
)

// PaymentProvider records where a purchase originated. Business rules never
// branch on the provider; it is a source label only.
type PaymentProvider string

const (
	PaymentProviderGumroad PaymentProvider = "gumroad"
	PaymentProviderAfdian  PaymentProvider = "afdian"
	PaymentProviderManual  PaymentProvider = "manual"
)

// SubscriptionStatus is the lifecycle state of a cloud subscription.
type SubscriptionStatus string

const (
	SubscriptionStatusActive   SubscriptionStatus = "active"
	SubscriptionStatusExpired  SubscriptionStatus = "expired"
	SubscriptionStatusCanceled SubscriptionStatus = "canceled"
)

// PaymentOrderStatus is the lifecycle state of a recorded payment order.
type PaymentOrderStatus string

const (
	PaymentOrderStatusPending   PaymentOrderStatus = "pending"
	PaymentOrderStatusPaid      PaymentOrderStatus = "paid"
	PaymentOrderStatusCompleted PaymentOrderStatus = "completed"
	PaymentOrderStatusFailed    PaymentOrderStatus = "failed"
	PaymentOrderStatusCanceled  PaymentOrderStatus = "canceled"
	PaymentOrderStatusExpired   PaymentOrderStatus = "expired"
	PaymentOrderStatusRefunded  PaymentOrderStatus = "refunded"
)

// Catalog plan codes seeded by migration 009.
const (
	PlanCodeFree       = "free"
	PlanCodePro        = "pro"
	PlanCodeMax        = "max"
	PlanCodeCloudLite  = "cloud_lite"
	PlanCodeCloud      = "cloud"
	PlanCodeMaxCloud1Y = "max_cloud_1y"
	PlanCodeMaxCloud2Y = "max_cloud_2y"
)

// PlanComponent describes one entitlement granted by a bundle plan.
type PlanComponent struct {
	PlanCode string `json:"plan_code"`
	// CloudMonths is the subscription length in months for a subscription
	// component (e.g. 12 or 24). Ignored for lifetime components.
	CloudMonths int `json:"cloud_months,omitempty"`
}

// PlanMetadata is the typed view of plans.metadata. It encodes a plan's effect
// (what tier/credits/flags it grants) so the catalog stays data-driven and
// provider-friendly: new plans can be added without code changes.
type PlanMetadata struct {
	// Tier is the lifetime tier this plan grants (lifetime plans).
	Tier EntitlementTier `json:"tier,omitempty"`
	// OneTimeCredits are non-expiring credits granted once (lifetime plans).
	OneTimeCredits int64 `json:"one_time_credits,omitempty"`
	// PeriodCredits are granted each billing period (subscription plans).
	PeriodCredits int64 `json:"period_credits,omitempty"`
	// CloudSync reports whether the plan enables cloud sync (subscription plans).
	CloudSync bool `json:"cloud_sync,omitempty"`
	// Writeback reports whether the plan enables the write-back feature.
	Writeback bool `json:"writeback,omitempty"`
	// Components lists the sub-plans a bundle splits into (bundle plans).
	Components []PlanComponent `json:"components,omitempty"`
}

// ParsePlanMetadata decodes a plans.metadata JSONB payload. An empty payload
// decodes to a zero PlanMetadata rather than an error.
func ParsePlanMetadata(raw []byte) (PlanMetadata, error) {
	var m PlanMetadata
	if len(raw) == 0 {
		return m, nil
	}
	if err := json.Unmarshal(raw, &m); err != nil {
		return PlanMetadata{}, err
	}
	return m, nil
}

// Plan is a catalog entry.
type Plan struct {
	ID        uuid.UUID    `json:"id"         db:"id"`
	Code      string       `json:"code"       db:"code"`
	Name      string       `json:"name"       db:"name"`
	Kind      PlanKind     `json:"kind"       db:"kind"`
	Enabled   bool         `json:"enabled"    db:"enabled"`
	Metadata  PlanMetadata `json:"metadata"   db:"metadata"`
	CreatedAt time.Time    `json:"created_at" db:"created_at"`
	UpdatedAt time.Time    `json:"updated_at" db:"updated_at"`
}

// PlanPrice is one region/currency/period price for a plan. Amount and
// EarlyBirdAmount are in minor units (cents / fen).
type PlanPrice struct {
	ID              uuid.UUID     `json:"id"                          db:"id"`
	PlanCode        string        `json:"plan_code"                   db:"plan_code"`
	Region          BillingRegion `json:"region"                      db:"region"`
	Currency        Currency      `json:"currency"                    db:"currency"`
	Amount          int64         `json:"amount"                      db:"amount"`
	BillingPeriod   BillingPeriod `json:"billing_period"              db:"billing_period"`
	EarlyBirdAmount *int64        `json:"early_bird_amount,omitempty" db:"early_bird_amount"`
	CreatedAt       time.Time     `json:"created_at"                  db:"created_at"`
	UpdatedAt       time.Time     `json:"updated_at"                  db:"updated_at"`
}

// UserEntitlement is the user's current effective entitlement (one row per
// user). Cloud expiry toggles CloudSyncEnabled but never lowers Tier or clears
// WritebackEnabled, so a Max lifetime entitlement survives Cloud lapsing.
type UserEntitlement struct {
	ID               uuid.UUID       `json:"id"                 db:"id"`
	UserID           uuid.UUID       `json:"user_id"            db:"user_id"`
	Tier             EntitlementTier `json:"tier"               db:"tier"`
	CloudSyncEnabled bool            `json:"cloud_sync_enabled" db:"cloud_sync_enabled"`
	WritebackEnabled bool            `json:"writeback_enabled"  db:"writeback_enabled"`
	SourcePlanCode   string          `json:"source_plan_code"   db:"source_plan_code"`
	StartsAt         time.Time       `json:"starts_at"          db:"starts_at"`
	EndsAt           *time.Time      `json:"ends_at,omitempty"  db:"ends_at"`
	CreatedAt        time.Time       `json:"created_at"         db:"created_at"`
	UpdatedAt        time.Time       `json:"updated_at"         db:"updated_at"`
}

// DefaultEntitlement is the implicit free-tier entitlement for a user without a
// stored row yet.
func DefaultEntitlement(userID uuid.UUID) UserEntitlement {
	return UserEntitlement{
		UserID:         userID,
		Tier:           EntitlementTierFree,
		SourcePlanCode: PlanCodeFree,
	}
}

// UserSubscription is a cloud subscription. ActiveUntil is the hard end; the
// CurrentPeriod window is the monthly credit-grant cycle (credits are granted
// monthly regardless of whether the subscription was paid monthly or yearly).
type UserSubscription struct {
	ID                 uuid.UUID          `json:"id"                   db:"id"`
	UserID             uuid.UUID          `json:"user_id"              db:"user_id"`
	PlanCode           string             `json:"plan_code"            db:"plan_code"`
	Status             SubscriptionStatus `json:"status"               db:"status"`
	CurrentPeriodStart time.Time          `json:"current_period_start" db:"current_period_start"`
	CurrentPeriodEnd   time.Time          `json:"current_period_end"   db:"current_period_end"`
	ActiveUntil        time.Time          `json:"active_until"         db:"active_until"`
	Provider           PaymentProvider    `json:"provider"             db:"provider"`
	ExternalOrderID    string             `json:"external_order_id"    db:"external_order_id"`
	CreatedAt          time.Time          `json:"created_at"           db:"created_at"`
	UpdatedAt          time.Time          `json:"updated_at"           db:"updated_at"`
}

// AICreditLedgerEntry is one row in the append-only AI credit ledger. Grant
// rows carry RemainingAmount (the live, consumable balance of that lot) and an
// optional ExpiresAt; consume/expire rows leave RemainingAmount nil.
type AICreditLedgerEntry struct {
	ID              uuid.UUID      `json:"id"                         db:"id"`
	UserID          uuid.UUID      `json:"user_id"                    db:"user_id"`
	Source          CreditSource   `json:"source"                     db:"source"`
	Amount          int64          `json:"amount"                     db:"amount"`
	BalanceAfter    int64          `json:"balance_after"              db:"balance_after"`
	RemainingAmount *int64         `json:"remaining_amount,omitempty" db:"remaining_amount"`
	IdempotencyKey  *string        `json:"-"                          db:"idempotency_key"`
	ExpiresAt       *time.Time     `json:"expires_at,omitempty"       db:"expires_at"`
	Metadata        map[string]any `json:"metadata,omitempty"         db:"metadata"`
	CreatedAt       time.Time      `json:"created_at"                 db:"created_at"`
}

// PaymentOrder records a purchase for audit/idempotency. RawMetadata may hold a
// sanitized JSON snapshot but is tagged json:"-" so it is never serialized to
// API responses, and the service strips payment secrets before storing it.
type PaymentOrder struct {
	ID              uuid.UUID          `json:"id"                db:"id"`
	UserID          uuid.UUID          `json:"user_id"           db:"user_id"`
	Provider        PaymentProvider    `json:"provider"          db:"provider"`
	ExternalOrderID string             `json:"external_order_id" db:"external_order_id"`
	PlanCode        string             `json:"plan_code"         db:"plan_code"`
	Currency        string             `json:"currency"          db:"currency"`
	Amount          int64              `json:"amount"            db:"amount"`
	Status          PaymentOrderStatus `json:"status"            db:"status"`
	RawMetadata     map[string]any     `json:"-"                 db:"raw_metadata"`
	PaidAt          *time.Time         `json:"paid_at,omitempty" db:"paid_at"`
	CompletedAt     *time.Time         `json:"completed_at,omitempty" db:"completed_at"`
	FailedAt        *time.Time         `json:"failed_at,omitempty" db:"failed_at"`
	FailedReason    string             `json:"failed_reason,omitempty" db:"failed_reason"`
	CreatedAt       time.Time          `json:"created_at"        db:"created_at"`
	UpdatedAt       time.Time          `json:"updated_at"        db:"updated_at"`
}

// PaymentOrderListFilter narrows admin order listing.
type PaymentOrderListFilter struct {
	Provider        PaymentProvider
	Status          PaymentOrderStatus
	ExternalOrderID string
	Limit           int32
	Offset          int32
}
