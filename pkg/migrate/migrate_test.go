package migrate

import (
	"testing"
	"testing/fstest"
)

func TestParseSortsAndPairs(t *testing.T) {
	fsys := fstest.MapFS{
		"002_add_index.up.sql":   {Data: []byte("CREATE INDEX i ON t(c);")},
		"002_add_index.down.sql": {Data: []byte("DROP INDEX i;")},
		"001_initial.up.sql":     {Data: []byte("CREATE TABLE t();")},
		"001_initial.down.sql":   {Data: []byte("DROP TABLE t;")},
		"README.md":              {Data: []byte("ignored")},
	}

	got, err := Parse(fsys)
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2", len(got))
	}
	if got[0].Version != 1 || got[1].Version != 2 {
		t.Fatalf("versions = %d,%d want 1,2", got[0].Version, got[1].Version)
	}
	if got[0].Name != "initial" {
		t.Fatalf("name = %q want initial", got[0].Name)
	}
	if got[0].Up == "" || got[0].Down == "" {
		t.Fatal("up/down content missing")
	}
}

func TestParseMissingDown(t *testing.T) {
	fsys := fstest.MapFS{"001_initial.up.sql": {Data: []byte("x")}}
	if _, err := Parse(fsys); err == nil {
		t.Fatal("Parse() error = nil, want missing-down error")
	}
}

func TestParseConflictingNames(t *testing.T) {
	fsys := fstest.MapFS{
		"001_a.up.sql":   {Data: []byte("x")},
		"001_a.down.sql": {Data: []byte("x")},
		"001_b.up.sql":   {Data: []byte("x")},
		"001_b.down.sql": {Data: []byte("x")},
	}
	if _, err := Parse(fsys); err == nil {
		t.Fatal("Parse() error = nil, want conflicting-names error")
	}
}

func TestParseRejectsBadFileNames(t *testing.T) {
	for _, name := range []string{
		"initial.up.sql",     // no version prefix
		"001_initial.sql",    // no direction
		"abc_initial.up.sql", // non-numeric version
		"001_.up.sql",        // empty name
	} {
		fsys := fstest.MapFS{name: {Data: []byte("x")}}
		if _, err := Parse(fsys); err == nil {
			t.Fatalf("Parse(%q) error = nil, want error", name)
		}
	}
}

func TestPending(t *testing.T) {
	all := []Migration{{Version: 1}, {Version: 2}, {Version: 3}}
	got := Pending(all, map[int64]bool{1: true})
	if len(got) != 2 || got[0].Version != 2 || got[1].Version != 3 {
		t.Fatalf("Pending() = %+v, want versions 2,3", got)
	}
}

func TestRollbackPlan(t *testing.T) {
	all := []Migration{{Version: 1, Name: "a"}, {Version: 2, Name: "b"}, {Version: 3, Name: "c"}}
	plan, err := RollbackPlan(all, []int64{3, 2, 1}, 2)
	if err != nil {
		t.Fatalf("RollbackPlan() error = %v", err)
	}
	if len(plan) != 2 || plan[0].Version != 3 || plan[1].Version != 2 {
		t.Fatalf("RollbackPlan() = %+v, want versions 3,2", plan)
	}
}

func TestRollbackPlanMissingDefinition(t *testing.T) {
	all := []Migration{{Version: 1}}
	if _, err := RollbackPlan(all, []int64{2}, 1); err == nil {
		t.Fatal("RollbackPlan() error = nil, want missing-definition error")
	}
}

func TestNextVersion(t *testing.T) {
	if v := NextVersion(nil); v != 1 {
		t.Fatalf("NextVersion(nil) = %d, want 1", v)
	}
	if v := NextVersion([]Migration{{Version: 1}, {Version: 5}}); v != 6 {
		t.Fatalf("NextVersion() = %d, want 6", v)
	}
}

func TestFormatVersion(t *testing.T) {
	if s := FormatVersion(2); s != "002" {
		t.Fatalf("FormatVersion(2) = %q, want 002", s)
	}
	if s := FormatVersion(123); s != "123" {
		t.Fatalf("FormatVersion(123) = %q, want 123", s)
	}
}

