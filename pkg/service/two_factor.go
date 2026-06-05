package service

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/pquerna/otp"
	"github.com/pquerna/otp/totp"

	"github.com/historysync/hsync-server/pkg/auth"
	"github.com/historysync/hsync-server/pkg/config"
	"github.com/historysync/hsync-server/pkg/model"
	"github.com/historysync/hsync-server/pkg/repository"
)

const (
	twoFactorIssuer       = "HistorySync"
	twoFactorBackupCount  = 10
	twoFactorBackupLength = 8
	twoFactorMaxFailures  = 5
	twoFactorLockDuration = 15 * time.Minute
)

// TwoFactorService manages TOTP setup, verification, and backup codes.
type TwoFactorService struct {
	repos        *repository.Repos
	tokenManager *auth.TokenManager
	aead         cipher.AEAD
	securityKey  []byte
	initErr      error
}

// TwoFactorStatus is returned by the authenticated status endpoint.
type TwoFactorStatus struct {
	Enabled              bool       `json:"enabled"`
	Locked               bool       `json:"locked"`
	LockedUntil          *time.Time `json:"locked_until,omitempty"`
	BackupCodesRemaining int        `json:"backup_codes_remaining"`
	LastUsedAt           *time.Time `json:"last_used_at,omitempty"`
	EnabledAt            *time.Time `json:"enabled_at,omitempty"`
}

// TwoFactorSetupResult returns the TOTP secret and one-time backup codes.
type TwoFactorSetupResult struct {
	Secret      string   `json:"secret"`
	OTPAuthURL  string   `json:"otpauth_url"`
	BackupCodes []string `json:"backup_codes"`
}

// LoginTwoFactorInput completes a password-verified login challenge.
type LoginTwoFactorInput struct {
	Challenge string `json:"challenge"`
	Code      string `json:"code"`
}

// StepUpVerificationInput verifies a logged-in user's current 2FA code.
type StepUpVerificationInput struct {
	Method string `json:"method"`
	Code   string `json:"code"`
}

// StepUpVerificationResult returns the short-lived token for sensitive routes.
type StepUpVerificationResult struct {
	VerificationToken string `json:"verification_token"`
	ExpiresIn         int64  `json:"expires_in"`
	Method            string `json:"method"`
}

func NewTwoFactorService(repos *repository.Repos, tokenManager *auth.TokenManager, securitySecret string) *TwoFactorService {
	key, err := config.DecodeSecuritySecret(securitySecret)
	if err != nil {
		return &TwoFactorService{repos: repos, tokenManager: tokenManager, initErr: err}
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return &TwoFactorService{repos: repos, tokenManager: tokenManager, initErr: err}
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return &TwoFactorService{repos: repos, tokenManager: tokenManager, initErr: err}
	}
	return &TwoFactorService{repos: repos, tokenManager: tokenManager, aead: aead, securityKey: key}
}

// Status returns a user's current two-factor state without exposing secrets.
func (s *TwoFactorService) Status(ctx context.Context, userID uuid.UUID) (*TwoFactorStatus, error) {
	tf, err := s.repos.TwoFactor.Get(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("get two factor: %w", err)
	}
	status := &TwoFactorStatus{}
	if tf == nil {
		return status, nil
	}
	status.Enabled = tf.Enabled
	status.LockedUntil = activeLock(tf)
	status.Locked = status.LockedUntil != nil
	status.LastUsedAt = tf.LastUsedAt
	status.EnabledAt = tf.EnabledAt
	if tf.Enabled {
		count, err := s.repos.TwoFactor.CountUnusedBackupCodes(ctx, userID)
		if err != nil {
			return nil, err
		}
		status.BackupCodesRemaining = count
	}
	return status, nil
}

