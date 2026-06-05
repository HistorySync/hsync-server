package service

import (
	"context"
	"crypto/sha256"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/pquerna/otp"
	"github.com/pquerna/otp/totp"

	"github.com/historysync/hsync-server/pkg/model"
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

func TestRegisterRespectsSignupsEnabled(t *testing.T) {
	ctx := context.Background()

	closed := &AuthService{
		settings: NewSettingsService(&fakeSettingStore{
			rows: map[string]model.SystemSetting{
				SettingKeySignupsEnabled: {Key: SettingKeySignupsEnabled, Value: "false"},
			},
		}, defaultSettingDefinitions()),
	}
	if _, err := closed.Register(ctx, RegisterInput{Email: "bad"}); err != ErrSignupsDisabled {
		t.Fatalf("Register(signups disabled) error = %v, want ErrSignupsDisabled", err)
	}

	open := &AuthService{
		settings: NewSettingsService(&fakeSettingStore{
			rows: map[string]model.SystemSetting{
				SettingKeySignupsEnabled: {Key: SettingKeySignupsEnabled, Value: "true"},
			},
		}, defaultSettingDefinitions()),
	}
	if _, err := open.Register(ctx, RegisterInput{Email: "bad"}); err != ErrInvalidEmail {
		t.Fatalf("Register(signups enabled) error = %v, want ErrInvalidEmail", err)
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

func TestVerifyEmailRequiresToken(t *testing.T) {
	svc := &AuthService{}
	if err := svc.VerifyEmail(context.Background(), ""); err != ErrVerifyTokenRequired {
		t.Fatalf("VerifyEmail() error = %v, want ErrVerifyTokenRequired", err)
	}
}

func TestStartEmailVerificationHidesInvalidEmail(t *testing.T) {
	svc := &AuthService{}
	token, err := svc.StartEmailVerification(context.Background(), "not-an-email")
	if err != nil {
		t.Fatalf("StartEmailVerification() error = %v, want nil", err)
	}
	if token != "" {
		t.Fatalf("StartEmailVerification() token = %q, want empty", token)
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

func TestNormalizeBackupCode(t *testing.T) {
	got, ok := normalizeBackupCode("abcd-2345")
	if !ok {
		t.Fatal("normalizeBackupCode() ok = false, want true")
	}
	if got != "ABCD2345" {
		t.Fatalf("normalizeBackupCode() = %q, want ABCD2345", got)
	}
	if _, ok := normalizeBackupCode("short"); ok {
		t.Fatal("normalizeBackupCode(short) ok = true, want false")
	}
}

func TestGenerateBackupCodesReturnsFormattedCodes(t *testing.T) {
	codes, err := generateBackupCodes()
	if err != nil {
		t.Fatalf("generateBackupCodes() error = %v", err)
	}
	if len(codes) != twoFactorBackupCount {
		t.Fatalf("count = %d, want %d", len(codes), twoFactorBackupCount)
	}
	for _, code := range codes {
		if _, ok := normalizeBackupCode(code); !ok {
			t.Fatalf("generated code %q did not normalize", code)
		}
	}
}

func TestBackupCodeHMACHash(t *testing.T) {
	svc := NewTwoFactorService(nil, nil, "0123456789abcdef0123456789abcdef")
	normalized, ok := normalizeBackupCode("abcd-2345")
	if !ok {
		t.Fatal("normalizeBackupCode() ok = false, want true")
	}
	hash := svc.hashBackupCode(normalized)
	if hash == normalized || hash == "" {
		t.Fatalf("hashBackupCode() = %q", hash)
	}
	if !svc.backupCodeHashMatches(normalized, hash) {
		t.Fatal("backupCodeHashMatches() = false, want true")
	}
	if svc.backupCodeHashMatches("ZZZZ9999", hash) {
		t.Fatal("backupCodeHashMatches() = true for wrong code")
	}
}

func TestValidateTOTP(t *testing.T) {
	key, err := totp.Generate(totp.GenerateOpts{
		Issuer:      twoFactorIssuer,
		AccountName: "user@example.com",
		Period:      30,
		Digits:      otp.DigitsSix,
		Algorithm:   otp.AlgorithmSHA1,
	})
	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	code, err := totp.GenerateCodeCustom(key.Secret(), time.Now(), totp.ValidateOpts{
		Period:    30,
		Skew:      1,
		Digits:    otp.DigitsSix,
		Algorithm: otp.AlgorithmSHA1,
	})
	if err != nil {
		t.Fatalf("GenerateCodeCustom() error = %v", err)
	}
	if !validateTOTP(key.Secret(), code) {
		t.Fatal("validateTOTP() = false, want true")
	}
	if validateTOTP(key.Secret(), "000000") {
		t.Fatal("validateTOTP() = true for wrong code")
	}
}
