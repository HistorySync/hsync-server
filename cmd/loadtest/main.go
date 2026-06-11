package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
)

type config struct {
	BaseURL          string
	AdminKey         string
	Users            int
	BundleUploads    int
	BundleSizeBytes  int
	RateLimitBursts  int
	WSAttempts       int
	Timeout          time.Duration
	PollTimeout      time.Duration
	OutputJSON       bool
}

type report struct {
	RunAt               time.Time         `json:"run_at"`
	BaseURL             string            `json:"base_url"`
	Scenarios           []scenarioReport  `json:"scenarios"`
	RejectionReasons    map[string]int    `json:"rejection_reasons"`
	QuotaRollbackCount  float64           `json:"quota_rollback_count"`
	RateLimitFallback   map[string]bool   `json:"rate_limit_fallback"`
	NotificationSummary notificationState `json:"notification_summary"`
}

type scenarioReport struct {
	Name       string        `json:"name"`
	Operations int           `json:"operations"`
	Successes  int           `json:"successes"`
	Failures   int           `json:"failures"`
	Errors     int           `json:"errors"`
	Rejections int           `json:"rejections"`
	ErrorRate  float64       `json:"error_rate"`
	P50        time.Duration `json:"p50"`
	P95        time.Duration `json:"p95"`
}

type notificationState struct {
	Seen   int `json:"seen"`
	Sent   int `json:"sent"`
	Failed int `json:"failed"`
}

type metricsSnapshot struct {
	NotificationBillingSuccess float64
	QuotaRollbackCount         float64
	WSCapacityRejections       float64
	RateLimitFallback          map[string]bool
}

type scenarioTracker struct {
	name       string
	durations  []time.Duration
	operations int
	successes  int
	failures   int
	errors     int
	rejections int
}

type userSession struct {
	Email       string
	Password    string
	AccessToken string
	UserID      string
	DeviceUUID  string
	DeviceToken string
	Bundles     []string
	SnapshotID  string
}

type client struct {
	baseURL string
	http    *http.Client
}

type responseEnvelope struct {
	RequestID string `json:"request_id"`
	Error     *struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func main() {
	cfg := parseFlags()
	ctx, cancel := context.WithTimeout(context.Background(), cfg.Timeout)
	defer cancel()

	cl := &client{
		baseURL: strings.TrimRight(cfg.BaseURL, "/"),
		http: &http.Client{
			Timeout: 30 * time.Second,
		},
	}

	rep, err := run(ctx, cl, cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "loadtest failed: %v\n", err)
		os.Exit(1)
	}

	if cfg.OutputJSON {
		out, err := json.MarshalIndent(rep, "", "  ")
		if err != nil {
			fmt.Fprintf(os.Stderr, "marshal report: %v\n", err)
			os.Exit(1)
		}
		fmt.Println(string(out))
		return
	}
	printReport(rep)
}

func parseFlags() config {
	cfg := config{}
	flag.StringVar(&cfg.BaseURL, "base-url", getenv("HSYNC_LOAD_BASE_URL", "http://127.0.0.1:8080"), "target server base URL")
	flag.StringVar(&cfg.AdminKey, "admin-key", getenv("HSYNC_LOAD_ADMIN_KEY", "load-admin-key"), "CE admin key")
	flag.IntVar(&cfg.Users, "users", getenvInt("HSYNC_LOAD_USERS", 3), "number of register/login users")
	flag.IntVar(&cfg.BundleUploads, "bundle-uploads", getenvInt("HSYNC_LOAD_BUNDLE_UPLOADS", 3), "bundle uploads per run")
	flag.IntVar(&cfg.BundleSizeBytes, "bundle-size-bytes", getenvInt("HSYNC_LOAD_BUNDLE_SIZE_BYTES", 8*1024*1024), "bundle payload size")
	flag.IntVar(&cfg.RateLimitBursts, "rate-limit-bursts", getenvInt("HSYNC_LOAD_RATE_LIMIT_BURSTS", 12), "login burst attempts for rate-limit rehearsal")
	flag.IntVar(&cfg.WSAttempts, "ws-attempts", getenvInt("HSYNC_LOAD_WS_ATTEMPTS", 3), "WebSocket connection attempts")
	flag.DurationVar(&cfg.Timeout, "timeout", getenvDuration("HSYNC_LOAD_TIMEOUT", 2*time.Minute), "overall timeout")
	flag.DurationVar(&cfg.PollTimeout, "poll-timeout", getenvDuration("HSYNC_LOAD_POLL_TIMEOUT", 20*time.Second), "outbox/metrics polling timeout")
	flag.BoolVar(&cfg.OutputJSON, "json", false, "print JSON report")
	flag.Parse()
	return cfg
}

