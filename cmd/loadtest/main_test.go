package main

import (
	"errors"
	"net/http"
	"testing"
	"time"
)

func TestClassifyStatusClass(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		err  error
		want string
	}{
		{name: "forbidden", err: &statusError{Status: http.StatusForbidden}, want: "http_403"},
		{name: "rate limited", err: &statusError{Status: http.StatusTooManyRequests}, want: "http_429"},
		{name: "server error", err: &statusError{Status: http.StatusBadGateway}, want: "http_5xx"},
		{name: "other status", err: &statusError{Status: http.StatusUnauthorized}, want: "other"},
		{name: "plain error", err: errors.New("boom"), want: "other"},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := classifyStatusClass(tc.err); got != tc.want {
				t.Fatalf("classifyStatusClass(%v) = %q, want %q", tc.err, got, tc.want)
			}
		})
	}
}

func TestScenarioTrackerFailRecordsStatusClassAndReason(t *testing.T) {
	t.Parallel()

	reasons := map[string]int{}
	classes := map[string]int{
		"http_403": 0,
		"http_429": 0,
		"http_5xx": 0,
		"other":    0,
	}
	tracker := newScenario("ce_test")

	tracker.fail(time.Second, &statusError{Status: http.StatusTooManyRequests}, reasons, classes)

	if got := tracker.rejections; got != 1 {
		t.Fatalf("tracker.rejections = %d, want 1", got)
	}
	if got := reasons["HTTP_429"]; got != 1 {
		t.Fatalf("reasons[HTTP_429] = %d, want 1", got)
	}
	if got := classes["http_429"]; got != 1 {
		t.Fatalf("classes[http_429] = %d, want 1", got)
	}
}
