package provider

import (
	"context"
	"errors"
	"testing"
)

func TestSingleUserAuthProvider(t *testing.T) {
	p := NewSingleUserAuthProvider(&UserInfo{ID: "user-1", Email: "owner@example.com", Tier: "free"})

	user, err := p.ValidateCredentials("owner@example.com", "ignored")
	if err != nil {
		t.Fatalf("ValidateCredentials() error = %v", err)
	}
	if user.ID != "user-1" {
		t.Fatalf("user ID = %s, want user-1", user.ID)
	}
	if _, err := p.ValidateCredentials("other@example.com", "ignored"); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("ValidateCredentials() error = %v, want ErrInvalidCredentials", err)
	}
	if _, err := p.CreateUser(CreateUserRequest{}); !errors.Is(err, ErrMultiUserNotSupported) {
		t.Fatalf("CreateUser() error = %v, want ErrMultiUserNotSupported", err)
	}
	if p.SupportsMultiUser() {
		t.Fatal("SupportsMultiUser() = true, want false")
	}
}

func TestNoopBillingProvider(t *testing.T) {
	p := &NoopBillingProvider{}
	if p.IsEnabled() {
		t.Fatal("IsEnabled() = true, want false")
	}
	if _, err := p.CreateCheckoutSession(context.Background(), "u", "price"); !errors.Is(err, ErrBillingNotSupported) {
		t.Fatalf("CreateCheckoutSession() error = %v, want ErrBillingNotSupported", err)
	}
	if sub, err := p.GetSubscription(context.Background(), "u"); err != nil || sub != nil {
		t.Fatalf("GetSubscription() = (%v, %v), want (nil, nil)", sub, err)
	}
}

func TestUnlimitedQuotaProvider(t *testing.T) {
	p := &UnlimitedQuotaProvider{}
	limits, err := p.GetLimits("u")
	if err != nil {
		t.Fatalf("GetLimits() error = %v", err)
	}
	if limits.StorageLimitBytes <= 0 || limits.MaxDevices <= 0 {
		t.Fatalf("limits look invalid: %+v", limits)
	}
	if err := p.CheckStorageQuota("u", 1<<60); err != nil {
		t.Fatalf("CheckStorageQuota() error = %v", err)
	}
	if err := p.RecordUsage("u", 123); err != nil {
		t.Fatalf("RecordUsage() error = %v", err)
	}
}

func TestSelectFirstHealthyUsesPriorityOrder(t *testing.T) {
	provider, name, err := SelectFirstHealthy(context.Background(), []ProviderCandidate[string]{
		{Name: "slow", Priority: 20, Provider: "slow"},
		{Name: "fast", Priority: 10, Provider: "fast"},
	})
	if err != nil {
		t.Fatalf("SelectFirstHealthy() error = %v", err)
	}
	if name != "fast" || provider != "fast" {
		t.Fatalf("selected (%q, %q), want (fast, fast)", name, provider)
	}
}

func TestSelectFirstHealthySkipsUnhealthyCandidate(t *testing.T) {
	provider, name, err := SelectFirstHealthy(context.Background(), []ProviderCandidate[string]{
		{Name: "primary", Priority: 10, Provider: "primary", Healthy: func(context.Context) bool { return false }},
		{Name: "fallback", Priority: 20, Provider: "fallback", Healthy: func(context.Context) bool { return true }},
	})
	if err != nil {
		t.Fatalf("SelectFirstHealthy() error = %v", err)
	}
	if name != "fallback" || provider != "fallback" {
		t.Fatalf("selected (%q, %q), want (fallback, fallback)", name, provider)
	}
}

func TestSelectFirstHealthySkipsNilProvider(t *testing.T) {
	provider, name, err := SelectFirstHealthy(context.Background(), []ProviderCandidate[QuotaProvider]{
		{Name: "nil", Priority: 10, Provider: nil},
		{Name: "fallback", Priority: 20, Provider: &UnlimitedQuotaProvider{}},
	})
	if err != nil {
		t.Fatalf("SelectFirstHealthy() error = %v", err)
	}
	if name != "fallback" || provider == nil {
		t.Fatalf("selected (%q, %v), want fallback provider", name, provider)
	}
}

func TestQuotaSelectorProviderDelegatesToActiveCandidate(t *testing.T) {
	selector := NewQuotaSelectorProvider(
		ProviderCandidate[QuotaProvider]{Name: "down", Priority: 10, Provider: &UnlimitedQuotaProvider{}, Healthy: func(context.Context) bool { return false }},
		ProviderCandidate[QuotaProvider]{Name: "active", Priority: 20, Provider: &UnlimitedQuotaProvider{}},
	)

	name, err := selector.ActiveProviderName(context.Background())
	if err != nil {
		t.Fatalf("ActiveProviderName() error = %v", err)
	}
	if name != "active" {
		t.Fatalf("active provider = %q, want active", name)
	}
	if err := selector.CheckStorageQuota("u", 1); err != nil {
		t.Fatalf("CheckStorageQuota() error = %v", err)
	}
}
