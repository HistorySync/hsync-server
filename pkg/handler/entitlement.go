package handler

import (
	"encoding/json"
	"errors"
	"net/url"
	"strconv"
	"strings"

	"github.com/gofiber/fiber/v3"
	"github.com/google/uuid"

	"github.com/historysync/hsync-server/pkg/apierrors"
	"github.com/historysync/hsync-server/pkg/auth"
	"github.com/historysync/hsync-server/pkg/model"
	"github.com/historysync/hsync-server/pkg/service"
)

// ── Plan catalog (public) ────────────────────────────────────

// ListPlans returns the enabled plan catalog with pricing. It is public so the
// pricing page can render before sign-in. An optional ?region= filter
// (international/china) narrows prices to one region.
func (h *Handlers) ListPlans(c fiber.Ctx) error {
	if h.deps.Services == nil || h.deps.Services.Entitlement == nil {
		return c.JSON(fiber.Map{"plans": []service.PlanView{}})
	}
	region, err := parseRegion(c.Query("region"))
	if err != nil {
		return err
	}
	plans, err := h.deps.Services.Entitlement.GetAvailablePlans(c.Context(), region)
	if err != nil {
		return apierrors.NewInternal(err.Error())
	}
	return c.JSON(fiber.Map{"plans": plans})
}

// ── Account entitlement / credits (JWT) ──────────────────────

// GetMyEntitlements returns the authenticated user's effective entitlement and
// active subscriptions.
func (h *Handlers) GetMyEntitlements(c fiber.Ctx) error {
	if h.deps.Services == nil || h.deps.Services.Entitlement == nil {
		return apierrors.NewInternal("entitlement service is not configured")
	}
	view, err := h.deps.Services.Entitlement.GetUserEntitlements(c.Context(), auth.UserID(c))
	if err != nil {
		return apierrors.NewInternal(err.Error())
	}
	return c.JSON(view)
}

// GetMyCredits returns the authenticated user's live AI credit balance.
func (h *Handlers) GetMyCredits(c fiber.Ctx) error {
	if h.deps.Services == nil || h.deps.Services.Entitlement == nil {
		return apierrors.NewInternal("entitlement service is not configured")
	}
	balance, err := h.deps.Services.Entitlement.GetAICreditBalance(c.Context(), auth.UserID(c))
	if err != nil {
		return apierrors.NewInternal(err.Error())
	}
	return c.JSON(fiber.Map{"balance": balance})
}

// GetMyCreditLedger returns the authenticated user's recent credit ledger
// entries. Idempotency keys and payment metadata are never exposed.
func (h *Handlers) GetMyCreditLedger(c fiber.Ctx) error {
	if h.deps.Services == nil || h.deps.Services.Entitlement == nil {
		return apierrors.NewInternal("entitlement service is not configured")
	}
	limit := int32(50)
	if l, err := strconv.Atoi(c.Query("limit", "50")); err == nil && l > 0 && l <= 200 {
		limit = int32(l)
	}
	entries, err := h.deps.Services.Entitlement.ListAICreditLedger(c.Context(), auth.UserID(c), limit)
	if err != nil {
		return apierrors.NewInternal(err.Error())
	}
	return c.JSON(fiber.Map{"ledger": entries, "limit": limit})
}

// ── Admin entry points (replace real webhooks in CE) ─────────

type adminGrantPlanRequest struct {
	PlanCode        string `json:"plan_code"`
	Provider        string `json:"provider"`
	ExternalOrderID string `json:"external_order_id"`
	Region          string `json:"region"`
	BillingPeriod   string `json:"billing_period"`
}

// AdminGrantPlan grants a plan (or bundle) to a user. This is the internal
// fulfillment entry point CE exposes in place of real Gumroad/afdian webhooks.
func (h *Handlers) AdminGrantPlan(c fiber.Ctx) error {
	if h.deps.Services == nil || h.deps.Services.Entitlement == nil {
		return apierrors.NewInternal("entitlement service is not configured")
	}
	userID, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return apierrors.New(apierrors.CodeInvalidUserID, "invalid user id")
	}

	var req adminGrantPlanRequest
	if err := json.Unmarshal(c.Body(), &req); err != nil {
		return apierrors.New(apierrors.CodeInvalidJSON, "invalid request body")
	}
	if strings.TrimSpace(req.PlanCode) == "" {
		return apierrors.NewBadRequest("plan_code is required")
	}
	provider, err := parseProvider(req.Provider)
	if err != nil {
		return err
	}
	region, err := parseRegion(req.Region)
	if err != nil {
		return err
	}
	period, err := parseBillingPeriod(req.BillingPeriod)
	if err != nil {
		return err
	}

	outcome, err := h.deps.Services.Entitlement.GrantPlanToUser(c.Context(), userID, req.PlanCode, service.GrantOptions{
		Provider:        provider,
		ExternalOrderID: strings.TrimSpace(req.ExternalOrderID),
		Region:          region,
		BillingPeriod:   period,
	})
	if err != nil {
		return mapEntitlementError(err)
	}

	h.recordAudit(c, service.AuditEventInput{
		EventType:  model.AuditEventAdminPlanGrant,
		TargetType: "user",
		TargetID:   userID.String(),
		Metadata: map[string]any{
			"plan_code": req.PlanCode,
			"provider":  string(provider),
		},
	})
	return c.JSON(outcome)
}

