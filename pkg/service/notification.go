package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"path"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog/log"

	"github.com/historysync/hsync-server/pkg/model"
	"github.com/historysync/hsync-server/pkg/observability"
	"github.com/historysync/hsync-server/pkg/provider"
	"github.com/historysync/hsync-server/pkg/repository"
)

const passwordResetTokenTTL = time.Hour
const emailVerificationTokenTTL = 24 * time.Hour

const (
	NotificationCategorySecurity = "security"
	NotificationCategoryBilling  = "billing"
)

const (
	notificationEmailGeneric        = "generic"
	notificationEmailQuotaWarning   = "quota_warning"
	notificationEmailQuotaExhausted = "quota_exhausted"
	notificationEmailQuotaRestored  = "quota_restored"
)

const (
	defaultNotificationOutboxBatchSize = int32(50)
	defaultNotificationMaxAttempts     = 5
	defaultNotificationBaseBackoff     = time.Minute
	defaultNotificationMaxBackoff      = time.Hour
	maxNotificationOutboxAdminBatch    = int32(200)
)

var (
	ErrWebhookURLRequired = errors.New("webhook_url is required when webhook notifications are enabled")
	ErrInvalidWebhookURL  = errors.New("invalid webhook_url")
)

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

type NotificationPreferenceStore interface {
	GetByUserID(ctx context.Context, userID uuid.UUID) (*model.NotificationPreferences, error)
	Upsert(ctx context.Context, prefs *model.NotificationPreferences) error
}

type NotificationOutboxStore interface {
	Enqueue(ctx context.Context, item *model.NotificationOutbox) error
	ClaimDue(ctx context.Context, now time.Time, limit int32) ([]model.NotificationOutbox, error)
	GetByID(ctx context.Context, id uuid.UUID) (*model.NotificationOutbox, error)
	ClaimFailedByID(ctx context.Context, id uuid.UUID) (*model.NotificationOutbox, error)
	ClaimFailed(ctx context.Context, limit int32) ([]model.NotificationOutbox, error)
	MarkSent(ctx context.Context, id uuid.UUID, sentAt time.Time) error
	MarkRetry(ctx context.Context, id uuid.UUID, nextRetryAt time.Time, errText string) error
	MarkFailed(ctx context.Context, id uuid.UUID, errText string) error
	RequeueFailed(ctx context.Context, id uuid.UUID, nextRetryAt time.Time) (bool, error)
	MarkDiscarded(ctx context.Context, id uuid.UUID) (bool, error)
	ListFailures(ctx context.Context, limit, offset int32) ([]model.NotificationOutbox, error)
}

type NotificationUserStore interface {
	GetByID(ctx context.Context, id uuid.UUID) (*model.User, error)
}

type NotificationPreferenceUpdate struct {
	SecurityEmail   *bool
	SecurityWebhook *bool
	BillingEmail    *bool
	BillingWebhook  *bool
	WebhookURL      *string
	WebhookSecret   *string
}

type NotificationInput struct {
	UserID          uuid.UUID
	Category        string
	Type            string
	Subject         string
	Message         string
	Data            map[string]any
	RequireDelivery bool
	EmailKind       string
}

// NotificationService coordinates user-facing notifications. Delivery is
// best-effort: callers should never fail core user actions solely because an
// email or webhook could not be sent.
type NotificationService struct {
	notifier    provider.Notifier
	webhook     provider.WebhookProvider
	users       NotificationUserStore
	preferences NotificationPreferenceStore
	outbox      NotificationOutboxStore
	config      NotificationConfig
}

func NewNotificationService(repos *repository.Repos, notifier provider.Notifier, cfg NotificationConfig) *NotificationService {
	var users NotificationUserStore
	var prefs NotificationPreferenceStore
	if repos != nil {
		users = repos.Users
		prefs = repos.NotificationPrefs
	}
	var outbox NotificationOutboxStore
	if repos != nil {
		outbox = repos.NotificationOutbox
	}
	return NewNotificationServiceWithStoresAndOutbox(users, prefs, outbox, notifier, provider.Registry().Webhook, cfg)
}

func NewNotificationServiceWithStores(users NotificationUserStore, prefs NotificationPreferenceStore, notifier provider.Notifier, webhook provider.WebhookProvider, cfg NotificationConfig) *NotificationService {
	return NewNotificationServiceWithStoresAndOutbox(users, prefs, nil, notifier, webhook, cfg)
}

