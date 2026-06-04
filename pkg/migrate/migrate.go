// Package migrate provides a minimal forward/backward SQL migration runner for
// the HistorySync Cloud Server.
//
// It reads migration files from an fs.FS (the embedded migrations package in
// production) and applies them in version order, tracking applied versions in a
// schema_migrations table. The runner intentionally avoids third-party
// migration libraries and executes each file as a single multi-statement script
// via the PostgreSQL simple query protocol, so files may manage their own
// BEGIN/COMMIT blocks (including statements that contain semicolons, such as
// PL/pgSQL function bodies).
package migrate

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
)

const schemaMigrationsTable = "schema_migrations"

// Migration is a single versioned migration with its up and down scripts.
type Migration struct {
	Version int64
	Name    string
	Up      string
	Down    string
}

// Parse reads and validates all migration files from fsys. It expects files
// named "<version>_<name>.up.sql" and "<version>_<name>.down.sql" and returns
// the migrations sorted by ascending version. Non-".sql" files are ignored.
func Parse(fsys fs.FS) ([]Migration, error) {
	entries, err := fs.ReadDir(fsys, ".")
	if err != nil {
		return nil, fmt.Errorf("read migrations dir: %w", err)
	}

	type pair struct {
		name           string
		up, down       string
		hasUp, hasDown bool
	}
	byVersion := map[int64]*pair{}
	var order []int64

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		fileName := entry.Name()
		if !strings.HasSuffix(fileName, ".sql") {
			continue
		}
		version, name, direction, err := parseFileName(fileName)
		if err != nil {
			return nil, err
		}
		content, err := fs.ReadFile(fsys, fileName)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", fileName, err)
		}

		p := byVersion[version]
		if p == nil {
			p = &pair{name: name}
			byVersion[version] = p
			order = append(order, version)
		}
		if p.name != name {
			return nil, fmt.Errorf("migration version %d has conflicting names %q and %q", version, p.name, name)
		}
		switch direction {
		case "up":
			if p.hasUp {
				return nil, fmt.Errorf("duplicate up migration for version %d", version)
			}
			p.up, p.hasUp = string(content), true
		case "down":
			if p.hasDown {
				return nil, fmt.Errorf("duplicate down migration for version %d", version)
			}
			p.down, p.hasDown = string(content), true
		}
	}

	sort.Slice(order, func(i, j int) bool { return order[i] < order[j] })

	migrations := make([]Migration, 0, len(order))
	for _, version := range order {
		p := byVersion[version]
		if !p.hasUp {
			return nil, fmt.Errorf("migration version %d is missing its .up.sql file", version)
		}
		if !p.hasDown {
			return nil, fmt.Errorf("migration version %d is missing its .down.sql file", version)
		}
		migrations = append(migrations, Migration{Version: version, Name: p.name, Up: p.up, Down: p.down})
	}
	return migrations, nil
}

// parseFileName extracts the version, name, and direction from a migration file
// name of the form "<version>_<name>.<up|down>.sql".
func parseFileName(fileName string) (version int64, name, direction string, err error) {
	base := strings.TrimSuffix(fileName, ".sql")
	dot := strings.LastIndex(base, ".")
	if dot < 0 {
		return 0, "", "", fmt.Errorf("migration file %q must end in .up.sql or .down.sql", fileName)
	}
	direction = base[dot+1:]
	if direction != "up" && direction != "down" {
		return 0, "", "", fmt.Errorf("migration file %q must end in .up.sql or .down.sql", fileName)
	}

	stem := base[:dot] // "<version>_<name>"
	underscore := strings.Index(stem, "_")
	if underscore <= 0 {
		return 0, "", "", fmt.Errorf("migration file %q must be named <version>_<name>.%s.sql", fileName, direction)
	}
	versionStr, name := stem[:underscore], stem[underscore+1:]
	version, err = strconv.ParseInt(versionStr, 10, 64)
	if err != nil {
		return 0, "", "", fmt.Errorf("migration file %q has invalid version %q", fileName, versionStr)
	}
	if name == "" {
		return 0, "", "", fmt.Errorf("migration file %q has an empty name", fileName)
	}
	return version, name, direction, nil
}

// Pending returns the migrations from all whose version has not been applied,
// preserving ascending version order.
func Pending(all []Migration, applied map[int64]bool) []Migration {
	pending := make([]Migration, 0)
	for _, m := range all {
		if !applied[m.Version] {
			pending = append(pending, m)
		}
	}
	return pending
}

// RollbackPlan returns up to n migrations to roll back, most recent first.
// appliedDesc must list applied versions in descending order. It errors if an
// applied version has no corresponding migration definition.
func RollbackPlan(all []Migration, appliedDesc []int64, n int) ([]Migration, error) {
	byVersion := map[int64]Migration{}
	for _, m := range all {
		byVersion[m.Version] = m
	}

	plan := make([]Migration, 0, n)
	for _, version := range appliedDesc {
		if len(plan) >= n {
			break
		}
		m, ok := byVersion[version]
		if !ok {
			return nil, fmt.Errorf("applied migration version %d has no definition to roll back", version)
		}
		plan = append(plan, m)
	}
	return plan, nil
}

// NextVersion returns the next version number (max existing + 1, or 1 if none).
func NextVersion(all []Migration) int64 {
	var max int64
	for _, m := range all {
		if m.Version > max {
			max = m.Version
		}
	}
	return max + 1
}

// FormatVersion renders a version as a zero-padded 3-digit string.
func FormatVersion(version int64) string {
	return fmt.Sprintf("%03d", version)
}