type adminAdjustCreditsRequest struct {
	Amount         int64  `json:"amount"`
	Reason         string `json:"reason"`
	IdempotencyKey string `json:"idempotency_key"`
}

// AdminAdjustCredits applies a manual AI credit grant (positive amount) or
// deduction (negative amount) to a user.
func (h *Handlers) AdminAdjustCredits(c fiber.Ctx) error {
	if h.deps.Services == nil || h.deps.Services.Entitlement == nil {
		return apierrors.NewInternal("entitlement service is not configured")
	}
	userID, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return apierrors.New(apierrors.CodeInvalidUserID, "invalid user id")
	}

	var req adminAdjustCreditsRequest
	if err := json.Unmarshal(c.Body(), &req); err != nil {
		return apierrors.New(apierrors.CodeInvalidJSON, "invalid request body")
	}

	result, err := h.deps.Services.Entitlement.AdjustAICredits(c.Context(), service.AdjustCreditsInput{
		UserID:         userID,
		Amount:         req.Amount,
		Reason:         req.Reason,
		IdempotencyKey: strings.TrimSpace(req.IdempotencyKey),
	})
	if err != nil {
		return mapEntitlementError(err)
	}

	h.recordAudit(c, service.AuditEventInput{
		EventType:  model.AuditEventAdminCreditAdjust,
		TargetType: "user",
		TargetID:   userID.String(),
		Metadata: map[string]any{
			"amount": req.Amount,
		},
	})
	return c.JSON(result)
}

// AdminRefreshSubscriptions expires due subscriptions and recomputes cloud sync.
// It is the manual trigger for what a scheduler would run periodically.
func (h *Handlers) AdminRefreshSubscriptions(c fiber.Ctx) error {
	if h.deps.Services == nil || h.deps.Services.Entitlement == nil {
		return apierrors.NewInternal("entitlement service is not configured")
	}
	expired, err := h.deps.Services.Entitlement.RefreshExpiredSubscriptions(c.Context())
	if err != nil {
		return apierrors.NewInternal(err.Error())
	}
	return c.JSON(fiber.Map{"expired": expired})
}

// PaymentWebhook accepts verified-provider payment notifications and fulfills
// paid plans through the entitlement service.
func (h *Handlers) PaymentWebhook(c fiber.Ctx) error {
	if h.deps.Services == nil || h.deps.Services.PaymentWebhook == nil {
		return apierrors.New(apierrors.CodeBillingDisabled, "payment webhooks are not configured")
	}
	providerName := c.Params("provider")
	provider, err := parseProvider(providerName)
	if err != nil || provider == model.PaymentProviderManual || provider == "" {
		return apierrors.NewBadRequest("provider must be 'gumroad' or 'afdian'")
	}
	query, _ := url.ParseQuery(string(c.Request().URI().QueryString()))
	result, err := h.deps.Services.PaymentWebhook.Handle(c.Context(), service.PaymentWebhookInput{
		Provider:  provider,
		Query:     query,
		Body:      c.Body(),
		IP:        c.IP(),
		UserAgent: c.Get("User-Agent"),
	})
	if provider == model.PaymentProviderAfdian {
		if err != nil {
			return c.Status(fiber.StatusOK).JSON(fiber.Map{"ec": 400, "em": err.Error()})
		}
		return c.JSON(fiber.Map{"ec": 200, "data": result})
	}
	if err != nil {
		return mapPaymentWebhookError(err)
	}
	return c.JSON(fiber.Map{"status": "ok", "result": result})
}

// AdminListPaymentOrders returns recent payment orders for troubleshooting.
func (h *Handlers) AdminListPaymentOrders(c fiber.Ctx) error {
	if h.deps.Services == nil || h.deps.Services.PaymentWebhook == nil {
		return apierrors.NewInternal("payment webhook service is not configured")
	}
	limit := int32(50)
	if l, err := strconv.Atoi(c.Query("limit", "50")); err == nil && l > 0 && l <= 200 {
		limit = int32(l)
	}
	offset := int32(0)
	if o, err := strconv.Atoi(c.Query("offset", "0")); err == nil && o > 0 {
		offset = int32(o)
	}
	provider, err := parseProvider(c.Query("provider"))
	if err != nil {
		return err
	}
	status, err := parsePaymentOrderStatus(c.Query("status"))
	if err != nil {
		return err
	}
	orders, err := h.deps.Services.PaymentWebhook.ListOrders(c.Context(), model.PaymentOrderListFilter{
		Provider:        provider,
		Status:          status,
		ExternalOrderID: strings.TrimSpace(c.Query("external_order_id")),
		Limit:           limit,
		Offset:          offset,
	})
	if err != nil {
		return apierrors.NewInternal(err.Error())
	}
	return c.JSON(fiber.Map{"payment_orders": orders, "limit": limit, "offset": offset})
}

