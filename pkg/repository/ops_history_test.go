package repository

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/historysync/hsync-server/pkg/model"
)

type fakeOpsHistoryDB struct {
	row      fakeOpsHistoryRow
	rows     *fakeOpsHistoryRows
	rowArgs  []any
	queryArg []any
}

func (db *fakeOpsHistoryDB) QueryRow(_ context.Context, _ string, args ...any) pgx.Row {
	db.rowArgs = args
	return db.row
}

func (db *fakeOpsHistoryDB) Query(_ context.Context, _ string, args ...any) (pgx.Rows, error) {
	db.queryArg = args
	return db.rows, nil
}

type fakeOpsHistoryRow struct {
	values []any
	err    error
}

func (r fakeOpsHistoryRow) Scan(dest ...any) error {
	if r.err != nil {
		return r.err
	}
	return scanOpsHistoryValues(r.values, dest...)
}

type fakeOpsHistoryRows struct {
	rows [][]any
	idx  int
	err  error
}

func (r *fakeOpsHistoryRows) Close() {}
func (r *fakeOpsHistoryRows) Err() error {
	return r.err
}
func (r *fakeOpsHistoryRows) CommandTag() pgconn.CommandTag { return pgconn.CommandTag{} }
func (r *fakeOpsHistoryRows) FieldDescriptions() []pgconn.FieldDescription {
	return nil
}
func (r *fakeOpsHistoryRows) Next() bool {
	if r.idx >= len(r.rows) {
		return false
	}
	r.idx++
	return true
}
func (r *fakeOpsHistoryRows) Scan(dest ...any) error {
	if r.idx == 0 || r.idx > len(r.rows) {
		return errors.New("scan without row")
	}
	return scanOpsHistoryValues(r.rows[r.idx-1], dest...)
}
func (r *fakeOpsHistoryRows) Values() ([]any, error) {
	if r.idx == 0 || r.idx > len(r.rows) {
		return nil, errors.New("values without row")
	}
	return r.rows[r.idx-1], nil
}
func (r *fakeOpsHistoryRows) RawValues() [][]byte { return nil }
func (r *fakeOpsHistoryRows) Conn() *pgx.Conn     { return nil }

func scanOpsHistoryValues(values []any, dest ...any) error {
	for i, value := range values {
		switch d := dest[i].(type) {
		case *uuid.UUID:
			*d = value.(uuid.UUID)
		case *string:
			*d = value.(string)
		case *int64:
			*d = value.(int64)
		case *json.RawMessage:
			*d = append((*d)[:0], value.(json.RawMessage)...)
		case *time.Time:
			*d = value.(time.Time)
		default:
			return errors.New("unsupported scan destination")
		}
	}
	return nil
}

func TestOpsHistoryRepoCreateAndList(t *testing.T) {
	now := time.Now().UTC()
	id := uuid.New()
	db := &fakeOpsHistoryDB{
		row: fakeOpsHistoryRow{values: []any{id, now}},
		rows: &fakeOpsHistoryRows{rows: [][]any{{
			id, string(model.OpsRunTypeDependency), "degraded", now, now.Add(time.Second), int64(1000),
			json.RawMessage(`{"failures":[{"name":"redis"}]}`), json.RawMessage(`{"dependencies":4}`),
			json.RawMessage(`{"overall":"degraded"}`), now,
		}}},
	}
	repo := NewOpsHistoryRepo(db)
	run := &model.OpsCheckRun{
		RunType:            model.OpsRunTypeDependency,
		OverallStatus:      "degraded",
		StartedAt:          now,
		FinishedAt:         now.Add(time.Second),
		DurationMillis:     1000,
		SummarizedFindings: json.RawMessage(`{"failures":[{"name":"redis"}]}`),
		ArtifactCounts:     json.RawMessage(`{"dependencies":4}`),
		ReportJSON:         json.RawMessage(`{"overall":"degraded"}`),
	}

	if err := repo.Create(context.Background(), run); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if run.ID != id || run.CreatedAt != now {
		t.Fatalf("run after create = %+v, want id/created_at", run)
	}
	if len(db.rowArgs) != 8 || db.rowArgs[0] != string(model.OpsRunTypeDependency) {
		t.Fatalf("create args = %+v", db.rowArgs)
	}

	runs, err := repo.ListRecent(context.Background(), 10)
	if err != nil {
		t.Fatalf("ListRecent: %v", err)
	}
	if len(runs) != 1 || runs[0].RunType != model.OpsRunTypeDependency || runs[0].OverallStatus != "degraded" {
		t.Fatalf("runs = %+v, want dependency degraded run", runs)
	}
	if len(db.queryArg) != 1 || db.queryArg[0] != int32(10) {
		t.Fatalf("query args = %+v", db.queryArg)
	}

	db.rows.idx = 0
	failures, err := repo.ListRecentFailures(context.Background(), 10)
	if err != nil {
		t.Fatalf("ListRecentFailures: %v", err)
	}
	if len(failures) != 1 || failures[0].OverallStatus != "degraded" {
		t.Fatalf("failures = %+v, want degraded run", failures)
	}
}
