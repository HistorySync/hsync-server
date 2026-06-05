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

type fakeNotificationOutboxDB struct {
	row      fakeNotificationOutboxRow
	rows     *fakeNotificationOutboxRows
	execArgs []any
	execTag  pgconn.CommandTag
}

func (db *fakeNotificationOutboxDB) QueryRow(context.Context, string, ...any) pgx.Row {
	return db.row
}

func (db *fakeNotificationOutboxDB) Query(_ context.Context, _ string, _ ...any) (pgx.Rows, error) {
	return db.rows, nil
}

func (db *fakeNotificationOutboxDB) Exec(_ context.Context, _ string, args ...any) (pgconn.CommandTag, error) {
	db.execArgs = args
	if db.execTag.RowsAffected() > 0 {
		return db.execTag, nil
	}
	return pgconn.CommandTag{}, nil
}

type fakeNotificationOutboxRow struct {
	values []any
	err    error
}

func (r fakeNotificationOutboxRow) Scan(dest ...any) error {
	if r.err != nil {
		return r.err
	}
	return scanNotificationOutboxValues(r.values, dest...)
}

type fakeNotificationOutboxRows struct {
	rows [][]any
	idx  int
	err  error
}

func (r *fakeNotificationOutboxRows) Close() {}
func (r *fakeNotificationOutboxRows) Err() error {
	return r.err
}
func (r *fakeNotificationOutboxRows) CommandTag() pgconn.CommandTag { return pgconn.CommandTag{} }
func (r *fakeNotificationOutboxRows) FieldDescriptions() []pgconn.FieldDescription {
	return nil
}
func (r *fakeNotificationOutboxRows) Next() bool {
	if r.idx >= len(r.rows) {
		return false
	}
	r.idx++
	return true
}
func (r *fakeNotificationOutboxRows) Scan(dest ...any) error {
	if r.idx == 0 || r.idx > len(r.rows) {
		return errors.New("scan without row")
	}
	return scanNotificationOutboxValues(r.rows[r.idx-1], dest...)
}
func (r *fakeNotificationOutboxRows) Values() ([]any, error) {
	if r.idx == 0 || r.idx > len(r.rows) {
		return nil, errors.New("values without row")
	}
	return r.rows[r.idx-1], nil
}
func (r *fakeNotificationOutboxRows) RawValues() [][]byte { return nil }
func (r *fakeNotificationOutboxRows) Conn() *pgx.Conn     { return nil }

func scanNotificationOutboxValues(values []any, dest ...any) error {
	for i, value := range values {
		switch d := dest[i].(type) {
		case *uuid.UUID:
			*d = value.(uuid.UUID)
		case *string:
			*d = value.(string)
		case *model.NotificationOutboxStatus:
			*d = model.NotificationOutboxStatus(value.(string))
		case *json.RawMessage:
			*d = append((*d)[:0], value.(json.RawMessage)...)
		case *int:
			*d = value.(int)
		case *time.Time:
			*d = value.(time.Time)
		case **time.Time:
			if value == nil {
				*d = nil
			} else {
				v := value.(time.Time)
				*d = &v
			}
		default:
			return errors.New("unsupported scan destination")
		}
	}
	return nil
}

func TestNotificationOutboxRepoEnqueueScansRow(t *testing.T) {
	now := time.Now().UTC()
	id := uuid.New()
	repo := NewNotificationOutboxRepo(&fakeNotificationOutboxDB{
		row: fakeNotificationOutboxRow{values: []any{
			id, string(model.NotificationOutboxPending), 0, "", now, now,
		}},
	})
	item := &model.NotificationOutbox{
		UserID:      uuid.New(),
		Channel:     model.NotificationChannelEmail,
		Category:    "security",
		Type:        "security.login",
		PayloadJSON: json.RawMessage(`{"subject":"Login"}`),
	}

	if err := repo.Enqueue(context.Background(), item); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	if item.ID != id || item.Status != model.NotificationOutboxPending || !item.CreatedAt.Equal(now) {
		t.Fatalf("item = %+v", item)
	}
}

