package provider

import (
	"context"

	"github.com/rs/zerolog/log"
)

type LogNotifier struct{}

func NewLogNotifier() *LogNotifier {
	return &LogNotifier{}
}

func (n *LogNotifier) DeliveryEnabled() bool {
	return false
}

func (n *LogNotifier) SendWelcome(_ context.Context, p WelcomeParams) error {
	log.Info().
		Str("user_id", p.UserID).
		Str("email", maskEmail(p.Email)).
		Msg("welcome notification skipped: no delivery provider configured")
	return nil
}

func (n *LogNotifier) SendPasswordReset(_ context.Context, p PasswordResetParams) error {
	log.Warn().
		Str("user_id", p.UserID).
		Str("email", maskEmail(p.Email)).
		Dur("expires_in", p.ExpiresIn).
		Msg("password reset notification skipped: no delivery provider configured")
	return nil
}

func (n *LogNotifier) SendQuotaWarning(_ context.Context, p QuotaWarningParams) error {
	log.Warn().
		Str("user_id", p.UserID).
		Str("email", maskEmail(p.Email)).
		Int("usage_percent", p.UsagePercent).
		Int64("usage_bytes", p.UsageBytes).
		Int64("limit_bytes", p.LimitBytes).
		Msg("quota warning notification skipped: no delivery provider configured")
	return nil
}

func (n *LogNotifier) SendQuotaExhausted(_ context.Context, p QuotaExhaustedParams) error {
	log.Error().
		Str("user_id", p.UserID).
		Str("email", maskEmail(p.Email)).
		Int("usage_percent", p.UsagePercent).
		Int64("usage_bytes", p.UsageBytes).
		Int64("limit_bytes", p.LimitBytes).
		Msg("quota exhausted notification skipped: no delivery provider configured")
	return nil
}

func (n *LogNotifier) SendQuotaRestored(_ context.Context, p QuotaRestoredParams) error {
	log.Info().
		Str("user_id", p.UserID).
		Str("email", maskEmail(p.Email)).
		Int("usage_percent", p.UsagePercent).
		Int64("usage_bytes", p.UsageBytes).
		Int64("limit_bytes", p.LimitBytes).
		Msg("quota restored notification skipped: no delivery provider configured")
	return nil
}

func maskEmail(email string) string {
	for i := 0; i < len(email); i++ {
		if email[i] == '@' {
			return "***" + email[i:]
		}
	}
	if email == "" {
		return ""
	}
	return "***"
}
