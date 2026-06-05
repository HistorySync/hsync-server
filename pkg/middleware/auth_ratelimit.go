package middleware

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"

	"github.com/gofiber/fiber/v3"
)

// AuthIPRateDecision limits an auth endpoint by client IP.
func AuthIPRateDecision(prefix string, limit int) func(fiber.Ctx) RateDecision {
	prefix = strings.TrimSpace(prefix)
	return func(c fiber.Ctx) RateDecision {
		ip := strings.TrimSpace(c.IP())
		if prefix == "" || ip == "" {
			return RateDecision{Skip: true}
		}
		return RateDecision{Key: prefix + ":" + ip, Limit: limit}
	}
}

// AuthEmailRateDecisionForValue limits an auth endpoint by a parsed email field.
func AuthEmailRateDecisionForValue(prefix string, limit int, email string) RateDecision {
	prefix = strings.TrimSpace(prefix)
	email = strings.ToLower(strings.TrimSpace(email))
	if prefix == "" || email == "" {
		return RateDecision{Skip: true}
	}
	return RateDecision{Key: prefix + ":" + email, Limit: limit}
}

// AuthTokenRateDecisionForValue limits an auth endpoint by a parsed token field.
// The token value is hashed before becoming part of the limiter key.
func AuthTokenRateDecisionForValue(prefix string, limit int, token string) RateDecision {
	prefix = strings.TrimSpace(prefix)
	token = strings.TrimSpace(token)
	if prefix == "" || token == "" {
		return RateDecision{Skip: true}
	}
	sum := sha256.Sum256([]byte(token))
	return RateDecision{Key: prefix + ":" + hex.EncodeToString(sum[:]), Limit: limit}
}