func TestNotificationOutboxRepoClaimAndListFailuresScanRows(t *testing.T) {
	now := time.Now().UTC()
	id := uuid.New()
	userID := uuid.New()
	db := &fakeNotificationOutboxDB{
		rows: &fakeNotificationOutboxRows{rows: [][]any{{
			id, userID, string(model.NotificationChannelWebhook), "billing", "quota.warning",
			json.RawMessage(`{"subject":"Quota"}`), string(model.NotificationOutboxProcessing),
			1, now, "timeout", now, now, nil,
		}}},
	}
	repo := NewNotificationOutboxRepo(db)

	items, err := repo.ClaimDue(context.Background(), now, 10)
	if err != nil {
		t.Fatalf("ClaimDue: %v", err)
	}
	if len(items) != 1 || items[0].ID != id || items[0].Channel != model.NotificationChannelWebhook ||
		items[0].Status != model.NotificationOutboxProcessing {
		t.Fatalf("claimed = %+v", items)
	}

	db.rows.idx = 0
	items, err = repo.ListFailures(context.Background(), 10, 0)
	if err != nil {
		t.Fatalf("ListFailures: %v", err)
	}
	if len(items) != 1 || items[0].LastError != "timeout" {
		t.Fatalf("failures = %+v", items)
	}
}

func TestNotificationOutboxRepoStatusUpdates(t *testing.T) {
	db := &fakeNotificationOutboxDB{}
	repo := NewNotificationOutboxRepo(db)
	id := uuid.New()

	if err := repo.MarkSent(context.Background(), id, time.Now()); err != nil {
		t.Fatalf("MarkSent: %v", err)
	}
	if len(db.execArgs) != 2 || db.execArgs[0] != id {
		t.Fatalf("MarkSent args = %+v", db.execArgs)
	}

	next := time.Now().Add(time.Minute)
	if err := repo.MarkRetry(context.Background(), id, next, "temporary failure"); err != nil {
		t.Fatalf("MarkRetry: %v", err)
	}
	if len(db.execArgs) != 3 || db.execArgs[0] != id || db.execArgs[1] != next {
		t.Fatalf("MarkRetry args = %+v", db.execArgs)
	}

	if err := repo.MarkFailed(context.Background(), id, "permanent failure"); err != nil {
		t.Fatalf("MarkFailed: %v", err)
	}
	if len(db.execArgs) != 2 || db.execArgs[0] != id {
		t.Fatalf("MarkFailed args = %+v", db.execArgs)
	}
}

func TestNotificationOutboxRepoAdminLifecycleQueries(t *testing.T) {
	now := time.Now().UTC()
	id := uuid.New()
	userID := uuid.New()
	db := &fakeNotificationOutboxDB{
		rows: &fakeNotificationOutboxRows{rows: [][]any{{
			id, userID, string(model.NotificationChannelEmail), "security", "security.login",
			json.RawMessage(`{"subject":"Login"}`), string(model.NotificationOutboxFailed),
			3, now, "timeout", now, now, nil,
		}}},
	}
	repo := NewNotificationOutboxRepo(db)

	item, err := repo.GetByID(context.Background(), id)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if item == nil || item.ID != id || item.Status != model.NotificationOutboxFailed {
		t.Fatalf("item = %+v, want failed item", item)
	}

	db.rows.idx = 0
	item, err = repo.ClaimFailedByID(context.Background(), id)
	if err != nil {
		t.Fatalf("ClaimFailedByID: %v", err)
	}
	if item == nil || item.ID != id {
		t.Fatalf("claimed item = %+v, want id %s", item, id)
	}

	db.rows.idx = 0
	items, err := repo.ClaimFailed(context.Background(), 10)
	if err != nil {
		t.Fatalf("ClaimFailed: %v", err)
	}
	if len(items) != 1 || items[0].ID != id {
		t.Fatalf("batch claimed = %+v, want id %s", items, id)
	}
}

func TestNotificationOutboxRepoAdminLifecycleUpdates(t *testing.T) {
	db := &fakeNotificationOutboxDB{execTag: pgconn.NewCommandTag("UPDATE 1")}
	repo := NewNotificationOutboxRepo(db)
	id := uuid.New()
	next := time.Now().UTC()

	updated, err := repo.RequeueFailed(context.Background(), id, next)
	if err != nil {
		t.Fatalf("RequeueFailed: %v", err)
	}
	if !updated || len(db.execArgs) != 2 || db.execArgs[0] != id || db.execArgs[1] != next {
		t.Fatalf("RequeueFailed updated=%v args=%+v", updated, db.execArgs)
	}

	updated, err = repo.MarkDiscarded(context.Background(), id)
	if err != nil {
		t.Fatalf("MarkDiscarded: %v", err)
	}
	if !updated || len(db.execArgs) != 1 || db.execArgs[0] != id {
		t.Fatalf("MarkDiscarded updated=%v args=%+v", updated, db.execArgs)
	}
}
