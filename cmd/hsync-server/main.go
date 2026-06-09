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
	"github.com/historysync/hsync-server/pkg/buildinfo"
	"github.com/historysync/hsync-server/pkg/config"
	"github.com/historysync/hsync-server/pkg/handler"
	"github.com/historysync/hsync-server/pkg/middleware"
	"github.com/historysync/hsync-server/pkg/observability"
	"github.com/historysync/hsync-server/pkg/provider"
	"github.com/historysync/hsync-server/pkg/repository"
	"github.com/historysync/hsync-server/pkg/scheduler"
	"github.com/historysync/hsync-server/pkg/service"
	"github.com/historysync/hsync-server/pkg/storage"
	"github.com/historysync/hsync-server/pkg/web"
	"github.com/historysync/hsync-server/pkg/ws"
)

func main() {
	// Logger
	log.Logger = zerolog.New(os.Stdout).With().
		Timestamp().
		Str("service", "hsync-server").
		Logger()

	// Subcommands
	// "migrate" runs database migrations and exits; anything else starts the server.
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "migrate":
			os.Exit(runMigrate(os.Args[2:]))
		case "doctor", "preflight":
			os.Exit(runDoctor(os.Args[2:]))
		case "ops":
			os.Exit(runOps(os.Args[2:]))
		}
	}

	// Config
	cfg, err := config.Load()
	if err != nil {
		log.Fatal().Err(err).Msg("failed to load config")
	}
	zerolog.SetGlobalLevel(cfg.LogLevel)
	log.Info().Msg("starting HistorySync Cloud Server")

	// Connect to PostgreSQL
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	pgPool, err := repository.NewPGXPool(ctx, cfg.DatabaseURL)
	if err != nil {
		log.Fatal().Err(err).Msg("failed to connect to postgresql")
	}
	defer pgPool.Close()
	log.Info().Msg("connected to PostgreSQL")

	// Connect to Redis
	redisClient, err := repository.NewRedisClient(ctx, cfg.RedisURL)
	if err != nil {
		// Redis is optional; the server degrades gracefully without it.
		log.Warn().Err(err).Msg("redis unavailable, continuing without it")
	}

	// Rate Limiter
	// Background context for long-lived tasks (e.g. in-memory limiter cleanup),
	// cancelled when main returns after shutdown.
	bgCtx, bgCancel := context.WithCancel(context.Background())
	defer bgCancel()

	rateLimitRuntime := middleware.NewRateLimitRuntimeConfig(
		cfg.RateLimitFailMode,
		cfg.RateLimitPublicAuthFailMode,
		cfg.RateLimitEnterpriseAdminFailMode,
		cfg.RateLimitEnterpriseBillingFailMode,
		cfg.RateLimitRedisUnavailableFallback,
	)
	var rateLimiter middleware.Limiter
	if redisClient != nil {
		rateLimiter = middleware.NewRedisLimiter(redisClient)
		log.Info().Msg("rate limiting backed by redis")
	} else {
		fallback := middleware.NewLimiterForRedisUnavailable(bgCtx, rateLimitRuntime.RedisFallback())
		rateLimiter = fallback.Limiter
		observability.SetRateLimitRedisFallbackActive(string(fallback.Mode))
		log.Warn().
			Str("redis_unavailable_fallback", string(fallback.Mode)).
			Str("default_fail_mode", string(rateLimitRuntime.DefaultFailMode())).
			Msg("rate limiting running without redis")
	}

	// Turnstile bot protection for public auth routes.
	var turnstile middleware.TurnstileConfig
	if cfg.TurnstileEnabled {
		turnstile = middleware.TurnstileConfig{
			Enabled: true,
			Verifier: middleware.NewCloudflareTurnstileVerifier(
				cfg.TurnstileSecret,
				cfg.TurnstileTimeout,
			),
		}
		log.Info().Msg("turnstile protection enabled for public auth routes")
	}

	// Blob Storage
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

	// Repositories
	repos := repository.New(pgPool, redisClient)

	// Token Manager
	tokenManager, err := auth.NewTokenManager(cfg.JWTPrivateKey, auth.TokenConfig{
		AccessTTL:  15 * time.Minute,
		RefreshTTL: 30 * 24 * time.Hour,
	})
	if err != nil {
		log.Fatal().Err(err).Msg("failed to initialize token manager")
	}

	notifier := provider.Registry().Notifier
	if cfg.SMTPEnabled {
		smtpNotifier, err := provider.NewSMTPNotifier(provider.SMTPConfig{
			Server:   cfg.SMTPServer,
			Port:     cfg.SMTPPort,
			Username: cfg.SMTPUsername,
			Password: cfg.SMTPPassword,
			From:     cfg.SMTPFrom,
			FromName: cfg.SMTPFromName,
			TLSMode:  cfg.SMTPTLSMode,
		})
		if err != nil {
			log.Fatal().Err(err).Msg("failed to initialize smtp notifier")
		}
		notifier = smtpNotifier
		provider.RegisterNotifier(notifier)
		log.Info().Str("server", cfg.SMTPServer).Int("port", cfg.SMTPPort).Msg("smtp notifications configured")
	} else if cfg.NotificationsEnabled {
		log.Warn().Msg("notifications enabled without smtp; using log-only notifier")
	}

	// Services
	databasePing := func(ctx context.Context) error {
		return pgPool.Ping(ctx)
	}
	var redisPing service.PingFunc
	if redisClient != nil {
		redisPing = func(ctx context.Context) error {
			return redisClient.Ping(ctx).Err()
		}
	}
	svcs := service.New(service.Deps{
		Repos:          repos,
		TokenManager:   tokenManager,
		BlobStore:      blobStore,
		StripeKey:      cfg.StripeSecretKey,
		StripeWebhook:  cfg.StripeWebhookSecret,
		StripeDisabled: cfg.StripeDisabled,
		SecuritySecret: cfg.SecuritySecret,
		Config:         cfg,
		DatabasePing:   databasePing,
		RedisPing:      redisPing,
		Notifier:       notifier,
		Notification: service.NotificationConfig{
			Enabled:            cfg.NotificationsEnabled,
			AppName:            cfg.WebAppName,
			PublicURL:          cfg.PublicURL,
			WarningThreshold:   cfg.QuotaWarningThreshold,
			ExhaustedThreshold: cfg.QuotaExhaustedThreshold,
			EmailVerifyPath:    cfg.EmailVerificationPath,
			PasswordResetPath:  cfg.PasswordResetPath,
		},
	})

	// WebSocket Hub
	hub := ws.NewHubWithOptions(repos.Devices, ws.Options{
		OriginCheckDisabled:   cfg.WebSocketOriginCheckDisabled,
		AllowedOrigins:        cfg.WebSocketAllowedOrigins,
		MaxConnections:        cfg.WebSocketMaxConnections,
		MaxConnectionsPerUser: cfg.WebSocketMaxConnectionsPerUser,
	})
	go hub.Run()

	// Dynamic Options
	var optionStore config.OptionStore
	if cfg.OptionsFile != "" {
		var err error
		optionStore, err = config.NewFileOptionStore(cfg.OptionsFile)
		if err != nil {
			log.Fatal().Err(err).Msg("failed to open option store")
		}
		log.Info().Str("path", cfg.OptionsFile).Msg("dynamic options loaded")
	}

	// Background Scheduler
	// Periodic maintenance tasks run on a single instance at a time (leader
	// elected via a Postgres advisory lock) and stop on shutdown via bgCtx.
	if cfg.BackgroundTasksEnabled {
		tasks := []scheduler.Task{
			scheduler.Task{
				Name:     "quota-reconcile",
				LockKey:  scheduler.LockQuotaReconcile,
				Interval: cfg.QuotaReconcileInterval,
				Run: func(ctx context.Context) error {
					n, err := svcs.Quota.RecalculateAllUsage(ctx)
					if err != nil {
						return err
					}
					log.Info().Int64("users", n).Msg("quota usage reconciled")
					return nil
				},
			},
			{
				Name:     "retention-cleanup",
				LockKey:  scheduler.LockRetentionCleanup,
				Interval: cfg.RetentionCleanupInterval,
				Run: func(ctx context.Context) error {
					// Two-stage opt-in: enabling the task (interval > 0) only logs a
					// dry-run report; actual deletion also requires retention_dry_run=false.
					if cfg.RetentionDryRun {
						bundleReport, err := svcs.Retention.ReportExpiredBundles(ctx, cfg.RetentionGracePeriod)
						if err != nil {
							return err
						}
						snapReport, err := svcs.Retention.ReportExpiredSnapshots(ctx, cfg.RetentionGracePeriod)
						if err != nil {
							return err
						}
						log.Info().
							Int64("expired_bundles", bundleReport.ExpiredBundles).
							Int64("expired_bytes", bundleReport.ExpiredBytes).
							Int64("expired_snapshots", snapReport.ExpiredSnapshots).
							Int64("expired_snapshot_bytes", snapReport.ExpiredBytes).
							Time("older_than", bundleReport.Before).
							Msg("retention cleanup dry-run: data eligible for purge")
						return nil
					}
					bundleReport, err := svcs.Retention.PurgeExpiredBundles(ctx, cfg.RetentionGracePeriod)
					if err != nil {
						return err
					}
					snapReport, err := svcs.Retention.PurgeExpiredSnapshots(ctx, cfg.RetentionGracePeriod)
					if err != nil {
						return err
					}
					erasureReport, err := svcs.Retention.RunErasureJobs(ctx)
					if err != nil {
						return err
					}
					log.Info().
						Int64("purged_bundles", bundleReport.ExpiredBundles).
						Int64("purged_bytes", bundleReport.ExpiredBytes).
						Int64("failed_bundles", bundleReport.Failed).
						Int64("purged_snapshots", snapReport.ExpiredSnapshots).
						Int64("purged_snapshot_bytes", snapReport.ExpiredBytes).
						Int64("failed_snapshots", snapReport.Failed).
						Int64("erasure_jobs_checked", erasureReport.Checked).
						Int64("erasure_jobs_completed", erasureReport.Completed).
						Int64("erasure_jobs_failed", erasureReport.Failed).
						Time("older_than", bundleReport.Before).
						Msg("retention cleanup: purged expired data")
					return nil
				},
			},
			{
				Name:     "notification-outbox",
				LockKey:  scheduler.LockNotificationOutbox,
				Interval: cfg.NotificationOutboxInterval,
				Run: func(ctx context.Context) error {
					result, err := svcs.Notification.ProcessOutbox(ctx, 50)
					if err != nil {
						return err
					}
					log.Info().
						Int("claimed", result.Claimed).
						Int("sent", result.Sent).
						Int("retried", result.Retried).
						Int("failed", result.Failed).
						Msg("notification outbox processed")
					return nil
				},
			},
			{
				Name:     "history-retention",
				LockKey:  scheduler.LockHistoryRetention,
				Interval: cfg.HistoryRetentionInterval,
				Run: func(ctx context.Context) error {
					result, err := svcs.History.Run(ctx, service.OperationalHistoryRetentionPolicy{
						HotRetention:     cfg.HistoryHotRetention,
						ArchiveRetention: cfg.HistoryArchiveRetention,
						DryRun:           cfg.HistoryRetentionDryRun,
					})
					if err != nil {
						return err
					}
					log.Info().
						Bool("dry_run", result.DryRun).
						Time("hot_cutoff", result.HotCutoff).
						Time("archive_cutoff", result.ArchiveCutoff).
						Int64("archived_audit_logs", result.ArchivedAuditLogs).
						Int64("archived_ops_check_runs", result.ArchivedOpsCheckRuns).
						Int64("archived_notification_outbox_rows", result.ArchivedNotificationOutboxRows).
						Int64("purged_audit_log_archives", result.PurgedAuditLogArchives).
						Int64("purged_ops_check_run_archives", result.PurgedOpsCheckRunArchives).
						Int64("purged_notification_archives", result.PurgedNotificationArchives).
						Msg("operational history retention completed")
					return nil
				},
			},
		}
		tasks = append(tasks, scheduler.OpsTasks(svcs.Ops, scheduler.OpsTaskConfig{
			DependencyInterval:  cfg.OpsDependencyCheckInterval,
			ConsistencyInterval: cfg.OpsConsistencyCheckInterval,
			ConsistencyLimit:    cfg.OpsConsistencyCheckLimit,
		})...)
		sched := scheduler.New(pgPool, log.Logger, tasks...)
		go sched.Run(bgCtx)
		log.Info().Msg("background scheduler started")
	} else {
		log.Info().Msg("background tasks disabled")
	}

	// HTTP Handlers
	h := handler.New(handler.Deps{
		Services:     svcs,
		TokenManager: tokenManager,
		Hub:          hub,
		DB:           pgPool,
		Redis:        redisClient,
		BlobStore:    blobStore,
		AdminKey:     cfg.AdminKey,
		BuildInfo:    buildinfo.Current(),
		RateLimiter:  rateLimiter,
		RateLimit:    rateLimitRuntime,
		OptionStore:  optionStore,
		Turnstile:    turnstile,
		Metrics: handler.MetricsConfig{
			Enabled:      cfg.MetricsEnabled,
			Path:         cfg.MetricsPath,
			AllowedCIDRs: cfg.MetricsAllowedCIDRs,
		},
	})

	// Fiber App
	app := fiber.New(fiber.Config{
		AppName:      "HistorySync Cloud Server",
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 60 * time.Second,
		IdleTimeout:  120 * time.Second,
		BodyLimit:    55 * 1024 * 1024, // 55 MB (稍大于 50 MB Bundle 上限)
		ErrorHandler: h.ErrorHandler,
	})
	app.Use(middleware.RequestID())
	if cfg.MetricsEnabled {
		app.Use(observability.HTTPMetricsMiddleware())
	}

	// Register all routes
	h.RegisterRoutes(app)
	web.Register(app, web.Options{
		Enabled:      cfg.WebEnabled,
		AppName:      cfg.WebAppName,
		ConsolePath:  cfg.WebConsolePath,
		SupportEmail: cfg.WebSupportEmail,
		Edition:      "community",
		BuildInfo:    buildinfo.Current(),
		APIPrefix:    "/api/v1",
		AdminPath:    "/admin",
	})

	// Graceful Shutdown
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		sig := <-quit
		log.Info().Str("signal", sig.String()).Msg("shutting down")

		// Stop background workers (scheduler, in-memory limiter) before draining
		// HTTP and closing the pool they depend on.
		bgCancel()

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

	// Listen
	log.Info().Str("addr", cfg.ListenAddr).Msg("server listening")
	if err := app.Listen(cfg.ListenAddr, fiber.ListenConfig{
		EnablePrefork: false, // 如果需要多进程，由外部进程管理器控制
	}); err != nil {
		log.Fatal().Err(err).Msg("server failed")
	}
}