func NewNotificationServiceWithStoresAndOutbox(users NotificationUserStore, prefs NotificationPreferenceStore, outbox NotificationOutboxStore, notifier provider.Notifier, webhook provider.WebhookProvider, cfg NotificationConfig) *NotificationService {
	if notifier == nil {
		notifier = provider.NewLogNotifier()
	}
	if webhook == nil {
		webhook = provider.NewWebhookNotifier(provider.WebhookConfig{})
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
	return &NotificationService{
		notifier:    notifier,
		webhook:     webhook,
		users:       users,
		preferences: prefs,
		outbox:      outbox,
		config:      cfg,
	}
}

func (s *NotificationService) DeliveryEnabled() bool {
	return s != nil && s.config.Enabled && s.notifier != nil && s.notifier.DeliveryEnabled()
}

func (s *NotificationService) WebhookDeliveryEnabled() bool {
	return s != nil && s.config.Enabled && s.webhook != nil && s.webhook.DeliveryEnabled()
}

func (s *NotificationService) GetPreferences(ctx context.Context, userID uuid.UUID) (*model.NotificationPreferences, error) {
	if s == nil {
		prefs := model.DefaultNotificationPreferences(userID)
		return &prefs, nil
	}
	if s.preferences == nil {
		prefs := model.DefaultNotificationPreferences(userID)
		return &prefs, nil
	}
	prefs, err := s.preferences.GetByUserID(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("get notification preferences: %w", err)
	}
	if prefs == nil {
		defaults := model.DefaultNotificationPreferences(userID)
		return &defaults, nil
	}
	return prefs, nil
}

func (s *NotificationService) UpdatePreferences(ctx context.Context, userID uuid.UUID, input NotificationPreferenceUpdate) (*model.NotificationPreferences, error) {
	if s == nil || s.preferences == nil {
		return nil, fmt.Errorf("notification preference repository is not configured")
	}
	prefs, err := s.GetPreferences(ctx, userID)
	if err != nil {
		return nil, err
	}
	if input.SecurityEmail != nil {
		prefs.SecurityEmail = *input.SecurityEmail
	}
	if input.SecurityWebhook != nil {
		prefs.SecurityWebhook = *input.SecurityWebhook
	}
	if input.BillingEmail != nil {
		prefs.BillingEmail = *input.BillingEmail
	}
	if input.BillingWebhook != nil {
		prefs.BillingWebhook = *input.BillingWebhook
	}
	if input.WebhookURL != nil {
		prefs.WebhookURL = strings.TrimSpace(*input.WebhookURL)
	}
	if input.WebhookSecret != nil {
		prefs.WebhookSecret = strings.TrimSpace(*input.WebhookSecret)
	}
	if err := validateNotificationPreferences(*prefs); err != nil {
		return nil, err
	}
	if err := s.preferences.Upsert(ctx, prefs); err != nil {
		return nil, fmt.Errorf("save notification preferences: %w", err)
	}
	return prefs, nil
}

func (s *NotificationService) SendNotification(ctx context.Context, input NotificationInput) error {
	if s == nil || input.UserID == uuid.Nil {
		return nil
	}
	user, err := s.notificationUser(ctx, input.UserID)
	if err != nil {
		return err
	}
	prefs, err := s.GetPreferences(ctx, input.UserID)
	if err != nil {
		return err
	}
	return s.dispatchUserNotification(ctx, user, prefs, input, nil)
}

func (s *NotificationService) SendNotificationAsync(input NotificationInput) {
	s.runAsync(input.Category+" notification", func(ctx context.Context) error {
		input.RequireDelivery = false
		return s.SendNotification(ctx, input)
	})
}

func validateNotificationPreferences(prefs model.NotificationPreferences) error {
	if prefs.SecurityWebhook || prefs.BillingWebhook {
		if strings.TrimSpace(prefs.WebhookURL) == "" {
			return ErrWebhookURLRequired
		}
	}
	if strings.TrimSpace(prefs.WebhookURL) != "" {
		if err := provider.ValidateWebhookURL(prefs.WebhookURL); err != nil {
			return fmt.Errorf("%w: %v", ErrInvalidWebhookURL, err)
		}
	}
	return nil
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
	prefs, err := s.GetPreferences(ctx, userID)
	if err != nil {
		return err
	}
	percent := usagePercent(usage.TotalBytes, limitBytes)
	return s.dispatchUserNotification(ctx, user, prefs, NotificationInput{
		UserID:    userID,
		Category:  NotificationCategoryBilling,
		Type:      "quota.warning",
		Subject:   "Storage quota warning",
		Message:   fmt.Sprintf("Storage usage reached %d%%.", percent),
		Data:      quotaNotificationData(usage, limitBytes, percent),
		EmailKind: notificationEmailQuotaWarning,
	}, func(ctx context.Context) error {
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
	prefs, err := s.GetPreferences(ctx, userID)
	if err != nil {
		return err
	}
	percent := usagePercent(usage.TotalBytes, limitBytes)
	return s.dispatchUserNotification(ctx, user, prefs, NotificationInput{
		UserID:    userID,
		Category:  NotificationCategoryBilling,
		Type:      "quota.exhausted",
		Subject:   "Storage quota exhausted",
		Message:   fmt.Sprintf("Storage usage reached %d%%.", percent),
		Data:      quotaNotificationData(usage, limitBytes, percent),
		EmailKind: notificationEmailQuotaExhausted,
	}, func(ctx context.Context) error {
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
	prefs, err := s.GetPreferences(ctx, userID)
	if err != nil {
		return err
	}
	percent := usagePercent(usage.TotalBytes, limitBytes)
	return s.dispatchUserNotification(ctx, user, prefs, NotificationInput{
		UserID:    userID,
		Category:  NotificationCategoryBilling,
		Type:      "quota.restored",
		Subject:   "Storage quota restored",
		Message:   fmt.Sprintf("Storage usage dropped to %d%%.", percent),
		Data:      quotaNotificationData(usage, limitBytes, percent),
		EmailKind: notificationEmailQuotaRestored,
	}, func(ctx context.Context) error {
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

func (s *NotificationService) dispatchUserNotification(ctx context.Context, user *model.User, prefs *model.NotificationPreferences, input NotificationInput, emailSend func(context.Context) error) error {
	var errs []error
	if user == nil || prefs == nil {
		return nil
	}
	if s.outbox != nil {
		return s.enqueueUserNotification(ctx, user, prefs, input)
	}
	if preferenceAllowsEmail(prefs, input.Category) {
		send := emailSend
		if send == nil {
			send = func(ctx context.Context) error {
				return s.notifier.SendNotification(ctx, provider.NotificationParams{
					UserID:      user.ID.String(),
					Email:       user.Email,
					DisplayName: user.DisplayName,
					AppName:     s.config.AppName,
					Category:    input.Category,
					Type:        input.Type,
					Subject:     input.Subject,
					Message:     input.Message,
				})
			}
		}
		if err := s.dispatch(ctx, send); err != nil {
			observability.RecordNotificationDelivery(input.Category, "failure")
			errs = append(errs, fmt.Errorf("send email notification: %w", err))
		} else {
			observability.RecordNotificationDelivery(input.Category, "success")
		}
	}
	if preferenceAllowsWebhook(prefs, input.Category) {
		if strings.TrimSpace(prefs.WebhookURL) == "" {
			observability.RecordNotificationDelivery(input.Category, "failure")
			errs = append(errs, ErrWebhookURLRequired)
		} else if err := s.dispatchWebhook(ctx, prefs.WebhookURL, prefs.WebhookSecret, provider.WebhookNotification{
			Type:     input.Type,
			Category: input.Category,
			Subject:  input.Subject,
			Message:  input.Message,
			Data:     input.Data,
		}); err != nil {
			observability.RecordNotificationDelivery(input.Category, "failure")
			errs = append(errs, fmt.Errorf("send webhook notification: %w", err))
		} else {
			observability.RecordNotificationDelivery(input.Category, "success")
		}
	}
	if len(errs) == 0 {
		return nil
	}
	err := errors.Join(errs...)
	if input.RequireDelivery {
		return err
	}
	log.Warn().
		Err(err).
		Str("user_id", user.ID.String()).
		Str("category", input.Category).
		Str("type", input.Type).
		Msg("notification delivery failed")
	return nil
}

type notificationPayload struct {
	EmailKind string         `json:"email_kind,omitempty"`
	Subject   string         `json:"subject"`
	Message   string         `json:"message"`
	Data      map[string]any `json:"data,omitempty"`
}

func (s *NotificationService) enqueueUserNotification(ctx context.Context, user *model.User, prefs *model.NotificationPreferences, input NotificationInput) error {
	payload := notificationPayload{
		EmailKind: fallbackNotificationEmailKind(input.EmailKind),
		Subject:   input.Subject,
		Message:   input.Message,
		Data:      input.Data,
	}
	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal notification outbox payload: %w", err)
	}
	var errs []error
	if preferenceAllowsEmail(prefs, input.Category) {
		if err := s.enqueueOutbox(ctx, user.ID, model.NotificationChannelEmail, input, payloadJSON); err != nil {
			errs = append(errs, fmt.Errorf("enqueue email notification: %w", err))
		}
	}
	if preferenceAllowsWebhook(prefs, input.Category) {
		if strings.TrimSpace(prefs.WebhookURL) == "" {
			errs = append(errs, ErrWebhookURLRequired)
		} else if err := s.enqueueOutbox(ctx, user.ID, model.NotificationChannelWebhook, input, payloadJSON); err != nil {
			errs = append(errs, fmt.Errorf("enqueue webhook notification: %w", err))
		}
	}
	if len(errs) == 0 {
		return nil
	}
	err = errors.Join(errs...)
	if input.RequireDelivery {
		return err
	}
	log.Warn().
		Err(err).
		Str("user_id", user.ID.String()).
		Str("category", input.Category).
		Str("type", input.Type).
		Msg("notification outbox enqueue failed")
	return nil
}

func (s *NotificationService) enqueueOutbox(ctx context.Context, userID uuid.UUID, channel model.NotificationChannel, input NotificationInput, payloadJSON []byte) error {
	item := &model.NotificationOutbox{
		UserID:      userID,
		Channel:     channel,
		Category:    input.Category,
		Type:        input.Type,
		PayloadJSON: append([]byte(nil), payloadJSON...),
		NextRetryAt: time.Now().UTC(),
	}
	if err := s.outbox.Enqueue(ctx, item); err != nil {
		return err
	}
	log.Info().
		Str("notification_id", item.ID.String()).
		Str("user_id", userID.String()).
		Str("channel", string(channel)).
		Str("category", input.Category).
		Str("type", input.Type).
		Msg("notification enqueued")
	return nil
}

func fallbackNotificationEmailKind(kind string) string {
	kind = strings.TrimSpace(kind)
	if kind == "" {
		return notificationEmailGeneric
	}
	return kind
}

func (s *NotificationService) dispatchWebhook(ctx context.Context, webhookURL, secret string, notification provider.WebhookNotification) error {
	if s == nil || s.webhook == nil {
		return nil
	}
	if !s.config.Enabled && s.webhook.DeliveryEnabled() {
		return nil
	}
	return s.webhook.Send(ctx, webhookURL, secret, notification)
}

type NotificationOutboxProcessResult struct {
	Claimed int `json:"claimed"`
	Sent    int `json:"sent"`
	Retried int `json:"retried"`
	Failed  int `json:"failed"`
}

type NotificationOutboxActionResultType string

const (
	NotificationOutboxActionRetried   NotificationOutboxActionResultType = "retried"
	NotificationOutboxActionRequeued  NotificationOutboxActionResultType = "requeued"
	NotificationOutboxActionDiscarded NotificationOutboxActionResultType = "discarded"
	NotificationOutboxActionSkipped   NotificationOutboxActionResultType = "skipped"
	NotificationOutboxActionNotFound  NotificationOutboxActionResultType = "not_found"
)

type NotificationOutboxActionResult struct {
	Result         NotificationOutboxActionResultType `json:"result"`
	NotificationID uuid.UUID                          `json:"notification_id,omitempty"`
	Replayed       bool                               `json:"replayed"`
	Limit          int32                              `json:"limit,omitempty"`
	Retried        int                                `json:"retried,omitempty"`
	Requeued       int                                `json:"requeued,omitempty"`
	Discarded      int                                `json:"discarded,omitempty"`
	Skipped        int                                `json:"skipped,omitempty"`
	NotFound       int                                `json:"not_found,omitempty"`
	Sent           int                                `json:"sent,omitempty"`
	Failed         int                                `json:"failed,omitempty"`
	ScheduledRetry int                                `json:"scheduled_retry,omitempty"`
	Status         model.NotificationOutboxStatus     `json:"status,omitempty"`
	PreviousStatus model.NotificationOutboxStatus     `json:"previous_status,omitempty"`
}

func (s *NotificationService) ProcessOutbox(ctx context.Context, limit int32) (NotificationOutboxProcessResult, error) {
	var result NotificationOutboxProcessResult
	if s == nil || s.outbox == nil {
		return result, nil
	}
	if limit <= 0 {
		limit = defaultNotificationOutboxBatchSize
	}
	items, err := s.outbox.ClaimDue(ctx, time.Now().UTC(), limit)
	if err != nil {
		return result, err
	}
	result.Claimed = len(items)
	for _, item := range items {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		if err := s.deliverOutboxItem(ctx, item); err != nil {
			observability.RecordNotificationDelivery(item.Category, "failure")
			errText := sanitizeNotificationError(err)
			nextAttempt := item.AttemptCount + 1
			if nextAttempt >= defaultNotificationMaxAttempts {
				if markErr := s.outbox.MarkFailed(ctx, item.ID, errText); markErr != nil {
					return result, markErr
				}
				result.Failed++
				log.Warn().
					Str("notification_id", item.ID.String()).
					Str("user_id", item.UserID.String()).
					Str("channel", string(item.Channel)).
					Str("category", item.Category).
					Str("type", item.Type).
					Int("attempt", nextAttempt).
					Str("error_summary", errText).
					Msg("notification delivery permanently failed")
				continue
			}
			nextRetry := time.Now().UTC().Add(notificationRetryDelay(nextAttempt))
			if markErr := s.outbox.MarkRetry(ctx, item.ID, nextRetry, errText); markErr != nil {
				return result, markErr
			}
			result.Retried++
			log.Warn().
				Str("notification_id", item.ID.String()).
				Str("user_id", item.UserID.String()).
				Str("channel", string(item.Channel)).
				Str("category", item.Category).
				Str("type", item.Type).
				Int("attempt", nextAttempt).
				Time("next_retry_at", nextRetry).
				Str("error_summary", errText).
				Msg("notification delivery failed; retry scheduled")
			continue
		}
		if err := s.outbox.MarkSent(ctx, item.ID, time.Now().UTC()); err != nil {
			return result, err
		}
		observability.RecordNotificationDelivery(item.Category, "success")
		result.Sent++
		log.Info().
			Str("notification_id", item.ID.String()).
			Str("user_id", item.UserID.String()).
			Str("channel", string(item.Channel)).
			Str("category", item.Category).
			Str("type", item.Type).
			Msg("notification delivered")
	}
	return result, nil
}

func (s *NotificationService) RetryFailure(ctx context.Context, id uuid.UUID) (NotificationOutboxActionResult, error) {
	result := NotificationOutboxActionResult{NotificationID: id}
	if s == nil || s.outbox == nil || id == uuid.Nil {
		result.Result = NotificationOutboxActionNotFound
		result.NotFound = 1
		return result, nil
	}
	item, err := s.outbox.GetByID(ctx, id)
	if err != nil {
		return result, err
	}
	if item == nil {
		result.Result = NotificationOutboxActionNotFound
		result.NotFound = 1
		return result, nil
	}
	result.PreviousStatus = item.Status
	if item.Status != model.NotificationOutboxFailed {
		result.Result = NotificationOutboxActionSkipped
		result.Skipped = 1
		result.Status = item.Status
		return result, nil
	}
	claimed, err := s.outbox.ClaimFailedByID(ctx, id)
	if err != nil {
		return result, err
	}
	if claimed == nil {
		result.Result = NotificationOutboxActionSkipped
		result.Skipped = 1
		result.Status = item.Status
		return result, nil
	}
	return s.retryClaimedOutboxItem(ctx, *claimed)
}

func (s *NotificationService) RetryFailures(ctx context.Context, limit int32) (NotificationOutboxActionResult, error) {
	limit = normalizeNotificationAdminLimit(limit)
	result := NotificationOutboxActionResult{
		Result: NotificationOutboxActionRetried,
		Limit:  limit,
	}
	if s == nil || s.outbox == nil {
		return result, nil
	}
	items, err := s.outbox.ClaimFailed(ctx, limit)
	if err != nil {
		return result, err
	}
	if len(items) == 0 {
		result.Result = NotificationOutboxActionSkipped
		result.Skipped = 1
		return result, nil
	}
	for _, item := range items {
		itemResult, err := s.retryClaimedOutboxItem(ctx, item)
		if err != nil {
			return result, err
		}
		result.Retried += itemResult.Retried
		result.Sent += itemResult.Sent
		result.Failed += itemResult.Failed
		result.ScheduledRetry += itemResult.ScheduledRetry
		result.Skipped += itemResult.Skipped
	}
	return result, nil
}

func (s *NotificationService) RequeueFailure(ctx context.Context, id uuid.UUID) (NotificationOutboxActionResult, error) {
	result := NotificationOutboxActionResult{NotificationID: id}
	if s == nil || s.outbox == nil || id == uuid.Nil {
		result.Result = NotificationOutboxActionNotFound
		result.NotFound = 1
		return result, nil
	}
	item, err := s.outbox.GetByID(ctx, id)
	if err != nil {
		return result, err
	}
	if item == nil {
		result.Result = NotificationOutboxActionNotFound
		result.NotFound = 1
		return result, nil
	}
	result.PreviousStatus = item.Status
	if item.Status != model.NotificationOutboxFailed {
		result.Result = NotificationOutboxActionSkipped
		result.Skipped = 1
		result.Status = item.Status
		return result, nil
	}
	updated, err := s.outbox.RequeueFailed(ctx, id, time.Now().UTC())
	if err != nil {
		return result, err
	}
	if !updated {
		result.Result = NotificationOutboxActionSkipped
		result.Skipped = 1
		result.Status = item.Status
		return result, nil
	}
	result.Result = NotificationOutboxActionRequeued
	result.Requeued = 1
	result.Status = model.NotificationOutboxPending
	return result, nil
}

func (s *NotificationService) DiscardFailure(ctx context.Context, id uuid.UUID) (NotificationOutboxActionResult, error) {
	result := NotificationOutboxActionResult{NotificationID: id}
	if s == nil || s.outbox == nil || id == uuid.Nil {
		result.Result = NotificationOutboxActionNotFound
		result.NotFound = 1
		return result, nil
	}
	item, err := s.outbox.GetByID(ctx, id)
	if err != nil {
		return result, err
	}
	if item == nil {
		result.Result = NotificationOutboxActionNotFound
		result.NotFound = 1
		return result, nil
	}
	result.PreviousStatus = item.Status
	if item.Status != model.NotificationOutboxFailed {
		result.Result = NotificationOutboxActionSkipped
		result.Skipped = 1
		result.Status = item.Status
		return result, nil
	}
	updated, err := s.outbox.MarkDiscarded(ctx, id)
	if err != nil {
		return result, err
	}
	if !updated {
		result.Result = NotificationOutboxActionSkipped
		result.Skipped = 1
		result.Status = item.Status
		return result, nil
	}
	result.Result = NotificationOutboxActionDiscarded
	result.Discarded = 1
	result.Status = model.NotificationOutboxDiscarded
	return result, nil
}

func (s *NotificationService) retryClaimedOutboxItem(ctx context.Context, item model.NotificationOutbox) (NotificationOutboxActionResult, error) {
	result := NotificationOutboxActionResult{
		Result:         NotificationOutboxActionRetried,
		NotificationID: item.ID,
		PreviousStatus: model.NotificationOutboxFailed,
		Retried:        1,
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	if err := s.deliverOutboxItem(ctx, item); err != nil {
		observability.RecordNotificationDelivery(item.Category, "failure")
		errText := sanitizeNotificationError(err)
		nextAttempt := item.AttemptCount + 1
		if nextAttempt >= defaultNotificationMaxAttempts {
			if markErr := s.outbox.MarkFailed(ctx, item.ID, errText); markErr != nil {
				return result, markErr
			}
			result.Failed = 1
			result.Status = model.NotificationOutboxFailed
			log.Warn().
				Str("notification_id", item.ID.String()).
				Str("user_id", item.UserID.String()).
				Str("channel", string(item.Channel)).
				Str("category", item.Category).
				Str("type", item.Type).
				Int("attempt", nextAttempt).
				Str("error_summary", errText).
				Msg("notification delivery permanently failed during admin retry")
			return result, nil
		}
		nextRetry := time.Now().UTC().Add(notificationRetryDelay(nextAttempt))
		if markErr := s.outbox.MarkRetry(ctx, item.ID, nextRetry, errText); markErr != nil {
			return result, markErr
		}
		result.ScheduledRetry = 1
		result.Status = model.NotificationOutboxPending
		log.Warn().
			Str("notification_id", item.ID.String()).
			Str("user_id", item.UserID.String()).
			Str("channel", string(item.Channel)).
			Str("category", item.Category).
			Str("type", item.Type).
			Int("attempt", nextAttempt).
			Time("next_retry_at", nextRetry).
			Str("error_summary", errText).
			Msg("notification delivery failed during admin retry; retry scheduled")
		return result, nil
	}
	if err := s.outbox.MarkSent(ctx, item.ID, time.Now().UTC()); err != nil {
		return result, err
	}
	observability.RecordNotificationDelivery(item.Category, "success")
	result.Sent = 1
	result.Status = model.NotificationOutboxSent
	log.Info().
		Str("notification_id", item.ID.String()).
		Str("user_id", item.UserID.String()).
		Str("channel", string(item.Channel)).
		Str("category", item.Category).
		Str("type", item.Type).
		Msg("notification delivered during admin retry")
	return result, nil
}

func normalizeNotificationAdminLimit(limit int32) int32 {
	if limit <= 0 {
		return defaultNotificationOutboxBatchSize
	}
	if limit > maxNotificationOutboxAdminBatch {
		return maxNotificationOutboxAdminBatch
	}
	return limit
}

func (s *NotificationService) deliverOutboxItem(ctx context.Context, item model.NotificationOutbox) error {
	user, err := s.notificationUser(ctx, item.UserID)
	if err != nil {
		return err
	}
	prefs, err := s.GetPreferences(ctx, item.UserID)
	if err != nil {
		return err
	}
	var payload notificationPayload
	if err := json.Unmarshal(item.PayloadJSON, &payload); err != nil {
		return fmt.Errorf("decode notification payload: %w", err)
	}
	switch item.Channel {
	case model.NotificationChannelEmail:
		if !preferenceAllowsEmail(prefs, item.Category) {
			return nil
		}
		return s.deliverOutboxEmail(ctx, user, item, payload)
	case model.NotificationChannelWebhook:
		if !preferenceAllowsWebhook(prefs, item.Category) {
			return nil
		}
		if strings.TrimSpace(prefs.WebhookURL) == "" {
			return ErrWebhookURLRequired
		}
		return s.dispatchWebhook(ctx, prefs.WebhookURL, prefs.WebhookSecret, provider.WebhookNotification{
			Type:     item.Type,
			Category: item.Category,
			Subject:  payload.Subject,
			Message:  payload.Message,
			Data:     payload.Data,
		})
	default:
		return fmt.Errorf("unsupported notification channel %q", item.Channel)
	}
}

func (s *NotificationService) deliverOutboxEmail(ctx context.Context, user *model.User, item model.NotificationOutbox, payload notificationPayload) error {
	switch payload.EmailKind {
	case notificationEmailQuotaWarning:
		q, err := quotaPayload(payload.Data)
		if err != nil {
			return err
		}
		return s.dispatch(ctx, func(ctx context.Context) error {
			return s.notifier.SendQuotaWarning(ctx, provider.QuotaWarningParams{
				UserID:        user.ID.String(),
				Email:         user.Email,
				DisplayName:   user.DisplayName,
				AppName:       s.config.AppName,
				UsageBytes:    q.UsageBytes,
				LimitBytes:    q.LimitBytes,
				UsagePercent:  q.UsagePercent,
				BundleCount:   q.BundleCount,
				SnapshotCount: q.SnapshotCount,
			})
		})
	case notificationEmailQuotaExhausted:
		q, err := quotaPayload(payload.Data)
		if err != nil {
			return err
		}
		return s.dispatch(ctx, func(ctx context.Context) error {
			return s.notifier.SendQuotaExhausted(ctx, provider.QuotaExhaustedParams{
				UserID:        user.ID.String(),
				Email:         user.Email,
				DisplayName:   user.DisplayName,
				AppName:       s.config.AppName,
				UsageBytes:    q.UsageBytes,
				LimitBytes:    q.LimitBytes,
				UsagePercent:  q.UsagePercent,
				BundleCount:   q.BundleCount,
				SnapshotCount: q.SnapshotCount,
			})
		})
	case notificationEmailQuotaRestored:
		q, err := quotaPayload(payload.Data)
		if err != nil {
			return err
		}
		return s.dispatch(ctx, func(ctx context.Context) error {
			return s.notifier.SendQuotaRestored(ctx, provider.QuotaRestoredParams{
				UserID:        user.ID.String(),
				Email:         user.Email,
				DisplayName:   user.DisplayName,
				AppName:       s.config.AppName,
				UsageBytes:    q.UsageBytes,
				LimitBytes:    q.LimitBytes,
				UsagePercent:  q.UsagePercent,
				BundleCount:   q.BundleCount,
				SnapshotCount: q.SnapshotCount,
			})
		})
	default:
		return s.dispatch(ctx, func(ctx context.Context) error {
			return s.notifier.SendNotification(ctx, provider.NotificationParams{
				UserID:      user.ID.String(),
				Email:       user.Email,
				DisplayName: user.DisplayName,
				AppName:     s.config.AppName,
				Category:    item.Category,
				Type:        item.Type,
				Subject:     payload.Subject,
				Message:     payload.Message,
			})
		})
	}
}

type quotaNotificationPayload struct {
	UsageBytes    int64
	LimitBytes    int64
	UsagePercent  int
	BundleCount   int64
	SnapshotCount int64
}

func quotaPayload(data map[string]any) (quotaNotificationPayload, error) {
	if data == nil {
		return quotaNotificationPayload{}, fmt.Errorf("quota notification payload is missing")
	}
	return quotaNotificationPayload{
		UsageBytes:    int64FromNotificationData(data["usage_bytes"]),
		LimitBytes:    int64FromNotificationData(data["limit_bytes"]),
		UsagePercent:  int(int64FromNotificationData(data["usage_percent"])),
		BundleCount:   int64FromNotificationData(data["bundle_count"]),
		SnapshotCount: int64FromNotificationData(data["snapshot_count"]),
	}, nil
}

func int64FromNotificationData(value any) int64 {
	switch v := value.(type) {
	case int:
		return int64(v)
	case int32:
		return int64(v)
	case int64:
		return v
	case float64:
		return int64(v)
	case json.Number:
		n, _ := v.Int64()
		return n
	default:
		return 0
	}
}

func notificationRetryDelay(attempt int) time.Duration {
	if attempt <= 0 {
		return defaultNotificationBaseBackoff
	}
	delay := defaultNotificationBaseBackoff
	for i := 1; i < attempt; i++ {
		delay *= 2
		if delay >= defaultNotificationMaxBackoff {
			return defaultNotificationMaxBackoff
		}
	}
	return delay
}

func (s *NotificationService) RecentFailures(ctx context.Context, limit, offset int32) ([]model.NotificationFailureView, error) {
	if s == nil || s.outbox == nil {
		return []model.NotificationFailureView{}, nil
	}
	items, err := s.outbox.ListFailures(ctx, limit, offset)
	if err != nil {
		return nil, err
	}
	views := make([]model.NotificationFailureView, 0, len(items))
	for _, item := range items {
		views = append(views, model.NotificationFailureView{
			ID:           item.ID,
			UserID:       item.UserID,
			Channel:      item.Channel,
			Category:     item.Category,
			Type:         item.Type,
			AttemptCount: item.AttemptCount,
			ErrorSummary: sanitizeNotificationError(errors.New(item.LastError)),
			CreatedAt:    item.CreatedAt,
			UpdatedAt:    item.UpdatedAt,
		})
	}
	return views, nil
}

func sanitizeNotificationError(err error) string {
	if err == nil {
		return ""
	}
	msg := provider.SanitizeWebhookError(err.Error())
	msg = redactNotificationSecretAssignments(msg)
	msg = redactNotificationSecretWords(msg)
	msg = strings.TrimSpace(msg)
	if len(msg) > 240 {
		msg = msg[:240]
	}
	return msg
}

func redactNotificationSecretAssignments(message string) string {
	for _, key := range []string{
		"smtp_secret=", "smtp-secret=", "smtp_token=", "smtp-token=", "smtp_password=", "smtp-password=",
		"password=", "token=", "secret=", "smtp_secret:", "smtp-token:", "smtp_password:", "smtp-password:",
		"password:", "token:", "secret:",
	} {
		message = redactNotificationAssignment(message, key)
	}
	return message
}

func redactNotificationAssignment(message, key string) string {
	lower := strings.ToLower(message)
	var out strings.Builder
	for {
		idx := strings.Index(lower, key)
		if idx < 0 {
			out.WriteString(message)
			return out.String()
		}
		out.WriteString(message[:idx])
		out.WriteString(message[idx : idx+len(key)])
		out.WriteString("<redacted>")
		valueStart := idx + len(key)
		valueEnd := valueStart
		for valueEnd < len(message) {
			switch message[valueEnd] {
			case '&', ' ', '\t', '\r', '\n', '"', '\'', ')', ']', '}', ',', ';':
				goto done
			default:
				valueEnd++
			}
		}
	done:
		message = message[valueEnd:]
		lower = strings.ToLower(message)
	}
}

func redactNotificationSecretWords(message string) string {
	fields := strings.Fields(message)
	for i := 0; i < len(fields); i++ {
		trimmed := strings.Trim(fields[i], `"'(),;[]{}<>`)
		normalized := strings.ToLower(strings.TrimRight(trimmed, ":="))
		if !notificationSecretMarker(normalized) {
			continue
		}
		if strings.HasSuffix(trimmed, ":") || strings.HasSuffix(trimmed, "=") {
			if i+1 < len(fields) {
				fields[i+1] = "<redacted>"
			}
			continue
		}
		if i+1 < len(fields) && !strings.Contains(fields[i], "<redacted>") {
			fields[i+1] = "<redacted>"
		}
	}
	return strings.Join(fields, " ")
}

func notificationSecretMarker(value string) bool {
	switch value {
	case "secret", "token", "password", "smtp_secret", "smtp-password", "smtp_token", "smtp-token":
		return true
	default:
		return false
	}
}

func preferenceAllowsEmail(prefs *model.NotificationPreferences, category string) bool {
	if prefs == nil {
		return false
	}
	switch category {
	case NotificationCategorySecurity:
		return prefs.SecurityEmail
	case NotificationCategoryBilling:
		return prefs.BillingEmail
	default:
		return false
	}
}

func preferenceAllowsWebhook(prefs *model.NotificationPreferences, category string) bool {
	if prefs == nil {
		return false
	}
	switch category {
	case NotificationCategorySecurity:
		return prefs.SecurityWebhook
	case NotificationCategoryBilling:
		return prefs.BillingWebhook
	default:
		return false
	}
}

func quotaNotificationData(usage model.QuotaUsage, limitBytes int64, percent int) map[string]any {
	return map[string]any{
		"usage_bytes":    usage.TotalBytes,
		"limit_bytes":    limitBytes,
		"usage_percent":  percent,
		"bundle_count":   usage.BundleCount,
		"snapshot_count": usage.SnapCount,
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
