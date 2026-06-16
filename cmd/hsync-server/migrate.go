package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/rs/zerolog/log"

	"github.com/historysync/hsync-server/migrations"
	"github.com/historysync/hsync-server/pkg/config"
	"github.com/historysync/hsync-server/pkg/migrate"
	"github.com/historysync/hsync-server/pkg/repository"
)

// runMigrate handles the "migrate" subcommand and returns a process exit code.
//
// Usage:
//
//	hsync-server migrate up             Apply all pending migrations
//	hsync-server migrate down [n]       Roll back the last n migrations (default 1)
//	hsync-server migrate status [--json] Show applied, pending, and rollback plans
//	hsync-server migrate plan            Show the upgrade plan without applying it
//	hsync-server migrate create <name>  Create a new up/down migration file pair
func runMigrate(args []string) int {
	if len(args) == 0 {
		printMigrateUsage()
		return 2
	}

	switch args[0] {
	case "up":
		return migrateUpOrDown(true, 0)
	case "down":
		n := 1
		if len(args) > 1 {
			parsed, err := strconv.Atoi(args[1])
			if err != nil || parsed <= 0 {
				log.Error().Msgf("migrate down: invalid count %q", args[1])
				return 2
			}
			n = parsed
		}
		return migrateUpOrDown(false, n)
	case "status":
		return migrateStatus(args[1:], false)
	case "plan":
		return migrateStatus(args[1:], true)
	case "create":
		if len(args) < 2 {
			log.Error().Msg("migrate create: missing migration name")
			printMigrateUsage()
			return 2
		}
		up, down, err := migrate.Create("migrations", args[1])
		if err != nil {
			log.Error().Err(err).Msg("migrate create failed")
			return 1
		}
		log.Info().Str("up", up).Str("down", down).Msg("created migration files")
		return 0
	default:
		log.Error().Msgf("unknown migrate command %q", args[0])
		printMigrateUsage()
		return 2
	}
}

func migrateStatus(args []string, forceHuman bool) int {
	jsonOutput := false
	for _, arg := range args {
		switch arg {
		case "--json", "-json":
			jsonOutput = true
		default:
			log.Error().Msgf("migrate status: unknown argument %q", arg)
			return 2
		}
	}
	if forceHuman {
		jsonOutput = false
	}

	cfg, err := config.LoadForMigrations()
	if err != nil {
		log.Error().Err(err).Msg("failed to load config")
		return 1
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	pool, err := repository.NewPGXPoolWithConfig(ctx, cfg.DatabaseURL, repository.PGXPoolConfig{
		MaxConns:          cfg.DatabasePoolMaxConns,
		MinConns:          cfg.DatabasePoolMinConns,
		MaxConnLifetime:   cfg.DatabasePoolMaxConnLifetime,
		MaxConnIdleTime:   cfg.DatabasePoolMaxConnIdleTime,
		HealthCheckPeriod: cfg.DatabasePoolHealthCheckPeriod,
	})
	if err != nil {
		log.Error().Err(err).Msg("failed to connect to postgresql")
		return 1
	}
	defer pool.Close()

	status, err := migrate.Status(ctx, pool, migrations.FS, "schema_migrations", "community")
	if err != nil {
		log.Error().Err(err).Msg("migrate status failed")
		return 1
	}
	if jsonOutput {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(status); err != nil {
			log.Error().Err(err).Msg("write migrate status failed")
			return 1
		}
		return 0
	}
	printMigrationStatus(status)
	if !status.Consistent {
		return 1
	}
	return 0
}

func migrateUpOrDown(up bool, n int) int {
	cfg, err := config.LoadForMigrations()
	if err != nil {
		log.Error().Err(err).Msg("failed to load config")
		return 1
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	pool, err := repository.NewPGXPoolWithConfig(ctx, cfg.DatabaseURL, repository.PGXPoolConfig{
		MaxConns:          cfg.DatabasePoolMaxConns,
		MinConns:          cfg.DatabasePoolMinConns,
		MaxConnLifetime:   cfg.DatabasePoolMaxConnLifetime,
		MaxConnIdleTime:   cfg.DatabasePoolMaxConnIdleTime,
		HealthCheckPeriod: cfg.DatabasePoolHealthCheckPeriod,
	})
	if err != nil {
		log.Error().Err(err).Msg("failed to connect to postgresql")
		return 1
	}
	defer pool.Close()

	if up {
		applied, err := migrate.Up(ctx, pool, migrations.FS)
		if err != nil {
			log.Error().Err(err).Msg("migrate up failed")
			return 1
		}
		if len(applied) == 0 {
			log.Info().Msg("no pending migrations")
		}
		for _, m := range applied {
			log.Info().Int64("version", m.Version).Str("name", m.Name).Msg("applied migration")
		}
		return 0
	}

	rolledBack, err := migrate.Down(ctx, pool, migrations.FS, n)
	if err != nil {
		log.Error().Err(err).Msg("migrate down failed")
		return 1
	}
	if len(rolledBack) == 0 {
		log.Info().Msg("no migrations to roll back")
	}
	for _, m := range rolledBack {
		log.Info().Int64("version", m.Version).Str("name", m.Name).Msg("rolled back migration")
	}
	return 0
}

func printMigrateUsage() {
	fmt.Println("usage:")
	fmt.Println("  hsync-server migrate up             Apply all pending migrations")
	fmt.Println("  hsync-server migrate down [n]       Roll back the last n migrations (default 1)")
	fmt.Println("  hsync-server migrate status [--json] Show applied, pending, and rollback plans")
	fmt.Println("  hsync-server migrate plan            Show the upgrade plan without applying it")
	fmt.Println("  hsync-server migrate create <name>  Create a new up/down migration file pair")
}

func printMigrationStatus(status migrate.StatusReport) {
	fmt.Printf("Migration status for %s (%s)\n", status.Scope, status.TrackingTable)
	fmt.Printf("tracking_table_ok=%v consistent=%v applied=%d pending=%d rollback_available=%d\n",
		status.TrackingTableOk,
		status.Consistent,
		len(status.Applied),
		len(status.Pending),
		len(status.RollbackAvailable),
	)
	if len(status.Pending) > 0 {
		fmt.Println("\npending:")
		for _, m := range status.Pending {
			fmt.Printf("  %03d_%s\n", m.Version, m.Name)
		}
	}
	if len(status.RollbackAvailable) > 0 {
		fmt.Println("\nrollback_available:")
		for _, m := range status.RollbackAvailable {
			fmt.Printf("  %03d_%s\n", m.Version, m.Name)
		}
	}
	if len(status.Problems) > 0 {
		fmt.Println("\nproblems:")
		for _, problem := range status.Problems {
			fmt.Printf("  [%s] %s\n", problem.Severity, problem.Message)
			if problem.Action != "" {
				fmt.Printf("    action: %s\n", problem.Action)
			}
		}
	}
}
