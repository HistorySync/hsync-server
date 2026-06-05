package service

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/historysync/hsync-server/pkg/model"
	"github.com/historysync/hsync-server/pkg/provider"
)

var (
	ErrPaymentWebhookRejected    = errors.New("payment webhook rejected")
	ErrPaymentWebhookIgnored     = errors.New("payment webhook ignored")
	ErrPaymentOrderNotFound      = errors.New("payment order not found")
	ErrPaymentOrderNotRetryable  = errors.New("payment order is not retryable")
	ErrPaymentPlanMappingMissing = errors.New("payment plan mapping missing")
)

type paymentOrderLifecycleStore interface {
	Upsert(ctx context.Context, o *model.PaymentOrder) error
	Get(ctx context.Context, provider model.PaymentProvider, externalOrderID string) (*model.PaymentOrder, error)
	GetPaymentOrderByExternalID(ctx context.Context, provider model.PaymentProvider, externalOrderID string) (*model.PaymentOrder, error)
	GetByID(ctx context.Context, id uuid.UUID) (*model.PaymentOrder, error)
	ListPaymentOrders(ctx context.Context, filter model.PaymentOrderListFilter) ([]model.PaymentOrder, error)
	MarkPaid(ctx context.Context, id uuid.UUID, paidAt time.Time) error
	MarkCompleted(ctx context.Context, id uuid.UUID, completedAt time.Time) error
	MarkFailed(ctx context.Context, id uuid.UUID, failedAt time.Time, reason string) error
	MarkRetryAttempt(ctx context.Context, id uuid.UUID, retriedAt time.Time) error
}

// PaymentWebhookService closes the verified-payment -> entitlement fulfillment
// loop for CE payment providers.
type PaymentWebhookService struct {
	orders      paymentOrderLifecycleStore
	entitlement *EntitlementService
	audit       *AuditService
	adapters    map[model.PaymentProvider]provider.PaymentWebhookAdapter
	now         func() time.Time
}

type PaymentWebhookDeps struct {
	Orders      paymentOrderLifecycleStore
	Entitlement *EntitlementService
	Audit       *AuditService
	Config      provider.PaymentWebhookConfig
}

type PaymentWebhookInput struct {
	Provider  model.PaymentProvider
	Headers   map[string]string
	Query     url.Values
	Body      []byte
	IP        string
	UserAgent string
}

type PaymentWebhookResult struct {
	OrderID   uuid.UUID                `json:"order_id,omitempty"`
	Status    model.PaymentOrderStatus `json:"status"`
	PlanCode  string                   `json:"plan_code,omitempty"`
	Fulfilled bool                     `json:"fulfilled"`
	Retryable bool                     `json:"retryable"`
	Ignored   bool                     `json:"ignored,omitempty"`
}

func NewPaymentWebhookService(deps PaymentWebhookDeps) *PaymentWebhookService {
	if deps.Orders == nil || deps.Entitlement == nil {
		return nil
	}
	return &PaymentWebhookService{
		orders:      deps.Orders,
		entitlement: deps.Entitlement,
		audit:       deps.Audit,
		adapters: map[model.PaymentProvider]provider.PaymentWebhookAdapter{
			model.PaymentProviderGumroad: provider.NewPaymentWebhookAdapter(model.PaymentProviderGumroad, deps.Config),
			model.PaymentProviderAfdian:  provider.NewPaymentWebhookAdapter(model.PaymentProviderAfdian, deps.Config),
		},
		now: time.Now,
	}
}

