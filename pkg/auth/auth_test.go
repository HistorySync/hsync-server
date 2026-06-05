package auth

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/google/uuid"
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