// Setup creates or replaces a pending TOTP secret and backup code set.
func (s *TwoFactorService) Setup(ctx context.Context, userID uuid.UUID) (*TwoFactorSetupResult, error) {
	if err := s.ready(); err != nil {
		return nil, err
	}
	user, err := s.repos.Users.GetByID(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("get user: %w", err)
	}
	if user == nil || user.Status != model.StatusActive {
		return nil, ErrUserNotFound
	}
	existing, err := s.repos.TwoFactor.Get(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("get two factor: %w", err)
	}
	if existing != nil && existing.Enabled {
		return nil, ErrTwoFactorEnabled
	}

	key, err := totp.Generate(totp.GenerateOpts{
		Issuer:      twoFactorIssuer,
		AccountName: user.Email,
		Period:      30,
		Digits:      otp.DigitsSix,
		Algorithm:   otp.AlgorithmSHA1,
	})
	if err != nil {
		return nil, fmt.Errorf("generate totp secret: %w", err)
	}
	secret := key.Secret()
	encrypted, err := s.encrypt(secret)
	if err != nil {
		return nil, err
	}
	backupCodes, err := generateBackupCodes()
	if err != nil {
		return nil, err
	}
	hashes := s.hashBackupCodes(backupCodes)
	if err := s.repos.TwoFactor.UpsertSetup(ctx, userID, encrypted); err != nil {
		return nil, err
	}
	if err := s.repos.TwoFactor.ReplaceBackupCodes(ctx, userID, hashes); err != nil {
		return nil, err
	}

	return &TwoFactorSetupResult{
		Secret:      secret,
		OTPAuthURL:  key.URL(),
		BackupCodes: backupCodes,
	}, nil
}

// Enable verifies the pending TOTP secret and turns on two-factor protection.
func (s *TwoFactorService) Enable(ctx context.Context, userID uuid.UUID, code string) error {
	tf, err := s.getConfigured(ctx, userID)
	if err != nil {
		return err
	}
	if tf.Enabled {
		return ErrTwoFactorEnabled
	}
	if err := s.verifyTOTPOnly(ctx, tf, code); err != nil {
		return err
	}
	return s.repos.TwoFactor.Enable(ctx, userID, time.Now())
}

// Disable verifies a current code and removes all two-factor state.
func (s *TwoFactorService) Disable(ctx context.Context, userID uuid.UUID, code string) error {
	tf, err := s.getEnabled(ctx, userID)
	if err != nil {
		return err
	}
	if err := s.verifyCode(ctx, tf, code); err != nil {
		return err
	}
	return s.repos.TwoFactor.DeleteByUser(ctx, userID)
}

// RegenerateBackupCodes verifies a current code and replaces all backup codes.
func (s *TwoFactorService) RegenerateBackupCodes(ctx context.Context, userID uuid.UUID, code string) ([]string, error) {
	tf, err := s.getEnabled(ctx, userID)
	if err != nil {
		return nil, err
	}
	if err := s.verifyCode(ctx, tf, code); err != nil {
		return nil, err
	}
	backupCodes, err := generateBackupCodes()
	if err != nil {
		return nil, err
	}
	hashes := s.hashBackupCodes(backupCodes)
	if err := s.repos.TwoFactor.ReplaceBackupCodes(ctx, userID, hashes); err != nil {
		return nil, err
	}
	return backupCodes, nil
}

// Login validates a two-factor challenge plus TOTP or backup code and issues
// normal access and refresh tokens.
func (s *TwoFactorService) Login(ctx context.Context, input LoginTwoFactorInput) (*RegisterResult, error) {
	if strings.TrimSpace(input.Challenge) == "" {
		return nil, ErrTwoFactorChallenge
	}
	userID, err := s.tokenManager.ValidateLoginChallengeToken(input.Challenge)
	if err != nil {
		return nil, ErrTwoFactorChallenge
	}
	user, err := s.repos.Users.GetByID(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("get user: %w", err)
	}
	if user == nil || user.Status != model.StatusActive {
		return nil, ErrTwoFactorChallenge
	}
	tf, err := s.getEnabled(ctx, userID)
	if err != nil {
		if errors.Is(err, ErrTwoFactorNotEnabled) {
			return nil, ErrTwoFactorChallenge
		}
		return nil, err
	}
	if err := s.verifyCode(ctx, tf, input.Code); err != nil {
		return nil, err
	}
	return s.issueTokens(ctx, user)
}

// VerifyStepUp validates the current user's 2FA code and issues a short-lived
// verification token for sensitive operations.
func (s *TwoFactorService) VerifyStepUp(ctx context.Context, userID uuid.UUID, input StepUpVerificationInput) (*StepUpVerificationResult, error) {
	method := strings.ToLower(strings.TrimSpace(input.Method))
	if method != auth.StepUpMethodTOTP {
		return nil, ErrTwoFactorInvalidCode
	}
	tf, err := s.getEnabled(ctx, userID)
	if err != nil {
		return nil, err
	}
	if err := s.verifyTOTPOnly(ctx, tf, input.Code); err != nil {
		return nil, err
	}
	token, expiresIn, err := s.tokenManager.IssueStepUpToken(userID, method)
	if err != nil {
		return nil, err
	}
	return &StepUpVerificationResult{
		VerificationToken: token,
		ExpiresIn:         expiresIn,
		Method:            method,
	}, nil
}

