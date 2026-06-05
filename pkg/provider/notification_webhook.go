package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type WebhookConfig struct {
	Client  *http.Client
	Timeout time.Duration
}

type WebhookNotifier struct {
	client  *http.Client
	timeout time.Duration
}

func NewWebhookNotifier(cfg WebhookConfig) *WebhookNotifier {
	if cfg.Timeout <= 0 {
		cfg.Timeout = 5 * time.Second
	}
	client := cfg.Client
	if client == nil {
		client = &http.Client{Timeout: cfg.Timeout}
	}
	return &WebhookNotifier{client: client, timeout: cfg.Timeout}
}

func (n *WebhookNotifier) DeliveryEnabled() bool {
	return n != nil && n.client != nil
}

func (n *WebhookNotifier) Send(ctx context.Context, webhookURL string, notification WebhookNotification) error {
	if n == nil || n.client == nil {
		return ErrNotificationNotConfigured
	}
	if err := ValidateWebhookURL(webhookURL); err != nil {
		return err
	}
	if notification.Timestamp.IsZero() {
		notification.Timestamp = time.Now().UTC()
	}
	payload, err := json.Marshal(notification)
	if err != nil {
		return fmt.Errorf("marshal webhook notification: %w", err)
	}

	ctx, cancel := context.WithTimeout(ctx, n.timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimSpace(webhookURL), bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("create webhook request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "HistorySync-Notifications/1.0")

	resp, err := n.client.Do(req)
	if err != nil {
		return fmt.Errorf("send webhook request: %w", err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1024))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("webhook request returned status %d", resp.StatusCode)
	}
	return nil
}

func ValidateWebhookURL(raw string) error {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return fmt.Errorf("webhook url is required")
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("webhook url is invalid")
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return fmt.Errorf("webhook url must use http or https")
	}
	if parsed.Host == "" || parsed.Hostname() == "" {
		return fmt.Errorf("webhook url must include a host")
	}
	if parsed.User != nil {
		return fmt.Errorf("webhook url must not include user info")
	}
	return nil
}