// SanitizeName normalizes a migration name to lowercase snake_case.
func SanitizeName(raw string) (string, error) {
	var b strings.Builder
	prevUnderscore := false
	for _, r := range strings.TrimSpace(strings.ToLower(raw)) {
		switch {
		case (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9'):
			b.WriteRune(r)
			prevUnderscore = false
		case r == ' ' || r == '-' || r == '_':
			if !prevUnderscore && b.Len() > 0 {
				b.WriteByte('_')
				prevUnderscore = true
			}
		}
	}
	name := strings.Trim(b.String(), "_")
	if name == "" {
		return "", fmt.Errorf("migration name must contain at least one letter or digit")
	}
	return name, nil
}

// Up applies all pending migrations in order and returns those it applied.
func Up(ctx context.Context, pool *pgxpool.Pool, fsys fs.FS) ([]Migration, error) {
	all, err := Parse(fsys)
	if err != nil {
		return nil, err
	}
	if err := ensureSchemaTable(ctx, pool); err != nil {
		return nil, err
	}
	applied, _, err := appliedVersions(ctx, pool)
	if err != nil {
		return nil, err
	}

	pending := Pending(all, applied)
	for _, m := range pending {
		if err := execScript(ctx, pool, m.Up); err != nil {
			return nil, fmt.Errorf("apply migration %d (%s): %w", m.Version, m.Name, err)
		}
		if _, err := pool.Exec(ctx,
			"INSERT INTO "+schemaMigrationsTable+" (version, name) VALUES ($1, $2)", m.Version, m.Name); err != nil {
			return nil, fmt.Errorf("record migration %d: %w", m.Version, err)
		}
	}
	return pending, nil
}

// Down rolls back the most recent n applied migrations and returns them.
func Down(ctx context.Context, pool *pgxpool.Pool, fsys fs.FS, n int) ([]Migration, error) {
	if n <= 0 {
		return nil, nil
	}
	all, err := Parse(fsys)
	if err != nil {
		return nil, err
	}
	if err := ensureSchemaTable(ctx, pool); err != nil {
		return nil, err
	}
	_, appliedDesc, err := appliedVersions(ctx, pool)
	if err != nil {
		return nil, err
	}

	plan, err := RollbackPlan(all, appliedDesc, n)
	if err != nil {
		return nil, err
	}
	for _, m := range plan {
		if err := execScript(ctx, pool, m.Down); err != nil {
			return nil, fmt.Errorf("roll back migration %d (%s): %w", m.Version, m.Name, err)
		}
		if _, err := pool.Exec(ctx,
			"DELETE FROM "+schemaMigrationsTable+" WHERE version = $1", m.Version); err != nil {
			return nil, fmt.Errorf("unrecord migration %d: %w", m.Version, err)
		}
	}
	return plan, nil
}

// Create writes empty up/down migration files for a new migration into dir and
// returns their paths. The version is the next sequential number based on the
// migrations already present in dir.
func Create(dir, rawName string) (upPath, downPath string, err error) {
	name, err := SanitizeName(rawName)
	if err != nil {
		return "", "", err
	}
	all, err := Parse(os.DirFS(dir))
	if err != nil {
		return "", "", err
	}
	version := FormatVersion(NextVersion(all))
	upPath = filepath.Join(dir, version+"_"+name+".up.sql")
	downPath = filepath.Join(dir, version+"_"+name+".down.sql")

	upTemplate := fmt.Sprintf("-- migrations/%s_%s.up.sql\n\nBEGIN;\n\n-- TODO: write the forward migration here.\n\nCOMMIT;\n", version, name)
	downTemplate := fmt.Sprintf("-- migrations/%s_%s.down.sql\n\nBEGIN;\n\n-- TODO: write the rollback migration here.\n\nCOMMIT;\n", version, name)

	if err := os.WriteFile(upPath, []byte(upTemplate), 0o644); err != nil {
		return "", "", fmt.Errorf("write %s: %w", upPath, err)
	}
	if err := os.WriteFile(downPath, []byte(downTemplate), 0o644); err != nil {
		return "", "", fmt.Errorf("write %s: %w", downPath, err)
	}
	return upPath, downPath, nil
}

func ensureSchemaTable(ctx context.Context, pool *pgxpool.Pool) error {
	const q = `CREATE TABLE IF NOT EXISTS ` + schemaMigrationsTable + ` (
		version    BIGINT PRIMARY KEY,
		name       TEXT NOT NULL,
		applied_at TIMESTAMPTZ NOT NULL DEFAULT now()
	)`
	if _, err := pool.Exec(ctx, q); err != nil {
		return fmt.Errorf("ensure %s table: %w", schemaMigrationsTable, err)
	}
	return nil
}

// appliedVersions returns the set of applied versions and the same versions in
// descending order (for rollback planning).
func appliedVersions(ctx context.Context, pool *pgxpool.Pool) (map[int64]bool, []int64, error) {
	rows, err := pool.Query(ctx, "SELECT version FROM "+schemaMigrationsTable+" ORDER BY version DESC")
	if err != nil {
		return nil, nil, fmt.Errorf("query applied migrations: %w", err)
	}
	defer rows.Close()

	applied := map[int64]bool{}
	var desc []int64
	for rows.Next() {
		var v int64
		if err := rows.Scan(&v); err != nil {
			return nil, nil, fmt.Errorf("scan applied migration: %w", err)
		}
		applied[v] = true
		desc = append(desc, v)
	}
	return applied, desc, rows.Err()
}

// execScript runs a multi-statement SQL script using the simple query protocol
// so a file may contain multiple statements and its own BEGIN/COMMIT block.
func execScript(ctx context.Context, pool *pgxpool.Pool, script string) error {
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("acquire connection: %w", err)
	}
	defer conn.Release()

	mrr := conn.Conn().PgConn().Exec(ctx, script)
	if _, err := mrr.ReadAll(); err != nil {
		return err
	}
	return mrr.Close()
}
