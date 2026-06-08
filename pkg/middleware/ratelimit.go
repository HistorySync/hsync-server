// Package middleware provides shared HTTP middleware for HistorySync services.
package middleware

import (
	"context"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/historysync/hsync-server/pkg/apierrors"
	"github.com/historysync/hsync-server/pkg/observability"
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

// RateLimitFailMode controls how middleware behaves when the backing limiter
// returns an error. The default remains fail-open for compatibility.
type RateLimitFailMode string

const (
	RateLimitFailOpen   RateLimitFailMode = "fail_open"
	RateLimitFailClosed RateLimitFailMode = "fail_closed"
)

// NormalizeRateLimitFailMode returns a supported fail mode, defaulting to
// fail-open when unset.
func NormalizeRateLimitFailMode(raw string) RateLimitFailMode {
	switch RateLimitFailMode(strings.ToLower(strings.TrimSpace(raw))) {
	case RateLimitFailClosed:
		return RateLimitFailClosed
	default:
		return RateLimitFailOpen
	}
}

// ValidRateLimitFailMode reports whether raw names a supported fail mode.
func ValidRateLimitFailMode(raw string) bool {
	switch RateLimitFailMode(strings.ToLower(strings.TrimSpace(raw))) {
	case RateLimitFailOpen, RateLimitFailClosed:
		return true
	default:
		return false
	}
}

// RedisUnavailableFallback controls startup behavior when Redis is configured
// but cannot be reached.
type RedisUnavailableFallback string

const (
	RedisFallbackMemory  RedisUnavailableFallback = "memory"
	RedisFallbackDeny    RedisUnavailableFallback = "deny"
	RedisFallbackDisable RedisUnavailableFallback = "disable"
)

// NormalizeRedisUnavailableFallback returns a supported Redis fallback mode.
func NormalizeRedisUnavailableFallback(raw string) RedisUnavailableFallback {
	switch RedisUnavailableFallback(strings.ToLower(strings.TrimSpace(raw))) {
	case RedisFallbackDeny:
		return RedisFallbackDeny
	case RedisFallbackDisable:
		return RedisFallbackDisable
	default:
		return RedisFallbackMemory
	}
}

// ValidRedisUnavailableFallback reports whether raw names a supported Redis
// fallback mode.
func ValidRedisUnavailableFallback(raw string) bool {
	switch RedisUnavailableFallback(strings.ToLower(strings.TrimSpace(raw))) {
	case RedisFallbackMemory, RedisFallbackDeny, RedisFallbackDisable:
		return true
	default:
		return false
	}
}

// RateLimitRuntimeConfig is the normalized operational policy passed to
// middleware at runtime.
type RateLimitRuntimeConfig struct {
	FailMode                 RateLimitFailMode
	PublicAuthMode           RateLimitFailMode
	EnterpriseAdminMode      RateLimitFailMode
	EnterpriseBillingMode    RateLimitFailMode
	RedisUnavailableFallback RedisUnavailableFallback
}

// NewRateLimitRuntimeConfig normalizes string config values into runtime
// policies. Empty bucket-specific fail modes inherit the default fail mode.
func NewRateLimitRuntimeConfig(defaultFailMode, publicAuthFailMode, enterpriseAdminFailMode, enterpriseBillingFailMode, redisFallback string) RateLimitRuntimeConfig {
	cfg := RateLimitRuntimeConfig{
		FailMode:                 NormalizeRateLimitFailMode(defaultFailMode),
		RedisUnavailableFallback: NormalizeRedisUnavailableFallback(redisFallback),
	}
	cfg.PublicAuthMode = normalizeBucketFailMode(publicAuthFailMode, cfg.FailMode)
	cfg.EnterpriseAdminMode = normalizeBucketFailMode(enterpriseAdminFailMode, cfg.FailMode)
	cfg.EnterpriseBillingMode = normalizeBucketFailMode(enterpriseBillingFailMode, cfg.FailMode)
	return cfg
}

func normalizeBucketFailMode(raw string, fallback RateLimitFailMode) RateLimitFailMode {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return fallback
	}
	return NormalizeRateLimitFailMode(raw)
}

func (cfg RateLimitRuntimeConfig) DefaultFailMode() RateLimitFailMode {
	if cfg.FailMode == "" {
		return RateLimitFailOpen
	}
	return cfg.FailMode
}

func (cfg RateLimitRuntimeConfig) PublicAuthFailMode() RateLimitFailMode {
	if cfg.PublicAuthMode == "" {
		return cfg.DefaultFailMode()
	}
	return cfg.PublicAuthMode
}

func (cfg RateLimitRuntimeConfig) EnterpriseAdminFailModeValue() RateLimitFailMode {
	if cfg.EnterpriseAdminMode == "" {
		return cfg.DefaultFailMode()
	}
	return cfg.EnterpriseAdminMode
}

func (cfg RateLimitRuntimeConfig) EnterpriseBillingFailModeValue() RateLimitFailMode {
	if cfg.EnterpriseBillingMode == "" {
		return cfg.DefaultFailMode()
	}
	return cfg.EnterpriseBillingMode
}

