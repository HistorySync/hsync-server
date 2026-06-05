package service

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/historysync/hsync-server/pkg/model"
	"github.com/historysync/hsync-server/pkg/provider"
)

type fakePaymentOrderLifecycle struct {
	*fakeBilling
}

func (f fakePaymentOrderLifecycle) Get(ctx context.Context, provider model.PaymentProvider, externalOrderID string) (*model.PaymentOrder, error) {
	return f.fakeBilling.GetOrder(ctx, provider, externalOrderID)
}

func (f fakePaymentOrderLifecycle) GetPaymentOrderByExternalID(ctx context.Context, provider model.PaymentProvider, externalOrderID string) (*model.PaymentOrder, error) {
	return f.fakeBilling.GetPaymentOrderByExternalID(ctx, provider, externalOrderID)
}

func (f fakePaymentOrderLifecycle) GetByID(_ context.Context, id uuid.UUID) (*model.PaymentOrder, error) {
	for _, order := range f.orders {
		if order.ID == id {
			cp := *order
			return &cp, nil
		}
	}
	return nil, nil
}

func (f fakePaymentOrderLifecycle) List(_ context.Context, filter model.PaymentOrderListFilter) ([]model.PaymentOrder, error) {
	return f.ListPaymentOrders(context.Background(), filter)
}

func (f fakePaymentOrderLifecycle) ListPaymentOrders(_ context.Context, filter model.PaymentOrderListFilter) ([]model.PaymentOrder, error) {
	var orders []model.PaymentOrder
	for _, order := range f.orders {
		if filter.Provider != "" && order.Provider != filter.Provider {
			continue
		}
		if filter.Status != "" && order.Status != filter.Status {
			continue
		}
		if filter.ExternalOrderID != "" && order.ExternalOrderID != filter.ExternalOrderID {
			continue
		}
		orders = append(orders, *order)
	}
	return orders, nil
}

func (f fakePaymentOrderLifecycle) MarkPaid(_ context.Context, id uuid.UUID, paidAt time.Time) error {
	for _, order := range f.orders {
		if order.ID == id && order.Status != model.PaymentOrderStatusCompleted {
			order.Status = model.PaymentOrderStatusPaid
			if order.PaidAt == nil {
				order.PaidAt = &paidAt
			}
			order.FailedAt = nil
			order.FailedReason = ""
		}
	}
	return nil
}

func (f fakePaymentOrderLifecycle) MarkRetryAttempt(_ context.Context, id uuid.UUID, retriedAt time.Time) error {
	for _, order := range f.orders {
		if order.ID == id && (order.Status == model.PaymentOrderStatusPaid || order.Status == model.PaymentOrderStatusFailed) {
			order.RetryCount++
			order.LastRetryAt = &retriedAt
		}
	}
	return nil
}

func (f fakePaymentOrderLifecycle) MarkCompleted(_ context.Context, id uuid.UUID, completedAt time.Time) error {
	for _, order := range f.orders {
		if order.ID == id {
			order.Status = model.PaymentOrderStatusCompleted
			if order.CompletedAt == nil {
				order.CompletedAt = &completedAt
			}
			order.FailedAt = nil
			order.FailedReason = ""
		}
	}
	return nil
}

func (f fakePaymentOrderLifecycle) MarkFailed(_ context.Context, id uuid.UUID, failedAt time.Time, reason string) error {
	for _, order := range f.orders {
		if order.ID == id && order.Status != model.PaymentOrderStatusCompleted {
			order.Status = model.PaymentOrderStatusFailed
			order.FailedAt = &failedAt
			order.FailedReason = reason
		}
	}
	return nil
}

func newTestPaymentWebhook() (*PaymentWebhookService, *EntitlementService, *fakeBilling, *testClock) {
	ent, fb, clock := newTestEntitlement()
	store := fakePaymentOrderLifecycle{fakeBilling: fb}
	idempotency, _ := newFakeIdempotencyService()
	idempotency.now = clock.now
	svc := NewPaymentWebhookService(PaymentWebhookDeps{
		Orders:      store,
		Entitlement: ent,
		Idempotency: idempotency,
		Config: provider.PaymentWebhookConfig{
			GumroadSecret: "gum-secret",
			AfdianToken:   "afdian-token",
		},
	})
	svc.now = clock.now
	return svc, ent, fb, clock
}