// AdminRetryPaymentOrder retries fulfillment for a paid or failed order.
func (h *Handlers) AdminRetryPaymentOrder(c fiber.Ctx) error {
	if h.deps.Services == nil || h.deps.Services.PaymentWebhook == nil {
		return apierrors.NewInternal("payment webhook service is not configured")
	}
	orderID, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return apierrors.NewBadRequest("invalid payment order id")
	}
	result, err := h.deps.Services.PaymentWebhook.RetryFulfillment(c.Context(), orderID)
	if err != nil {
		return mapPaymentWebhookError(err)
	}
	return c.JSON(result)
}

// ── Helpers ──────────────────────────────────────────────────

func mapEntitlementError(err error) error {
	switch {
	case errors.Is(err, service.ErrPlanNotFound):
		return apierrors.New(apierrors.CodePlanNotFound, err.Error())
	case errors.Is(err, service.ErrPlanDisabled):
		return apierrors.New(apierrors.CodePlanUnavailable, err.Error())
	case errors.Is(err, service.ErrInsufficientCredits):
		return apierrors.New(apierrors.CodeInsufficientCredits, err.Error())
	case errors.Is(err, service.ErrIdempotencyKeyRequired):
		return apierrors.New(apierrors.CodeIdempotencyKeyMissing, err.Error())
	case errors.Is(err, service.ErrInvalidCreditAmount):
		return apierrors.New(apierrors.CodeInvalidCreditAmount, err.Error())
	case errors.Is(err, service.ErrSubscriptionNotFound):
		return apierrors.New(apierrors.CodeSubscriptionNotFound, err.Error())
	default:
		return apierrors.NewInternal(err.Error())
	}
}

func mapPaymentWebhookError(err error) error {
	switch {
	case errors.Is(err, service.ErrPaymentWebhookRejected):
		return apierrors.NewBadRequest(err.Error())
	case errors.Is(err, service.ErrPaymentPlanMappingMissing):
		return apierrors.NewBadRequest(err.Error())
	case errors.Is(err, service.ErrPaymentOrderNotFound):
		return apierrors.New(apierrors.CodeNotFound, err.Error())
	case errors.Is(err, service.ErrPaymentOrderNotRetryable):
		return apierrors.New(apierrors.CodeConflict, err.Error())
	case errors.Is(err, service.ErrPlanNotFound), errors.Is(err, service.ErrPlanDisabled),
		errors.Is(err, service.ErrInsufficientCredits), errors.Is(err, service.ErrIdempotencyKeyRequired),
		errors.Is(err, service.ErrInvalidCreditAmount), errors.Is(err, service.ErrSubscriptionNotFound):
		return mapEntitlementError(err)
	default:
		return apierrors.NewInternal(err.Error())
	}
}

func parseRegion(raw string) (model.BillingRegion, error) {
	switch strings.TrimSpace(raw) {
	case "":
		return "", nil
	case string(model.RegionInternational):
		return model.RegionInternational, nil
	case string(model.RegionChina):
		return model.RegionChina, nil
	default:
		return "", apierrors.NewBadRequest("region must be 'international' or 'china'")
	}
}

func parseProvider(raw string) (model.PaymentProvider, error) {
	switch strings.TrimSpace(raw) {
	case "":
		return "", nil
	case string(model.PaymentProviderGumroad):
		return model.PaymentProviderGumroad, nil
	case string(model.PaymentProviderAfdian):
		return model.PaymentProviderAfdian, nil
	case string(model.PaymentProviderManual):
		return model.PaymentProviderManual, nil
	default:
		return "", apierrors.NewBadRequest("provider must be 'gumroad', 'afdian', or 'manual'")
	}
}

func parseBillingPeriod(raw string) (model.BillingPeriod, error) {
	switch strings.TrimSpace(raw) {
	case "":
		return "", nil
	case string(model.BillingPeriodNone):
		return model.BillingPeriodNone, nil
	case string(model.BillingPeriodMonthly):
		return model.BillingPeriodMonthly, nil
	case string(model.BillingPeriodYearly):
		return model.BillingPeriodYearly, nil
	default:
		return "", apierrors.NewBadRequest("billing_period must be 'none', 'monthly', or 'yearly'")
	}
}

func parsePaymentOrderStatus(raw string) (model.PaymentOrderStatus, error) {
	switch strings.TrimSpace(raw) {
	case "":
		return "", nil
	case string(model.PaymentOrderStatusPending):
		return model.PaymentOrderStatusPending, nil
	case string(model.PaymentOrderStatusPaid):
		return model.PaymentOrderStatusPaid, nil
	case string(model.PaymentOrderStatusCompleted):
		return model.PaymentOrderStatusCompleted, nil
	case string(model.PaymentOrderStatusFailed):
		return model.PaymentOrderStatusFailed, nil
	case string(model.PaymentOrderStatusCanceled):
		return model.PaymentOrderStatusCanceled, nil
	case string(model.PaymentOrderStatusExpired):
		return model.PaymentOrderStatusExpired, nil
	case string(model.PaymentOrderStatusRefunded):
		return model.PaymentOrderStatusRefunded, nil
	default:
		return "", apierrors.NewBadRequest("status must be a valid payment order status")
	}
}