func run(ctx context.Context, cl *client, cfg config) (*report, error) {
	rep := &report{
		RunAt:            time.Now().UTC(),
		BaseURL:          cl.baseURL,
		RejectionReasons: map[string]int{},
		RateLimitFallback: map[string]bool{
			"memory":  false,
			"deny":    false,
			"disable": false,
		},
	}

	startMetrics, err := readMetrics(ctx, cl)
	if err != nil {
		return nil, err
	}

	users, regScenario, err := runRegisterLogin(ctx, cl, cfg, rep.RejectionReasons)
	if err != nil {
		return nil, err
	}
	rep.Scenarios = append(rep.Scenarios, regScenario.report())

	deviceScenario, err := runDeviceAndSync(ctx, cl, cfg, users, rep.RejectionReasons)
	if err != nil {
		return nil, err
	}
	rep.Scenarios = append(rep.Scenarios, deviceScenario.report())

	rateLimitScenario, err := runRateLimitBurst(ctx, cl, cfg, users[0], rep.RejectionReasons)
	if err != nil {
		return nil, err
	}
	rep.Scenarios = append(rep.Scenarios, rateLimitScenario.report())

	wsScenario, wsCapacityDelta, err := runWebSocketCap(ctx, cl, cfg, users[0], startMetrics.WSCapacityRejections, rep.RejectionReasons)
	if err != nil {
		return nil, err
	}
	rep.Scenarios = append(rep.Scenarios, wsScenario.report())
	if wsCapacityDelta > 0 {
		rep.RejectionReasons["ws.capacity"] += wsCapacityDelta
	}

	beforeNotificationMetrics, err := readMetrics(ctx, cl)
	if err != nil {
		return nil, err
	}

	notificationUser := users[len(users)-1]
	if len(users) == 1 {
		notificationUser = nil
	}
	outboxScenario, summary, err := runNotificationDrain(ctx, cl, cfg, notificationUser, beforeNotificationMetrics.NotificationBillingSuccess, rep.RejectionReasons)
	if err != nil {
		return nil, err
	}
	rep.Scenarios = append(rep.Scenarios, outboxScenario.report())
	rep.NotificationSummary = summary

	finalMetrics, err := readMetrics(ctx, cl)
	if err != nil {
		return nil, err
	}
	rep.RateLimitFallback = finalMetrics.RateLimitFallback
	rep.QuotaRollbackCount = finalMetrics.QuotaRollbackCount - startMetrics.QuotaRollbackCount
	if rep.QuotaRollbackCount < 0 {
		rep.QuotaRollbackCount = 0
	}

	return rep, nil
}

