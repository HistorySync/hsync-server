package opsrehearsal

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"

	"github.com/historysync/hsync-server/migrations"
	"github.com/historysync/hsync-server/pkg/buildinfo"
	"github.com/historysync/hsync-server/pkg/config"
	"github.com/historysync/hsync-server/pkg/migrate"
	"github.com/historysync/hsync-server/pkg/preflight"
	"github.com/historysync/hsync-server/pkg/repository"
	"github.com/historysync/hsync-server/pkg/service"
	"github.com/historysync/hsync-server/pkg/storage"
	"github.com/historysync/hsync-server/pkg/supportbundle"
)

const (
	StatusOK            = "ok"
	StatusWarn          = "warn"
	StatusError         = "error"
	StatusSkipped       = "skipped"
	StatusNotConfigured = "not_configured"
)

const (
	defaultTimeout        = 60 * time.Second
	defaultDoctorTimeout  = 5 * time.Second
	defaultSupportWindow  = 24 * time.Hour
	defaultManifestLimit  = service.DefaultOpsRestoreLimit
	defaultRehearsalLimit = service.DefaultOpsRestoreLimit
)

type Options struct {
	Edition          string
	Config           *config.Config
	BuildInfo        buildinfo.Info
	Timeout          time.Duration
	DoctorTimeout    time.Duration
	RestoreLimit     int32
	Manifest         *service.OpsRestoreManifest
	SupportSince     time.Time
	Now              func() time.Time
	ExtraSteps       []StepRunner
	SchemaDrift      SchemaDriftRunner
	MigrationStatus  MigrationStatusRunner
	SupportBundle    SupportBundleRunner
	RestoreRehearsal RestoreRehearsalRunner
	EndpointList     EndpointListRunner
	Steps            []StepRunner
	Runtime          *Runtime
	CloseRuntime     bool
}

type Runtime struct {
	Pool      *pgxpool.Pool
	Redis     *redis.Client
	Repos     *repository.Repos
	BlobStore storage.BlobStorage
	Ops       *service.OpsService
}

type StepContext struct {
	Context context.Context
	Options Options
	Runtime *Runtime
	Now     func() time.Time
}

type StepRunner struct {
	ID          string
	Name        string
	Description string
	Run         func(StepContext) (StepResult, error)
}

type Result struct {
	SchemaVersion int          `json:"schema_version"`
	Edition       string       `json:"edition"`
	GeneratedAt   time.Time    `json:"generated_at"`
	Overall       string       `json:"overall"`
	DurationMS    int64        `json:"duration_ms"`
	Steps         []StepResult `json:"steps"`
}

type StepResult struct {
	ID          string         `json:"id"`
	Name        string         `json:"name"`
	Status      string         `json:"status"`
	DurationMS  int64          `json:"duration_ms"`
	Blocking    bool           `json:"blocking"`
	Action      string         `json:"action,omitempty"`
	Description string         `json:"description,omitempty"`
	Details     map[string]any `json:"details,omitempty"`
}

type SchemaDriftRunner func(context.Context, *Runtime) ([]migrate.DriftFinding, error)
type MigrationStatusRunner func(context.Context, *Runtime) (any, bool, error)
type SupportBundleRunner func(context.Context, StepContext) (any, error)
type RestoreRehearsalRunner func(context.Context, StepContext) (service.OpsRestoreReport, error)
type EndpointListRunner func(StepContext) EndpointList

type EndpointList struct {
	SmokeCompatible []EndpointRef `json:"smoke_compatible"`
	Notes           []string      `json:"notes,omitempty"`
}

type EndpointRef struct {
	Method      string `json:"method"`
	Path        string `json:"path"`
	Auth        string `json:"auth"`
	Description string `json:"description,omitempty"`
}

func Run(ctx context.Context, opts Options) (Result, error) {
	opts = normalizeOptions(opts)
	start := opts.Now()
	ctx, cancel := context.WithTimeout(ctx, opts.Timeout)
	defer cancel()

	runtime, err := runtimeFor(ctx, opts)
	if err != nil {
		result := Result{
			SchemaVersion: 1,
			Edition:       opts.Edition,
			GeneratedAt:   start.UTC(),
			Overall:       StatusError,
			DurationMS:    millisSince(start, opts.Now),
		}
		result.Steps = append(result.Steps, StepResult{
			ID:         "runtime.init",
			Name:       "Initialize rehearsal runtime",
			Status:     StatusError,
			Blocking:   true,
			Action:     "Fix PostgreSQL or S3 connectivity, then rerun `ops rehearsal`.",
			DurationMS: result.DurationMS,
			Details:    map[string]any{"error": err.Error()},
		})
		return result, nil
	}
	if opts.CloseRuntime && runtime != nil {
		defer runtime.Close()
	}

	stepCtx := StepContext{Context: ctx, Options: opts, Runtime: runtime, Now: opts.Now}
	steps := opts.Steps
	if len(steps) == 0 {
		steps = defaultSteps()
		steps = append(steps, opts.ExtraSteps...)
	}
	result := Result{
		SchemaVersion: 1,
		Edition:       opts.Edition,
		GeneratedAt:   start.UTC(),
		Steps:         make([]StepResult, 0, len(steps)),
	}
	for _, runner := range steps {
		result.Steps = append(result.Steps, runStep(stepCtx, runner))
	}
	result.DurationMS = millisSince(start, opts.Now)
	result.Overall = overall(result.Steps)
	return result, nil
}