func TestSanitizeName(t *testing.T) {
	cases := map[string]string{
		"Add Index":       "add_index",
		"add-rate-limits": "add_rate_limits",
		"  Mixed__Case  ": "mixed_case",
	}
	for in, want := range cases {
		got, err := SanitizeName(in)
		if err != nil {
			t.Fatalf("SanitizeName(%q) error = %v", in, err)
		}
		if got != want {
			t.Fatalf("SanitizeName(%q) = %q, want %q", in, got, want)
		}
	}
	if _, err := SanitizeName("  --  "); err == nil {
		t.Fatal("SanitizeName() error = nil for empty name, want error")
	}
}

func TestValidateTrackingTable(t *testing.T) {
	for _, table := range []string{"schema_migrations", "enterprise_schema_migrations", "Migrations_2"} {
		if err := validateTrackingTable(table); err != nil {
			t.Fatalf("validateTrackingTable(%q) error = %v", table, err)
		}
	}
	for _, table := range []string{"", "1schema", "schema-migrations", "schema;migrations"} {
		if err := validateTrackingTable(table); err == nil {
			t.Fatalf("validateTrackingTable(%q) error = nil, want error", table)
		}
	}
}

func TestBuildStatusPlansPendingAndRollback(t *testing.T) {
	all := []Migration{
		{Version: 1, Name: "initial"},
		{Version: 2, Name: "settings"},
		{Version: 3, Name: "passkeys"},
	}
	applied := []AppliedMigration{
		{Version: 1, Name: "initial"},
		{Version: 2, Name: "settings"},
	}

	status := BuildStatus(all, applied, "schema_migrations", "community", true)
	if !status.Consistent {
		t.Fatalf("Consistent = false, problems = %#v", status.Problems)
	}
	if len(status.Pending) != 1 || status.Pending[0].Version != 3 {
		t.Fatalf("Pending = %#v, want version 3", status.Pending)
	}
	if len(status.RollbackAvailable) != 2 || status.RollbackAvailable[0].Version != 2 || status.RollbackAvailable[1].Version != 1 {
		t.Fatalf("RollbackAvailable = %#v, want versions 2,1", status.RollbackAvailable)
	}
}

func TestBuildStatusDetectsUnknownAppliedMigration(t *testing.T) {
	status := BuildStatus(
		[]Migration{{Version: 1, Name: "initial"}},
		[]AppliedMigration{{Version: 1, Name: "initial"}, {Version: 2, Name: "future"}},
		"schema_migrations",
		"community",
		true,
	)
	if status.Consistent {
		t.Fatal("Consistent = true, want false for unknown applied migration")
	}
	if len(status.Problems) != 1 || status.Problems[0].Severity != "error" {
		t.Fatalf("Problems = %#v, want one error", status.Problems)
	}
	if len(status.RollbackAvailable) != 1 || status.RollbackAvailable[0].Version != 1 {
		t.Fatalf("RollbackAvailable = %#v, want only known version 1", status.RollbackAvailable)
	}
}

func TestBuildStatusDetectsNameMismatch(t *testing.T) {
	status := BuildStatus(
		[]Migration{{Version: 1, Name: "initial"}},
		[]AppliedMigration{{Version: 1, Name: "renamed"}},
		"schema_migrations",
		"community",
		true,
	)
	if status.Consistent {
		t.Fatal("Consistent = true, want false for migration name mismatch")
	}
	if len(status.Problems) != 1 {
		t.Fatalf("Problems = %#v, want one problem", status.Problems)
	}
}

func TestBuildStatusTrackingTableMissingPlansAllPending(t *testing.T) {
	status := BuildStatus(
		[]Migration{{Version: 1, Name: "initial"}, {Version: 2, Name: "settings"}},
		nil,
		"schema_migrations",
		"community",
		false,
	)
	if status.TrackingTableOk {
		t.Fatal("TrackingTableOk = true, want false")
	}
	if len(status.Pending) != 2 {
		t.Fatalf("Pending = %#v, want all migrations", status.Pending)
	}
	if len(status.RollbackAvailable) != 0 {
		t.Fatalf("RollbackAvailable = %#v, want empty", status.RollbackAvailable)
	}
	if len(status.Problems) != 1 || status.Problems[0].Severity != "warn" {
		t.Fatalf("Problems = %#v, want one warn", status.Problems)
	}
}