func runRegisterLogin(ctx context.Context, cl *client, cfg config, reasons map[string]int) ([]*userSession, *scenarioTracker, error) {
	tracker := newScenario("ce_register_login")
	users := make([]*userSession, 0, cfg.Users)
	for i := 0; i < cfg.Users; i++ {
		user := &userSession{
			Email:    fmt.Sprintf("load-%d-%d@example.com", time.Now().UnixNano(), i),
			Password: "load-password-123",
		}
		body := map[string]any{
			"email":        user.Email,
			"password":     user.Password,
			"display_name": fmt.Sprintf("Load %d", i),
		}
		start := time.Now()
		var reg struct {
			User struct {
				ID string `json:"id"`
			} `json:"user"`
			AccessToken string `json:"access_token"`
		}
		if err := cl.postJSON(ctx, "/api/v1/auth/register", body, nil, &reg); err != nil {
			tracker.fail(time.Since(start), err, reasons)
			return nil, tracker, fmt.Errorf("register %s: %w", user.Email, err)
		}
		tracker.ok(time.Since(start))
		user.UserID = reg.User.ID

		start = time.Now()
		var login struct {
			AccessToken string `json:"access_token"`
		}
		if err := cl.postJSON(ctx, "/api/v1/auth/login", map[string]any{
			"email":    user.Email,
			"password": user.Password,
		}, nil, &login); err != nil {
			tracker.fail(time.Since(start), err, reasons)
			return nil, tracker, fmt.Errorf("login %s: %w", user.Email, err)
		}
		tracker.ok(time.Since(start))
		user.AccessToken = login.AccessToken
		users = append(users, user)
	}
	return users, tracker, nil
}

func runDeviceAndSync(ctx context.Context, cl *client, cfg config, users []*userSession, reasons map[string]int) (*scenarioTracker, error) {
	tracker := newScenario("ce_bundle_snapshot_sync")
	user := users[0]
	user.DeviceUUID = uuid.NewString()

	start := time.Now()
	var tokenResp struct {
		DeviceToken string `json:"device_token"`
	}
	if err := cl.postJSON(ctx, "/api/v1/devices/"+user.DeviceUUID+"/token", map[string]any{
		"device_name": "load-laptop",
		"platform":    "windows",
		"app_version": "loadtest",
	}, bearer(user.AccessToken), &tokenResp); err != nil {
		tracker.fail(time.Since(start), err, reasons)
		return tracker, fmt.Errorf("request device token: %w", err)
	}
	tracker.ok(time.Since(start))
	user.DeviceToken = tokenResp.DeviceToken

	payload := bytes.Repeat([]byte("b"), cfg.BundleSizeBytes)
	for i := 0; i < cfg.BundleUploads; i++ {
		bundleID := fmt.Sprintf("bundle-%d-%d", time.Now().UnixNano(), i)
		start = time.Now()
		if err := cl.uploadBundle(ctx, user, bundleID, payload); err != nil {
			tracker.fail(time.Since(start), err, reasons)
			return tracker, fmt.Errorf("upload bundle %s: %w", bundleID, err)
		}
		tracker.ok(time.Since(start))
		user.Bundles = append(user.Bundles, bundleID)
	}

	start = time.Now()
	var listResp struct {
		Bundles []struct {
			BundleID string `json:"bundle_id"`
		} `json:"bundles"`
	}
	if err := cl.getJSON(ctx, "/api/v1/bundles?limit=50", bearer(user.AccessToken), &listResp); err != nil {
		tracker.fail(time.Since(start), err, reasons)
		return tracker, fmt.Errorf("list bundles: %w", err)
	}
	tracker.ok(time.Since(start))

	for _, bundleID := range user.Bundles {
		start = time.Now()
		if _, err := cl.getRaw(ctx, "/api/v1/bundles/"+url.PathEscape(bundleID), bearer(user.AccessToken)); err != nil {
			tracker.fail(time.Since(start), err, reasons)
			return tracker, fmt.Errorf("download bundle %s: %w", bundleID, err)
		}
		tracker.ok(time.Since(start))
	}

	user.SnapshotID = fmt.Sprintf("snapshot-%d", time.Now().UnixNano())
	start = time.Now()
	if err := cl.uploadSnapshot(ctx, user, user.SnapshotID, payload[:min(len(payload), 1024*1024)]); err != nil {
		tracker.fail(time.Since(start), err, reasons)
		return tracker, fmt.Errorf("upload snapshot: %w", err)
	}
	tracker.ok(time.Since(start))

	start = time.Now()
	if _, err := cl.getRaw(ctx, "/api/v1/snapshots/"+url.PathEscape(user.SnapshotID), bearer(user.AccessToken)); err != nil {
		tracker.fail(time.Since(start), err, reasons)
		return tracker, fmt.Errorf("download snapshot: %w", err)
	}
	tracker.ok(time.Since(start))

	return tracker, nil
}

