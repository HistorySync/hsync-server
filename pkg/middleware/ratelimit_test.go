package middleware

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/historysync/hsync-server/pkg/apierrors"
)

func TestMemoryLimiterAllowsUpToLimit(t *testing.T) {
	cur := time.Unix(1_000_000, 0)
	m := NewMemoryLimiter()
	m.now = func() time.Time { return cur }

	for i := 1; i <= 3; i++ {
		res, err := m.Allow(context.Background(), "k", 3, time.Minute)
		if err != nil {
			t.Fatalf("Allow() error = %v", err)
		}
		if !res.Allowed {
			t.Fatalf("request %d denied, want allowed", i)
		}
	}

	res, _ := m.Allow(context.Background(), "k", 3, time.Minute)
	if res.Allowed {
		t.Fatal("4th request allowed, want denied")
	}
	if res.Remaining != 0 {
		t.Fatalf("remaining = %d, want 0", res.Remaining)
	}
	if res.RetryAfter <= 0 {
		t.Fatalf("retryAfter = %v, want > 0", res.RetryAfter)
	}
}

func TestMemoryLimiterResetsNextWindow(t *testing.T) {
	cur := time.Unix(1_000_000, 0)
	m := NewMemoryLimiter()
	m.now = func() time.Time { return cur }

	if res, _ := m.Allow(context.Background(), "k", 1, time.Minute); !res.Allowed {
		t.Fatal("1st request denied, want allowed")
	}
	if res, _ := m.Allow(context.Background(), "k", 1, time.Minute); res.Allowed {
		t.Fatal("2nd request in window allowed, want denied")
	}

	cur = cur.Add(time.Minute) // advance into the next fixed window
	if res, _ := m.Allow(context.Background(), "k", 1, time.Minute); !res.Allowed {
		t.Fatal("request in new window denied, want allowed")
	}
}

func TestMemoryLimiterSeparateKeys(t *testing.T) {
	cur := time.Unix(1_000_000, 0)
	m := NewMemoryLimiter()
	m.now = func() time.Time { return cur }

	_, _ = m.Allow(context.Background(), "a", 1, time.Minute)
	if res, _ := m.Allow(context.Background(), "b", 1, time.Minute); !res.Allowed {
		t.Fatal("key b limited by key a's usage, want independent")
	}
}

func TestMemoryLimiterNonPositiveLimitAllows(t *testing.T) {
	m := NewMemoryLimiter()
	if res, _ := m.Allow(context.Background(), "k", 0, time.Minute); !res.Allowed {
		t.Fatal("zero limit denied, want allowed (disabled)")
	}
}

func TestMemoryLimiterSweepEvictsExpired(t *testing.T) {
	cur := time.Unix(1_000_000, 0)
	m := NewMemoryLimiter()
	m.now = func() time.Time { return cur }

	_, _ = m.Allow(context.Background(), "k", 1, time.Minute)
	cur = cur.Add(2 * time.Minute)
	m.sweep()

	m.mu.Lock()
	_, present := m.buckets["k"]
	m.mu.Unlock()
	if present {
		t.Fatal("expired bucket not evicted by sweep")
	}
}

func TestDecideBoundary(t *testing.T) {
	now := time.Unix(120, 0) // bucket 2 for a 60s window

	if r := decide(3, 3, 2, 60, now); !r.Allowed || r.Remaining != 0 {
		t.Fatalf("count==limit: allowed=%v remaining=%d, want allowed remaining 0", r.Allowed, r.Remaining)
	}

	r := decide(4, 3, 2, 60, now)
	if r.Allowed {
		t.Fatal("count>limit allowed, want denied")
	}
	if r.RetryAfter != 60*time.Second { // resetAt = (2+1)*60 = 180; 180-120 = 60
		t.Fatalf("retryAfter = %v, want 60s", r.RetryAfter)
	}
}

func TestAuthEmailRateDecisionForValueNormalizesEmail(t *testing.T) {
	d := AuthEmailRateDecisionForValue("auth:login:email", 5, " User@Example.COM ")
	if d.Key != "auth:login:email:user@example.com" {
		t.Fatalf("key = %q", d.Key)
	}
	if d.Limit != 5 {
		t.Fatalf("limit = %d, want 5", d.Limit)
	}
	if d.Skip {
		t.Fatal("Skip = true, want false")
	}
}

func TestAuthTokenRateDecisionForValueHashesToken(t *testing.T) {
	const token = "reset-token-secret"
	sum := sha256.Sum256([]byte(token))
	expectedKey := "auth:reset:token:" + hex.EncodeToString(sum[:])

	d := AuthTokenRateDecisionForValue("auth:reset:token", 5, token)
	if d.Key != expectedKey {
		t.Fatalf("key = %q, want %q", d.Key, expectedKey)
	}
	if d.Limit != 5 {
		t.Fatalf("limit = %d, want 5", d.Limit)
	}
}

func TestRateLimitExhaustionSetsHeadersAndErrorCode(t *testing.T) {
	cur := time.Unix(1_000_000, 0)
	limiter := NewMemoryLimiter()
	limiter.now = func() time.Time { return cur }
	calls := 0

	app := fiber.New()
	app.Post("/", func(c fiber.Ctx) error {
		calls++
		return c.SendStatus(fiber.StatusNoContent)
	}, RateLimit(RateLimitConfig{
		Limiter: limiter,
		Window:  time.Minute,
		Classify: func(fiber.Ctx) RateDecision {
			return RateDecision{Key: "auth:login:email:user@example.com", Limit: 1}
		},
	}))

	for i := 0; i < 2; i++ {
		req := httptest.NewRequest(fiber.MethodPost, "/", nil)
		resp, err := app.Test(req)
		if err != nil {
			t.Fatalf("app.Test() request %d error = %v", i+1, err)
		}
		defer func() { _ = resp.Body.Close() }()

		if i == 0 && resp.StatusCode != fiber.StatusNoContent {
			t.Fatalf("first status = %d, want %d", resp.StatusCode, fiber.StatusNoContent)
		}
		if i == 1 {
			if resp.StatusCode != fiber.StatusTooManyRequests {
				t.Fatalf("second status = %d, want %d", resp.StatusCode, fiber.StatusTooManyRequests)
			}
			if got := resp.Header.Get("Retry-After"); got == "" {
				t.Fatal("Retry-After header is empty")
			}
			if got := resp.Header.Get("X-RateLimit-Limit"); got != "1" {
				t.Fatalf("X-RateLimit-Limit = %q, want 1", got)
			}
			if got := resp.Header.Get("X-RateLimit-Remaining"); got != "0" {
				t.Fatalf("X-RateLimit-Remaining = %q, want 0", got)
			}
			body, err := io.ReadAll(resp.Body)
			if err != nil {
				t.Fatalf("ReadAll() error = %v", err)
			}
			if !strings.Contains(string(body), string(apierrors.CodeRateLimited)) {
				t.Fatalf("body = %s, want %s", body, apierrors.CodeRateLimited)
			}
		}
	}

	if calls != 1 {
		t.Fatalf("handler calls = %d, want 1", calls)
	}
}
