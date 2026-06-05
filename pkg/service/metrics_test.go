package service

import (
	"testing"

	"github.com/prometheus/client_model/go"

	"github.com/historysync/hsync-server/pkg/observability"
)

func metricValue(t *testing.T, name string, labels map[string]string) float64 {
	t.Helper()
	families, err := observability.Registry().Gather()
	if err != nil {
		t.Fatalf("gather metrics: %v", err)
	}
	for _, family := range families {
		if family.GetName() != name {
			continue
		}
		for _, metric := range family.GetMetric() {
			if !labelsMatch(metric, labels) {
				continue
			}
			if metric.Counter != nil {
				return metric.Counter.GetValue()
			}
			if metric.Gauge != nil {
				return metric.Gauge.GetValue()
			}
		}
	}
	return 0
}

func labelsMatch(metric *io_prometheus_client.Metric, labels map[string]string) bool {
	if len(labels) == 0 {
		return true
	}
	actual := map[string]string{}
	for _, pair := range metric.GetLabel() {
		actual[pair.GetName()] = pair.GetValue()
	}
	for key, value := range labels {
		if actual[key] != value {
			return false
		}
	}
	return true
}
