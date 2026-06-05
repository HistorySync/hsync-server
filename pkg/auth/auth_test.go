package auth

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/historysync/hsync-server/pkg/apierrors"
)

func newTestTokenManager(t *testing.T) *TokenManager {
	t.Helper()
	seed := make([]byte, ed25519.SeedSize)
	for i := range seed {
		seed[i] = byte(i + 1)
	}
	tm, err := NewTokenManager(base64.StdEncoding.EncodeToString(seed), TokenConfig{
		AccessTTL:  15 * time.Minute,
		RefreshTTL: 30 * 24 * time.Hour,
	})
	if err != nil {
		t.Fatalf("NewTokenManager() error = %v", err)
	}
	return tm
}

func TestIssueAndValidateAccessToken(t *testing.T) {
	tm := newTestTokenManager(t)
	userID := uuid.New()

	token, err := tm.IssueAccessToken(userID, "pro")
	if err != nil {
		t.Fatalf("IssueAccessToken() error = %v", err)
	}

	claims, err := tm.ValidateAccessToken(token)
	if err != nil {
		t.Fatalf("ValidateAccessToken() error = %v", err)
	}
	if got := claims["sub"]; got != userID.String() {
		t.Fatalf("sub = %v, want %s", got, userID)
	}
	if got := claims["tier"]; got != "pro" {
		t.Fatalf("tier = %v, want pro", got)
	}
	if claims["jti"] == "" {
		t.Fatal("jti claim is empty")
	}
}

func TestValidateAccessTokenRejectsMalformedToken(t *testing.T) {
	tm := newTestTokenManager(t)
	if _, err := tm.ValidateAccessToken("not-a-jwt"); err == nil {
		t.Fatal("ValidateAccessToken() error = nil, want error")
	}
}

func TestLoginChallengeTokenRoundTrip(t *testing.T) {
	tm := newTestTokenManager(t)
	userID := uuid.New()

	token, expiresIn, err := tm.IssueLoginChallengeToken(userID)
	if err != nil {
		t.Fatalf("IssueLoginChallengeToken() error = %v", err)
	}
	if expiresIn <= 0 {
		t.Fatalf("expiresIn = %d, want > 0", expiresIn)
	}
	got, err := tm.ValidateLoginChallengeToken(token)
	if err != nil {
		t.Fatalf("ValidateLoginChallengeToken() error = %v", err)
	}
	if got != userID {
		t.Fatalf("challenge user = %s, want %s", got, userID)
	}
}

func TestAccessValidatorRejectsLoginChallenge(t *testing.T) {
	tm := newTestTokenManager(t)
	token, _, err := tm.IssueLoginChallengeToken(uuid.New())
	if err != nil {
		t.Fatalf("IssueLoginChallengeToken() error = %v", err)
	}
	if _, err := tm.ValidateAccessToken(token); err == nil {
		t.Fatal("ValidateAccessToken() accepted login challenge")
	}
}

func TestStepUpTokenRoundTrip(t *testing.T) {
	tm := newTestTokenManager(t)
	userID := uuid.New()

	token, expiresIn, err := tm.IssueStepUpToken(userID, StepUpMethodPasskey)
	if err != nil {
		t.Fatalf("IssueStepUpToken() error = %v", err)
	}
	if expiresIn != int64((5*time.Minute)/time.Second) {
		t.Fatalf("expiresIn = %d, want 300", expiresIn)
	}

	claims, err := tm.ValidateStepUpToken(token)
	if err != nil {
		t.Fatalf("ValidateStepUpToken() error = %v", err)
	}
	if claims.UserID != userID {
		t.Fatalf("step-up user = %s, want %s", claims.UserID, userID)
	}
	if claims.Purpose != stepUpPurpose {
		t.Fatalf("purpose = %q, want %q", claims.Purpose, stepUpPurpose)
	}
	if claims.Method != StepUpMethodPasskey {
		t.Fatalf("method = %q, want %q", claims.Method, StepUpMethodPasskey)
	}
}

