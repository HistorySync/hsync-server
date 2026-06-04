package main

import (
	"context"
	"fmt"
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

func migrateUpOrDown(up bool, n int) int {
	cfg, err := config.LoadForMigrations()
	if err != nil {
		log.Error().Err(err).Msg("failed to load config")
		return 1
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	pool, err := repository.NewPGXPool(ctx, cfg.DatabaseURL)
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
	fmt.Println("  hsync-server migrate create <name>  Create a new up/down migration file pair")
}
