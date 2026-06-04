package middleware

import (
	"context"
	"testing"
	"time"
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