func (s *TwoFactorService) ready() error {
	if s.initErr != nil {
		return fmt.Errorf("two factor crypto: %w", s.initErr)
	}
	if s.aead == nil {
		return fmt.Errorf("two factor crypto is not configured")
	}
	return nil
}

func (s *TwoFactorService) getConfigured(ctx context.Context, userID uuid.UUID) (*model.TwoFactor, error) {
	if err := s.ready(); err != nil {
		return nil, err
	}
	tf, err := s.repos.TwoFactor.Get(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("get two factor: %w", err)
	}
	if tf == nil {
		return nil, ErrTwoFactorNotEnabled
	}
	return tf, nil
}

func (s *TwoFactorService) getEnabled(ctx context.Context, userID uuid.UUID) (*model.TwoFactor, error) {
	tf, err := s.getConfigured(ctx, userID)
	if err != nil {
		return nil, err
	}
	if !tf.Enabled {
		return nil, ErrTwoFactorNotEnabled
	}
	return tf, nil
}

func (s *TwoFactorService) verifyTOTPOnly(ctx context.Context, tf *model.TwoFactor, code string) error {
	if locked := activeLock(tf); locked != nil {
		return ErrTwoFactorLocked
	}
	secret, err := s.decrypt(tf.SecretEncrypted)
	if err != nil {
		return err
	}
	if !validateTOTP(secret, code) {
		return s.recordInvalid(ctx, tf.UserID)
	}
	if err := s.repos.TwoFactor.RecordSuccess(ctx, tf.UserID, time.Now()); err != nil {
		return err
	}
	return nil
}

func (s *TwoFactorService) verifyCode(ctx context.Context, tf *model.TwoFactor, code string) error {
	if locked := activeLock(tf); locked != nil {
		return ErrTwoFactorLocked
	}
	secret, err := s.decrypt(tf.SecretEncrypted)
	if err != nil {
		return err
	}
	if validateTOTP(secret, code) {
		return s.repos.TwoFactor.RecordSuccess(ctx, tf.UserID, time.Now())
	}
	ok, err := s.consumeBackupCode(ctx, tf.UserID, code)
	if err != nil {
		return err
	}
	if ok {
		return s.repos.TwoFactor.RecordSuccess(ctx, tf.UserID, time.Now())
	}
	return s.recordInvalid(ctx, tf.UserID)
}

func (s *TwoFactorService) recordInvalid(ctx context.Context, userID uuid.UUID) error {
	_, lockedUntil, err := s.repos.TwoFactor.RecordFailure(ctx, userID, twoFactorMaxFailures, time.Now().Add(twoFactorLockDuration))
	if err != nil {
		return err
	}
	if lockedUntil != nil && time.Now().Before(*lockedUntil) {
		return ErrTwoFactorLocked
	}
	return ErrTwoFactorInvalidCode
}

func (s *TwoFactorService) consumeBackupCode(ctx context.Context, userID uuid.UUID, code string) (bool, error) {
	normalized, ok := normalizeBackupCode(code)
	if !ok {
		return false, nil
	}
	codes, err := s.repos.TwoFactor.ListUnusedBackupCodes(ctx, userID)
	if err != nil {
		return false, err
	}
	for _, stored := range codes {
		if !s.backupCodeHashMatches(normalized, stored.CodeHash) {
			continue
		}
		used, err := s.repos.TwoFactor.MarkBackupCodeUsed(ctx, stored.ID, time.Now())
		if err != nil {
			return false, err
		}
		return used, nil
	}
	return false, nil
}

