package handler

import (
	"encoding/csv"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gofiber/fiber/v3"

	"github.com/historysync/hsync-server/pkg/repository"
)

func TestOperationalExportCSVFormat(t *testing.T) {
	app := fiber.New()
	app.Get("/export", func(c fiber.Ctx) error {
		return sendOperationalExportCSV(c, "audit_logs", []repository.OperationalExportRecord{
			{
				RecordType: "audit_logs",
				Source:     "hot",
				ID:         "row-1",
				Timestamp:  time.Date(2026, 6, 6, 12, 0, 0, 0, time.UTC),
				Type:       "auth.login.success",
				Actor:      "user-1",
				Target:     "user:user-1",
				Details:    map[string]any{"ip": "127.0.0.1"},
			},
		})
	})

	resp, err := app.Test(httptest.NewRequest("GET", "/export", nil))
	if err != nil {
		t.Fatalf("app.Test: %v", err)
	}
	if resp.StatusCode != fiber.StatusOK {
		t.Fatalf("status = %d, want %d", resp.StatusCode, fiber.StatusOK)
	}
	if got := resp.Header.Get("Content-Type"); !strings.HasPrefix(got, "text/csv") {
		t.Fatalf("content-type = %q, want text/csv", got)
	}
	rows, err := csv.NewReader(resp.Body).ReadAll()
	if err != nil {
		t.Fatalf("read csv: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("csv rows = %d, want 2", len(rows))
	}
	if rows[0][0] != "record_type" || rows[1][0] != "audit_logs" || rows[1][4] != "auth.login.success" {
		t.Fatalf("csv rows = %#v", rows)
	}
	var details map[string]any
	if err := json.Unmarshal([]byte(rows[1][8]), &details); err != nil {
		t.Fatalf("details json: %v", err)
	}
	if details["ip"] != "127.0.0.1" {
		t.Fatalf("details = %#v, want ip", details)
	}
}

func TestOperationalExportParsers(t *testing.T) {
	if _, err := parseOperationalExportRecordType("audit_logs"); err != nil {
		t.Fatalf("record type error = %v", err)
	}
	if _, err := parseOperationalExportRecordType("users"); err == nil {
		t.Fatal("record type error = nil, want invalid type")
	}
	if _, err := parseOperationalExportFormat("csv"); err != nil {
		t.Fatalf("format error = %v", err)
	}
	if _, err := parseOperationalExportSource("all"); err != nil {
		t.Fatalf("source error = %v", err)
	}
	if _, err := parseOperationalExportTime("not-time", "from"); err == nil {
		t.Fatal("time error = nil, want invalid RFC3339")
	}
}
