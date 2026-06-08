package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/historysync/hsync-server/pkg/config"
	"github.com/historysync/hsync-server/pkg/preflight"
)

func runDoctor(args []string) int {
	fs := flag.NewFlagSet("doctor", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	format := fs.String("format", "human", "output format: human or json")
	timeout := fs.Duration("timeout", 5*time.Second, "maximum time to spend on external probes")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	cfg, err := config.LoadUnchecked()
	if err != nil {
		report := preflight.NewReport("community", time.Now())
		report.Add(preflight.Check{
			ID:       "config.load",
			Scope:    "config",
			Severity: preflight.SeverityError,
			Message:  "Configuration could not be loaded.",
			Action:   "Fix config.yaml or HSYNC_* environment variables, then rerun doctor.",
			Details:  map[string]any{"error": err.Error()},
		})
		if writeErr := writeDoctorReport(os.Stdout, strings.ToLower(strings.TrimSpace(*format)), report); writeErr != nil {
			fmt.Fprintln(os.Stderr, writeErr)
			return 2
		}
		return 1
	}

	report := preflight.RunCE(context.Background(), cfg, preflight.CEOptions{
		Edition: "community",
		Timeout: *timeout,
	})
	if err := writeDoctorReport(os.Stdout, strings.ToLower(strings.TrimSpace(*format)), report); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	if report.HasErrors() {
		return 1
	}
	return 0
}

func writeDoctorReport(out *os.File, format string, report preflight.Report) error {
	switch format {
	case "", "human", "text":
		return preflight.WriteHuman(out, report)
	case "json":
		return preflight.WriteJSON(out, report)
	default:
		return fmt.Errorf("unsupported doctor format %q; use human or json", format)
	}
}
