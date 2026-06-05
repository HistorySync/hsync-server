// Package auth provides JWT token management and Fiber middleware for
// authenticating API requests to the HistorySync Cloud Server.
package auth

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/historysync/hsync-server/pkg/apierrors"
	"github.com/historysync/hsync-server/pkg/config"
)

// ── TokenManager ─────────────────────────────────────────────

// TokenConfig configures token lifetimes.
type TokenConfig struct {
	AccessTTL  time.Duration
	RefreshTTL time.Duration
}

// TokenManager issues and validates JWTs using Ed25519.
type TokenManager struct {
	privateKey ed25519.PrivateKey
	publicKey  ed25519.PublicKey
	accessTTL  time.Duration
	refreshTTL time.Duration
}

// StepUpClaims captures the validated short-lived verification token payload.
type StepUpClaims struct {
	UserID  uuid.UUID
	Purpose string
	Method  string
}

const (
	tokenTypeAccess         = "access"
	tokenTypeLoginChallenge = "login_2fa"
	tokenTypeStepUp         = "verification"
	stepUpPurpose           = "step_up"
	StepUpMethodTOTP        = "totp"
	StepUpMethodPasskey     = "passkey"
	StepUpHeader            = "X-HSync-Verification"
	loginChallengeTTL       = 5 * time.Minute
	stepUpTTL               = 5 * time.Minute
)

var (
	ErrStepUpExpired = errors.New("step-up verification token is expired")
	ErrStepUpInvalid = errors.New("invalid step-up verification token")
)

// NewTokenManager creates a TokenManager from a base64-encoded Ed25519 seed.
func NewTokenManager(encodedKey string, cfg TokenConfig) (*TokenManager, error) {
	priv, err := config.DecodeEd25519PrivateKey(encodedKey)
	if err != nil {
		return nil, fmt.Errorf("jwt key: %w", err)
	}
	return &TokenManager{
		privateKey: priv,
		publicKey:  priv.Public().(ed25519.PublicKey),
		accessTTL:  cfg.AccessTTL,
		refreshTTL: cfg.RefreshTTL,
	}, nil
}

// IssueAccessToken creates a short-lived JWT for API authentication.
// The token payload includes user ID, tier, and token JTI for revocation.
func (tm *TokenManager) IssueAccessToken(userID uuid.UUID, tier string) (string, error) {
	now := time.Now()
	claims := jwt.MapClaims{
		"sub":  userID.String(),
		"tier": tier,
		"typ":  tokenTypeAccess,
		"iat":  jwt.NewNumericDate(now),
		"exp":  jwt.NewNumericDate(now.Add(tm.accessTTL)),
		"iss":  "historysync",
		"jti":  uuid.New().String(),
	}
	token := jwt.NewWithClaims(jwt.SigningMethodEdDSA, claims)
	return token.SignedString(tm.privateKey)
}

// IssueLoginChallengeToken creates a short-lived JWT used only to complete a
// password-verified login for accounts with two-factor authentication enabled.
func (tm *TokenManager) IssueLoginChallengeToken(userID uuid.UUID) (string, int64, error) {
	now := time.Now()
	claims := jwt.MapClaims{
		"sub": userID.String(),
		"typ": tokenTypeLoginChallenge,
		"iat": jwt.NewNumericDate(now),
		"exp": jwt.NewNumericDate(now.Add(loginChallengeTTL)),
		"iss": "historysync",
		"jti": uuid.New().String(),
	}
	token := jwt.NewWithClaims(jwt.SigningMethodEdDSA, claims)
	signed, err := token.SignedString(tm.privateKey)
	if err != nil {
		return "", 0, err
	}
	return signed, int64(loginChallengeTTL / time.Second), nil
}

// IssueStepUpToken creates a short-lived JWT proving the user recently
// completed a sensitive-operation verification method.
func (tm *TokenManager) IssueStepUpToken(userID uuid.UUID, method string) (string, int64, error) {
	if !validStepUpMethod(method) {
		return "", 0, ErrStepUpInvalid
	}
	now := time.Now()
	claims := jwt.MapClaims{
		"sub":     userID.String(),
		"typ":     tokenTypeStepUp,
		"purpose": stepUpPurpose,
		"method":  method,
		"iat":     jwt.NewNumericDate(now),
		"exp":     jwt.NewNumericDate(now.Add(stepUpTTL)),
		"iss":     "historysync",
		"jti":     uuid.New().String(),
	}
	token := jwt.NewWithClaims(jwt.SigningMethodEdDSA, claims)
	signed, err := token.SignedString(tm.privateKey)
	if err != nil {
		return "", 0, err
	}
	return signed, int64(stepUpTTL / time.Second), nil
}