func runRateLimitBurst(ctx context.Context, cl *client, cfg config, user *userSession, reasons map[string]int) (*scenarioTracker, error) {
	tracker := newScenario("ce_rate_limit_fallback")
	for i := 0; i < cfg.RateLimitBursts; i++ {
		start := time.Now()
		err := cl.postJSON(ctx, "/api/v1/auth/login", map[string]any{
			"email":    user.Email,
			"password": user.Password,
		}, nil, &struct{}{})
		if err != nil {
			tracker.fail(time.Since(start), err, reasons)
			continue
		}
		tracker.ok(time.Since(start))
	}
	return tracker, nil
}

func runWebSocketCap(ctx context.Context, cl *client, cfg config, user *userSession, beforeCapacity float64, reasons map[string]int) (*scenarioTracker, int, error) {
	tracker := newScenario("ce_ws_connect_cap")
	wsURL := strings.Replace(cl.baseURL, "http://", "ws://", 1)
	wsURL = strings.Replace(wsURL, "https://", "wss://", 1) + "/ws/push"

	var conns []*websocket.Conn
	defer func() {
		for _, conn := range conns {
			_ = conn.Close()
		}
	}()

	for i := 0; i < cfg.WSAttempts; i++ {
		start := time.Now()
		header := http.Header{}
		header.Set("Authorization", "Bearer "+user.DeviceToken)
		conn, resp, err := websocket.DefaultDialer.DialContext(ctx, wsURL, header)
		if err != nil {
			if resp != nil && resp.Body != nil {
				_, _ = io.Copy(io.Discard, resp.Body)
				_ = resp.Body.Close()
			}
			tracker.fail(time.Since(start), httpStatusError(resp), reasons)
			continue
		}
		conns = append(conns, conn)
		tracker.ok(time.Since(start))
	}

	afterMetrics, err := readMetrics(ctx, cl)
	if err != nil {
		return tracker, 0, fmt.Errorf("read ws metrics: %w", err)
	}
	delta := int(afterMetrics.WSCapacityRejections - beforeCapacity)
	if delta < 0 {
		delta = 0
	}
	return tracker, delta, nil
}