func TestAccessValidatorRejectsStepUpToken(t *testing.T) {
	tm := newTestTokenManager(t)
	token, _, err := tm.IssueStepUpToken(uuid.New(), StepUpMethodTOTP)
	if err != nil {
		t.Fatalf("IssueStepUpToken() error = %v", err)
	}
	if _, err := tm.ValidateAccessToken(token); err == nil {
		t.Fatal("ValidateAccessToken() accepted step-up token")
	}
}

func TestValidateStepUpTokenReportsExpired(t *testing.T) {
	tm := newTestTokenManager(t)
	token := signStepUpToken(t, tm, uuid.New(), StepUpMethodTOTP, time.Now().Add(-time.Minute))

	if _, err := tm.ValidateStepUpToken(token); !errors.Is(err, ErrStepUpExpired) {
		t.Fatalf("ValidateStepUpToken() error = %v, want ErrStepUpExpired", err)
	}
}

func TestStepUpMiddleware(t *testing.T) {
	tm := newTestTokenManager(t)
	userID := uuid.New()
	accessToken, err := tm.IssueAccessToken(userID, "pro")
	if err != nil {
		t.Fatalf("IssueAccessToken() error = %v", err)
	}
	validStepUp, _, err := tm.IssueStepUpToken(userID, StepUpMethodPasskey)
	if err != nil {
		t.Fatalf("IssueStepUpToken() error = %v", err)
	}
	wrongUserStepUp, _, err := tm.IssueStepUpToken(uuid.New(), StepUpMethodTOTP)
	if err != nil {
		t.Fatalf("IssueStepUpToken(wrong user) error = %v", err)
	}
	expiredStepUp := signStepUpToken(t, tm, userID, StepUpMethodTOTP, time.Now().Add(-time.Minute))

	tests := []struct {
		name      string
		stepUp    string
		wantCode  int
		wantError apierrors.Code
	}{
		{
			name:      "missing",
			wantCode:  fiber.StatusForbidden,
			wantError: apierrors.CodeStepUpRequired,
		},
		{
			name:      "expired",
			stepUp:    expiredStepUp,
			wantCode:  fiber.StatusForbidden,
			wantError: apierrors.CodeStepUpExpired,
		},
		{
			name:      "wrong user",
			stepUp:    wrongUserStepUp,
			wantCode:  fiber.StatusForbidden,
			wantError: apierrors.CodeStepUpInvalid,
		},
		{
			name:     "success",
			stepUp:   validStepUp,
			wantCode: fiber.StatusNoContent,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			app := newStepUpTestApp(tm)
			req := httptest.NewRequest("POST", "/sensitive", nil)
			req.Header.Set("Authorization", "Bearer "+accessToken)
			if tt.stepUp != "" {
				req.Header.Set(StepUpHeader, tt.stepUp)
			}
			resp, err := app.Test(req)
			if err != nil {
				t.Fatalf("app.Test() error = %v", err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != tt.wantCode {
				t.Fatalf("status = %d, want %d", resp.StatusCode, tt.wantCode)
			}
			if tt.wantError != "" {
				if got := readAPIErrorCode(t, resp); got != string(tt.wantError) {
					t.Fatalf("error code = %q, want %q", got, tt.wantError)
				}
			}
		})
	}
}

func TestIssueRefreshTokenReturnsOpaqueTokenAndHash(t *testing.T) {
	tm := newTestTokenManager(t)

	token, hash, err := tm.IssueRefreshToken(uuid.New())
	if err != nil {
		t.Fatalf("IssueRefreshToken() error = %v", err)
	}
	if token == "" {
		t.Fatal("refresh token is empty")
	}
	if len(hash) != sha256.Size {
		t.Fatalf("hash length = %d, want %d", len(hash), sha256.Size)
	}
	want := sha256.Sum256([]byte(token))
	if string(hash) != string(want[:]) {
		t.Fatal("refresh token hash does not match token")
	}
}

func TestAdminMiddlewareAcceptsExactKey(t *testing.T) {
	app := fiber.New(fiber.Config{
		ErrorHandler: func(c fiber.Ctx, err error) error {
			if e, ok := err.(*fiber.Error); ok {
				return c.Status(e.Code).SendString(e.Message)
			}
			return c.Status(fiber.StatusInternalServerError).SendString(err.Error())
		},
	})
	app.Use(AdminMiddleware("Secret-Key"))
	app.Get("/admin", func(c fiber.Ctx) error {
		return c.SendStatus(fiber.StatusNoContent)
	})

	req := httptest.NewRequest("GET", "/admin", nil)
	req.Header.Set("X-Admin-Key", "Secret-Key")
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("app.Test() error = %v", err)
	}
	if resp.StatusCode != fiber.StatusNoContent {
		t.Fatalf("status = %d, want %d", resp.StatusCode, fiber.StatusNoContent)
	}
}

