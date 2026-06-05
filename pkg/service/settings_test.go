package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/historysync/hsync-server/pkg/model"
)

// fakeSettingStore is an in-memory settingStore for unit tests.
type fakeSettingStore struct {
	rows    map[string]model.SystemSetting
	getErr  error
	upserts int
}

func (f *fakeSettingStore) Get(_ context.Context, key string) (*model.SystemSetting, error) {
	if f.getErr != nil {
		return nil, f.getErr
	}
	if row, ok := f.rows[key]; ok {
		cp := row
		return &cp, nil
	}
	return nil, nil
}

func (f *fakeSettingStore) Upsert(_ context.Context, s *model.SystemSetting) error {
	if f.rows == nil {
		f.rows = make(map[string]model.SystemSetting)
	}
	s.UpdatedAt = time.Unix(1_700_000_000, 0).UTC()
	f.rows[s.Key] = *s
	f.upserts++
	return nil
}

func testSettingDefs() []SettingDefinition {
	return []SettingDefinition{
		{Key: "feature_enabled", Type: SettingTypeBool, Default: "false", Description: "a bool"},
		{Key: "max_items", Type: SettingTypeInt, Default: "10", Description: "an int"},
		{Key: "ratio", Type: SettingTypeFloat, Default: "1.5", Description: "a float"},
		{Key: "banner", Type: SettingTypeString, Default: "hello", Description: "a string"},
		{Key: "api_token", Type: SettingTypeString, Default: "", Description: "a secret", Sensitive: true},
	}
}

func TestSettingsServiceRejectsUnknownKey(t *testing.T) {
	svc := NewSettingsService(&fakeSettingStore{}, testSettingDefs())

	if _, err := svc.Get(context.Background(), "nope"); !errors.Is(err, ErrUnknownSetting) {
		t.Fatalf("Get unknown key err = %v, want ErrUnknownSetting", err)
	}
	if err := svc.Set(context.Background(), "nope", "x"); !errors.Is(err, ErrUnknownSetting) {
		t.Fatalf("Set unknown key err = %v, want ErrUnknownSetting", err)
	}
}

func TestSettingsServiceGetReturnsDefault(t *testing.T) {
	svc := NewSettingsService(&fakeSettingStore{}, testSettingDefs())

	v, err := svc.Get(context.Background(), "banner")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if v != "hello" {
		t.Fatalf("Get banner = %q, want default %q", v, "hello")
	}
}

