package buildinfo

import (
	"strings"

	"github.com/historysync/hsync-server/migrations"
	"github.com/historysync/hsync-server/pkg/migrate"
)

var (
	version   = "dev"
	commit    = "unknown"
	buildTime = "unknown"
	edition   = "community"
)

// Info describes the binary and schema contract compiled into this server.
type Info struct {
	Version       string `json:"version"`
	Commit        string `json:"commit"`
	BuildTime     string `json:"build_time"`
	Edition       string `json:"edition"`
	SchemaVersion int64  `json:"schema_version"`
}

// Current returns the build metadata injected by ldflags, with safe defaults
// for local development builds.
func Current() Info {
	return Info{
		Version:       valueOrDefault(version, "dev"),
		Commit:        valueOrDefault(commit, "unknown"),
		BuildTime:     valueOrDefault(buildTime, "unknown"),
		Edition:       valueOrDefault(edition, "community"),
		SchemaVersion: LatestSchemaVersion(),
	}
}

// WithEdition returns the current build info with a caller-owned edition value.
func WithEdition(value string) Info {
	info := Current()
	info.Edition = valueOrDefault(value, info.Edition)
	return info
}

// LatestSchemaVersion reports the highest CE migration version compiled into
// the binary. It returns 0 when the embedded migrations cannot be parsed.
func LatestSchemaVersion() int64 {
	all, err := migrate.Parse(migrations.FS)
	if err != nil {
		return 0
	}
	var latest int64
	for _, migration := range all {
		if migration.Version > latest {
			latest = migration.Version
		}
	}
	return latest
}

func valueOrDefault(value, fallback string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return fallback
	}
	return value
}