func (s *TwoFactorService) issueTokens(ctx context.Context, user *model.User) (*RegisterResult, error) {
	accessToken, err := s.tokenManager.IssueAccessToken(user.ID, string(user.Tier))
	if err != nil {
		return nil, fmt.Errorf("issue access token: %w", err)
	}
	refreshToken, tokenHash, err := s.tokenManager.IssueRefreshToken(user.ID)
	if err != nil {
		return nil, fmt.Errorf("issue refresh token: %w", err)
	}
	if err := s.repos.RefreshTokens.SaveRefreshToken(ctx, user.ID, tokenHash, "", time.Now().Add(30*24*time.Hour)); err != nil {
		return nil, fmt.Errorf("save refresh token: %w", err)
	}
	return &RegisterResult{
		User:         *user,
		AccessToken:  accessToken,
		RefreshToken: refreshToken,
		ExpiresIn:    900,
	}, nil
}

func (s *TwoFactorService) encrypt(plaintext string) ([]byte, error) {
	nonce := make([]byte, s.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("generate nonce: %w", err)
	}
	out := make([]byte, 0, len(nonce)+len(plaintext)+s.aead.Overhead())
	out = append(out, nonce...)
	out = s.aead.Seal(out, nonce, []byte(plaintext), nil)
	return out, nil
}

func (s *TwoFactorService) decrypt(ciphertext []byte) (string, error) {
	if err := s.ready(); err != nil {
		return "", err
	}
	if len(ciphertext) <= s.aead.NonceSize() {
		return "", fmt.Errorf("two factor secret ciphertext is invalid")
	}
	nonce := ciphertext[:s.aead.NonceSize()]
	data := ciphertext[s.aead.NonceSize():]
	plaintext, err := s.aead.Open(nil, nonce, data, nil)
	if err != nil {
		return "", fmt.Errorf("decrypt two factor secret: %w", err)
	}
	return string(plaintext), nil
}

func validateTOTP(secret, code string) bool {
	clean := strings.ReplaceAll(strings.TrimSpace(code), " ", "")
	if len(clean) != 6 {
		return false
	}
	for _, ch := range clean {
		if ch < '0' || ch > '9' {
			return false
		}
	}
	ok, err := totp.ValidateCustom(clean, secret, time.Now(), totp.ValidateOpts{
		Period:    30,
		Skew:      1,
		Digits:    otp.DigitsSix,
		Algorithm: otp.AlgorithmSHA1,
	})
	return err == nil && ok
}

func (s *TwoFactorService) hashBackupCodes(codes []string) []string {
	hashes := make([]string, 0, len(codes))
	for _, code := range codes {
		normalized, _ := normalizeBackupCode(code)
		hashes = append(hashes, s.hashBackupCode(normalized))
	}
	return hashes
}

func (s *TwoFactorService) hashBackupCode(normalized string) string {
	mac := hmac.New(sha256.New, s.securityKey)
	mac.Write([]byte("hsync:2fa:backup-code:v1:"))
	mac.Write([]byte(normalized))
	return "hmac-sha256:" + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func (s *TwoFactorService) backupCodeHashMatches(normalized, storedHash string) bool {
	want := s.hashBackupCode(normalized)
	return subtle.ConstantTimeCompare([]byte(want), []byte(storedHash)) == 1
}

func generateBackupCodes() ([]string, error) {
	codes := make([]string, 0, twoFactorBackupCount)
	for len(codes) < twoFactorBackupCount {
		code, err := randomBackupCode()
		if err != nil {
			return nil, err
		}
		codes = append(codes, formatBackupCode(code))
	}
	return codes, nil
}

func randomBackupCode() (string, error) {
	const alphabet = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789"
	raw := make([]byte, twoFactorBackupLength)
	random := make([]byte, twoFactorBackupLength)
	if _, err := rand.Read(random); err != nil {
		return "", fmt.Errorf("generate backup code: %w", err)
	}
	for i, b := range random {
		raw[i] = alphabet[int(b)%len(alphabet)]
	}
	return string(raw), nil
}

func normalizeBackupCode(code string) (string, bool) {
	clean := strings.ToUpper(strings.TrimSpace(code))
	clean = strings.NewReplacer("-", "", " ", "").Replace(clean)
	if len(clean) != twoFactorBackupLength {
		return "", false
	}
	for _, ch := range clean {
		if (ch < 'A' || ch > 'Z') && (ch < '0' || ch > '9') {
			return "", false
		}
	}
	return clean, true
}

func formatBackupCode(code string) string {
	return code[:4] + "-" + code[4:]
}

func activeLock(tf *model.TwoFactor) *time.Time {
	if tf == nil || tf.LockedUntil == nil || !time.Now().Before(*tf.LockedUntil) {
		return nil
	}
	return tf.LockedUntil
}
