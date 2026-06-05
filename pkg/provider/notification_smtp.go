package provider

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"fmt"
	"html/template"
	"mime"
	"net"
	"net/mail"
	"net/smtp"
	"strconv"
	"strings"
	"time"
)

type SMTPConfig struct {
	Server   string
	Port     int
	Username string
	Password string
	From     string
	FromName string
	TLSMode  string
	Timeout  time.Duration
}

type SMTPNotifier struct {
	cfg SMTPConfig
}

func NewSMTPNotifier(cfg SMTPConfig) (*SMTPNotifier, error) {
	if cfg.Port == 0 {
		cfg.Port = 587
	}
	if cfg.TLSMode == "" {
		cfg.TLSMode = SMTPTLSModeStartTLS
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 10 * time.Second
	}
	if err := validateSMTPConfig(cfg); err != nil {
		return nil, err
	}
	return &SMTPNotifier{cfg: cfg}, nil
}

func validateSMTPConfig(cfg SMTPConfig) error {
	if strings.TrimSpace(cfg.Server) == "" {
		return fmt.Errorf("smtp server is required")
	}
	if cfg.Port <= 0 || cfg.Port > 65535 {
		return fmt.Errorf("smtp port must be between 1 and 65535")
	}
	if _, err := mail.ParseAddress(cfg.From); err != nil {
		return fmt.Errorf("smtp from address is invalid: %w", err)
	}
	switch cfg.TLSMode {
	case SMTPTLSModeNone, SMTPTLSModeStartTLS, SMTPTLSModeTLS:
	default:
		return fmt.Errorf("smtp tls mode must be one of none, starttls, tls")
	}
	return nil
}

func (n *SMTPNotifier) DeliveryEnabled() bool {
	return n != nil
}

func (n *SMTPNotifier) SendWelcome(ctx context.Context, p WelcomeParams) error {
	body, err := renderEmailTemplate(welcomeTemplate, map[string]any{
		"AppName":     fallback(p.AppName, "HistorySync Cloud"),
		"DisplayName": fallback(p.DisplayName, p.Email),
	})
	if err != nil {
		return err
	}
	return n.send(ctx, p.Email, fallback(p.AppName, "HistorySync Cloud")+" welcome", body)
}

func (n *SMTPNotifier) SendEmailVerification(ctx context.Context, p EmailVerificationParams) error {
	body, err := renderEmailTemplate(emailVerificationTemplate, map[string]any{
		"AppName":         fallback(p.AppName, "HistorySync Cloud"),
		"DisplayName":     fallback(p.DisplayName, p.Email),
		"VerificationURL": p.VerificationURL,
		"ExpiresIn":       friendlyDuration(p.ExpiresIn),
	})
	if err != nil {
		return err
	}
	return n.send(ctx, p.Email, fallback(p.AppName, "HistorySync Cloud")+" email verification", body)
}

func (n *SMTPNotifier) SendPasswordReset(ctx context.Context, p PasswordResetParams) error {
	body, err := renderEmailTemplate(passwordResetTemplate, map[string]any{
		"AppName":     fallback(p.AppName, "HistorySync Cloud"),
		"DisplayName": fallback(p.DisplayName, p.Email),
		"ResetURL":    p.ResetURL,
		"ExpiresIn":   friendlyDuration(p.ExpiresIn),
	})
	if err != nil {
		return err
	}
	return n.send(ctx, p.Email, fallback(p.AppName, "HistorySync Cloud")+" password reset", body)
}

func (n *SMTPNotifier) SendQuotaWarning(ctx context.Context, p QuotaWarningParams) error {
	body, err := renderEmailTemplate(quotaTemplate, quotaTemplateData("Storage quota warning", p.AppName, p.DisplayName, p.Email, p.UsageBytes, p.LimitBytes, p.UsagePercent, p.BundleCount, p.SnapshotCount))
	if err != nil {
		return err
	}
	return n.send(ctx, p.Email, fallback(p.AppName, "HistorySync Cloud")+" quota warning", body)
}

func (n *SMTPNotifier) SendQuotaExhausted(ctx context.Context, p QuotaExhaustedParams) error {
	body, err := renderEmailTemplate(quotaTemplate, quotaTemplateData("Storage quota exhausted", p.AppName, p.DisplayName, p.Email, p.UsageBytes, p.LimitBytes, p.UsagePercent, p.BundleCount, p.SnapshotCount))
	if err != nil {
		return err
	}
	return n.send(ctx, p.Email, fallback(p.AppName, "HistorySync Cloud")+" quota exhausted", body)
}