func TestSettingsServiceSetValidatesType(t *testing.T) {
	svc := NewSettingsService(&fakeSettingStore{}, testSettingDefs())

	cases := []struct {
		name    string
		key     string
		value   string
		wantErr bool
	}{
		{"bool valid", "feature_enabled", "true", false},
		{"bool invalid", "feature_enabled", "yes", true},
		{"int valid", "max_items", "42", false},
		{"int invalid", "max_items", "1.5", true},
		{"float valid", "ratio", "2.25", false},
		{"float invalid", "ratio", "abc", true},
		{"string anything", "banner", "any value", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := svc.Set(context.Background(), tc.key, tc.value)
			if tc.wantErr {
				if !errors.Is(err, ErrInvalidSettingValue) {
					t.Fatalf("Set(%q,%q) err = %v, want ErrInvalidSettingValue", tc.key, tc.value, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("Set(%q,%q) err = %v, want nil", tc.key, tc.value, err)
			}
		})
	}
}

func TestSettingsServiceSetThenGet(t *testing.T) {
	store := &fakeSettingStore{}
	svc := NewSettingsService(store, testSettingDefs())

	if err := svc.Set(context.Background(), "feature_enabled", "true"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	got, err := svc.GetBool(context.Background(), "feature_enabled")
	if err != nil {
		t.Fatalf("GetBool: %v", err)
	}
	if !got {
		t.Fatal("GetBool feature_enabled = false, want true")
	}
	// value_type and description are written from the definition, not the caller.
	row := store.rows["feature_enabled"]
	if row.ValueType != string(SettingTypeBool) || row.Description != "a bool" {
		t.Fatalf("stored row = %+v, want type bool / description from def", row)
	}
}

func TestSettingsServiceTypedAccessorsParseDefaults(t *testing.T) {
	svc := NewSettingsService(&fakeSettingStore{}, testSettingDefs())
	ctx := context.Background()

	if b, err := svc.GetBool(ctx, "feature_enabled"); err != nil || b {
		t.Fatalf("GetBool default = (%v, %v), want (false, nil)", b, err)
	}
	if n, err := svc.GetInt(ctx, "max_items"); err != nil || n != 10 {
		t.Fatalf("GetInt default = (%v, %v), want (10, nil)", n, err)
	}
	if f, err := svc.GetFloat(ctx, "ratio"); err != nil || f != 1.5 {
		t.Fatalf("GetFloat default = (%v, %v), want (1.5, nil)", f, err)
	}
}

func TestSettingsServiceListMasksSensitive(t *testing.T) {
	store := &fakeSettingStore{}
	svc := NewSettingsService(store, testSettingDefs())
	if err := svc.Set(context.Background(), "api_token", "super-secret"); err != nil {
		t.Fatalf("Set api_token: %v", err)
	}
	if err := svc.Set(context.Background(), "banner", "live"); err != nil {
		t.Fatalf("Set banner: %v", err)
	}

	views, err := svc.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(views) != len(testSettingDefs()) {
		t.Fatalf("List len = %d, want %d", len(views), len(testSettingDefs()))
	}
	// Sorted by key: api_token, banner, feature_enabled, max_items, ratio.
	if views[0].Key != "api_token" || views[1].Key != "banner" {
		t.Fatalf("List not sorted by key: %q, %q", views[0].Key, views[1].Key)
	}

	byKey := make(map[string]SettingView, len(views))
	for _, v := range views {
		byKey[v.Key] = v
	}
	token := byKey["api_token"]
	if !token.Sensitive || token.Value != "" || !token.IsSet {
		t.Fatalf("api_token view = %+v, want sensitive masked value, IsSet true", token)
	}
	if token.UpdatedAt == nil {
		t.Fatal("api_token view UpdatedAt = nil, want set timestamp")
	}
	banner := byKey["banner"]
	if banner.Sensitive || banner.Value != "live" || !banner.IsSet {
		t.Fatalf("banner view = %+v, want plaintext live, IsSet true", banner)
	}
	ratio := byKey["ratio"]
	if ratio.IsSet || ratio.Value != "1.5" {
		t.Fatalf("ratio view = %+v, want default 1.5, IsSet false", ratio)
	}
}

func TestSettingsServiceNilStore(t *testing.T) {
	svc := NewSettingsService(nil, testSettingDefs())
	ctx := context.Background()

	v, err := svc.Get(ctx, "banner")
	if err != nil || v != "hello" {
		t.Fatalf("Get with nil store = (%q, %v), want (hello, nil)", v, err)
	}
	if err := svc.Set(ctx, "banner", "x"); !errors.Is(err, ErrSettingsUnavailable) {
		t.Fatalf("Set with nil store err = %v, want ErrSettingsUnavailable", err)
	}
	views, err := svc.List(ctx)
	if err != nil || len(views) != len(testSettingDefs()) {
		t.Fatalf("List with nil store = (%d views, %v), want all defaults", len(views), err)
	}
	for _, view := range views {
		if view.IsSet {
			t.Fatalf("view %q IsSet = true with nil store, want false", view.Key)
		}
	}
}

func TestSettingsServiceSetRejectsBeforePersist(t *testing.T) {
	// An invalid value must not reach the store.
	store := &fakeSettingStore{}
	svc := NewSettingsService(store, testSettingDefs())
	if err := svc.Set(context.Background(), "max_items", "not-an-int"); !errors.Is(err, ErrInvalidSettingValue) {
		t.Fatalf("Set invalid int err = %v, want ErrInvalidSettingValue", err)
	}
	if store.upserts != 0 {
		t.Fatalf("store.upserts = %d, want 0 (invalid value must not persist)", store.upserts)
	}
}
