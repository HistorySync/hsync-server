package opsrehearsal

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/historysync/hsync-server/pkg/migrate"
	"github.com/historysync/hsync-server/pkg/service"
)

func TestRunAggregatesBlockingStep(t *testing.T) {
	now := time.Date(2026, 6, 9, 1, 2, 3, 0, time.UTC)
	result, err := Run(context.Background(), Options{
		Edition: "community",
		Runtime: &Runtime{},
		Now: func() time.Time {
			return now
		},
		Steps: []StepRunner{
			{
				ID:   "extra.blocking",
				Name: "Blocking extra",
				Run: func(StepContext) (StepResult, error) {
					return StepResult{Status: StatusError, Blocking: true, Action: "fix it"}, nil
				},
			},
		},
		SchemaDrift: func(context.Context, *Runtime) ([]migrate.DriftFinding, error) {
			return nil, nil
		},
		MigrationStatus: func(context.Context, *Runtime) (any, bool, error) {
			return map[string]any{"consistent": true}, true, nil
		},
		SupportBundle: func(context.Context, StepContext) (any, error) {
			return map[string]any{"schema_version": 1}, nil
		},
		RestoreRehearsal: healthyRestore,
		EndpointList: func(StepContext) EndpointList {
			return EndpointList{SmokeCompatible: []EndpointRef{{Method: "GET", Path: "/healthz", Auth: "none"}}}
		},
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.Overall != StatusError {
		t.Fatalf("overall = %q, want error", result.Overall)
	}
	if got := result.Steps[len(result.Steps)-1]; got.ID != "extra.blocking" || !got.Blocking || got.Action != "fix it" {
		t.Fatalf("extra step = %+v", got)
	}
}

func TestWriteFormats(t *testing.T) {
	result := Result{
		SchemaVersion: 1,
		Edition:       "community",
		GeneratedAt:   time.Date(2026, 6, 9, 1, 2, 3, 0, time.UTC),
		Overall:       StatusOK,
		Steps: []StepResult{{
			ID:     "build.info",
			Name:   "Build info",
			Status: StatusOK,
		}},
	}
	var human bytes.Buffer
	if err := WriteHuman(&human, result); err != nil {
		t.Fatalf("WriteHuman() error = %v", err)
	}
	if !strings.Contains(human.String(), "HistorySync community recovery rehearsal") {
		t.Fatalf("human output = %q", human.String())
	}

	var raw bytes.Buffer
	if err := WriteJSON(&raw, result); err != nil {
		t.Fatalf("WriteJSON() error = %v", err)
	}
	var decoded Result
	if err := json.Unmarshal(raw.Bytes(), &decoded); err != nil {
		t.Fatalf("json output is invalid: %v", err)
	}
	if decoded.Overall != StatusOK || len(decoded.Steps) != 1 {
		t.Fatalf("decoded = %+v", decoded)
	}
}

func healthyRestore(context.Context, StepContext) (service.OpsRestoreReport, error) {
	return service.OpsRestoreReport{Overall: "ok"}, nil
}
