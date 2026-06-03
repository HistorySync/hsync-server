package provider

import (
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
	if _, err := p.CreateCheckoutSession("u", "price"); !errors.Is(err, ErrBillingNotSupported) {
		t.Fatalf("CreateCheckoutSession() error = %v, want ErrBillingNotSupported", err)
	}
	if sub, err := p.GetSubscription("u"); err != nil || sub != nil {
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