func (r *Runtime) Close() {
	if r == nil {
		return
	}
	if r.Redis != nil {
		_ = r.Redis.Close()
	}
	if r.Pool != nil {
		r.Pool.Close()
	}
}

func WriteJSON(w io.Writer, result Result) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(result)
}

func WriteHuman(w io.Writer, result Result) error {
	if _, err := fmt.Fprintf(w, "HistorySync %s recovery rehearsal\n", result.Edition); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "Generated: %s\n", result.GeneratedAt.Format(time.RFC3339)); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "Overall: %s  duration=%dms\n\n", result.Overall, result.DurationMS); err != nil {
		return err
	}
	for _, step := range result.Steps {
		marker := " "
		if step.Blocking {
			marker = "!"
		}
		if _, err := fmt.Fprintf(w, "[%s] %s %s (%dms)\n", step.Status, marker, step.Name, step.DurationMS); err != nil {
			return err
		}
		if step.Description != "" {
			if _, err := fmt.Fprintf(w, "  %s\n", step.Description); err != nil {
				return err
			}
		}
		if step.Action != "" {
			if _, err := fmt.Fprintf(w, "  action: %s\n", step.Action); err != nil {
				return err
			}
		}
		if len(step.Details) > 0 {
			keys := make([]string, 0, len(step.Details))
			for key := range step.Details {
				keys = append(keys, key)
			}
			sort.Strings(keys)
			parts := make([]string, 0, len(keys))
			for _, key := range keys {
				parts = append(parts, fmt.Sprintf("%s=%v", key, step.Details[key]))
			}
			if _, err := fmt.Fprintf(w, "  details: %s\n", strings.Join(parts, ", ")); err != nil {
				return err
			}
		}
	}
	return nil
}