func (n *SMTPNotifier) SendQuotaRestored(ctx context.Context, p QuotaRestoredParams) error {
	body, err := renderEmailTemplate(quotaTemplate, quotaTemplateData("Storage quota restored", p.AppName, p.DisplayName, p.Email, p.UsageBytes, p.LimitBytes, p.UsagePercent, p.BundleCount, p.SnapshotCount))
	if err != nil {
		return err
	}
	return n.send(ctx, p.Email, fallback(p.AppName, "HistorySync Cloud")+" quota restored", body)
}

func (n *SMTPNotifier) SendNotification(ctx context.Context, p NotificationParams) error {
	body, err := renderEmailTemplate(genericNotificationTemplate, map[string]any{
		"AppName":     fallback(p.AppName, "HistorySync Cloud"),
		"DisplayName": fallback(p.DisplayName, p.Email),
		"Subject":     fallback(p.Subject, fallback(p.AppName, "HistorySync Cloud")+" notification"),
		"Message":     p.Message,
	})
	if err != nil {
		return err
	}
	subject := fallback(p.Subject, fallback(p.AppName, "HistorySync Cloud")+" notification")
	return n.send(ctx, p.Email, subject, body)
}

func (n *SMTPNotifier) send(ctx context.Context, to, subject, htmlBody string) error {
	if n == nil {
		return ErrNotificationNotConfigured
	}
	if _, err := mail.ParseAddress(to); err != nil {
		return fmt.Errorf("recipient address is invalid: %w", err)
	}
	ctx, cancel := context.WithTimeout(ctx, n.cfg.Timeout)
	defer cancel()

	client, err := n.smtpClient(ctx)
	if err != nil {
		return err
	}
	defer client.Close()

	if n.cfg.Username != "" {
		if err := client.Auth(smtp.PlainAuth("", n.cfg.Username, n.cfg.Password, n.cfg.Server)); err != nil {
			return fmt.Errorf("smtp auth: %w", err)
		}
	}
	if err := client.Mail(n.cfg.From); err != nil {
		return fmt.Errorf("smtp mail from: %w", err)
	}
	if err := client.Rcpt(to); err != nil {
		return fmt.Errorf("smtp rcpt to: %w", err)
	}
	writer, err := client.Data()
	if err != nil {
		return fmt.Errorf("smtp data: %w", err)
	}
	if _, err := writer.Write(n.message(to, subject, htmlBody)); err != nil {
		_ = writer.Close()
		return fmt.Errorf("smtp write: %w", err)
	}
	if err := writer.Close(); err != nil {
		return fmt.Errorf("smtp close data: %w", err)
	}
	_ = client.Quit()
	return nil
}

func (n *SMTPNotifier) smtpClient(ctx context.Context) (*smtp.Client, error) {
	addr := net.JoinHostPort(n.cfg.Server, strconv.Itoa(n.cfg.Port))
	dialer := &net.Dialer{}

	var conn net.Conn
	var err error
	if n.cfg.TLSMode == SMTPTLSModeTLS {
		rawConn, err := dialer.DialContext(ctx, "tcp", addr)
		if err != nil {
			return nil, fmt.Errorf("smtp dial: %w", err)
		}
		tlsConn := tls.Client(rawConn, &tls.Config{ServerName: n.cfg.Server, MinVersion: tls.VersionTLS12})
		if err := tlsConn.HandshakeContext(ctx); err != nil {
			_ = rawConn.Close()
			return nil, fmt.Errorf("smtp tls handshake: %w", err)
		}
		conn = tlsConn
	} else {
		conn, err = dialer.DialContext(ctx, "tcp", addr)
		if err != nil {
			return nil, fmt.Errorf("smtp dial: %w", err)
		}
	}

	client, err := smtp.NewClient(conn, n.cfg.Server)
	if err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("smtp client: %w", err)
	}
	if n.cfg.TLSMode == SMTPTLSModeStartTLS {
		if ok, _ := client.Extension("STARTTLS"); !ok {
			_ = client.Close()
			return nil, fmt.Errorf("smtp server does not advertise STARTTLS")
		}
		if err := client.StartTLS(&tls.Config{ServerName: n.cfg.Server, MinVersion: tls.VersionTLS12}); err != nil {
			_ = client.Close()
			return nil, fmt.Errorf("smtp starttls: %w", err)
		}
	}
	return client, nil
}

