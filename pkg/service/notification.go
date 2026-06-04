package service

import (
	"context"
	"fmt"
	"net/url"
	"path"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog/log"

	"github.com/historysync/hsync-server/pkg/model"
	"github.com/historysync/hsync-server/pkg/provider"
	"github.com/historysync/hsync-server/pkg/repository"
)

const passwordResetTokenTTL = time.Hour
const emailVerificationTokenTTL = 24 * time.Hour

type NotificationConfig struct {
	Enabled            bool
	AppName            string
	PublicURL          string
	WarningThreshold   int
	ExhaustedThreshold int
	EmailVerifyPath    string
	PasswordResetPath  string
	BackgroundTimeout  time.Duration
}

// NotificationService coordinates user-facing notifications. Delivery is
// best-effort: callers should never fail core user actions solely because an
// email or webhook could not be sent.
type NotificationService struct {
	notifier provider.Notifier
	users    *repository.UserRepo
	config   NotificationConfig
}

func NewNotificationService(repos *repository.Repos, notifier provider.Notifier, cfg NotificationConfig) *NotificationService {
	if notifier == nil {
		notifier = provider.NewLogNotifier()
	}
	if cfg.AppName == "" {
		cfg.AppName = "HistorySync Cloud"
	}
	if cfg.PublicURL == "" {
		cfg.PublicURL = "http://localhost:8080"
	}
	if cfg.WarningThreshold == 0 {
		cfg.WarningThreshold = 80
	}
	if cfg.ExhaustedThreshold == 0 {
		cfg.ExhaustedThreshold = 100
	}
	if cfg.EmailVerifyPath == "" {
		cfg.EmailVerifyPath = "/verify-email"
	}
	if cfg.PasswordResetPath == "" {
		cfg.PasswordResetPath = "/reset-password"
	}
	if cfg.BackgroundTimeout == 0 {
		cfg.BackgroundTimeout = 10 * time.Second
	}
	var users *repository.UserRepo
	if repos != nil {
		users = repos.Users
	}
	return &NotificationService{notifier: notifier, users: users, config: cfg}
}

func (s *NotificationService) DeliveryEnabled() bool {
	return s != nil && s.config.Enabled && s.notifier != nil && s.notifier.DeliveryEnabled()
}

func (s *NotificationService) SendWelcomeEmail(ctx context.Context, email, displayName string) error {
	if s == nil || strings.TrimSpace(email) == "" {
		return nil
	}
	return s.dispatch(ctx, func(ctx context.Context) error {
		return s.notifier.SendWelcome(ctx, provider.WelcomeParams{
			Email:       email,
			DisplayName: displayName,
			AppName:     s.config.AppName,
		})
	})
}

func (s *NotificationService) SendWelcomeEmailAsync(email, displayName string) {
	s.runAsync("welcome notification", func(ctx context.Context) error {
		return s.SendWelcomeEmail(ctx, email, displayName)
	})
}

func (s *NotificationService) SendEmailVerification(ctx context.Context, userID uuid.UUID, email, displayName, token string) error {
	if s == nil || strings.TrimSpace(email) == "" || token == "" {
		return nil
	}
	verificationURL := s.emailVerificationURL(token)
	return s.dispatch(ctx, func(ctx context.Context) error {
		return s.notifier.SendEmailVerification(ctx, provider.EmailVerificationParams{
			UserID:          userID.String(),
			Email:           email,
			DisplayName:     displayName,
			AppName:         s.config.AppName,
			VerificationURL: verificationURL,
			ExpiresIn:       emailVerificationTokenTTL,
		})
	})
}

func (s *NotificationService) SendEmailVerificationAsync(userID uuid.UUID, email, displayName, token string) {
	s.runAsync("email verification notification", func(ctx context.Context) error {
		return s.SendEmailVerification(ctx, userID, email, displayName, token)
	})
}

func (s *NotificationService) SendPasswordReset(ctx context.Context, userID uuid.UUID, email, displayName, token string) error {
	if s == nil || strings.TrimSpace(email) == "" || token == "" {
		return nil
	}
	resetURL := s.passwordResetURL(token)
	return s.dispatch(ctx, func(ctx context.Context) error {
		return s.notifier.SendPasswordReset(ctx, provider.PasswordResetParams{
			UserID:      userID.String(),
			Email:       email,
			DisplayName: displayName,
			AppName:     s.config.AppName,
			ResetURL:    resetURL,
			ExpiresIn:   passwordResetTokenTTL,
		})
	})
}

func (s *NotificationService) SendPasswordResetAsync(userID uuid.UUID, email, displayName, token string) {
	s.runAsync("password reset notification", func(ctx context.Context) error {
		return s.SendPasswordReset(ctx, userID, email, displayName, token)
	})
}

func (s *NotificationService) NotifyQuotaWarning(ctx context.Context, userID uuid.UUID, usage model.QuotaUsage, limitBytes int64) error {
	user, err := s.notificationUser(ctx, userID)
	if err != nil {
		return err
	}
	percent := usagePercent(usage.TotalBytes, limitBytes)
	return s.dispatch(ctx, func(ctx context.Context) error {
		return s.notifier.SendQuotaWarning(ctx, provider.QuotaWarningParams{
			UserID:        user.ID.String(),
			Email:         user.Email,
			DisplayName:   user.DisplayName,
			AppName:       s.config.AppName,
			UsageBytes:    usage.TotalBytes,
			LimitBytes:    limitBytes,
			UsagePercent:  percent,
			BundleCount:   int64(usage.BundleCount),
			SnapshotCount: int64(usage.SnapCount),
		})
	})
}

