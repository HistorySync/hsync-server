package middleware

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gofiber/fiber/v3"
	"github.com/historysync/hsync-server/pkg/apierrors"
)

type fakeTurnstileVerifier struct {
	err    error
	calls  int
	token  string
	remote string
}

func (f *fakeTurnstileVerifier) Verify(_ context.Context, token string, remoteIP string) error {
	f.calls++
	f.token = token
	f.remote = remoteIP
	return f.err
}

func newTurnstileTestApp(cfg TurnstileConfig) *fiber.App {
	app := fiber.New(fiber.Config{
		ErrorHandler: func(c fiber.Ctx, err error) error {
			if e, ok := err.(*apierrors.Error); ok {
				return c.Status(e.HTTPStatus).JSON(fiber.Map{
					"error": fiber.Map{
						"code": string(e.Code),
					},
				})
			}
			return err
		},
	})
	app.Post("/", func(c fiber.Ctx) error {
		var req struct {
			Token string `json:"turnstile_token"`
		}
		if len(c.Body()) > 0 {
			if err := json.Unmarshal(c.Body(), &req); err != nil {
				return err
			}
		}
		if err := EnforceTurnstile(c, cfg, req.Token); err != nil {
			return err
		}
		return c.SendStatus(fiber.StatusNoContent)
	})
	return app
}

func TestEnforceTurnstileDisabledSkipsVerifier(t *testing.T) {
	verifier := &fakeTurnstileVerifier{}
	app := newTurnstileTestApp(TurnstileConfig{Enabled: false, Verifier: verifier})

	resp, err := app.Test(httptest.NewRequest(fiber.MethodPost, "/", nil))
	if err != nil {
		t.Fatalf("app.Test() error = %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != fiber.StatusNoContent {
		t.Fatalf("status = %d, want %d", resp.StatusCode, fiber.StatusNoContent)
	}
	if verifier.calls != 0 {
		t.Fatalf("verifier calls = %d, want 0", verifier.calls)
	}
}

func TestEnforceTurnstileRequiresToken(t *testing.T) {
	app := newTurnstileTestApp(TurnstileConfig{Enabled: true, Verifier: &fakeTurnstileVerifier{}})

	resp, err := app.Test(httptest.NewRequest(fiber.MethodPost, "/", nil))
	if err != nil {
		t.Fatalf("app.Test() error = %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != fiber.StatusBadRequest {
		t.Fatalf("status = %d, want %d", resp.StatusCode, fiber.StatusBadRequest)
	}
}

func TestEnforceTurnstileMapsVerifierFailure(t *testing.T) {
	app := newTurnstileTestApp(TurnstileConfig{
		Enabled:  true,
		Verifier: &fakeTurnstileVerifier{err: ErrTurnstileFailed},
	})

	req := httptest.NewRequest(fiber.MethodPost, "/", strings.NewReader(`{"turnstile_token":"bad"}`))
	req.Header.Set(fiber.HeaderContentType, fiber.MIMEApplicationJSON)
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("app.Test() error = %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != fiber.StatusForbidden {
		t.Fatalf("status = %d, want %d", resp.StatusCode, fiber.StatusForbidden)
	}
}

func TestEnforceTurnstileMapsVerifierUnavailable(t *testing.T) {
	app := newTurnstileTestApp(TurnstileConfig{
		Enabled:  true,
		Verifier: &fakeTurnstileVerifier{err: errors.New("network down")},
	})

	req := httptest.NewRequest(fiber.MethodPost, "/", strings.NewReader(`{"turnstile_token":"token"}`))
	req.Header.Set(fiber.HeaderContentType, fiber.MIMEApplicationJSON)
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("app.Test() error = %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != fiber.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d", resp.StatusCode, fiber.StatusServiceUnavailable)
	}
}

func TestEnforceTurnstileAcceptsValidToken(t *testing.T) {
	verifier := &fakeTurnstileVerifier{}
	app := newTurnstileTestApp(TurnstileConfig{Enabled: true, Verifier: verifier})

	req := httptest.NewRequest(fiber.MethodPost, "/", strings.NewReader(`{"turnstile_token":"ok"}`))
	req.Header.Set(fiber.HeaderContentType, fiber.MIMEApplicationJSON)
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("app.Test() error = %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != fiber.StatusNoContent {
		t.Fatalf("status = %d, want %d", resp.StatusCode, fiber.StatusNoContent)
	}
	if verifier.calls != 1 {
		t.Fatalf("verifier calls = %d, want 1", verifier.calls)
	}
	if verifier.token != "ok" {
		t.Fatalf("verifier token = %q, want ok", verifier.token)
	}
}