func (n *SMTPNotifier) message(to, subject, htmlBody string) []byte {
	fromName := fallback(n.cfg.FromName, "HistorySync Cloud")
	headers := map[string]string{
		"From":         (&mail.Address{Name: fromName, Address: n.cfg.From}).String(),
		"To":           to,
		"Subject":      mime.QEncoding.Encode("utf-8", cleanHeader(subject)),
		"Date":         time.Now().Format(time.RFC1123Z),
		"Message-ID":   messageID(n.cfg.From),
		"MIME-Version": "1.0",
		"Content-Type": "text/html; charset=UTF-8",
	}
	var msg bytes.Buffer
	for _, key := range []string{"From", "To", "Subject", "Date", "Message-ID", "MIME-Version", "Content-Type"} {
		msg.WriteString(key)
		msg.WriteString(": ")
		msg.WriteString(cleanHeader(headers[key]))
		msg.WriteString("\r\n")
	}
	msg.WriteString("\r\n")
	msg.WriteString(htmlBody)
	msg.WriteString("\r\n")
	return msg.Bytes()
}

func renderEmailTemplate(tmpl string, data map[string]any) (string, error) {
	t, err := template.New("email").Parse(tmpl)
	if err != nil {
		return "", err
	}
	var buf bytes.Buffer
	if err := t.Execute(&buf, data); err != nil {
		return "", err
	}
	return buf.String(), nil
}

func quotaTemplateData(title, appName, displayName, email string, usageBytes, limitBytes int64, usagePercent int, bundleCount, snapshotCount int64) map[string]any {
	return map[string]any{
		"Title":         title,
		"AppName":       fallback(appName, "HistorySync Cloud"),
		"DisplayName":   fallback(displayName, email),
		"Usage":         formatBytes(usageBytes),
		"Limit":         formatBytes(limitBytes),
		"UsagePercent":  usagePercent,
		"BundleCount":   bundleCount,
		"SnapshotCount": snapshotCount,
	}
}

func friendlyDuration(d time.Duration) string {
	if d <= 0 {
		return "soon"
	}
	if d%time.Hour == 0 {
		hours := int(d / time.Hour)
		if hours == 1 {
			return "1 hour"
		}
		return fmt.Sprintf("%d hours", hours)
	}
	return d.String()
}

func formatBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for value := n / unit; value >= unit; value /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}

func fallback(value, fallbackValue string) string {
	value = strings.TrimSpace(value)
	if value != "" {
		return value
	}
	return fallbackValue
}

func cleanHeader(value string) string {
	value = strings.ReplaceAll(value, "\r", "")
	value = strings.ReplaceAll(value, "\n", "")
	return strings.TrimSpace(value)
}

func messageID(from string) string {
	domain := "historysync.local"
	if addr, err := mail.ParseAddress(from); err == nil {
		if at := strings.LastIndex(addr.Address, "@"); at >= 0 && at+1 < len(addr.Address) {
			domain = addr.Address[at+1:]
		}
	}
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("<%d@%s>", time.Now().UnixNano(), domain)
	}
	return fmt.Sprintf("<%d.%s@%s>", time.Now().UnixNano(), hex.EncodeToString(b[:]), domain)
}

const welcomeTemplate = `<!doctype html>
<html><body>
<p>Hello {{.DisplayName}},</p>
<p>Welcome to {{.AppName}}. Your account is ready.</p>
<p>If this was not you, contact support immediately.</p>
</body></html>`

const emailVerificationTemplate = `<!doctype html>
<html><body>
<p>Hello {{.DisplayName}},</p>
<p>Use the link below to verify your {{.AppName}} email address. This link expires in {{.ExpiresIn}}.</p>
<p><a href="{{.VerificationURL}}">Verify email</a></p>
<p>If you did not create this account, you can ignore this email.</p>
</body></html>`

const passwordResetTemplate = `<!doctype html>
<html><body>
<p>Hello {{.DisplayName}},</p>
<p>Use the link below to reset your {{.AppName}} password. This link expires in {{.ExpiresIn}}.</p>
<p><a href="{{.ResetURL}}">Reset password</a></p>
<p>If you did not request this, you can ignore this email.</p>
</body></html>`

const quotaTemplate = `<!doctype html>
<html><body>
<p>Hello {{.DisplayName}},</p>
<p><strong>{{.Title}}</strong></p>
<p>Your {{.AppName}} storage usage is {{.UsagePercent}}% ({{.Usage}} of {{.Limit}}).</p>
<p>Bundles: {{.BundleCount}}<br>Snapshots: {{.SnapshotCount}}</p>
</body></html>`

const genericNotificationTemplate = `<!doctype html>
<html><body>
<p>Hello {{.DisplayName}},</p>
<p><strong>{{.Subject}}</strong></p>
<p>{{.Message}}</p>
<p>{{.AppName}}</p>
</body></html>`
