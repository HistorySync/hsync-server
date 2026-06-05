// Package middleware provides shared HTTP middleware for HistorySync services.
package middleware

import (
	"context"
	"strconv"
	"sync"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/historysync/hsync-server/pkg/apierrors"
	"github.com/redis/go-redis/v9"
	"github.com/rs/zerolog/log"
)

// Result is the outcome of a rate-limit check for one request.
type Result struct {
	Allowed    bool
	Limit      int
	Remaining  int
	RetryAfter time.Duration
}

// Limiter enforces a fixed-window request limit per key. Implementations must
// be safe for concurrent use.
type Limiter interface {
	// Allow records one request against key and reports whether it is within
	// limit for the current window. A non-positive limit or window allows the
	// request unconditionally.
	Allow(ctx context.Context, key string, limit int, window time.Duration) (Result, error)
}

// decide builds a Result from the post-increment count for a fixed window.
func decide(count, limit int, bucket, windowSecs int64, now time.Time) Result {
	resetAtUnix := (bucket + 1) * windowSecs
	retryAfter := time.Duration(resetAtUnix-now.Unix()) * time.Second
	if retryAfter < 0 {
		retryAfter = 0
	}
	remaining := limit - count
	if remaining < 0 {
		remaining = 0
	}
	return Result{
		Allowed:    count <= limit,
		Limit:      limit,
		Remaining:  remaining,
		RetryAfter: retryAfter,
	}
}

func windowSeconds(window time.Duration) int64 {
	secs := int64(window / time.Second)
	if secs <= 0 {
		secs = 1
	}
	return secs
}

// ── MemoryLimiter ────────────────────────────────────────────

type memoryBucket struct {
	bucket    int64
	count     int
	expiresAt time.Time
}

// MemoryLimiter is an in-process fixed-window limiter used when Redis is
// unavailable. In a single-instance deployment it is authoritative; across
// multiple instances each enforces its own local window.
type MemoryLimiter struct {
	mu      sync.Mutex
	buckets map[string]*memoryBucket
	now     func() time.Time
}

// NewMemoryLimiter creates an empty in-process limiter.
func NewMemoryLimiter() *MemoryLimiter {
	return &MemoryLimiter{
		buckets: make(map[string]*memoryBucket),
		now:     time.Now,
	}
}

// Allow implements Limiter.
func (m *MemoryLimiter) Allow(_ context.Context, key string, limit int, window time.Duration) (Result, error) {
	if limit <= 0 || window <= 0 {
		return Result{Allowed: true, Limit: limit}, nil
	}
	now := m.now()
	secs := windowSeconds(window)
	bucket := now.Unix() / secs

	m.mu.Lock()
	b := m.buckets[key]
	if b == nil || b.bucket != bucket {
		b = &memoryBucket{bucket: bucket}
		m.buckets[key] = b
	}
	b.count++
	b.expiresAt = time.Unix((bucket+1)*secs, 0)
	count := b.count
	m.mu.Unlock()

	return decide(count, limit, bucket, secs, now), nil
}

// Run periodically evicts expired buckets until ctx is cancelled. Callers
// should start it in a goroutine and cancel ctx on shutdown.
func (m *MemoryLimiter) Run(ctx context.Context) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.sweep()
		}
	}
}

func (m *MemoryLimiter) sweep() {
	now := m.now()
	m.mu.Lock()
	defer m.mu.Unlock()
	for key, b := range m.buckets {
		if now.After(b.expiresAt) {
			delete(m.buckets, key)
		}
	}
}

// ── RedisLimiter ─────────────────────────────────────────────

// RedisLimiter is a fixed-window limiter backed by Redis, so the window is
// shared across all server instances.
type RedisLimiter struct {
	client *redis.Client
}

// NewRedisLimiter creates a Redis-backed limiter.
func NewRedisLimiter(client *redis.Client) *RedisLimiter {
	return &RedisLimiter{client: client}
}

// Allow implements Limiter. The per-bucket key carries its own TTL so expired
// windows clean themselves up.
func (r *RedisLimiter) Allow(ctx context.Context, key string, limit int, window time.Duration) (Result, error) {
	if limit <= 0 || window <= 0 {
		return Result{Allowed: true, Limit: limit}, nil
	}
	now := time.Now()
	secs := windowSeconds(window)
	bucket := now.Unix() / secs
	rkey := "ratelimit:" + key + ":" + strconv.FormatInt(bucket, 10)

	pipe := r.client.Pipeline()
	incr := pipe.Incr(ctx, rkey)
	pipe.Expire(ctx, rkey, window+time.Second)
	if _, err := pipe.Exec(ctx); err != nil {
		return Result{}, err
	}

	return decide(int(incr.Val()), limit, bucket, secs, now), nil
}

// ── Middleware ───────────────────────────────────────────────

// RateDecision describes how to limit a single request. A zero Limit, empty
// Key, or Skip means the request is not rate limited.
type RateDecision struct {
	Key   string
	Limit int
	Skip  bool
}

// RateLimitConfig configures the RateLimit middleware.
type RateLimitConfig struct {
	Limiter  Limiter
	Window   time.Duration
	Classify func(fiber.Ctx) RateDecision
}

// RateLimit returns middleware that enforces per-key request limits.
//
// On a limiter error it fails open (allows the request) so a backend blip does
// not take down the API; on limit exhaustion it returns 429 in the standard
// error shape with Retry-After and X-RateLimit-* headers.
func RateLimit(cfg RateLimitConfig) fiber.Handler {
	return func(c fiber.Ctx) error {
		if cfg.Limiter == nil || cfg.Classify == nil {
			return c.Next()
		}
		d := cfg.Classify(c)
		allowed, err := EnforceRateLimit(c, cfg.Limiter, cfg.Window, d)
		if err != nil || !allowed {
			return err
		}

		return c.Next()
	}
}

// EnforceRateLimit applies one concrete rate-limit decision and writes the
// standard 429 response when the limit is exhausted.
func EnforceRateLimit(c fiber.Ctx, limiter Limiter, window time.Duration, d RateDecision) (bool, error) {
	if limiter == nil || d.Skip || d.Limit <= 0 || d.Key == "" {
		return true, nil
	}

	res, err := limiter.Allow(c.Context(), d.Key, d.Limit, window)
	if err != nil {
		log.Warn().Err(err).Str("key", d.Key).Msg("rate limiter error, allowing request")
		return true, nil
	}

	c.Set("X-RateLimit-Limit", strconv.Itoa(res.Limit))
	c.Set("X-RateLimit-Remaining", strconv.Itoa(res.Remaining))

	if res.Allowed {
		return true, nil
	}

	retry := int(res.RetryAfter.Seconds())
	if retry < 1 {
		retry = 1
	}
	c.Set("Retry-After", strconv.Itoa(retry))
	return false, c.Status(fiber.StatusTooManyRequests).JSON(fiber.Map{
		"request_id": GetRequestID(c),
		"error": fiber.Map{
			"code":    string(apierrors.CodeRateLimited),
			"message": "rate limit exceeded, retry later",
		},
	})
}
