package observability

import (
	"testing"

	io_prometheus_client "github.com/prometheus/client_model/go"
)

func TestDatabasePoolMetricsExposeBoundStats(t *testing.T) {
	dbPoolStatsMu.Lock()
	previous := dbPoolStatsProvider
	dbPoolStatsProvider = func() DatabasePoolStatsSnapshot {
		return DatabasePoolStatsSnapshot{
			AcquiredConns:           7,
			CanceledAcquireCount:    2,
			ConstructingConns:       1,
			EmptyAcquireCount:       4,
			EmptyAcquireWaitSeconds: 3.5,
			IdleConns:               5,
			MaxConns:                20,
			TotalConns:              12,
			AcquireCount:            19,
		}
	}
	dbPoolStatsMu.Unlock()
	t.Cleanup(func() {
		dbPoolStatsMu.Lock()
		dbPoolStatsProvider = previous
		dbPoolStatsMu.Unlock()
	})

	assertMetricValue(t, "hsync_db_pool_acquired_connections", 7)
	assertMetricValue(t, "hsync_db_pool_idle_connections", 5)
	assertMetricValue(t, "hsync_db_pool_total_connections", 12)
	assertMetricValue(t, "hsync_db_pool_max_connections", 20)
	assertMetricValue(t, "hsync_db_pool_constructing_connections", 1)
	assertMetricValue(t, "hsync_db_pool_acquire_total", 19)
	assertMetricValue(t, "hsync_db_pool_canceled_acquire_total", 2)
	assertMetricValue(t, "hsync_db_pool_empty_acquire_total", 4)
	assertMetricValue(t, "hsync_db_pool_empty_acquire_wait_seconds_total", 3.5)
}

func assertMetricValue(t *testing.T, name string, want float64) {
	t.Helper()
	families, err := Registry().Gather()
	if err != nil {
		t.Fatalf("gather metrics: %v", err)
	}
	for _, family := range families {
		if family.GetName() != name {
			continue
		}
		for _, metric := range family.GetMetric() {
			got := metricNumber(metric)
			if got != want {
				t.Fatalf("%s = %v, want %v", name, got, want)
			}
			return
		}
	}
	t.Fatalf("metric %s not found", name)
}

func metricNumber(metric *io_prometheus_client.Metric) float64 {
	if metric.Gauge != nil {
		return metric.Gauge.GetValue()
	}
	if metric.Counter != nil {
		return metric.Counter.GetValue()
	}
	return 0
}
