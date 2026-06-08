package migrate

import (
	"context"
	"fmt"
	"io/fs"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

type StatusProblem struct {
	Severity string         `json:"severity"`
	Message  string         `json:"message"`
	Action   string         `json:"action,omitempty"`
	Details  map[string]any `json:"details,omitempty"`
}

type MigrationRef struct {
	Version int64  `json:"version"`
	Name    string `json:"name"`
}

type AppliedMigration struct {
	Version   int64     `json:"version"`
	Name      string    `json:"name"`
	AppliedAt time.Time `json:"applied_at"`
}

type StatusReport struct {
	Scope             string             `json:"scope"`
	TrackingTable     string             `json:"tracking_table"`
	TrackingTableOk   bool               `json:"tracking_table_ok"`
	Consistent        bool               `json:"consistent"`
	Applied           []AppliedMigration `json:"applied"`
	Pending           []MigrationRef     `json:"pending"`
	RollbackAvailable []MigrationRef     `json:"rollback_available"`
	Problems          []StatusProblem    `json:"problems,omitempty"`
}

type SchemaRequirementKind string

const (
	SchemaRequirementTable  SchemaRequirementKind = "table"
	SchemaRequirementColumn SchemaRequirementKind = "column"
	SchemaRequirementIndex  SchemaRequirementKind = "index"
)

type SchemaRequirement struct {
	Kind     SchemaRequirementKind
	Table    string
	Name     string
	Severity string
	Action   string
}

type DriftFinding struct {
	Severity string `json:"severity"`
	Object   string `json:"object"`
	Message  string `json:"message"`
	Action   string `json:"action"`
}

func Status(ctx context.Context, pool *pgxpool.Pool, fsys fs.FS, table, scope string) (StatusReport, error) {
	if err := validateTrackingTable(table); err != nil {
		return StatusReport{}, err
	}
	if strings.TrimSpace(scope) == "" {
		scope = table
	}
	all, err := Parse(fsys)
	if err != nil {
		return StatusReport{}, err
	}
	report := StatusReport{
		Scope:         scope,
		TrackingTable: table,
		Consistent:    true,
	}
	exists, err := trackingTableExists(ctx, pool, table)
	if err != nil {
		return report, fmt.Errorf("inspect %s table: %w", table, err)
	}
	report.TrackingTableOk = exists
	if !exists {
		report.Pending = migrationRefs(all)
		report.RollbackAvailable = []MigrationRef{}
		report.Problems = append(report.Problems, StatusProblem{
			Severity: "warn",
			Message:  fmt.Sprintf("Migration tracking table %s does not exist.", table),
			Action:   "Run migrate up to initialize the database schema.",
		})
		return report, nil
	}

	applied, err := appliedMigrations(ctx, pool, table)
	if err != nil {
		return report, err
	}
	return BuildStatus(all, applied, table, scope, true), nil
}

func BuildStatus(all []Migration, applied []AppliedMigration, table, scope string, trackingTableOk bool) StatusReport {
	report := StatusReport{
		Scope:           scope,
		TrackingTable:   table,
		TrackingTableOk: trackingTableOk,
		Consistent:      true,
		Applied:         applied,
	}
	if !trackingTableOk {
		report.Pending = migrationRefs(all)
		report.Problems = append(report.Problems, StatusProblem{
			Severity: "warn",
			Message:  fmt.Sprintf("Migration tracking table %s does not exist.", table),
			Action:   "Run migrate up to initialize the database schema.",
		})
		return report
	}
	embedded := map[int64]Migration{}
	appliedSet := map[int64]bool{}
	for _, m := range all {
		embedded[m.Version] = m
	}
	for _, m := range applied {
		appliedSet[m.Version] = true
		if expected, ok := embedded[m.Version]; !ok {
			report.Consistent = false
			report.Problems = append(report.Problems, StatusProblem{
				Severity: "error",
				Message:  fmt.Sprintf("Applied migration %d (%s) is not embedded in this binary.", m.Version, m.Name),
				Action:   "Deploy the binary that contains this migration, or restore the database/code pair to a matching release.",
				Details:  map[string]any{"version": m.Version, "name": m.Name},
			})
		} else if expected.Name != m.Name {
			report.Consistent = false
			report.Problems = append(report.Problems, StatusProblem{
				Severity: "error",
				Message:  fmt.Sprintf("Applied migration %d has name %q but the binary expects %q.", m.Version, m.Name, expected.Name),
				Action:   "Use a database and binary built from the same migration history.",
				Details:  map[string]any{"version": m.Version, "applied_name": m.Name, "embedded_name": expected.Name},
			})
		}
	}

	report.Pending = migrationRefs(Pending(all, appliedSet))
	report.RollbackAvailable = rollbackAvailableRefs(all, applied)
	return report
}

func Drift(ctx context.Context, pool *pgxpool.Pool, requirements []SchemaRequirement) ([]DriftFinding, error) {
	findings := make([]DriftFinding, 0)
	for _, req := range requirements {
		if strings.TrimSpace(req.Severity) == "" {
			req.Severity = "error"
		}
		if strings.TrimSpace(req.Action) == "" {
			req.Action = "Run migrate up and rerun doctor."
		}
		ok, err := schemaRequirementExists(ctx, pool, req)
		if err != nil {
			return nil, err
		}
		if ok {
			continue
		}
		findings = append(findings, DriftFinding{
			Severity: req.Severity,
			Object:   schemaRequirementObject(req),
			Message:  schemaRequirementMessage(req),
			Action:   req.Action,
		})
	}
	return findings, nil
}

func trackingTableExists(ctx context.Context, pool *pgxpool.Pool, table string) (bool, error) {
	var exists bool
	err := pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1
			FROM information_schema.tables
			WHERE table_schema = current_schema()
			  AND table_name = $1
		)`, table).Scan(&exists)
	return exists, err
}

func appliedMigrations(ctx context.Context, pool *pgxpool.Pool, table string) ([]AppliedMigration, error) {
	rows, err := pool.Query(ctx, "SELECT version, name, applied_at FROM "+table+" ORDER BY version ASC")
	if err != nil {
		return nil, fmt.Errorf("query applied migrations: %w", err)
	}
	defer rows.Close()

	applied := []AppliedMigration{}
	for rows.Next() {
		var m AppliedMigration
		if err := rows.Scan(&m.Version, &m.Name, &m.AppliedAt); err != nil {
			return nil, fmt.Errorf("scan applied migration: %w", err)
		}
		applied = append(applied, m)
	}
	return applied, rows.Err()
}

func migrationRefs(migrations []Migration) []MigrationRef {
	refs := make([]MigrationRef, 0, len(migrations))
	for _, m := range migrations {
		refs = append(refs, MigrationRef{Version: m.Version, Name: m.Name})
	}
	return refs
}

func rollbackAvailableRefs(all []Migration, applied []AppliedMigration) []MigrationRef {
	byVersion := map[int64]Migration{}
	for _, m := range all {
		byVersion[m.Version] = m
	}
	refs := make([]MigrationRef, 0, len(applied))
	for i := len(applied) - 1; i >= 0; i-- {
		m, ok := byVersion[applied[i].Version]
		if !ok {
			continue
		}
		refs = append(refs, MigrationRef{Version: m.Version, Name: m.Name})
	}
	return refs
}

func schemaRequirementExists(ctx context.Context, pool *pgxpool.Pool, req SchemaRequirement) (bool, error) {
	switch req.Kind {
	case SchemaRequirementTable:
		var exists bool
		err := pool.QueryRow(ctx, `
			SELECT EXISTS (
				SELECT 1
				FROM information_schema.tables
				WHERE table_schema = current_schema()
				  AND table_name = $1
			)`, req.Table).Scan(&exists)
		return exists, err
	case SchemaRequirementColumn:
		var exists bool
		err := pool.QueryRow(ctx, `
			SELECT EXISTS (
				SELECT 1
				FROM information_schema.columns
				WHERE table_schema = current_schema()
				  AND table_name = $1
				  AND column_name = $2
			)`, req.Table, req.Name).Scan(&exists)
		return exists, err
	case SchemaRequirementIndex:
		var exists bool
		err := pool.QueryRow(ctx, `
			SELECT EXISTS (
				SELECT 1
				FROM pg_indexes
				WHERE schemaname = current_schema()
				  AND indexname = $1
			)`, req.Name).Scan(&exists)
		return exists, err
	default:
		return false, fmt.Errorf("unknown schema requirement kind %q", req.Kind)
	}
}

func schemaRequirementObject(req SchemaRequirement) string {
	switch req.Kind {
	case SchemaRequirementTable:
		return "table:" + req.Table
	case SchemaRequirementColumn:
		return "column:" + req.Table + "." + req.Name
	case SchemaRequirementIndex:
		return "index:" + req.Name
	default:
		return string(req.Kind)
	}
}

func schemaRequirementMessage(req SchemaRequirement) string {
	switch req.Kind {
	case SchemaRequirementTable:
		return fmt.Sprintf("Required table %s is missing.", req.Table)
	case SchemaRequirementColumn:
		return fmt.Sprintf("Required column %s.%s is missing.", req.Table, req.Name)
	case SchemaRequirementIndex:
		return fmt.Sprintf("Required index %s is missing.", req.Name)
	default:
		return "Required schema object is missing."
	}
}