func (cfg RateLimitRuntimeConfig) RedisFallback() RedisUnavailableFallback {
	if cfg.RedisUnavailableFallback == "" {
		return RedisFallbackMemory
	}
	return cfg.RedisUnavailableFallback
}

// LimiterFallbackResult describes the limiter selected when Redis is absent.
type LimiterFallbackResult struct {
	Limiter Limiter
	Mode    RedisUnavailableFallback
}

// NewLimiterForRedisUnavailable applies the configured startup fallback when
// Redis is configured but unavailable. Memory is per process, deny fails all
// positive-limit buckets closed, and disable removes limiter enforcement.
func NewLimiterForRedisUnavailable(ctx context.Context, fallback RedisUnavailableFallback) LimiterFallbackResult {
	mode := fallback
	if mode == "" {
		mode = RedisFallbackMemory
	}
	switch mode {
	case RedisFallbackDeny:
		return LimiterFallbackResult{Limiter: NewDenyLimiter(), Mode: mode}
	case RedisFallbackDisable:
		return LimiterFallbackResult{Mode: mode}
	default:
		memLimiter := NewMemoryLimiter()
		if ctx != nil {
			go memLimiter.Run(ctx)
		}
		return LimiterFallbackResult{Limiter: memLimiter, Mode: RedisFallbackMemory}
	}
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

// DenyLimiter rejects every positive-limit request. It is used when operators
// choose redis_unavailable_fallback=deny so rate-limited routes fail closed
// until shared Redis is restored.
type DenyLimiter struct{}

// NewDenyLimiter creates a limiter that denies all positive-limit checks.
func NewDenyLimiter() *DenyLimiter {
	return &DenyLimiter{}
}

// Allow implements Limiter.
func (d *DenyLimiter) Allow(_ context.Context, _ string, limit int, window time.Duration) (Result, error) {
	if limit <= 0 || window <= 0 {
		return Result{Allowed: true, Limit: limit}, nil
	}
	return Result{Allowed: false, Limit: limit, Remaining: 0, RetryAfter: window}, nil
}

func windowSeconds(window time.Duration) int64 {
	secs := int64(window / time.Second)
	if secs <= 0 {
		secs = 1
	}
	return secs
}

// MemoryLimiter
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

// RedisLimiter
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

// Middleware
// RateDecision describes how to limit a single request. A zero Limit, empty
// Key, or Skip means the request is not rate limited.
type RateDecision struct {
	Key      string
	Limit    int
	Skip     bool
	Policy   string
	FailMode RateLimitFailMode
}

// RateLimitConfig configures the RateLimit middleware.
type RateLimitConfig struct {
	Limiter  Limiter
	Window   time.Duration
	Policy   string
	FailMode RateLimitFailMode
	Classify func(fiber.Ctx) RateDecision
}

// RateLimit returns middleware that enforces per-key request limits.
//
// On a limiter error it follows the configured fail mode; on limit exhaustion
// it returns 429 in the standard error shape with Retry-After and
// X-RateLimit-* headers. The fixed-window counter is global only when backed by
// Redis; in-memory mode is per process.
func RateLimit(cfg RateLimitConfig) fiber.Handler {
	return func(c fiber.Ctx) error {
		if cfg.Limiter == nil || cfg.Classify == nil {
			return c.Next()
		}
		d := cfg.Classify(c)
		d = mergeRateDecisionDefaults(d, cfg.Policy, cfg.FailMode)
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
	d.Policy = normalizeRateLimitPolicy(d.Policy)
	if d.FailMode == "" {
		d.FailMode = RateLimitFailOpen
	}

	res, err := limiter.Allow(c.Context(), d.Key, d.Limit, window)
	if err != nil {
		if d.FailMode == RateLimitFailClosed {
			observability.RecordRateLimitError(d.Policy, string(d.FailMode), "deny")
			log.Error().Err(err).Str("key", d.Key).Str("policy", d.Policy).Str("fail_mode", string(d.FailMode)).Msg("rate limiter error, denying request")
			return false, rateLimitUnavailableResponse(c)
		}
		observability.RecordRateLimitError(d.Policy, string(d.FailMode), "allow")
		log.Warn().Err(err).Str("key", d.Key).Str("policy", d.Policy).Str("fail_mode", string(d.FailMode)).Msg("rate limiter error, allowing request")
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

func mergeRateDecisionDefaults(d RateDecision, policy string, failMode RateLimitFailMode) RateDecision {
	if d.Policy == "" {
		d.Policy = policy
	}
	if d.FailMode == "" {
		d.FailMode = failMode
	}
	return d
}

func normalizeRateLimitPolicy(policy string) string {
	policy = strings.ToLower(strings.TrimSpace(policy))
	if policy == "" {
		return "default"
	}
	return policy
}

func rateLimitUnavailableResponse(c fiber.Ctx) error {
	return c.Status(fiber.StatusTooManyRequests).JSON(fiber.Map{
		"request_id": GetRequestID(c),
		"error": fiber.Map{
			"code":    string(apierrors.CodeRateLimited),
			"message": "rate limiter unavailable, request denied",
		},
	})
}
