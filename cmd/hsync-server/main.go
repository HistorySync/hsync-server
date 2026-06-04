// Package main is the entry point for the HistorySync Cloud Server.
//
// It initializes configuration, database connections, storage backends,
// and starts the HTTP server with all API routes mounted.
package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"

	"github.com/historysync/hsync-server/pkg/auth"
	"github.com/historysync/hsync-server/pkg/config"
	"github.com/historysync/hsync-server/pkg/handler"
	"github.com/historysync/hsync-server/pkg/middleware"
	"github.com/historysync/hsync-server/pkg/repository"
	"github.com/historysync/hsync-server/pkg/service"
	"github.com/historysync/hsync-server/pkg/storage"
	"github.com/historysync/hsync-server/pkg/web"
	"github.com/historysync/hsync-server/pkg/ws"
)

func main() {
	// ── Logger ────────────────────────────────────────────────
	log.Logger = zerolog.New(os.Stdout).With().
		Timestamp().
		Str("service", "hsync-server").
		Logger()

	// ── Subcommands ───────────────────────────────────────────
	// "migrate" runs database migrations and exits; anything else starts the server.
	if len(os.Args) > 1 && os.Args[1] == "migrate" {
		os.Exit(runMigrate(os.Args[2:]))
	}

	// ── Config ────────────────────────────────────────────────
	cfg, err := config.Load()
	if err != nil {
		log.Fatal().Err(err).Msg("failed to load config")
	}
	zerolog.SetGlobalLevel(cfg.LogLevel)
	log.Info().Msg("starting HistorySync Cloud Server")

	// ── Connect to PostgreSQL ─────────────────────────────────
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	pgPool, err := repository.NewPGXPool(ctx, cfg.DatabaseURL)
	if err != nil {
		log.Fatal().Err(err).Msg("failed to connect to postgresql")
	}
	defer pgPool.Close()
	log.Info().Msg("connected to PostgreSQL")

	// ── Connect to Redis ──────────────────────────────────────
	redisClient, err := repository.NewRedisClient(ctx, cfg.RedisURL)
	if err != nil {
		// Redis is optional; the server degrades gracefully without it.
		log.Warn().Err(err).Msg("redis unavailable, continuing without it")
	}

	// ── Blob Storage ──────────────────────────────────────────
	blobStore, err := storage.NewS3Storage(ctx, storage.S3Config{
		Endpoint:  cfg.S3Endpoint,
		Bucket:    cfg.S3Bucket,
		AccessKey: cfg.S3AccessKey,
		SecretKey: cfg.S3SecretKey,
		UseSSL:    cfg.S3UseSSL,
	})
	if err != nil {
		log.Fatal().Err(err).Msg("failed to initialize blob storage")
	}
	log.Info().Msg("connected to S3-compatible storage")

	// ── Repositories ──────────────────────────────────────────
	repos := repository.New(pgPool, redisClient)

	// ── Token Manager ─────────────────────────────────────────
	tokenManager, err := auth.NewTokenManager(cfg.JWTPrivateKey, auth.TokenConfig{
		AccessTTL:  15 * time.Minute,
		RefreshTTL: 30 * 24 * time.Hour,
	})
	if err != nil {
		log.Fatal().Err(err).Msg("failed to initialize token manager")
	}

	// ── Services ──────────────────────────────────────────────
	svcs := service.New(service.Deps{
		Repos:          repos,
		TokenManager:   tokenManager,
		BlobStore:      blobStore,
		StripeKey:      cfg.StripeSecretKey,
		StripeWebhook:  cfg.StripeWebhookSecret,
		StripeDisabled: cfg.StripeDisabled,
	})

	// ── WebSocket Hub ─────────────────────────────────────────
	hub := ws.NewHub(repos.Devices)
	go hub.Run()

	// ── HTTP Handlers ─────────────────────────────────────────
	h := handler.New(handler.Deps{
		Services:     svcs,
		TokenManager: tokenManager,
		Hub:          hub,
		DB:           pgPool,
		Redis:        redisClient,
		BlobStore:    blobStore,
		AdminKey:     cfg.AdminKey,
	})

	// ── Fiber App ─────────────────────────────────────────────
	app := fiber.New(fiber.Config{
		AppName:      "HistorySync Cloud Server",
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 60 * time.Second,
		IdleTimeout:  120 * time.Second,
		BodyLimit:    55 * 1024 * 1024, // 55 MB (稍大于 50 MB Bundle 上限)
		ErrorHandler: h.ErrorHandler,
	})
	app.Use(middleware.RequestID())

	// Register all routes
	h.RegisterRoutes(app)
	web.Register(app, web.Options{
		Enabled:      cfg.WebEnabled,
		AppName:      cfg.WebAppName,
		ConsolePath:  cfg.WebConsolePath,
		SupportEmail: cfg.WebSupportEmail,
		Edition:      "community",
		APIPrefix:    "/api/v1",
		AdminPath:    "/admin",
	})

	// ── Graceful Shutdown ─────────────────────────────────────
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		sig := <-quit
		log.Info().Str("signal", sig.String()).Msg("shutting down")

		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer shutdownCancel()

		if err := app.ShutdownWithContext(shutdownCtx); err != nil {
			log.Error().Err(err).Msg("error during shutdown")
		}
		pgPool.Close()
		if redisClient != nil {
			redisClient.Close()
		}
	}()

	// ── Listen ────────────────────────────────────────────────
	log.Info().Str("addr", cfg.ListenAddr).Msg("server listening")
	if err := app.Listen(cfg.ListenAddr, fiber.ListenConfig{
		EnablePrefork: false, // 如果需要多进程，由外部进程管理器控制
	}); err != nil {
		log.Fatal().Err(err).Msg("server failed")
	}
}