func normalizeOptions(opts Options) Options {
	if strings.TrimSpace(opts.Edition) == "" {
		opts.Edition = "community"
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.Timeout <= 0 {
		opts.Timeout = defaultTimeout
	}
	if opts.DoctorTimeout <= 0 {
		opts.DoctorTimeout = defaultDoctorTimeout
	}
	if opts.RestoreLimit <= 0 || opts.RestoreLimit > defaultRehearsalLimit {
		opts.RestoreLimit = defaultManifestLimit
	}
	if opts.BuildInfo.SchemaVersion == 0 {
		if opts.BuildInfo.Version == "" && opts.BuildInfo.Commit == "" && opts.BuildInfo.BuildTime == "" && opts.BuildInfo.Edition == "" {
			opts.BuildInfo = buildinfo.WithEdition(opts.Edition)
		} else {
			opts.BuildInfo.SchemaVersion = buildinfo.LatestSchemaVersion()
		}
	}
	if strings.TrimSpace(opts.BuildInfo.Edition) == "" {
		opts.BuildInfo.Edition = opts.Edition
	}
	if opts.SchemaDrift == nil {
		opts.SchemaDrift = runCESchemaDrift
	}
	if opts.MigrationStatus == nil {
		opts.MigrationStatus = runCEMigrationStatus
	}
	if opts.SupportBundle == nil {
		opts.SupportBundle = runCESupportBundle
	}
	if opts.RestoreRehearsal == nil {
		opts.RestoreRehearsal = runCERestoreRehearsal
	}
	if opts.EndpointList == nil {
		opts.EndpointList = defaultEndpointList
	}
	if opts.SupportSince.IsZero() {
		opts.SupportSince = opts.Now().Add(-defaultSupportWindow)
	}
	return opts
}

func runtimeFor(ctx context.Context, opts Options) (*Runtime, error) {
	if opts.Runtime != nil {
		return opts.Runtime, nil
	}
	if opts.Config == nil {
		return nil, errors.New("config is not loaded")
	}
	pool, err := repository.NewPGXPoolWithConfig(ctx, opts.Config.DatabaseURL, repository.PGXPoolConfig{
		MaxConns:          opts.Config.DatabasePoolMaxConns,
		MinConns:          opts.Config.DatabasePoolMinConns,
		MaxConnLifetime:   opts.Config.DatabasePoolMaxConnLifetime,
		MaxConnIdleTime:   opts.Config.DatabasePoolMaxConnIdleTime,
		HealthCheckPeriod: opts.Config.DatabasePoolHealthCheckPeriod,
	})
	if err != nil {
		return nil, fmt.Errorf("connect postgresql: %w", err)
	}
	runtime := &Runtime{Pool: pool}
	var redisClient *redis.Client
	if strings.TrimSpace(opts.Config.RedisURL) != "" {
		redisClient, _ = repository.NewRedisClient(ctx, opts.Config.RedisURL)
	}
	blobStore, err := storage.NewS3Storage(ctx, storage.S3Config{
		Endpoint:     opts.Config.S3Endpoint,
		Bucket:       opts.Config.S3Bucket,
		AccessKey:    opts.Config.S3AccessKey,
		SecretKey:    opts.Config.S3SecretKey,
		UseSSL:       opts.Config.S3UseSSL,
		StorageClass: opts.Config.S3StorageClass,
	})
	if err != nil {
		runtime.Close()
		return nil, fmt.Errorf("initialize storage: %w", err)
	}
	repos := repository.New(pool, redisClient)
	databasePing := func(ctx context.Context) error { return pool.Ping(ctx) }
	var redisPing service.PingFunc
	if redisClient != nil {
		redisPing = func(ctx context.Context) error { return redisClient.Ping(ctx).Err() }
	}
	ops := service.NewOpsService(service.OpsDeps{
		Config:       opts.Config,
		BuildInfo:    opts.BuildInfo,
		Repos:        repos,
		BlobStore:    blobStore,
		DatabasePing: databasePing,
		RedisPing:    redisPing,
	})
	runtime.Redis = redisClient
	runtime.Repos = repos
	runtime.BlobStore = blobStore
	runtime.Ops = ops
	return runtime, nil
}

func defaultSteps() []StepRunner {
	return []StepRunner{
		{
			ID:          "build.info",
			Name:        "Build info",
			Description: "Record binary version, commit, build time, edition, and compiled schema version.",
			Run: func(ctx StepContext) (StepResult, error) {
				return StepResult{
					Status: StatusOK,
					Details: map[string]any{
						"build_info": ctx.Options.BuildInfo,
					},
				}, nil
			},
		},
		{
			ID:          "doctor",
			Name:        "Doctor",
			Description: "Run the existing offline deployment preflight checks.",
			Run: func(ctx StepContext) (StepResult, error) {
				report := preflight.RunCE(ctx.Context, ctx.Options.Config, preflight.CEOptions{
					Edition: ctx.Options.Edition,
					Timeout: ctx.Options.DoctorTimeout,
				})
				return StepResult{
					Status:   severityStatus(report.Overall),
					Blocking: report.HasErrors(),
					Action:   actionForPreflight(report.Checks, "Fix doctor error checks before continuing the rehearsal."),
					Details:  map[string]any{"report": report},
				}, nil
			},
		},
		{
			ID:          "migrate.status",
			Name:        "Migrate status",
			Description: "Inspect applied, pending, and rollback migration metadata without applying changes.",
			Run: func(ctx StepContext) (StepResult, error) {
				status, consistent, err := ctx.Options.MigrationStatus(ctx.Context, ctx.Runtime)
				if err != nil {
					return StepResult{
						Status:   StatusError,
						Blocking: true,
						Action:   "Fix PostgreSQL permissions or migration tracking, then rerun migrate status.",
						Details:  map[string]any{"error": err.Error()},
					}, nil
				}
				result := StepResult{
					Status:  StatusOK,
					Details: map[string]any{"status": status},
				}
				if !consistent {
					result.Status = StatusError
					result.Blocking = true
					result.Action = "Resolve migration tracking inconsistencies before trusting restore validation."
				}
				return result, nil
			},
		},
		{
			ID:          "schema.drift",
			Name:        "Schema drift",
			Description: "Verify required schema tables, columns, and indexes are present.",
			Run: func(ctx StepContext) (StepResult, error) {
				findings, err := ctx.Options.SchemaDrift(ctx.Context, ctx.Runtime)
				if err != nil {
					return StepResult{
						Status:   StatusError,
						Blocking: true,
						Action:   "Verify database permissions and rerun migrations before continuing.",
						Details:  map[string]any{"error": err.Error()},
					}, nil
				}
				result := StepResult{
					Status:  StatusOK,
					Details: map[string]any{"findings": findings},
				}
				for _, finding := range findings {
					if finding.Severity == "error" {
						result.Status = StatusError
						result.Blocking = true
						result.Action = "Run migrations or restore a matching schema backup before continuing."
						return result, nil
					}
					result.Status = StatusWarn
					if result.Action == "" {
						result.Action = "Review warn-level schema drift before declaring the rehearsal clean."
					}
				}
				return result, nil
			},
		},
		{
			ID:          "restore.manifest.verify",
			Name:        "Restore manifest verify",
			Description: "Verify restore manifest metadata and object availability without starting a restore database.",
			Run: func(ctx StepContext) (StepResult, error) {
				report, err := ctx.Options.RestoreRehearsal(ctx.Context, ctx)
				if err != nil {
					return StepResult{
						Status:   StatusError,
						Blocking: true,
						Action:   "Fix restore manifest or storage access, then rerun the rehearsal.",
						Details:  map[string]any{"error": err.Error()},
					}, nil
				}
				status, blocking := opsStatus(report.Overall)
				return StepResult{
					Status:   status,
					Blocking: blocking,
					Action:   firstNonEmpty(report.Recommendations),
					Details: map[string]any{
						"report": report,
					},
				}, nil
			},
		},
		{
			ID:          "support.bundle.summary",
			Name:        "Support bundle summary",
			Description: "Generate the redacted support bundle and summarize the safe-to-share diagnostic surface.",
			Run: func(ctx StepContext) (StepResult, error) {
				bundle, err := ctx.Options.SupportBundle(ctx.Context, ctx)
				if err != nil {
					return StepResult{
						Status:  StatusWarn,
						Action:  "Fix support bundle generation before relying on support handoff material.",
						Details: map[string]any{"error": err.Error()},
					}, nil
				}
				return StepResult{
					Status: StatusOK,
					Details: map[string]any{
						"summary": summarizeSupportBundle(bundle),
					},
				}, nil
			},
		},
		{
			ID:          "smoke.endpoint.list",
			Name:        "Smoke-compatible endpoint list",
			Description: "List endpoints that existing smoke tests and manual cutover checks can probe.",
			Run: func(ctx StepContext) (StepResult, error) {
				return StepResult{
					Status:  StatusOK,
					Details: map[string]any{"endpoints": ctx.Options.EndpointList(ctx)},
				}, nil
			},
		},
	}
}

func runStep(ctx StepContext, runner StepRunner) StepResult {
	start := ctx.Now()
	result, err := runner.Run(ctx)
	if err != nil {
		result = StepResult{
			Status:   StatusError,
			Blocking: true,
			Action:   "Fix this rehearsal step, then rerun the full sequence.",
			Details:  map[string]any{"error": err.Error()},
		}
	}
	result.ID = runner.ID
	result.Name = runner.Name
	if result.Description == "" {
		result.Description = runner.Description
	}
	if result.Status == "" {
		result.Status = StatusOK
	}
	result.DurationMS = millisSince(start, ctx.Now)
	return result
}

func runCEMigrationStatus(ctx context.Context, runtime *Runtime) (any, bool, error) {
	if runtime == nil || runtime.Pool == nil {
		return nil, false, errors.New("postgres runtime is not configured")
	}
	status, err := migrate.Status(ctx, runtime.Pool, migrations.FS, "schema_migrations", "community")
	if err != nil {
		return nil, false, err
	}
	return status, status.Consistent, nil
}

func runCESchemaDrift(ctx context.Context, runtime *Runtime) ([]migrate.DriftFinding, error) {
	if runtime == nil || runtime.Pool == nil {
		return nil, errors.New("postgres runtime is not configured")
	}
	return migrate.Drift(ctx, runtime.Pool, preflight.CEDriftRequirements())
}

func runCERestoreRehearsal(ctx context.Context, step StepContext) (service.OpsRestoreReport, error) {
	if step.Runtime == nil || step.Runtime.Ops == nil {
		return service.OpsRestoreReport{}, errors.New("ops service is not configured")
	}
	if step.Options.Manifest != nil {
		return step.Runtime.Ops.VerifyRestore(ctx, *step.Options.Manifest, step.Options.RestoreLimit), nil
	}
	baseline := step.Runtime.Ops.GenerateRestoreBaseline(ctx, step.Options.RestoreLimit)
	if baseline.Manifest == nil {
		return baseline, nil
	}
	verify := step.Runtime.Ops.VerifyRestore(ctx, *baseline.Manifest, step.Options.RestoreLimit)
	verify.Manifest = baseline.Manifest
	return verify, nil
}

func runCESupportBundle(ctx context.Context, step StepContext) (any, error) {
	if step.Runtime == nil || step.Runtime.Ops == nil {
		return nil, errors.New("ops service is not configured")
	}
	return supportbundle.Generate(ctx, supportbundle.Options{
		Config:    step.Options.Config,
		BuildInfo: step.Options.BuildInfo,
		Ops:       step.Runtime.Ops,
		Readyz:    supportbundle.ProviderReadyz,
		Since:     step.Options.SupportSince,
		OpenAPI: supportbundle.OpenAPIVersion{
			Version: "1.0.0",
			Path:    "docs/api/openapi.ce.yaml",
		},
		Now: step.Now,
	})
}

func defaultEndpointList(StepContext) EndpointList {
	return EndpointList{
		SmokeCompatible: []EndpointRef{
			{Method: "GET", Path: "/healthz", Auth: "none", Description: "liveness"},
			{Method: "GET", Path: "/readyz", Auth: "none", Description: "readiness"},
			{Method: "GET", Path: "/api/meta/version", Auth: "none", Description: "build metadata"},
			{Method: "GET", Path: "/admin/ops/summary", Auth: "admin_key", Description: "ops summary"},
			{Method: "GET", Path: "/admin/support-bundle", Auth: "admin_key", Description: "redacted diagnostic bundle"},
			{Method: "POST", Path: "/admin/ops/check", Auth: "admin_key+idempotency", Description: "dependency check"},
			{Method: "POST", Path: "/admin/ops/consistency", Auth: "admin_key+idempotency", Description: "metadata/object consistency check"},
			{Method: "POST", Path: "/admin/ops/restore-rehearsal", Auth: "admin_key+idempotency", Description: "restore manifest baseline or verify"},
		},
		Notes: []string{
			"CLI rehearsal lists smoke-compatible endpoints but does not start an HTTP server or run the smoke test binary.",
		},
	}
}

func severityStatus(severity preflight.Severity) string {
	switch severity {
	case preflight.SeverityOK:
		return StatusOK
	case preflight.SeverityWarn:
		return StatusWarn
	case preflight.SeverityError:
		return StatusError
	default:
		return StatusWarn
	}
}

func opsStatus(status string) (string, bool) {
	switch status {
	case service.OpsStatusOK, service.OpsStatusDisabled:
		return StatusOK, false
	case service.OpsStatusDegraded, service.OpsStatusSkipped, service.OpsStatusNotChecked:
		return StatusWarn, false
	default:
		return StatusError, true
	}
}

func actionForPreflight(checks []preflight.Check, fallback string) string {
	for _, check := range checks {
		if check.Severity == preflight.SeverityError && strings.TrimSpace(check.Action) != "" {
			return check.Action
		}
	}
	for _, check := range checks {
		if check.Severity == preflight.SeverityWarn && strings.TrimSpace(check.Action) != "" {
			return check.Action
		}
	}
	return fallback
}

func summarizeSupportBundle(bundle any) map[string]any {
	raw, err := json.Marshal(bundle)
	if err != nil {
		return map[string]any{"available": true, "summary_error": err.Error()}
	}
	var object map[string]any
	if err := json.Unmarshal(raw, &object); err != nil {
		return map[string]any{"available": true, "summary_error": err.Error()}
	}
	summary := map[string]any{
		"available": true,
	}
	for _, key := range []string{"schema_version", "generated_at", "safe_to_share_boundary", "since", "build_info", "readyz", "config_presence", "openapi", "extension"} {
		if value, ok := object[key]; ok {
			summary[key] = value
		}
	}
	if doctor, ok := object["doctor_report"].(map[string]any); ok {
		summary["doctor_overall"] = doctor["overall"]
		summary["doctor_summary"] = doctor["summary"]
	}
	if runs, ok := object["recent_scheduler_runs"].([]any); ok {
		summary["recent_scheduler_run_count"] = len(runs)
	}
	return summary
}

func overall(steps []StepResult) string {
	result := StatusOK
	for _, step := range steps {
		switch step.Status {
		case StatusError:
			return StatusError
		case StatusWarn, StatusSkipped, StatusNotConfigured:
			if result == StatusOK {
				result = StatusWarn
			}
		}
	}
	return result
}

func millisSince(start time.Time, now func() time.Time) int64 {
	elapsed := now().Sub(start).Milliseconds()
	if elapsed < 0 {
		return 0
	}
	return elapsed
}

func firstNonEmpty(values []string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
