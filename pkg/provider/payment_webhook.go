package provider

import (
	"crypto/md5"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/historysync/hsync-server/pkg/model"
)

var (
	ErrPaymentWebhookRejected = errors.New("payment webhook rejected")
	ErrPaymentWebhookIgnored  = errors.New("payment webhook ignored")
)

// PaymentWebhookConfig carries provider secrets used only for verification.
type PaymentWebhookConfig struct {
	GumroadSecret string
	AfdianToken   string
}

// PaymentWebhookRequest is the HTTP-neutral webhook input.
type PaymentWebhookRequest struct {
	Provider model.PaymentProvider
	Headers  map[string]string
	Body     []byte
	Query    url.Values
}

// NormalizedPaymentEvent is the verified, provider-neutral purchase event.
type NormalizedPaymentEvent struct {
	Provider        model.PaymentProvider
	ExternalOrderID string
	UserID          uuid.UUID
	Payer           string
	ProductID       string
	VariantID       string
	PlanSource      string
	Currency        string
	Amount          int64
	PaidAt          time.Time
	RawMetadata     map[string]any
}

type PaymentWebhookAdapter interface {
	Normalize(req PaymentWebhookRequest) (NormalizedPaymentEvent, error)
}

// NewPaymentWebhookAdapter returns the CE provider adapter.
func NewPaymentWebhookAdapter(provider model.PaymentProvider, cfg PaymentWebhookConfig) PaymentWebhookAdapter {
	switch provider {
	case model.PaymentProviderGumroad:
		return gumroadPaymentWebhookAdapter{secret: cfg.GumroadSecret}
	case model.PaymentProviderAfdian:
		return afdianPaymentWebhookAdapter{token: cfg.AfdianToken}
	default:
		return nil
	}
}

type gumroadPaymentWebhookAdapter struct {
	secret string
}

func (a gumroadPaymentWebhookAdapter) Normalize(req PaymentWebhookRequest) (NormalizedPaymentEvent, error) {
	values, err := url.ParseQuery(string(req.Body))
	if err != nil {
		return NormalizedPaymentEvent{}, fmt.Errorf("%w: invalid gumroad form", ErrPaymentWebhookRejected)
	}
	if strings.TrimSpace(a.secret) == "" {
		return NormalizedPaymentEvent{}, fmt.Errorf("%w: gumroad webhook secret is not configured", ErrPaymentWebhookRejected)
	}
	if !constantTimeEqual(values.Get("hsync_secret"), a.secret) && !constantTimeEqual(req.Query.Get("secret"), a.secret) {
		// Gumroad Ping webhooks do not provide a documented cryptographic
		// signature, so CE requires an operator-configured shared secret carried
		// in a custom field or endpoint query string.
		return NormalizedPaymentEvent{}, fmt.Errorf("%w: gumroad shared secret mismatch", ErrPaymentWebhookRejected)
	}
	if resource := values.Get("resource_name"); resource != "" && resource != "sale" {
		return NormalizedPaymentEvent{}, ErrPaymentWebhookIgnored
	}
	if strings.EqualFold(values.Get("refunded"), "true") || strings.EqualFold(values.Get("disputed"), "true") {
		return NormalizedPaymentEvent{}, ErrPaymentWebhookIgnored
	}
	externalID := firstNonEmpty(values.Get("sale_id"), values.Get("order_id"), values.Get("purchase_id"))
	userID, err := parseUserID(firstNonEmpty(values.Get("hsync_user_id"), values.Get("user_id"), values.Get("custom_fields[hsync_user_id]")))
	if err != nil {
		return NormalizedPaymentEvent{}, fmt.Errorf("%w: %v", ErrPaymentWebhookRejected, err)
	}
	amount := parseMinorAmount(firstNonEmpty(values.Get("price"), values.Get("paid_amount_cents")))
	paidAt := parseTime(firstNonEmpty(values.Get("created_at"), values.Get("sale_timestamp")))
	event := NormalizedPaymentEvent{
		Provider:        model.PaymentProviderGumroad,
		ExternalOrderID: strings.TrimSpace(externalID),
		UserID:          userID,
		Payer:           maskPaymentEmail(values.Get("email")),
		ProductID:       firstNonEmpty(values.Get("product_id"), values.Get("product_permalink")),
		VariantID:       firstNonEmpty(values.Get("variant_id"), values.Get("variant")),
		PlanSource:      firstNonEmpty(values.Get("product_id"), values.Get("product_permalink"), values.Get("variant_id"), values.Get("variant")),
		Currency:        strings.ToUpper(values.Get("currency")),
		Amount:          amount,
		PaidAt:          paidAt,
		RawMetadata: map[string]any{
			"resource_name": values.Get("resource_name"),
			"sale_id":       externalID,
			"product_id":    values.Get("product_id"),
			"variant_id":    values.Get("variant_id"),
			"payer":         maskPaymentEmail(values.Get("email")),
		},
	}
	if event.ExternalOrderID == "" {
		return NormalizedPaymentEvent{}, fmt.Errorf("%w: missing gumroad sale id", ErrPaymentWebhookRejected)
	}
	return event, nil
}

type afdianPaymentWebhookAdapter struct {
	token string
}

