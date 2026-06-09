package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/historysync/hsync-server/pkg/buildinfo"
	"github.com/historysync/hsync-server/pkg/config"
	"github.com/historysync/hsync-server/pkg/opsrehearsal"
	"github.com/historysync/hsync-server/pkg/service"
)

func runOps(args []string) int {
	if len(args) == 0 {
		printOpsUsage()
		return 2
	}
	switch args[0] {
	case "rehearsal":
		return runOpsRehearsal(args[1:])
	default:
		fmt.Fprintf(os.Stderr, "unknown ops command %q\n", args[0])
		printOpsUsage()
		return 2
	}
}

func runOpsRehearsal(args []string) int {
	fs := flag.NewFlagSet("ops rehearsal", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	format := fs.String("format", "human", "output format: human or json")
	timeout := fs.Duration("timeout", 60*time.Second, "maximum time for the full rehearsal")
	doctorTimeout := fs.Duration("doctor-timeout", 5*time.Second, "maximum time for doctor probes")
	limit := fs.Int("limit", int(service.DefaultOpsRestoreLimit), "maximum metadata rows per restore manifest artifact")
	manifestPath := fs.String("manifest", "", "optional restore manifest JSON file to verify")
	sinceRaw := fs.String("since", "", "optional RFC3339 lower bound for support bundle summaries")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(os.Stderr, "ops rehearsal: unexpected argument %q\n", fs.Arg(0))
		return 2
	}

	cfg, err := config.LoadUnchecked()
	if err != nil {
		result := opsrehearsal.Result{
			SchemaVersion: 1,
			Edition:       "community",
			GeneratedAt:   time.Now().UTC(),
			Overall:       opsrehearsal.StatusError,
			Steps: []opsrehearsal.StepResult{{
				ID:       "config.load",
				Name:     "Load config",
				Status:   opsrehearsal.StatusError,
				Blocking: true,
				Action:   "Fix config.yaml or HSYNC_* environment variables, then rerun `ops rehearsal`.",
				Details:  map[string]any{"error": err.Error()},
			}},
		}
		if writeErr := writeRehearsalReport(os.Stdout, *format, result); writeErr != nil {
			fmt.Fprintln(os.Stderr, writeErr)
			return 2
		}
		return 1
	}

	var manifest *service.OpsRestoreManifest
	if strings.TrimSpace(*manifestPath) != "" {
		loaded, err := loadRestoreManifest(*manifestPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "ops rehearsal: load manifest: %v\n", err)
			return 2
		}
		manifest = &loaded
	}
	var since time.Time
	if strings.TrimSpace(*sinceRaw) != "" {
		parsed, err := time.Parse(time.RFC3339, strings.TrimSpace(*sinceRaw))
		if err != nil {
			fmt.Fprintf(os.Stderr, "ops rehearsal: since must be RFC3339: %v\n", err)
			return 2
		}
		since = parsed
	}

	result, err := opsrehearsal.Run(context.Background(), opsrehearsal.Options{
		Edition:       "community",
		Config:        cfg,
		BuildInfo:     buildinfo.WithEdition("community"),
		Timeout:       *timeout,
		DoctorTimeout: *doctorTimeout,
		RestoreLimit:  int32(*limit),
		Manifest:      manifest,
		SupportSince:  since,
		CloseRuntime:  true,
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if err := writeRehearsalReport(os.Stdout, *format, result); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	if result.Overall == opsrehearsal.StatusError {
		return 1
	}
	return 0
}

func loadRestoreManifest(path string) (service.OpsRestoreManifest, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return service.OpsRestoreManifest{}, err
	}
	var manifest service.OpsRestoreManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return service.OpsRestoreManifest{}, err
	}
	if manifest.Version == 0 && manifest.GeneratedAt.IsZero() && len(manifest.Artifacts) == 0 && len(manifest.Objects) == 0 {
		return service.OpsRestoreManifest{}, fmt.Errorf("manifest is empty")
	}
	return manifest, nil
}

func writeRehearsalReport(out *os.File, format string, result opsrehearsal.Result) error {
	switch strings.ToLower(strings.TrimSpace(format)) {
	case "", "human", "text":
		return opsrehearsal.WriteHuman(out, result)
	case "json":
		return opsrehearsal.WriteJSON(out, result)
	default:
		return fmt.Errorf("unsupported ops rehearsal format %q; use human or json", format)
	}
}

func printOpsUsage() {
	fmt.Println("usage:")
	fmt.Println("  hsync-server ops rehearsal [--format human|json] [--timeout 60s] [--doctor-timeout 5s] [--limit 1000] [--manifest manifest.json] [--since RFC3339]")
}