func TestAdminMiddlewareRejectsWrongKey(t *testing.T) {
	app := fiber.New(fiber.Config{
		ErrorHandler: func(c fiber.Ctx, err error) error {
			if e, ok := err.(*fiber.Error); ok {
				return c.Status(e.Code).SendString(e.Message)
			}
			return c.Status(fiber.StatusInternalServerError).SendString(err.Error())
		},
	})
	app.Use(AdminMiddleware("Secret-Key"))
	app.Get("/admin", func(c fiber.Ctx) error {
		return c.SendStatus(fiber.StatusNoContent)
	})

	req := httptest.NewRequest("GET", "/admin", nil)
	req.Header.Set("X-Admin-Key", "Secret-Key-other")
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("app.Test() error = %v", err)
	}
	if resp.StatusCode != fiber.StatusForbidden {
		t.Fatalf("status = %d, want %d", resp.StatusCode, fiber.StatusForbidden)
	}
}

func signStepUpToken(t *testing.T, tm *TokenManager, userID uuid.UUID, method string, expiresAt time.Time) string {
	t.Helper()
	claims := jwt.MapClaims{
		"sub":     userID.String(),
		"typ":     tokenTypeStepUp,
		"purpose": stepUpPurpose,
		"method":  method,
		"iat":     jwt.NewNumericDate(expiresAt.Add(-time.Minute)),
		"exp":     jwt.NewNumericDate(expiresAt),
		"iss":     "historysync",
		"jti":     uuid.New().String(),
	}
	token := jwt.NewWithClaims(jwt.SigningMethodEdDSA, claims)
	signed, err := token.SignedString(tm.privateKey)
	if err != nil {
		t.Fatalf("SignedString() error = %v", err)
	}
	return signed
}

func newStepUpTestApp(tm *TokenManager) *fiber.App {
	app := fiber.New(fiber.Config{
		ErrorHandler: func(c fiber.Ctx, err error) error {
			if apiErr, ok := err.(*apierrors.Error); ok {
				return c.Status(apiErr.HTTPStatus).JSON(fiber.Map{
					"error": fiber.Map{
						"code": apiErr.Code,
					},
				})
			}
			if fiberErr, ok := err.(*fiber.Error); ok {
				return c.Status(fiberErr.Code).SendString(fiberErr.Message)
			}
			return c.Status(fiber.StatusInternalServerError).SendString(err.Error())
		},
	})
	app.Post("/sensitive", func(c fiber.Ctx) error {
		return c.SendStatus(fiber.StatusNoContent)
	}, AuthMiddleware(tm), StepUpMiddleware(tm))
	return app
}

func readAPIErrorCode(t *testing.T, resp *http.Response) string {
	t.Helper()
	var body struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode error response: %v", err)
	}
	return body.Error.Code
}