func runNotificationDrain(ctx context.Context, cl *client, cfg config, user *userSession, beforeSuccess float64, reasons map[string]int) (*scenarioTracker, notificationState, error) {
	tracker := newScenario("ce_notification_outbox_drain")
	if user == nil {
		var err error
		user, err = createScenarioUser(ctx, cl, tracker, reasons, "notify")
		if err != nil {
			return tracker, notificationState{}, err
		}
	}

	beforeState, err := cl.notificationState(ctx, cfg.AdminKey)
	if err != nil {
		return tracker, notificationState{}, fmt.Errorf("notification baseline: %w", err)
	}

	user.DeviceUUID = uuid.NewString()
	start := time.Now()
	var tokenResp struct {
		DeviceToken string `json:"device_token"`
	}
	if err := cl.postJSON(ctx, "/api/v1/devices/"+user.DeviceUUID+"/token", map[string]any{
		"device_name": "load-notify",
		"platform":    "windows",
		"app_version": "loadtest",
	}, bearer(user.AccessToken), &tokenResp); err != nil {
		tracker.fail(time.Since(start), err, reasons)
		return tracker, notificationState{}, fmt.Errorf("request notification device token: %w", err)
	}
	tracker.ok(time.Since(start))
	user.DeviceToken = tokenResp.DeviceToken

	payload := bytes.Repeat([]byte("n"), notificationTriggerSize(cfg))
	bundleID := fmt.Sprintf("notification-%d", time.Now().UnixNano())
	start = time.Now()
	if err := cl.uploadBundle(ctx, user, bundleID, payload); err != nil {
		tracker.fail(time.Since(start), err, reasons)
		return tracker, notificationState{}, fmt.Errorf("trigger notification upload: %w", err)
	}
	tracker.ok(time.Since(start))

	deadline := time.Now().Add(cfg.PollTimeout)
	var summary notificationState

	for time.Now().Before(deadline) {
		start = time.Now()
		state, err := cl.notificationState(ctx, cfg.AdminKey)
		if err != nil {
			tracker.fail(time.Since(start), err, reasons)
			return tracker, summary, fmt.Errorf("notification state: %w", err)
		}
		metrics, err := readMetrics(ctx, cl)
		if err != nil {
			tracker.fail(time.Since(start), err, reasons)
			return tracker, summary, fmt.Errorf("metrics poll: %w", err)
		}
		summary = state
		if metrics.NotificationBillingSuccess > beforeSuccess &&
			state.Seen > beforeState.Seen &&
			state.Sent > beforeState.Sent {
			tracker.ok(time.Since(start))
			return tracker, summary, nil
		}
		tracker.ok(time.Since(start))
		select {
		case <-ctx.Done():
			return tracker, summary, ctx.Err()
		case <-time.After(time.Second):
		}
	}
	return tracker, summary, fmt.Errorf("notification outbox did not drain before %s", cfg.PollTimeout)
}

func readMetrics(ctx context.Context, cl *client) (metricsSnapshot, error) {
	body, err := cl.getRaw(ctx, "/metrics", nil)
	if err != nil {
		return metricsSnapshot{}, err
	}
	lines := strings.Split(string(body), "\n")
	snapshot := metricsSnapshot{
		RateLimitFallback: map[string]bool{
			"memory":  false,
			"deny":    false,
			"disable": false,
		},
	}
	fallback := map[string]bool{
		"memory":  false,
		"deny":    false,
		"disable": false,
	}
	for _, line := range lines {
		if strings.HasPrefix(line, "hsync_notification_delivery_total{") &&
			strings.Contains(line, `category="billing"`) &&
			strings.Contains(line, `result="success"`) {
			snapshot.NotificationBillingSuccess = parseMetricValue(line)
		}
		if strings.HasPrefix(line, "hsync_quota_reservations_total{") &&
			strings.Contains(line, `result="rollback"`) {
			snapshot.QuotaRollbackCount += parseMetricValue(line)
		}
		if strings.HasPrefix(line, "hsync_websocket_upgrade_rejections_total{") &&
			strings.Contains(line, `reason="capacity"`) {
			snapshot.WSCapacityRejections = parseMetricValue(line)
		}
		if strings.HasPrefix(line, "hsync_rate_limit_redis_fallback_active{") {
			mode := metricLabel(line, "mode")
			if parseMetricValue(line) >= 1 {
				fallback[mode] = true
			}
		}
	}
	snapshot.RateLimitFallback = fallback
	return snapshot, nil
}

func (c *client) notificationState(ctx context.Context, adminKey string) (notificationState, error) {
	var out struct {
		Count   int `json:"count"`
		Records []struct {
			Type   string `json:"type"`
			Status string `json:"status"`
		} `json:"records"`
	}
	headers := http.Header{}
	headers.Set("X-Admin-Key", adminKey)
	if err := c.getJSON(ctx, "/admin/exports/operational-records?record_type=notification_outbox&limit=100", headers, &out); err != nil {
		return notificationState{}, err
	}
	state := notificationState{Seen: out.Count}
	for _, record := range out.Records {
		if record.Type != "quota.warning" {
			continue
		}
		switch record.Status {
		case "sent":
			state.Sent++
		case "failed":
			state.Failed++
		}
	}
	return state, nil
}

