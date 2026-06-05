package provider

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"
)

type WebhookConfig struct {
	Client       *http.Client
	Timeout      time.Duration
	MaxRedirects int
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
		client = &http.Client{
			Timeout:       cfg.Timeout,
			Transport:     hardenedWebhookTransport(),
			CheckRedirect: webhookRedirectPolicy(cfg.MaxRedirects),
		}
	}
	return &WebhookNotifier{client: client, timeout: cfg.Timeout}
}

func (n *WebhookNotifier) DeliveryEnabled() bool {
	return n != nil && n.client != nil
}

func (n *WebhookNotifier) Send(ctx context.Context, webhookURL, secret string, notification WebhookNotification) error {
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
	if strings.TrimSpace(secret) != "" {
		req.Header.Set("X-HistorySync-Webhook-Signature", webhookSignature(secret, payload))
	}

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
	if unsafeWebhookHost(parsed.Hostname()) {
		return fmt.Errorf("webhook url host is not allowed")
	}
	return nil
}

func webhookSignature(secret string, payload []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write(payload)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

func webhookRedirectPolicy(maxRedirects int) func(*http.Request, []*http.Request) error {
	return func(_ *http.Request, via []*http.Request) error {
		if maxRedirects <= 0 {
			return http.ErrUseLastResponse
		}
		if len(via) >= maxRedirects {
			return fmt.Errorf("webhook redirect limit exceeded")
		}
		return nil
	}
}

func hardenedWebhookTransport() *http.Transport {
	base := http.DefaultTransport.(*http.Transport).Clone()
	dialer := &net.Dialer{Timeout: 5 * time.Second}
	base.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, err
		}
		if unsafeWebhookHost(host) {
			return nil, fmt.Errorf("webhook target host is not allowed")
		}
		ips, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
		if err != nil {
			return nil, fmt.Errorf("resolve webhook host: %w", err)
		}
		for _, ip := range ips {
			if unsafeWebhookAddr(ip) {
				return nil, fmt.Errorf("webhook target resolved to a disallowed address")
			}
		}
		if len(ips) == 0 {
			return nil, fmt.Errorf("webhook host resolved no addresses")
		}
		return dialer.DialContext(ctx, network, net.JoinHostPort(ips[0].String(), port))
	}
	return base
}

func unsafeWebhookHost(host string) bool {
	host = strings.Trim(strings.ToLower(strings.TrimSpace(host)), ".")
	if host == "" || host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return true
	}
	if ip, err := netip.ParseAddr(host); err == nil {
		return unsafeWebhookAddr(ip)
	}
	return false
}

func unsafeWebhookAddr(ip netip.Addr) bool {
	if !ip.IsValid() {
		return true
	}
	return ip.IsLoopback() ||
		ip.IsPrivate() ||
		ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() ||
		ip.IsUnspecified()
}

func SanitizeWebhookError(message string) string {
	message = strings.TrimSpace(message)
	if message == "" {
		return ""
	}
	if u, err := url.Parse(message); err == nil && u.Scheme != "" && u.Host != "" {
		return sanitizeWebhookURL(u)
	}
	fields := strings.Fields(message)
	for i, field := range fields {
		trimmed := strings.Trim(field, `"'(),;`)
		if u, err := url.Parse(trimmed); err == nil && u.Scheme != "" && u.Host != "" {
			fields[i] = strings.Replace(field, trimmed, sanitizeWebhookURL(u), 1)
		}
	}
	message = strings.Join(fields, " ")
	message = redactSensitiveQueryLikeText(message)
	return message
}

func sanitizeWebhookURL(u *url.URL) string {
	clean := *u
	clean.User = nil
	clean.RawQuery = ""
	clean.Fragment = ""
	if clean.Path != "" && clean.Path != "/" {
		clean.Path = "/..."
	}
	return clean.String()
}

func redactSensitiveQueryLikeText(message string) string {
	for _, key := range []string{"token=", "secret=", "signature=", "key="} {
		message = redactSensitiveAssignment(message, key)
	}
	return message
}

func redactSensitiveAssignment(message, key string) string {
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