func TestGumroadWebhookFirstDeliveryFulfillsPro(t *testing.T) {
	svc, _, fb, _ := newTestPaymentWebhook()
	uid := uuid.New()

	result, err := svc.Handle(ctx(), PaymentWebhookInput{
		Provider: model.PaymentProviderGumroad,
		Body: gumroadBody(map[string]string{
			"resource_name": "sale",
			"sale_id":       "sale-pro-1",
			"product_id":    "pro",
			"hsync_user_id": uid.String(),
			"hsync_secret":  "gum-secret",
			"email":         "buyer@example.com",
			"price":         "999",
			"currency":      "usd",
		}),
	})
	if err != nil {
		t.Fatalf("Handle() error = %v", err)
	}
	if !result.Fulfilled || result.Status != model.PaymentOrderStatusCompleted || result.PlanCode != model.PlanCodePro {
		t.Fatalf("result = %+v, want completed pro fulfillment", result)
	}
	if bal := fb.liveBalance(uid); bal != 200 {
		t.Fatalf("balance = %d, want 200", bal)
	}
	order, _ := fb.GetOrder(ctx(), model.PaymentProviderGumroad, "sale-pro-1")
	if order == nil || order.Status != model.PaymentOrderStatusCompleted {
		t.Fatalf("order = %+v, want completed", order)
	}
	if order.RawMetadata["hsync_secret"] != nil {
		t.Fatal("secret leaked into raw metadata")
	}
}

func TestGumroadWebhookReplayIsIdempotent(t *testing.T) {
	svc, _, fb, _ := newTestPaymentWebhook()
	uid := uuid.New()
	input := PaymentWebhookInput{
		Provider: model.PaymentProviderGumroad,
		Body: gumroadBody(map[string]string{
			"resource_name": "sale",
			"sale_id":       "sale-pro-replay",
			"product_id":    "pro",
			"hsync_user_id": uid.String(),
			"hsync_secret":  "gum-secret",
		}),
	}

	if _, err := svc.Handle(ctx(), input); err != nil {
		t.Fatalf("first Handle(): %v", err)
	}
	result, err := svc.Handle(ctx(), input)
	if err != nil {
		t.Fatalf("replay Handle(): %v", err)
	}
	if result.Fulfilled {
		t.Fatal("replay reported a fresh fulfillment")
	}
	if bal := fb.liveBalance(uid); bal != 200 {
		t.Fatalf("balance = %d after replay, want 200", bal)
	}
	if subs, _ := fb.ListActiveByUser(ctx(), uid); len(subs) != 0 {
		t.Fatalf("subscriptions = %d, want none for pro", len(subs))
	}
}

func TestWebhookReplayWithDifferentPayloadIsRejected(t *testing.T) {
	svc, _, fb, _ := newTestPaymentWebhook()
	uid := uuid.New()
	first := PaymentWebhookInput{
		Provider: model.PaymentProviderGumroad,
		Body: gumroadBody(map[string]string{
			"resource_name": "sale",
			"sale_id":       "sale-conflict",
			"product_id":    "pro",
			"hsync_user_id": uid.String(),
			"hsync_secret":  "gum-secret",
		}),
	}
	conflict := PaymentWebhookInput{
		Provider: model.PaymentProviderGumroad,
		Body: gumroadBody(map[string]string{
			"resource_name": "sale",
			"sale_id":       "sale-conflict",
			"product_id":    "max",
			"hsync_user_id": uid.String(),
			"hsync_secret":  "gum-secret",
		}),
	}

	if _, err := svc.Handle(ctx(), first); err != nil {
		t.Fatalf("first Handle(): %v", err)
	}
	_, err := svc.Handle(ctx(), conflict)
	if !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("conflict Handle() error = %v, want ErrIdempotencyConflict", err)
	}
	if bal := fb.liveBalance(uid); bal != 200 {
		t.Fatalf("balance = %d after conflict, want 200", bal)
	}
}

