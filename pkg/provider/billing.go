// Package provider contains the default (CE) BillingProvider implementation.
package provider

// NoopBillingProvider is the CE default: all billing operations are no-ops.
type NoopBillingProvider struct{}

// CreateCheckoutSession returns an error in CE mode.
func (p *NoopBillingProvider) CreateCheckoutSession(userID, priceID string) (string, error) {
	return "", ErrBillingNotSupported
}

// HandleWebhook returns an error in CE mode.
func (p *NoopBillingProvider) HandleWebhook(payload []byte, signature string) error {
	return ErrBillingNotSupported
}

// GetSubscription returns nil (no subscription) in CE mode.
func (p *NoopBillingProvider) GetSubscription(userID string) (*SubscriptionInfo, error) {
	return nil, nil
}

// CreatePortalSession returns an error in CE mode.
func (p *NoopBillingProvider) CreatePortalSession(userID string) (string, error) {
	return "", ErrBillingNotSupported
}

// IsEnabled always returns false for CE.
func (p *NoopBillingProvider) IsEnabled() bool {
	return false
}

// defaultBillingProvider is the globally registered default.
var defaultBillingProvider BillingProvider = &NoopBillingProvider{}
