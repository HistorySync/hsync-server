// Package observability owns the low-cardinality Prometheus metrics exposed by
// the CE server.
package observability

import (
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

var (
	registry = prometheus.NewRegistry()

	httpRequests = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "hsync_http_requests_total",
		Help: "Total HTTP requests handled by route, method, and result.",
	}, []string{"route", "method", "result"})

	authFailures = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "hsync_auth_failures_total",
		Help: "Total authentication failures by category and result.",
	}, []string{"category", "result"})

	uploads = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "hsync_uploads_total",
		Help: "Total bundle and snapshot upload outcomes.",
	}, []string{"category", "result"})

	quotaReservations = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "hsync_quota_reservations_total",
		Help: "Total quota reservation outcomes.",
	}, []string{"category", "result"})

	schedulerRuns = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "hsync_scheduler_runs_total",
		Help: "Total scheduler task runs.",
	}, []string{"task", "result"})

	schedulerDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "hsync_scheduler_run_duration_seconds",
		Help:    "Scheduler task run duration in seconds.",
		Buckets: prometheus.DefBuckets,
	}, []string{"task"})

	schedulerFailures = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "hsync_scheduler_failures_total",
		Help: "Total scheduler task failures.",
	}, []string{"task"})

	notificationDeliveries = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "hsync_notification_delivery_total",
		Help: "Total notification delivery outcomes.",
	}, []string{"category", "result"})

	idempotencyEvents = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "hsync_idempotency_events_total",
		Help: "Total idempotency outcomes.",
	}, []string{"result"})

	readinessDependencyStatus = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "hsync_readiness_dependency_status",
		Help: "Readiness dependency status, set to 1 for the current result and 0 for the other known results.",
	}, []string{"dependency", "result"})

	websocketConnectionsActive = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "hsync_websocket_connections_active",
		Help: "Current active WebSocket connections.",
	})

	websocketUpgradeRejections = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "hsync_websocket_upgrade_rejections_total",
		Help: "Total WebSocket upgrade rejections by reason.",
	}, []string{"reason"})

	rateLimitErrors = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "hsync_rate_limit_errors_total",
		Help: "Total rate limiter backend errors by policy, fail mode, and action.",
	}, []string{"policy", "fail_mode", "action"})

	rateLimitRedisFallbackActive = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "hsync_rate_limit_redis_fallback_active",
		Help: "Whether a Redis-unavailable rate-limit fallback mode is active in this process.",
	}, []string{"mode"})
)

func init() {
	registry.MustRegister(
		httpRequests,
		authFailures,
		uploads,
		quotaReservations,
		schedulerRuns,
		schedulerDuration,
		schedulerFailures,
		notificationDeliveries,
		idempotencyEvents,
		readinessDependencyStatus,
		websocketConnectionsActive,
		websocketUpgradeRejections,
		rateLimitErrors,
		rateLimitRedisFallbackActive,
	)
}

// Registry returns the process-local Prometheus registry for /metrics.
func Registry() *prometheus.Registry {
	return registry
}

func RecordHTTPRequest(route, method string, status int) {
	httpRequests.WithLabelValues(normalizeRoute(route), strings.ToUpper(method), statusResult(status)).Inc()
}

func RecordAuthFailure(category, result string) {
	authFailures.WithLabelValues(normalizeLabel(category, "unknown"), normalizeLabel(result, "failure")).Inc()
}

func RecordUpload(category, result string) {
	uploads.WithLabelValues(normalizeLabel(category, "unknown"), normalizeLabel(result, "failure")).Inc()
}

func RecordQuotaReservation(category, result string) {
	quotaReservations.WithLabelValues(normalizeLabel(category, "unknown"), normalizeLabel(result, "failure")).Inc()
}

func RecordSchedulerRun(task string, duration time.Duration, err error) {
	task = normalizeLabel(task, "unknown")
	result := "success"
	if err != nil {
		result = "failure"
		schedulerFailures.WithLabelValues(task).Inc()
	}
	schedulerRuns.WithLabelValues(task, result).Inc()
	schedulerDuration.WithLabelValues(task).Observe(duration.Seconds())
}

func RecordNotificationDelivery(category, result string) {
	notificationDeliveries.WithLabelValues(normalizeLabel(category, "unknown"), normalizeLabel(result, "failure")).Inc()
}

func RecordIdempotency(result string) {
	idempotencyEvents.WithLabelValues(normalizeLabel(result, "failure")).Inc()
}

func RecordReadinessDependency(dependency, result string) {
	dependency = normalizeLabel(dependency, "unknown")
	result = normalizeReadinessResult(result)
	for _, candidate := range []string{"ok", "disabled", "not_configured", "error"} {
		value := 0.0
		if candidate == result {
			value = 1
		}
		readinessDependencyStatus.WithLabelValues(dependency, candidate).Set(value)
	}
}

func SetWebSocketActiveConnections(count int) {
	websocketConnectionsActive.Set(float64(count))
}

func RecordWebSocketUpgradeRejected(reason string) {
	websocketUpgradeRejections.WithLabelValues(normalizeLabel(reason, "unknown")).Inc()
}

func RecordRateLimitError(policy, failMode, action string) {
	rateLimitErrors.WithLabelValues(
		normalizeLabel(policy, "default"),
		normalizeLabel(failMode, "fail_open"),
		normalizeLabel(action, "allow"),
	).Inc()
}

func SetRateLimitRedisFallbackActive(mode string) {
	mode = normalizeLabel(mode, "")
	for _, candidate := range []string{"memory", "deny", "disable"} {
		value := 0.0
		if mode == candidate {
			value = 1
		}
		rateLimitRedisFallbackActive.WithLabelValues(candidate).Set(value)
	}
}

func normalizeRoute(route string) string {
	route = strings.TrimSpace(route)
	if route == "" {
		return "unknown"
	}
	return route
}

func normalizeLabel(value, fallback string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return fallback
	}
	value = strings.ReplaceAll(value, ".", "_")
	value = strings.ReplaceAll(value, "-", "_")
	return value
}

func normalizeReadinessResult(result string) string {
	result = strings.ToLower(strings.TrimSpace(result))
	switch {
	case result == "ok":
		return "ok"
	case result == "disabled":
		return "disabled"
	case result == "not_configured":
		return "not_configured"
	case strings.HasPrefix(result, "error"):
		return "error"
	default:
		return "error"
	}
}

func statusResult(status int) string {
	switch {
	case status >= 200 && status < 400:
		return "success"
	case status >= 400 && status < 500:
		return "client_error"
	case status >= 500:
		return "server_error"
	default:
		return "unknown"
	}
}