// IssueRefreshToken creates a long-lived refresh token (stored server-side).
// Returns the raw token string and its SHA-256 hash for DB storage.
func (tm *TokenManager) IssueRefreshToken(userID uuid.UUID) (tokenStr string, tokenHash []byte, err error) {
	// Refresh tokens are random 256-bit strings, not JWTs.
	// This avoids JWT revocation problems — the server stores a hash.
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", nil, err
	}
	tokenStr = base64.RawURLEncoding.EncodeToString(raw)
	sum := sha256.Sum256([]byte(tokenStr))
	return tokenStr, sum[:], nil
}

// ValidateAccessToken parses and validates an access token.
// Returns the claims map if valid, or an error.
func (tm *TokenManager) ValidateAccessToken(tokenString string) (jwt.MapClaims, error) {
	token, err := jwt.Parse(tokenString, tm.keyfunc,
		jwt.WithValidMethods([]string{"EdDSA"}),
		jwt.WithIssuer("historysync"),
		jwt.WithLeeway(30*time.Second),
	)
	if err != nil {
		return nil, fmt.Errorf("parse token: %w", err)
	}
	claims, ok := token.Claims.(jwt.MapClaims)
	if !ok || !token.Valid {
		return nil, fmt.Errorf("invalid token claims")
	}
	if typ, _ := claims["typ"].(string); typ != "" && typ != tokenTypeAccess {
		return nil, fmt.Errorf("invalid token type")
	}
	if _, ok := claims["tier"].(string); !ok {
		return nil, fmt.Errorf("missing tier claim")
	}
	return claims, nil
}

// ValidateLoginChallengeToken validates a two-factor login challenge and returns
// the user ID that already passed password authentication.
func (tm *TokenManager) ValidateLoginChallengeToken(tokenString string) (uuid.UUID, error) {
	token, err := jwt.Parse(tokenString, tm.keyfunc,
		jwt.WithValidMethods([]string{"EdDSA"}),
		jwt.WithIssuer("historysync"),
		jwt.WithLeeway(30*time.Second),
	)
	if err != nil {
		return uuid.Nil, fmt.Errorf("parse token: %w", err)
	}
	claims, ok := token.Claims.(jwt.MapClaims)
	if !ok || !token.Valid {
		return uuid.Nil, fmt.Errorf("invalid token claims")
	}
	if typ, _ := claims["typ"].(string); typ != tokenTypeLoginChallenge {
		return uuid.Nil, fmt.Errorf("invalid token type")
	}
	sub, _ := claims["sub"].(string)
	userID, err := uuid.Parse(sub)
	if err != nil {
		return uuid.Nil, fmt.Errorf("invalid subject")
	}
	return userID, nil
}

// ValidateStepUpToken validates a step-up verification token and returns its
// security-sensitive claims.
func (tm *TokenManager) ValidateStepUpToken(tokenString string) (*StepUpClaims, error) {
	token, err := jwt.Parse(tokenString, tm.keyfunc,
		jwt.WithValidMethods([]string{"EdDSA"}),
		jwt.WithIssuer("historysync"),
		jwt.WithExpirationRequired(),
	)
	if err != nil {
		if errors.Is(err, jwt.ErrTokenExpired) {
			return nil, fmt.Errorf("%w: %v", ErrStepUpExpired, err)
		}
		return nil, fmt.Errorf("%w: %v", ErrStepUpInvalid, err)
	}
	claims, ok := token.Claims.(jwt.MapClaims)
	if !ok || !token.Valid {
		return nil, ErrStepUpInvalid
	}
	if typ, _ := claims["typ"].(string); typ != tokenTypeStepUp {
		return nil, ErrStepUpInvalid
	}
	purpose, _ := claims["purpose"].(string)
	if purpose != stepUpPurpose {
		return nil, ErrStepUpInvalid
	}
	method, _ := claims["method"].(string)
	if !validStepUpMethod(method) {
		return nil, ErrStepUpInvalid
	}
	sub, _ := claims["sub"].(string)
	userID, err := uuid.Parse(sub)
	if err != nil {
		return nil, ErrStepUpInvalid
	}
	return &StepUpClaims{
		UserID:  userID,
		Purpose: purpose,
		Method:  method,
	}, nil
}