func TestAfdianWebhookFulfillsMaxCloudBundle(t *testing.T) {
	svc, _, fb, clock := newTestPaymentWebhook()
	uid := uuid.New()
	body := afdianBody("afdian-token", map[string]any{
		"out_trade_no": "afdian-bundle-1",
		"user_id":      "afdian-user-1",
		"plan_id":      "max_cloud_1y",
		"remark":       uid.String(),
		"total_amount": "188.00",
		"status":       2,
		"create_time":  clock.t.Unix(),
	})

	result, err := svc.Handle(ctx(), PaymentWebhookInput{Provider: model.PaymentProviderAfdian, Body: body})
	if err != nil {
		t.Fatalf("Handle() error = %v", err)
	}
	if result.Status != model.PaymentOrderStatusCompleted || result.PlanCode != model.PlanCodeMaxCloud1Y {
		t.Fatalf("result = %+v, want completed max_cloud_1y", result)
	}
	if bal := fb.liveBalance(uid); bal != 1100 {
		t.Fatalf("balance = %d, want 1100", bal)
	}
	subs, _ := fb.ListActiveByUser(ctx(), uid)
	if len(subs) != 1 || subs[0].PlanCode != model.PlanCodeCloud {
		t.Fatalf("subscriptions = %+v, want one cloud subscription", subs)
	}
}

func TestWebhookUnknownPlanMappingCreatesFailedOrder(t *testing.T) {
	svc, _, fb, _ := newTestPaymentWebhook()
	uid := uuid.New()

	_, err := svc.Handle(ctx(), PaymentWebhookInput{
		Provider: model.PaymentProviderGumroad,
		Body: gumroadBody(map[string]string{
			"resource_name": "sale",
			"sale_id":       "sale-unknown-plan",
			"product_id":    "mystery",
			"hsync_user_id": uid.String(),
			"hsync_secret":  "gum-secret",
		}),
	})
	if !errors.Is(err, ErrPaymentPlanMappingMissing) {
		t.Fatalf("Handle() error = %v, want ErrPaymentPlanMappingMissing", err)
	}
	order, _ := fb.GetOrder(ctx(), model.PaymentProviderGumroad, "sale-unknown-plan")
	if order == nil || order.Status != model.PaymentOrderStatusFailed || order.FailedReason == "" {
		t.Fatalf("order = %+v, want failed with reason", order)
	}
	if bal := fb.liveBalance(uid); bal != 0 {
		t.Fatalf("balance = %d, want 0", bal)
	}
}

func TestWebhookVerificationFailureRejectsWithoutOrder(t *testing.T) {
	svc, _, fb, _ := newTestPaymentWebhook()
	uid := uuid.New()

	_, err := svc.Handle(ctx(), PaymentWebhookInput{
		Provider: model.PaymentProviderGumroad,
		Body: gumroadBody(map[string]string{
			"resource_name": "sale",
			"sale_id":       "bad-signature",
			"product_id":    "pro",
			"hsync_user_id": uid.String(),
			"hsync_secret":  "wrong",
		}),
	})
	if !errors.Is(err, ErrPaymentWebhookRejected) {
		t.Fatalf("Handle() error = %v, want ErrPaymentWebhookRejected", err)
	}
	if order, _ := fb.GetOrder(ctx(), model.PaymentProviderGumroad, "bad-signature"); order != nil {
		t.Fatalf("order = %+v, want nil", order)
	}
}

