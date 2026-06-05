package repository

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/historysync/hsync-server/pkg/model"
)

type fakeSystemSettingDB struct {
	row fakeSystemSettingRow
}

func (db *fakeSystemSettingDB) QueryRow(context.Context, string, ...any) pgx.Row {
	return db.row
}

func (db *fakeSystemSettingDB) Exec(context.Context, string, ...any) (pgconn.CommandTag, error) {
	return pgconn.CommandTag{}, nil
}

type fakeSystemSettingRow struct {
	values []any
	err    error
}

func (r fakeSystemSettingRow) Scan(dest ...any) error {
	if r.err != nil {
		return r.err
	}
	for i, value := range r.values {
		switch d := dest[i].(type) {
		case *string:
			*d = value.(string)
		case *time.Time:
			*d = value.(time.Time)
		default:
			return errors.New("unsupported scan destination")
		}
	}
	return nil
}

func TestSystemSettingRepoGetMissingReturnsNil(t *testing.T) {
	repo := NewSystemSettingRepo(&fakeSystemSettingDB{
		row: fakeSystemSettingRow{err: pgx.ErrNoRows},
	})

	got, err := repo.Get(context.Background(), "missing")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got != nil {
		t.Fatalf("Get missing = %+v, want nil", got)
	}
}

func TestSystemSettingRepoGetScansRow(t *testing.T) {
	now := time.Now().UTC()
	repo := NewSystemSettingRepo(&fakeSystemSettingDB{
		row: fakeSystemSettingRow{values: []any{"announcement", "hi", "string", "a banner", now}},
	})

	got, err := repo.Get(context.Background(), "announcement")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got == nil {
		t.Fatal("Get = nil, want row")
	}
	if got.Key != "announcement" || got.Value != "hi" || got.ValueType != "string" ||
		got.Description != "a banner" || !got.UpdatedAt.Equal(now) {
		t.Fatalf("Get = %+v", got)
	}
}

func TestSystemSettingRepoUpsertScansUpdatedAt(t *testing.T) {
	now := time.Now().UTC()
	repo := NewSystemSettingRepo(&fakeSystemSettingDB{
		row: fakeSystemSettingRow{values: []any{now}},
	})
	s := &model.SystemSetting{Key: "announcement", Value: "hi", ValueType: "string", Description: "a banner"}

	if err := repo.Upsert(context.Background(), s); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	if !s.UpdatedAt.Equal(now) {
		t.Fatalf("UpdatedAt = %s, want %s", s.UpdatedAt, now)
	}
}

func TestSystemSettingRepoNilTolerant(t *testing.T) {
	var repo *SystemSettingRepo
	got, err := repo.Get(context.Background(), "k")
	if err != nil || got != nil {
		t.Fatalf("nil repo Get = (%+v, %v), want (nil, nil)", got, err)
	}
	if err := repo.Upsert(context.Background(), &model.SystemSetting{Key: "k"}); err == nil {
		t.Fatal("nil repo Upsert err = nil, want error")
	}
}