func createScenarioUser(ctx context.Context, cl *client, tracker *scenarioTracker, reasons map[string]int, prefix string) (*userSession, error) {
	user := &userSession{
		Email:    fmt.Sprintf("%s-%d@example.com", prefix, time.Now().UnixNano()),
		Password: "load-password-123",
	}
	body := map[string]any{
		"email":        user.Email,
		"password":     user.Password,
		"display_name": strings.Title(prefix) + " Load",
	}

	start := time.Now()
	var reg struct {
		User struct {
			ID string `json:"id"`
		} `json:"user"`
	}
	if err := cl.postJSON(ctx, "/api/v1/auth/register", body, nil, &reg); err != nil {
		tracker.fail(time.Since(start), err, reasons)
		return nil, fmt.Errorf("register %s: %w", user.Email, err)
	}
	tracker.ok(time.Since(start))
	user.UserID = reg.User.ID

	start = time.Now()
	var login struct {
		AccessToken string `json:"access_token"`
	}
	if err := cl.postJSON(ctx, "/api/v1/auth/login", map[string]any{
		"email":    user.Email,
		"password": user.Password,
	}, nil, &login); err != nil {
		tracker.fail(time.Since(start), err, reasons)
		return nil, fmt.Errorf("login %s: %w", user.Email, err)
	}
	tracker.ok(time.Since(start))
	user.AccessToken = login.AccessToken

	return user, nil
}

func (c *client) uploadBundle(ctx context.Context, user *userSession, bundleID string, payload []byte) error {
	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)
	if err := writer.WriteField("bundle_id", bundleID); err != nil {
		return err
	}
	if err := writer.WriteField("device_uuid", user.DeviceUUID); err != nil {
		return err
	}
	if err := writer.WriteField("lamport_lo", strconv.FormatInt(time.Now().UnixNano(), 10)); err != nil {
		return err
	}
	if err := writer.WriteField("lamport_hi", strconv.FormatInt(time.Now().UnixNano()+1, 10)); err != nil {
		return err
	}
	if err := writer.WriteField("event_count", "10"); err != nil {
		return err
	}
	if err := writer.WriteField("cipher_id", "1"); err != nil {
		return err
	}
	if err := writer.WriteField("key_generation", "1"); err != nil {
		return err
	}
	part, err := writer.CreateFormFile("bundle", bundleID+".hsb")
	if err != nil {
		return err
	}
	if _, err := part.Write(payload); err != nil {
		return err
	}
	if err := writer.Close(); err != nil {
		return err
	}
	return c.doNoJSON(ctx, http.MethodPost, "/api/v1/bundles", bearer(user.AccessToken), writer.FormDataContentType(), body)
}

func (c *client) uploadSnapshot(ctx context.Context, user *userSession, snapshotID string, payload []byte) error {
	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)
	if err := writer.WriteField("snapshot_id", snapshotID); err != nil {
		return err
	}
	if err := writer.WriteField("base_hlc", strconv.FormatInt(time.Now().UnixNano(), 10)); err != nil {
		return err
	}
	if err := writer.WriteField("cipher_id", "1"); err != nil {
		return err
	}
	if err := writer.WriteField("key_generation", "1"); err != nil {
		return err
	}
	part, err := writer.CreateFormFile("snapshot", snapshotID+".hsb")
	if err != nil {
		return err
	}
	if _, err := part.Write(payload); err != nil {
		return err
	}
	if err := writer.Close(); err != nil {
		return err
	}
	return c.doNoJSON(ctx, http.MethodPost, "/api/v1/snapshots", bearer(user.AccessToken), writer.FormDataContentType(), body)
}