func (a afdianPaymentWebhookAdapter) Normalize(req PaymentWebhookRequest) (NormalizedPaymentEvent, error) {
	if strings.TrimSpace(a.token) == "" {
		return NormalizedPaymentEvent{}, fmt.Errorf("%w: afdian token is not configured", ErrPaymentWebhookRejected)
	}
	var payload afdianWebhookPayload
	if err := json.Unmarshal(req.Body, &payload); err != nil {
		return NormalizedPaymentEvent{}, fmt.Errorf("%w: invalid afdian JSON", ErrPaymentWebhookRejected)
	}
	if payload.EC != 200 && payload.EC != 0 {
		return NormalizedPaymentEvent{}, ErrPaymentWebhookIgnored
	}
	if !constantTimeEqual(payload.Sign, afdianSign(a.token, payload.Data)) {
		return NormalizedPaymentEvent{}, fmt.Errorf("%w: afdian signature mismatch", ErrPaymentWebhookRejected)
	}
	if payload.Data.Status != 2 {
		return NormalizedPaymentEvent{}, ErrPaymentWebhookIgnored
	}
	userID, err := parseUserID(firstNonEmpty(payload.Data.Remark, payload.Data.CustomOrderID))
	if err != nil {
		return NormalizedPaymentEvent{}, fmt.Errorf("%w: %v", ErrPaymentWebhookRejected, err)
	}
	externalID := firstNonEmpty(payload.Data.OutTradeNo, payload.Data.OrderNo, payload.Data.TradeNo)
	event := NormalizedPaymentEvent{
		Provider:        model.PaymentProviderAfdian,
		ExternalOrderID: strings.TrimSpace(externalID),
		UserID:          userID,
		Payer:           maskText(payload.Data.UserID),
		ProductID:       payload.Data.PlanID,
		VariantID:       payload.Data.SKUDetail,
		PlanSource:      firstNonEmpty(payload.Data.PlanID, payload.Data.SKUDetail),
		Currency:        string(model.CurrencyCNY),
		Amount:          parseYuanToFen(payload.Data.TotalAmount),
		PaidAt:          timeFromUnix(payload.Data.CreateTime),
		RawMetadata: map[string]any{
			"out_trade_no": externalID,
			"plan_id":      payload.Data.PlanID,
			"sku_detail":   payload.Data.SKUDetail,
			"status":       payload.Data.Status,
			"payer":        maskText(payload.Data.UserID),
		},
	}
	if event.ExternalOrderID == "" {
		return NormalizedPaymentEvent{}, fmt.Errorf("%w: missing afdian order id", ErrPaymentWebhookRejected)
	}
	return event, nil
}

type afdianWebhookPayload struct {
	EC   int             `json:"ec"`
	Sign string          `json:"sign"`
	Data afdianOrderData `json:"data"`
}

type afdianOrderData struct {
	OutTradeNo    string `json:"out_trade_no"`
	OrderNo       string `json:"order_no"`
	TradeNo       string `json:"trade_no"`
	UserID        string `json:"user_id"`
	PlanID        string `json:"plan_id"`
	SKUDetail     string `json:"sku_detail"`
	CustomOrderID string `json:"custom_order_id"`
	Remark        string `json:"remark"`
	TotalAmount   string `json:"total_amount"`
	Status        int    `json:"status"`
	CreateTime    int64  `json:"create_time"`
}

func afdianSign(token string, data afdianOrderData) string {
	values := map[string]string{
		"out_trade_no":    data.OutTradeNo,
		"order_no":        data.OrderNo,
		"trade_no":        data.TradeNo,
		"user_id":         data.UserID,
		"plan_id":         data.PlanID,
		"sku_detail":      data.SKUDetail,
		"custom_order_id": data.CustomOrderID,
		"remark":          data.Remark,
		"total_amount":    data.TotalAmount,
	}
	if data.Status != 0 {
		values["status"] = strconv.Itoa(data.Status)
	}
	if data.CreateTime != 0 {
		values["create_time"] = strconv.FormatInt(data.CreateTime, 10)
	}
	keys := make([]string, 0, len(values))
	for key, value := range values {
		if value != "" {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	var b strings.Builder
	b.WriteString(token)
	for _, key := range keys {
		b.WriteString(key)
		b.WriteString(values[key])
	}
	sum := md5.Sum([]byte(b.String()))
	return hex.EncodeToString(sum[:])
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func parseUserID(raw string) (uuid.UUID, error) {
	if raw == "" {
		return uuid.Nil, errors.New("missing user id")
	}
	id, err := uuid.Parse(raw)
	if err != nil {
		return uuid.Nil, errors.New("invalid user id")
	}
	return id, nil
}

func constantTimeEqual(got, want string) bool {
	got = strings.TrimSpace(got)
	want = strings.TrimSpace(want)
	if got == "" || want == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(got), []byte(want)) == 1
}

func parseMinorAmount(raw string) int64 {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0
	}
	if cents, err := strconv.ParseInt(raw, 10, 64); err == nil {
		return cents
	}
	if amount, err := strconv.ParseFloat(raw, 64); err == nil {
		return int64(amount * 100)
	}
	return 0
}

func parseYuanToFen(raw string) int64 {
	amount, err := strconv.ParseFloat(strings.TrimSpace(raw), 64)
	if err != nil {
		return 0
	}
	return int64(amount * 100)
}

func parseTime(raw string) time.Time {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.Now()
	}
	layouts := []string{time.RFC3339, "2006-01-02 15:04:05", time.RFC1123Z}
	for _, layout := range layouts {
		if t, err := time.Parse(layout, raw); err == nil {
			return t
		}
	}
	return time.Now()
}

func timeFromUnix(ts int64) time.Time {
	if ts <= 0 {
		return time.Now()
	}
	return time.Unix(ts, 0).UTC()
}

func maskPaymentEmail(email string) string {
	email = strings.TrimSpace(email)
	parts := strings.Split(email, "@")
	if len(parts) != 2 || parts[0] == "" {
		return maskText(email)
	}
	return string(parts[0][0]) + "***@" + parts[1]
}

func maskText(value string) string {
	value = strings.TrimSpace(value)
	if len(value) <= 4 {
		return value
	}
	return value[:2] + "***" + value[len(value)-2:]
}