func (s *PaymentWebhookService) Handle(ctx context.Context, in PaymentWebhookInput) (*PaymentWebhookResult, error) {
	adapter := s.adapters[in.Provider]
	if adapter == nil {
		return nil, fmt.Errorf("%w: unsupported provider", ErrPaymentWebhookRejected)
	}
	event, err := adapter.Normalize(provider.PaymentWebhookRequest{
		Provider: in.Provider,
		Headers:  in.Headers,
		Body:     in.Body,
		Query:    in.Query,
	})
	if err != nil {
		if errors.Is(err, provider.ErrPaymentWebhookIgnored) {
			s.auditPayment(ctx, model.AuditEventBillingWebhookReceived, "", uuid.Nil, in, nil)
			return &PaymentWebhookResult{Ignored: true}, nil
		}
		s.auditPayment(ctx, model.AuditEventBillingWebhookRejected, "", uuid.Nil, in, map[string]any{"reason": err.Error()})
		return nil, fmt.Errorf("%w: %v", ErrPaymentWebhookRejected, err)
	}
	s.auditPayment(ctx, model.AuditEventBillingWebhookReceived, event.ExternalOrderID, event.UserID, in, event.RawMetadata)

	planCode, ok := mapPaymentPlan(event)
	if !ok {
		order := s.orderFromEvent(event, "")
		order.Status = model.PaymentOrderStatusFailed
		order.FailedAt = timePtr(s.now())
		order.FailedReason = ErrPaymentPlanMappingMissing.Error()
		if err := s.orders.Upsert(ctx, order); err != nil {
			return nil, fmt.Errorf("record failed order: %w", err)
		}
		s.auditOrder(ctx, model.AuditEventBillingOrderFulfillmentFailed, order, map[string]any{"reason": order.FailedReason})
		return &PaymentWebhookResult{OrderID: order.ID, Status: order.Status, Retryable: true}, ErrPaymentPlanMappingMissing
	}

	order := s.orderFromEvent(event, planCode)
	order.Status = model.PaymentOrderStatusPaid
	if err := s.orders.Upsert(ctx, order); err != nil {
		return nil, fmt.Errorf("record paid order: %w", err)
	}
	if err := s.orders.MarkPaid(ctx, order.ID, event.PaidAt); err != nil {
		return nil, err
	}
	s.auditOrder(ctx, model.AuditEventBillingOrderPaid, order, nil)

	if existing, err := s.GetPaymentOrderByExternalID(ctx, event.Provider, event.ExternalOrderID); err == nil && existing != nil {
		order = existing
	}
	if order.Status == model.PaymentOrderStatusCompleted {
		return &PaymentWebhookResult{OrderID: order.ID, Status: order.Status, PlanCode: order.PlanCode, Fulfilled: false}, nil
	}
	return s.fulfill(ctx, order, "webhook")
}

func (s *PaymentWebhookService) RetryFulfillment(ctx context.Context, orderID uuid.UUID) (*PaymentWebhookResult, error) {
	return s.RetryPaymentFulfillment(ctx, orderID)
}

func (s *PaymentWebhookService) RetryPaymentFulfillment(ctx context.Context, orderID uuid.UUID) (*PaymentWebhookResult, error) {
	order, err := s.orders.GetByID(ctx, orderID)
	if err != nil {
		return nil, err
	}
	if order == nil {
		return nil, ErrPaymentOrderNotFound
	}
	if order.Status == model.PaymentOrderStatusCompleted {
		return &PaymentWebhookResult{OrderID: order.ID, Status: order.Status, PlanCode: order.PlanCode}, nil
	}
	if order.Status != model.PaymentOrderStatusPaid && order.Status != model.PaymentOrderStatusFailed {
		return nil, ErrPaymentOrderNotRetryable
	}
	if err := s.orders.MarkRetryAttempt(ctx, order.ID, s.now()); err != nil {
		return nil, err
	}
	if updated, err := s.orders.GetByID(ctx, order.ID); err == nil && updated != nil {
		order = updated
	}
	s.auditOrder(ctx, model.AuditEventBillingOrderRetry, order, nil)
	return s.fulfill(ctx, order, "admin_retry")
}

func (s *PaymentWebhookService) ListOrders(ctx context.Context, filter model.PaymentOrderListFilter) ([]model.PaymentOrder, error) {
	return s.ListPaymentOrders(ctx, filter)
}

func (s *PaymentWebhookService) ListPaymentOrders(ctx context.Context, filter model.PaymentOrderListFilter) ([]model.PaymentOrder, error) {
	orders, err := s.orders.ListPaymentOrders(ctx, filter)
	if err != nil {
		return nil, err
	}
	if orders == nil {
		return []model.PaymentOrder{}, nil
	}
	return orders, nil
}

func (s *PaymentWebhookService) GetPaymentOrderByExternalID(ctx context.Context, provider model.PaymentProvider, externalOrderID string) (*model.PaymentOrder, error) {
	if externalOrderID == "" {
		return nil, nil
	}
	return s.orders.GetPaymentOrderByExternalID(ctx, provider, externalOrderID)
}

func (s *PaymentWebhookService) fulfill(ctx context.Context, order *model.PaymentOrder, reason string) (*PaymentWebhookResult, error) {
	if order.Status == model.PaymentOrderStatusCompleted {
		return &PaymentWebhookResult{OrderID: order.ID, Status: order.Status, PlanCode: order.PlanCode}, nil
	}
	_, err := s.entitlement.GrantPlanToUser(ctx, order.UserID, order.PlanCode, GrantOptions{
		Provider:        order.Provider,
		ExternalOrderID: order.ExternalOrderID,
		RawMetadata: map[string]any{
			"payment_order_id": order.ID.String(),
			"fulfillment":      reason,
		},
	})
	if err != nil {
		_ = s.orders.MarkFailed(ctx, order.ID, s.now(), err.Error())
		failed, _ := s.orders.GetByID(ctx, order.ID)
		if failed != nil {
			order = failed
		}
		s.auditOrder(ctx, model.AuditEventBillingOrderFulfillmentFailed, order, map[string]any{"reason": err.Error()})
		return &PaymentWebhookResult{OrderID: order.ID, Status: model.PaymentOrderStatusFailed, PlanCode: order.PlanCode, Retryable: true}, err
	}
	if err := s.orders.MarkCompleted(ctx, order.ID, s.now()); err != nil {
		return nil, err
	}
	completed, _ := s.orders.GetByID(ctx, order.ID)
	if completed != nil {
		order = completed
	}
	s.auditOrder(ctx, model.AuditEventBillingOrderCompleted, order, nil)
	return &PaymentWebhookResult{OrderID: order.ID, Status: model.PaymentOrderStatusCompleted, PlanCode: order.PlanCode, Fulfilled: true}, nil
}