func (c *client) postJSON(ctx context.Context, path string, payload any, headers http.Header, out any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	return c.doJSON(ctx, http.MethodPost, path, headers, "application/json", bytes.NewReader(body), out)
}

func (c *client) getJSON(ctx context.Context, path string, headers http.Header, out any) error {
	return c.doJSON(ctx, http.MethodGet, path, headers, "", nil, out)
}

func (c *client) getRaw(ctx context.Context, path string, headers http.Header) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return nil, err
	}
	copyHeaders(req.Header, headers)
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, decodeEnvelopeError(resp.StatusCode, body)
	}
	return body, nil
}

func (c *client) doNoJSON(ctx context.Context, method, path string, headers http.Header, contentType string, body io.Reader) error {
	return c.doJSON(ctx, method, path, headers, contentType, body, nil)
}

func (c *client) doJSON(ctx context.Context, method, path string, headers http.Header, contentType string, body io.Reader, out any) error {
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, body)
	if err != nil {
		return err
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	copyHeaders(req.Header, headers)
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return decodeEnvelopeError(resp.StatusCode, raw)
	}
	if out != nil && len(raw) > 0 {
		if err := json.Unmarshal(raw, out); err != nil {
			return fmt.Errorf("decode %s %s: %w", method, path, err)
		}
	}
	return nil
}

func decodeEnvelopeError(status int, body []byte) error {
	var env responseEnvelope
	if err := json.Unmarshal(body, &env); err == nil && env.Error != nil {
		return &statusError{Status: status, Code: env.Error.Code, Message: env.Error.Message}
	}
	return &statusError{Status: status, Message: strings.TrimSpace(string(body))}
}

func httpStatusError(resp *http.Response) error {
	if resp == nil {
		return fmt.Errorf("websocket dial failed")
	}
	return &statusError{Status: resp.StatusCode, Message: resp.Status}
}

func (s *scenarioTracker) report() scenarioReport {
	sort.Slice(s.durations, func(i, j int) bool { return s.durations[i] < s.durations[j] })
	return scenarioReport{
		Name:       s.name,
		Operations: s.operations,
		Successes:  s.successes,
		Failures:   s.failures,
		Errors:     s.errors,
		Rejections: s.rejections,
		ErrorRate:  percent(s.failures, s.operations),
		P50:        quantile(s.durations, 0.50),
		P95:        quantile(s.durations, 0.95),
	}
}

func newScenario(name string) *scenarioTracker {
	return &scenarioTracker{name: name}
}

func (s *scenarioTracker) ok(d time.Duration) {
	s.operations++
	s.successes++
	s.durations = append(s.durations, d)
}

func (s *scenarioTracker) fail(d time.Duration, err error, reasons map[string]int) {
	s.operations++
	s.failures++
	s.durations = append(s.durations, d)
	if isRejection(err) {
		s.rejections++
		reasons[rejectionReason(err)]++
		return
	}
	s.errors++
}

type statusError struct {
	Status  int
	Code    string
	Message string
}

func (e *statusError) Error() string {
	if e.Code != "" {
		return fmt.Sprintf("%d %s: %s", e.Status, e.Code, e.Message)
	}
	return fmt.Sprintf("%d %s", e.Status, e.Message)
}

func isRejection(err error) bool {
	var statusErr *statusError
	if !asStatusError(err, &statusErr) {
		return false
	}
	if statusErr.Status == http.StatusTooManyRequests || statusErr.Status == http.StatusForbidden || statusErr.Status == http.StatusUnauthorized || statusErr.Status == 507 {
		return true
	}
	return statusErr.Code == "RATE_LIMITED" || statusErr.Code == "QUOTA_EXCEEDED"
}

func rejectionReason(err error) string {
	var statusErr *statusError
	if !asStatusError(err, &statusErr) {
		return "error"
	}
	if statusErr.Code != "" {
		return statusErr.Code
	}
	switch statusErr.Status {
	case http.StatusTooManyRequests:
		return "HTTP_429"
	case http.StatusForbidden:
		return "HTTP_403"
	case http.StatusUnauthorized:
		return "HTTP_401"
	default:
		return "HTTP_" + strconv.Itoa(statusErr.Status)
	}
}