func TestAdminRetryFailedOrderFulfillsOnce(t *testing.T) {
	svc, _, fb, _ := newTestPaymentWebhook()
	uid := uuid.New()
	order := &model.PaymentOrder{
		UserID:          uid,
		Provider:        model.PaymentProviderGumroad,
		ExternalOrderID: "retry-pro",
		PlanCode:        model.PlanCodePro,
		Status:          model.PaymentOrderStatusFailed,
		FailedReason:    "temporary entitlement error",
	}
	if err := fb.Upsert(ctx(), order); err != nil {
		t.Fatalf("Upsert() error = %v", err)
	}

	result, err := svc.RetryPaymentFulfillment(ctx(), order.ID)
	if err != nil {
		t.Fatalf("RetryFulfillment() error = %v", err)
	}
	if !result.Fulfilled || result.Status != model.PaymentOrderStatusCompleted {
		t.Fatalf("result = %+v, want completed retry", result)
	}
	if bal := fb.liveBalance(uid); bal != 200 {
		t.Fatalf("balance = %d, want 200", bal)
	}
	got, _ := fb.GetOrder(ctx(), model.PaymentProviderGumroad, "retry-pro")
	if got.RetryCount != 1 || got.LastRetryAt == nil {
		t.Fatalf("retry tracking = count %d at %v, want one attempt", got.RetryCount, got.LastRetryAt)
	}
	result, err = svc.RetryPaymentFulfillment(ctx(), order.ID)
	if err != nil {
		t.Fatalf("retry completed order error = %v", err)
	}
	if result.Fulfilled {
		t.Fatal("completed retry reported fresh fulfillment")
	}
	if bal := fb.liveBalance(uid); bal != 200 {
		t.Fatalf("balance = %d after completed retry, want 200", bal)
	}
	got, _ = fb.GetOrder(ctx(), model.PaymentProviderGumroad, "retry-pro")
	if got.RetryCount != 1 {
		t.Fatalf("retry count after completed retry = %d, want still 1", got.RetryCount)
	}
}

func TestFulfillmentErrorMarksOrderFailed(t *testing.T) {
	svc, ent, fb, _ := newTestPaymentWebhook()
	uid := uuid.New()
	delete(fb.plans, model.PlanCodePro)

	_, err := svc.Handle(ctx(), PaymentWebhookInput{
		Provider: model.PaymentProviderGumroad,
		Body: gumroadBody(map[string]string{
			"resource_name": "sale",
			"sale_id":       "fulfillment-fails",
			"product_id":    "pro",
			"hsync_user_id": uid.String(),
			"hsync_secret":  "gum-secret",
		}),
	})
	if !errors.Is(err, ErrPlanNotFound) {
		t.Fatalf("Handle() error = %v, want ErrPlanNotFound", err)
	}
	order, _ := fb.GetOrder(ctx(), model.PaymentProviderGumroad, "fulfillment-fails")
	if order == nil || order.Status != model.PaymentOrderStatusFailed || order.FailedReason == "" {
		t.Fatalf("order = %+v, want failed with reason", order)
	}

	fb.seedCatalog()
	ent.now = svc.now
	result, err := svc.RetryPaymentFulfillment(context.Background(), order.ID)
	if err != nil {
		t.Fatalf("RetryFulfillment() error = %v", err)
	}
	if result.Status != model.PaymentOrderStatusCompleted || fb.liveBalance(uid) != 200 {
		t.Fatalf("result = %+v balance=%d, want completed and 200", result, fb.liveBalance(uid))
	}
}

func gumroadBody(values map[string]string) []byte {
	form := url.Values{}
	for key, value := range values {
		form.Set(key, value)
	}
	return []byte(form.Encode())
}

func afdianBody(token string, data map[string]any) []byte {
	sign := afdianTestSign(token, data)
	payload := map[string]any{"ec": 200, "sign": sign, "data": data}
	body, _ := json.Marshal(payload)
	return body
}

func afdianTestSign(token string, data map[string]any) string {
	keys := make([]string, 0, len(data))
	for key, value := range data {
		if fmt.Sprint(value) != "" {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	var b strings.Builder
	b.WriteString(token)
	for _, key := range keys {
		b.WriteString(key)
		b.WriteString(fmt.Sprint(data[key]))
	}
	sum := md5.Sum([]byte(b.String()))
	return hex.EncodeToString(sum[:])
}
