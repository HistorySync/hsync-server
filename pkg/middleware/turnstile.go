package middleware

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/historysync/hsync-server/pkg/apierrors"
)

const defaultTurnstileVerifyURL = "https://challenges.cloudflare.com/turnstile/v0/siteverify"

var (
	ErrTurnstileFailed      = errors.New("turnstile verification failed")
	ErrTurnstileUnavailable = errors.New("turnstile verification unavailable")
)

// TurnstileVerifier verifies one Turnstile token for a request.
type TurnstileVerifier interface {
	Verify(ctx context.Context, token string, remoteIP string) error
}

// TurnstileConfig configures Turnstile protection for auth handlers.
type TurnstileConfig struct {
	Enabled  bool
	Verifier TurnstileVerifier
}

// CloudflareTurnstileVerifier verifies tokens with Cloudflare's siteverify API.
type CloudflareTurnstileVerifier struct {
	Secret   string
	Endpoint string
	Client   *http.Client
}

// NewCloudflareTurnstileVerifier creates a Cloudflare-backed verifier.
func NewCloudflareTurnstileVerifier(secret string, timeout time.Duration) *CloudflareTurnstileVerifier {
	if timeout <= 0 {
		timeout = 3 * time.Second
	}
	return &CloudflareTurnstileVerifier{
		Secret:   secret,
		Endpoint: defaultTurnstileVerifyURL,
		Client:   &http.Client{Timeout: timeout},
	}
}

type turnstileVerifyResponse struct {
	Success bool `json:"success"`
}

// Verify implements TurnstileVerifier.
func (v *CloudflareTurnstileVerifier) Verify(ctx context.Context, token string, remoteIP string) error {
	if v == nil || strings.TrimSpace(v.Secret) == "" {
		return ErrTurnstileUnavailable
	}
	endpoint := strings.TrimSpace(v.Endpoint)
	if endpoint == "" {
		endpoint = defaultTurnstileVerifyURL
	}
	client := v.Client
	if client == nil {
		client = http.DefaultClient
	}

	form := url.Values{
		"secret":   {v.Secret},
		"response": {token},
		"remoteip": {remoteIP},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return ErrTurnstileUnavailable
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := client.Do(req)
	if err != nil {
		return ErrTurnstileUnavailable
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 500 {
		return ErrTurnstileUnavailable
	}
	if resp.StatusCode >= 400 {
		return ErrTurnstileFailed
	}

	var result turnstileVerifyResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return ErrTurnstileUnavailable
	}
	if !result.Success {
		return ErrTurnstileFailed
	}
	return nil
}

// EnforceTurnstile verifies a parsed turnstile_token value when protection is enabled.
func EnforceTurnstile(c fiber.Ctx, cfg TurnstileConfig, token string) error {
	if !cfg.Enabled {
		return nil
	}
	token = strings.TrimSpace(token)
	if token == "" {
		return apierrors.New(apierrors.CodeTurnstileRequired, "")
	}
	if cfg.Verifier == nil {
		return apierrors.New(apierrors.CodeTurnstileUnavailable, "")
	}

	err := cfg.Verifier.Verify(c.Context(), token, c.IP())
	switch {
	case err == nil:
		return nil
	case errors.Is(err, ErrTurnstileFailed):
		return apierrors.New(apierrors.CodeTurnstileFailed, "")
	default:
		return apierrors.New(apierrors.CodeTurnstileUnavailable, "")
	}
}
