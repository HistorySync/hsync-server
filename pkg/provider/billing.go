// Package provider contains the default (CE) BillingProvider implementation.
package provider

import "context"

// NoopBillingProvider is the CE default: all billing operations are no-ops.
type NoopBillingProvider struct{}

// CreateCheckoutSession returns an error in CE mode.
func (p *NoopBillingProvider) CreateCheckoutSession(ctx context.Context, userID, priceID string) (string, error) {
	return "", ErrBillingNotSupported
}

// HandleWebhook returns an error in CE mode.
func (p *NoopBillingProvider) HandleWebhook(ctx context.Context, payload []byte, signature string) error {
	return ErrBillingNotSupported
}

// GetSubscription returns nil (no subscription) in CE mode.
func (p *NoopBillingProvider) GetSubscription(ctx context.Context, userID string) (*SubscriptionInfo, error) {
	return nil, nil
}

// CreatePortalSession returns an error in CE mode.
func (p *NoopBillingProvider) CreatePortalSession(ctx context.Context, userID string) (string, error) {
	return "", ErrBillingNotSupported
}

// IsEnabled always returns false for CE.
func (p *NoopBillingProvider) IsEnabled() bool {
	return false
}

// defaultBillingProvider is the globally registered default.
var defaultBillingProvider BillingProvider = &NoopBillingProvider{}