func (s *PaymentWebhookService) orderFromEvent(event provider.NormalizedPaymentEvent, planCode string) *model.PaymentOrder {
	paidAt := event.PaidAt
	if paidAt.IsZero() {
		paidAt = s.now()
	}
	metadata := event.RawMetadata
	if metadata == nil {
		metadata = map[string]any{}
	}
	metadata["plan_source"] = event.PlanSource
	return &model.PaymentOrder{
		UserID:          event.UserID,
		Provider:        event.Provider,
		ExternalOrderID: event.ExternalOrderID,
		PlanCode:        planCode,
		Currency:        event.Currency,
		Amount:          event.Amount,
		Status:          model.PaymentOrderStatusPaid,
		RawMetadata:     metadata,
		PaidAt:          &paidAt,
	}
}

func mapPaymentPlan(event provider.NormalizedPaymentEvent) (string, bool) {
	candidates := []string{
		strings.ToLower(strings.TrimSpace(event.PlanSource)),
		strings.ToLower(strings.TrimSpace(event.ProductID)),
		strings.ToLower(strings.TrimSpace(event.VariantID)),
	}
	mappings := []struct {
		key      string
		planCode string
	}{
		{"max_cloud_2y", model.PlanCodeMaxCloud2Y},
		{"max-cloud-2y", model.PlanCodeMaxCloud2Y},
		{"max_cloud_1y", model.PlanCodeMaxCloud1Y},
		{"max-cloud-1y", model.PlanCodeMaxCloud1Y},
		{"cloud_lite", model.PlanCodeCloudLite},
		{"cloud-lite", model.PlanCodeCloudLite},
		{"cloud", model.PlanCodeCloud},
		{"max", model.PlanCodeMax},
		{"pro", model.PlanCodePro},
		{"free", model.PlanCodeFree},
	}
	for _, candidate := range candidates {
		if candidate == "" {
			continue
		}
		for _, mapping := range mappings {
			if candidate == mapping.key {
				return mapping.planCode, true
			}
		}
		for _, mapping := range mappings {
			if strings.Contains(candidate, mapping.key) {
				return mapping.planCode, true
			}
		}
	}
	return "", false
}

func (s *PaymentWebhookService) auditPayment(ctx context.Context, eventType model.AuditEventType, externalOrderID string, userID uuid.UUID, in PaymentWebhookInput, metadata map[string]any) {
	if s.audit == nil {
		return
	}
	clean := map[string]any{
		"provider":          string(in.Provider),
		"external_order_id": externalOrderID,
	}
	for key, value := range metadata {
		clean[key] = value
	}
	_ = s.audit.Record(ctx, AuditEventInput{
		EventType:   eventType,
		TargetType:  "payment_order",
		TargetID:    externalOrderID,
		IP:          in.IP,
		UserAgent:   in.UserAgent,
		ActorUserID: auditActorPtr(userID),
		Metadata:    clean,
	})
}

func (s *PaymentWebhookService) auditOrder(ctx context.Context, eventType model.AuditEventType, order *model.PaymentOrder, metadata map[string]any) {
	if s.audit == nil || order == nil {
		return
	}
	clean := map[string]any{
		"provider":          string(order.Provider),
		"external_order_id": order.ExternalOrderID,
		"plan_code":         order.PlanCode,
		"status":            string(order.Status),
		"retry_count":       order.RetryCount,
	}
	for key, value := range metadata {
		clean[key] = value
	}
	_ = s.audit.Record(ctx, AuditEventInput{
		EventType:   eventType,
		TargetType:  "payment_order",
		TargetID:    order.ID.String(),
		ActorUserID: auditActorPtr(order.UserID),
		Metadata:    clean,
	})
}

func auditActorPtr(id uuid.UUID) *uuid.UUID {
	if id == uuid.Nil {
		return nil
	}
	return &id
}

func timePtr(t time.Time) *time.Time {
	return &t
}