func asStatusError(err error, target **statusError) bool {
	se, ok := err.(*statusError)
	if !ok {
		return false
	}
	*target = se
	return true
}

func quantile(values []time.Duration, q float64) time.Duration {
	if len(values) == 0 {
		return 0
	}
	if len(values) == 1 {
		return values[0]
	}
	idx := int(float64(len(values)-1) * q)
	if idx < 0 {
		idx = 0
	}
	if idx >= len(values) {
		idx = len(values) - 1
	}
	return values[idx]
}

func percent(num, denom int) float64 {
	if denom == 0 {
		return 0
	}
	return float64(num) * 100 / float64(denom)
}

func printReport(rep *report) {
	fmt.Printf("run_at=%s base_url=%s\n", rep.RunAt.Format(time.RFC3339), rep.BaseURL)
	for _, scenario := range rep.Scenarios {
		fmt.Printf(
			"scenario=%s ops=%d success=%d failure=%d error_rate=%.1f%% p50=%s p95=%s rejections=%d\n",
			scenario.Name,
			scenario.Operations,
			scenario.Successes,
			scenario.Failures,
			scenario.ErrorRate,
			scenario.P50,
			scenario.P95,
			scenario.Rejections,
		)
	}
	fmt.Printf("rejection_reasons=%s\n", formatReasons(rep.RejectionReasons))
	fmt.Printf("quota_rollback_count=%.0f\n", rep.QuotaRollbackCount)
	fmt.Printf("rate_limit_fallback memory=%t deny=%t disable=%t\n",
		rep.RateLimitFallback["memory"],
		rep.RateLimitFallback["deny"],
		rep.RateLimitFallback["disable"],
	)
	fmt.Printf("notification_outbox seen=%d sent=%d failed=%d\n",
		rep.NotificationSummary.Seen,
		rep.NotificationSummary.Sent,
		rep.NotificationSummary.Failed,
	)
}

func formatReasons(reasons map[string]int) string {
	if len(reasons) == 0 {
		return "{}"
	}
	keys := make([]string, 0, len(reasons))
	for key := range reasons {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, fmt.Sprintf("%s=%d", key, reasons[key]))
	}
	return "{" + strings.Join(parts, ", ") + "}"
}

func copyHeaders(dst http.Header, src http.Header) {
	for key, values := range src {
		for _, value := range values {
			dst.Add(key, value)
		}
	}
}

func bearer(token string) http.Header {
	header := http.Header{}
	header.Set("Authorization", "Bearer "+token)
	return header
}

func metricLabel(line, key string) string {
	start := strings.Index(line, key+`="`)
	if start < 0 {
		return ""
	}
	start += len(key) + 2
	end := strings.Index(line[start:], `"`)
	if end < 0 {
		return ""
	}
	return line[start : start+end]
}

func parseMetricValue(line string) float64 {
	fields := strings.Fields(line)
	if len(fields) == 0 {
		return 0
	}
	value, err := strconv.ParseFloat(fields[len(fields)-1], 64)
	if err != nil {
		return 0
	}
	return value
}

func notificationTriggerSize(cfg config) int {
	if cfg.BundleSizeBytes > 16*1024*1024 {
		return cfg.BundleSizeBytes
	}
	return 16 * 1024 * 1024
}

func getenv(key, fallback string) string {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	return value
}

func getenvInt(key string, fallback int) int {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback
	}
	value, err := strconv.Atoi(raw)
	if err != nil {
		return fallback
	}
	return value
}

func getenvDuration(key string, fallback time.Duration) time.Duration {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback
	}
	value, err := time.ParseDuration(raw)
	if err != nil {
		return fallback
	}
	return value
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
