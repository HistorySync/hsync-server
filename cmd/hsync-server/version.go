package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"github.com/historysync/hsync-server/pkg/buildinfo"
)

type versionResponse struct {
	BuildInfo buildinfo.Info `json:"build_info"`
}

func runVersion(args []string) int {
	fs := flag.NewFlagSet("version", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	format := fs.String("format", "human", "output format: human or json")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	payload := versionResponse{BuildInfo: buildinfo.Current()}
	switch *format {
	case "human":
		fmt.Fprintf(os.Stdout, "version: %s\n", payload.BuildInfo.Version)
		fmt.Fprintf(os.Stdout, "commit: %s\n", payload.BuildInfo.Commit)
		fmt.Fprintf(os.Stdout, "build_time: %s\n", payload.BuildInfo.BuildTime)
		fmt.Fprintf(os.Stdout, "edition: %s\n", payload.BuildInfo.Edition)
		fmt.Fprintf(os.Stdout, "schema_version: %d\n", payload.BuildInfo.SchemaVersion)
		return 0
	case "json":
		encoder := json.NewEncoder(os.Stdout)
		encoder.SetIndent("", "  ")
		if err := encoder.Encode(payload); err != nil {
			fmt.Fprintf(os.Stderr, "encode version payload: %v\n", err)
			return 1
		}
		return 0
	default:
		fmt.Fprintf(os.Stderr, "unsupported version format %q; use human or json\n", *format)
		return 2
	}
}
