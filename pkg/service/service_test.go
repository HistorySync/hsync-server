package service

import (
	"context"
	"crypto/sha256"
	"testing"

	"github.com/google/uuid"
)

func TestPasswordHashRoundTrip(t *testing.T) {
	hash, err := hashPassword("correct horse battery staple")
	if err != nil {
		t.Fatalf("hashPassword() error = %v", err)
	}
	if !verifyPassword("correct horse battery staple", hash) {
		t.Fatal("verifyPassword() = false, want true")
	}
	if verifyPassword("wrong password", hash) {
		t.Fatal("verifyPassword() = true for wrong password")
	}
}

func TestVerifyPasswordRejectsMalformedHash(t *testing.T) {
	if verifyPassword("password", "not-a-phc-hash") {
		t.Fatal("verifyPassword() = true for malformed hash")
	}
}

func TestNormalizeEmail(t *testing.T) {
	email, err := normalizeEmail(" User@Example.COM ")
	if err != nil {
		t.Fatalf("normalizeEmail() error = %v", err)
	}
	if email != "user@example.com" {
		t.Fatalf("normalizeEmail() = %q, want user@example.com", email)
	}
}

func TestNormalizeEmailRejectsInvalid(t *testing.T) {
	for _, input := range []string{"", "not-an-email", "user@example.com other"} {
		if _, err := normalizeEmail(input); err != ErrInvalidEmail {
			t.Fatalf("normalizeEmail(%q) error = %v, want ErrInvalidEmail", input, err)
		}
	}
}

func TestValidatePassword(t *testing.T) {
	if err := validatePassword("123456789"); err != ErrWeakPassword {
		t.Fatalf("validatePassword() error = %v, want ErrWeakPassword", err)
	}
	if err := validatePassword("1234567890"); err != nil {
		t.Fatalf("validatePassword() error = %v, want nil", err)
	}
}

func TestIssueRefreshToken(t *testing.T) {
	svc := &AuthService{}
	token, hash, err := svc.issueRefreshToken(uuid.New())
	if err != nil {
		t.Fatalf("issueRefreshToken() error = %v", err)
	}
	if token == "" {
		t.Fatal("token is empty")
	}
	if len(hash) != sha256.Size {
		t.Fatalf("hash length = %d, want %d", len(hash), sha256.Size)
	}
	if got := hashToken(token); string(got) != string(hash) {
		t.Fatal("hashToken(token) does not match returned hash")
	}
}

func TestResetPasswordRejectsEmptyFields(t *testing.T) {
	svc := &AuthService{}
	if err := svc.ResetPassword(context.Background(), ResetPasswordInput{}); err == nil || err.Error() != "reset token is required" {
		t.Fatalf("ResetPassword() error = %v, want reset token is required", err)
	}
	if err := svc.ResetPassword(context.Background(), ResetPasswordInput{Token: "token"}); err == nil || err.Error() != "new password is required" {
		t.Fatalf("ResetPassword() error = %v, want new password is required", err)
	}
}

func TestResetPasswordRejectsWeakPassword(t *testing.T) {
	svc := &AuthService{}
	if err := svc.ResetPassword(context.Background(), ResetPasswordInput{Token: "token", NewPassword: "short"}); err != ErrWeakPassword {
		t.Fatalf("ResetPassword() error = %v, want ErrWeakPassword", err)
	}
}

func TestRefreshAndLogoutRequireToken(t *testing.T) {
	svc := &AuthService{}
	if _, err := svc.RefreshAccessToken(context.Background(), ""); err != ErrRefreshTokenRequired {
		t.Fatalf("RefreshAccessToken() error = %v, want ErrRefreshTokenRequired", err)
	}
	if err := svc.Logout(context.Background(), ""); err != ErrRefreshTokenRequired {
		t.Fatalf("Logout() error = %v, want ErrRefreshTokenRequired", err)
	}
}

func TestHashTokenIsDeterministic(t *testing.T) {
	first := hashToken("reset-token")
	second := hashToken("reset-token")
	if string(first) != string(second) {
		t.Fatal("hashToken() produced different hashes for same token")
	}
}

func TestQuotaExceeded(t *testing.T) {
	cases := []struct {
		name                    string
		used, additional, limit int64
		want                    bool
	}{
		{"exactly at limit is allowed", 0, 100, 100, false},
		{"sum equals limit is allowed", 50, 50, 100, false},
		{"one byte over is rejected", 50, 51, 100, true},
		{"already full is rejected", 100, 1, 100, true},
		{"zero add at zero limit allowed", 0, 0, 0, false},
		{"positive add at zero limit rejected", 0, 1, 0, true},
	}
	for _, c := range cases {
		if got := quotaExceeded(c.used, c.additional, c.limit); got != c.want {
			t.Fatalf("%s: quotaExceeded(%d,%d,%d) = %v, want %v",
				c.name, c.used, c.additional, c.limit, got, c.want)
		}
	}
}