func (s *NotificationService) NotifyQuotaExhausted(ctx context.Context, userID uuid.UUID, usage model.QuotaUsage, limitBytes int64) error {
	user, err := s.notificationUser(ctx, userID)
	if err != nil {
		return err
	}
	percent := usagePercent(usage.TotalBytes, limitBytes)
	return s.dispatch(ctx, func(ctx context.Context) error {
		return s.notifier.SendQuotaExhausted(ctx, provider.QuotaExhaustedParams{
			UserID:        user.ID.String(),
			Email:         user.Email,
			DisplayName:   user.DisplayName,
			AppName:       s.config.AppName,
			UsageBytes:    usage.TotalBytes,
			LimitBytes:    limitBytes,
			UsagePercent:  percent,
			BundleCount:   int64(usage.BundleCount),
			SnapshotCount: int64(usage.SnapCount),
		})
	})
}

func (s *NotificationService) NotifyQuotaRestored(ctx context.Context, userID uuid.UUID, usage model.QuotaUsage, limitBytes int64) error {
	user, err := s.notificationUser(ctx, userID)
	if err != nil {
		return err
	}
	percent := usagePercent(usage.TotalBytes, limitBytes)
	return s.dispatch(ctx, func(ctx context.Context) error {
		return s.notifier.SendQuotaRestored(ctx, provider.QuotaRestoredParams{
			UserID:        user.ID.String(),
			Email:         user.Email,
			DisplayName:   user.DisplayName,
			AppName:       s.config.AppName,
			UsageBytes:    usage.TotalBytes,
			LimitBytes:    limitBytes,
			UsagePercent:  percent,
			BundleCount:   int64(usage.BundleCount),
			SnapshotCount: int64(usage.SnapCount),
		})
	})
}

func (s *NotificationService) MaybeNotifyQuotaIncrease(userID uuid.UUID, before, after model.QuotaUsage, limitBytes int64) {
	if s == nil || limitBytes <= 0 {
		return
	}
	switch quotaIncreaseEvent(before.TotalBytes, after.TotalBytes, limitBytes, s.config.WarningThreshold, s.config.ExhaustedThreshold) {
	case "exhausted":
		s.runAsync("quota exhausted notification", func(ctx context.Context) error {
			return s.NotifyQuotaExhausted(ctx, userID, after, limitBytes)
		})
	case "warning":
		s.runAsync("quota warning notification", func(ctx context.Context) error {
			return s.NotifyQuotaWarning(ctx, userID, after, limitBytes)
		})
	}
}

func (s *NotificationService) MaybeNotifyQuotaRestored(userID uuid.UUID, before, after model.QuotaUsage, limitBytes int64) {
	if s == nil || limitBytes <= 0 {
		return
	}
	if quotaRestored(before.TotalBytes, after.TotalBytes, limitBytes, s.config.ExhaustedThreshold) {
		s.runAsync("quota restored notification", func(ctx context.Context) error {
			return s.NotifyQuotaRestored(ctx, userID, after, limitBytes)
		})
	}
}

func (s *NotificationService) dispatch(ctx context.Context, send func(context.Context) error) error {
	if s == nil || s.notifier == nil {
		return nil
	}
	if !s.config.Enabled && s.notifier.DeliveryEnabled() {
		return nil
	}
	return send(ctx)
}

func (s *NotificationService) runAsync(label string, send func(context.Context) error) {
	if s == nil {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), s.config.BackgroundTimeout)
		defer cancel()
		if err := send(ctx); err != nil {
			log.Warn().Err(err).Msg(label + " failed")
		}
	}()
}

func (s *NotificationService) notificationUser(ctx context.Context, userID uuid.UUID) (*model.User, error) {
	if s == nil || s.users == nil {
		return nil, fmt.Errorf("notification user repository is not configured")
	}
	user, err := s.users.GetByID(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("get notification user: %w", err)
	}
	if user == nil {
		return nil, ErrUserNotFound
	}
	return user, nil
}

func (s *NotificationService) passwordResetURL(token string) string {
	return s.linkWithToken(s.config.PasswordResetPath, token)
}

func (s *NotificationService) emailVerificationURL(token string) string {
	return s.linkWithToken(s.config.EmailVerifyPath, token)
}

func (s *NotificationService) linkWithToken(linkPath, token string) string {
	base, err := url.Parse(s.config.PublicURL)
	if err != nil {
		return token
	}
	if !strings.HasPrefix(linkPath, "/") {
		linkPath = "/" + linkPath
	}
	base.Path = path.Join(base.Path, linkPath)
	query := base.Query()
	query.Set("token", token)
	base.RawQuery = query.Encode()
	return base.String()
}

func usagePercent(usageBytes, limitBytes int64) int {
	if limitBytes <= 0 {
		return 0
	}
	percent := usageBytes * 100 / limitBytes
	if percent > 100 {
		return 100
	}
	if percent < 0 {
		return 0
	}
	return int(percent)
}

func quotaIncreaseEvent(beforeBytes, afterBytes, limitBytes int64, warningThreshold, exhaustedThreshold int) string {
	if limitBytes <= 0 {
		return ""
	}
	oldPercent := usagePercent(beforeBytes, limitBytes)
	newPercent := usagePercent(afterBytes, limitBytes)
	if oldPercent < exhaustedThreshold && newPercent >= exhaustedThreshold {
		return "exhausted"
	}
	if oldPercent < warningThreshold && newPercent >= warningThreshold && newPercent < exhaustedThreshold {
		return "warning"
	}
	return ""
}

func quotaRestored(beforeBytes, afterBytes, limitBytes int64, exhaustedThreshold int) bool {
	if limitBytes <= 0 {
		return false
	}
	return usagePercent(beforeBytes, limitBytes) >= exhaustedThreshold &&
		usagePercent(afterBytes, limitBytes) < exhaustedThreshold
}