// keyfunc returns the Ed25519 public key for verifying JWT signatures.
func (tm *TokenManager) keyfunc(token *jwt.Token) (interface{}, error) {
	if _, ok := token.Method.(*jwt.SigningMethodEd25519); !ok {
		return nil, fmt.Errorf("unexpected signing method: %v", token.Header["alg"])
	}
	return tm.publicKey, nil
}

// ── Fiber Middleware ─────────────────────────────────────────

// AuthMiddleware validates JWT Bearer tokens and injects user context.
// On success, the following keys are set in c.Locals:
//   - "user_id" (string)
//   - "tier"    (string)
//   - "token_jti" (string)
func AuthMiddleware(tm *TokenManager) fiber.Handler {
	return func(c fiber.Ctx) error {
		authHeader := c.Get("Authorization")
		if authHeader == "" {
			return fiber.NewError(fiber.StatusUnauthorized, "missing Authorization header")
		}

		// Support both "Bearer <token>" and just "<token>"
		tokenStr := authHeader
		if strings.HasPrefix(authHeader, "Bearer ") {
			tokenStr = strings.TrimPrefix(authHeader, "Bearer ")
		}

		claims, err := tm.ValidateAccessToken(tokenStr)
		if err != nil {
			return fiber.NewError(fiber.StatusUnauthorized, fmt.Sprintf("invalid token: %v", err))
		}

		// Inject user context
		c.Locals("user_id", claims["sub"])
		c.Locals("tier", claims["tier"])
		c.Locals("token_jti", claims["jti"])

		return c.Next()
	}
}

// StepUpMiddleware requires a valid short-lived verification token for
// sensitive authenticated routes. It must run after AuthMiddleware.
func StepUpMiddleware(tm *TokenManager) fiber.Handler {
	return func(c fiber.Ctx) error {
		tokenStr := strings.TrimSpace(c.Get(StepUpHeader))
		if tokenStr == "" {
			return apierrors.New(apierrors.CodeStepUpRequired, "step-up verification is required")
		}

		userID := UserID(c)
		if userID == uuid.Nil {
			return apierrors.New(apierrors.CodeStepUpInvalid, "step-up verification requires an authenticated user")
		}

		claims, err := tm.ValidateStepUpToken(tokenStr)
		if err != nil {
			if errors.Is(err, ErrStepUpExpired) {
				return apierrors.New(apierrors.CodeStepUpExpired, ErrStepUpExpired.Error())
			}
			return apierrors.New(apierrors.CodeStepUpInvalid, ErrStepUpInvalid.Error())
		}
		if claims.UserID != userID || claims.Purpose != stepUpPurpose || !validStepUpMethod(claims.Method) {
			return apierrors.New(apierrors.CodeStepUpInvalid, ErrStepUpInvalid.Error())
		}

		c.Locals("verification_method", claims.Method)
		return c.Next()
	}
}

func validStepUpMethod(method string) bool {
	return method == StepUpMethodTOTP || method == StepUpMethodPasskey
}

// AdminMiddleware validates the X-Admin-Key header for admin API access.
func AdminMiddleware(adminKey string) fiber.Handler {
	return func(c fiber.Ctx) error {
		if adminKey == "" {
			return fiber.NewError(fiber.StatusForbidden, "admin API not configured")
		}
		key := c.Get("X-Admin-Key")
		if key == "" {
			key = c.Get("x-admin-key")
		}
		if key == "" {
			return fiber.NewError(fiber.StatusUnauthorized, "missing X-Admin-Key header")
		}
		if subtle.ConstantTimeCompare([]byte(key), []byte(adminKey)) != 1 {
			return fiber.NewError(fiber.StatusForbidden, "invalid admin key")
		}
		return c.Next()
	}
}

// UserID extracts the authenticated user ID from request context.
// Returns uuid.Nil if not authenticated.
func UserID(c fiber.Ctx) uuid.UUID {
	raw, ok := c.Locals("user_id").(string)
	if !ok {
		return uuid.Nil
	}
	id, err := uuid.Parse(raw)
	if err != nil {
		return uuid.Nil
	}
	return id
}
