package service

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/historysync/hsync-server/pkg/model"
)

// Errors returned by the dynamic system settings flow.
var (
	// ErrUnknownSetting is returned when a key is not in the code-declared
	// whitelist. Only declared keys can be read or written, which prevents
	// arbitrary configuration injection through the admin API.
	ErrUnknownSetting = errors.New("unknown system setting key")
	// ErrInvalidSettingValue is returned when a value does not parse for the
	// setting's declared type.
	ErrInvalidSettingValue = errors.New("invalid value for system setting type")
	// ErrSettingsUnavailable is returned by Set when no persistence store is
	// configured. Reads still succeed by falling back to declared defaults.
	ErrSettingsUnavailable = errors.New("system settings store is not configured")
)

// SettingValueType is the declared type of a system setting value. Values are
// stored as strings and parsed/validated against this type on write.
type SettingValueType string

const (
	SettingTypeString SettingValueType = "string"
	SettingTypeBool   SettingValueType = "bool"
	SettingTypeInt    SettingValueType = "int"
	SettingTypeFloat  SettingValueType = "float"
)

const (
	SettingKeySignupsEnabled  = "signups_enabled"
	SettingKeyMaintenanceMode = "maintenance_mode"
	SettingKeyAnnouncement    = "announcement"
	SettingKeyPasskeyEnabled  = "passkey_enabled"
	SettingKeyPasskeyOrigins  = "passkey_origins"
	SettingKeyPasskeyRPID     = "passkey_rp_id"
	SettingKeyPasskeyRPName   = "passkey_rp_name"
)

const (
	SettingGroupAuth          = "auth"
	SettingGroupSecurity      = "security"
	SettingGroupNotifications = "notifications"
	SettingGroupOperations    = "operations"
	SettingGroupStorage       = "storage"
)

// SettingDefinition declares a whitelisted system setting. The definition is
// the authoritative source for the value type, default, description, group, and
// operational metadata; the database only stores override values.
type SettingDefinition struct {
	Key             string
	Type            SettingValueType
	Default         string
	Description     string
	Group           string
	RequiresRestart bool
	// Sensitive marks a value that must never be returned in plaintext over the
	// API. It is masked in List output and not echoed by writes. Internal typed
	// accessors still return the real value for server-side use.
	Sensitive bool
}

// SettingView is the API-safe representation of a setting. For sensitive
// settings Value is blanked and IsSet reports whether an override exists, so a
// secret value is never exposed while operators can still see that one is set.
type SettingView struct {
	Key             string     `json:"key"`
	Value           string     `json:"value"`
	ValueType       string     `json:"value_type"`
	Description     string     `json:"description"`
	Group           string     `json:"group"`
	Sensitive       bool       `json:"sensitive"`
	RequiresRestart bool       `json:"requires_restart"`
	IsSet           bool       `json:"is_set"`
	UpdatedAt       *time.Time `json:"updated_at,omitempty"`
}

// settingStore is the persistence surface the settings service needs.
// *repository.SystemSettingRepo satisfies it; tests supply an in-memory fake.
type settingStore interface {
	Get(ctx context.Context, key string) (*model.SystemSetting, error)
	Upsert(ctx context.Context, s *model.SystemSetting) error
}

// SettingsService reads and writes database-driven dynamic system settings,
// enforcing a code-declared whitelist and per-type validation. A missing row
// falls back to the declared default, so the store is never a hard dependency
// for reads and the database is never required for the server to start.
type SettingsService struct {
	store settingStore
	defs  map[string]SettingDefinition
}

// NewSettingsService builds a settings service over store with the given
// whitelist. A nil store keeps reads working (always returning defaults) while
// making writes fail with ErrSettingsUnavailable.
func NewSettingsService(store settingStore, defs []SettingDefinition) *SettingsService {
	m := make(map[string]SettingDefinition, len(defs))
	for _, d := range defs {
		m[d.Key] = d
	}
	return &SettingsService{store: store, defs: m}
}

// Definition returns the whitelist definition for key.
func (s *SettingsService) Definition(key string) (SettingDefinition, bool) {
	def, ok := s.defs[key]
	return def, ok
}

// Get returns the effective string value for key: the stored override when
// present, otherwise the declared default. Unknown keys are rejected.
func (s *SettingsService) Get(ctx context.Context, key string) (string, error) {
	def, ok := s.defs[key]
	if !ok {
		return "", ErrUnknownSetting
	}
	if s.store != nil {
		row, err := s.store.Get(ctx, key)
		if err != nil {
			return "", err
		}
		if row != nil {
			return row.Value, nil
		}
	}
	return def.Default, nil
}

// GetBool returns the effective value of a bool setting.
func (s *SettingsService) GetBool(ctx context.Context, key string) (bool, error) {
	v, err := s.Get(ctx, key)
	if err != nil {
		return false, err
	}
	return strconv.ParseBool(strings.TrimSpace(v))
}

// GetBoolOrDefault returns a bool setting, falling back to the declared default
// when the backing store is unavailable. Runtime feature gates use this so a
// transient settings-store failure does not become a hard service dependency.
func (s *SettingsService) GetBoolOrDefault(ctx context.Context, key string) bool {
	if s == nil {
		return false
	}
	def, ok := s.defs[key]
	if !ok {
		return false
	}
	fallback, _ := strconv.ParseBool(strings.TrimSpace(def.Default))
	v, err := s.GetBool(ctx, key)
	if err != nil {
		return fallback
	}
	return v
}

// GetInt returns the effective value of an int setting.
func (s *SettingsService) GetInt(ctx context.Context, key string) (int64, error) {
	v, err := s.Get(ctx, key)
	if err != nil {
		return 0, err
	}
	return strconv.ParseInt(strings.TrimSpace(v), 10, 64)
}

// GetFloat returns the effective value of a float setting.
func (s *SettingsService) GetFloat(ctx context.Context, key string) (float64, error) {
	v, err := s.Get(ctx, key)
	if err != nil {
		return 0, err
	}
	return strconv.ParseFloat(strings.TrimSpace(v), 64)
}

// GetString returns the effective value of a string setting (an alias for Get
// that reads clearly at call sites alongside the other typed accessors).
func (s *SettingsService) GetString(ctx context.Context, key string) (string, error) {
	return s.Get(ctx, key)
}

// Set validates and persists an override for key. The key must be declared and
// the value must parse for its declared type; otherwise ErrUnknownSetting or
// ErrInvalidSettingValue is returned. value_type and description are written
// from the definition so the row stays in sync with code.
func (s *SettingsService) Set(ctx context.Context, key, value string) error {
	def, ok := s.defs[key]
	if !ok {
		return ErrUnknownSetting
	}
	if err := validateSettingValue(def.Type, value); err != nil {
		return err
	}
	if s.store == nil {
		return ErrSettingsUnavailable
	}
	return s.store.Upsert(ctx, &model.SystemSetting{
		Key:         def.Key,
		Value:       value,
		ValueType:   string(def.Type),
		Description: def.Description,
	})
}

// List returns an API-safe view of every declared setting, sorted by key, using
// the stored override when present and the declared default otherwise. Sensitive
// values are blanked.
func (s *SettingsService) List(ctx context.Context) ([]SettingView, error) {
	keys := make([]string, 0, len(s.defs))
	for k := range s.defs {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	views := make([]SettingView, 0, len(keys))
	for _, k := range keys {
		def := s.defs[k]
		view := SettingView{
			Key:             def.Key,
			Value:           def.Default,
			ValueType:       string(def.Type),
			Description:     def.Description,
			Group:           def.Group,
			Sensitive:       def.Sensitive,
			RequiresRestart: def.RequiresRestart,
		}
		if s.store != nil {
			row, err := s.store.Get(ctx, k)
			if err != nil {
				return nil, err
			}
			if row != nil {
				updatedAt := row.UpdatedAt
				view.Value = row.Value
				view.IsSet = true
				view.UpdatedAt = &updatedAt
			}
		}
		if def.Sensitive {
			// Never expose sensitive plaintext over the API; IsSet still tells
			// operators whether an override exists.
			view.Value = ""
		}
		views = append(views, view)
	}
	return views, nil
}

// validateSettingValue reports whether value parses for the declared type.
func validateSettingValue(t SettingValueType, value string) error {
	switch t {
	case SettingTypeString:
		return nil
	case SettingTypeBool:
		if _, err := strconv.ParseBool(strings.TrimSpace(value)); err != nil {
			return fmt.Errorf("%w: expected a boolean", ErrInvalidSettingValue)
		}
	case SettingTypeInt:
		if _, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64); err != nil {
			return fmt.Errorf("%w: expected an integer", ErrInvalidSettingValue)
		}
	case SettingTypeFloat:
		if _, err := strconv.ParseFloat(strings.TrimSpace(value), 64); err != nil {
			return fmt.Errorf("%w: expected a number", ErrInvalidSettingValue)
		}
	default:
		return fmt.Errorf("%w: unknown type %q", ErrInvalidSettingValue, t)
	}
	return nil
}

// defaultSettingDefinitions is the initial CE whitelist of dynamic system
// settings. Declaring a key here only makes it readable and writable through
// the settings API and typed accessors; it does NOT by itself change server
// behavior. Feature code consumes a setting via SettingsService where it is
// wired up, which is a separate, intentional change. Keep keys generic and
// self-host friendly.
func defaultSettingDefinitions() []SettingDefinition {
	return []SettingDefinition{
		{
			Key:         SettingKeySignupsEnabled,
			Type:        SettingTypeBool,
			Default:     "true",
			Description: "Whether new account self-registration is allowed.",
			Group:       SettingGroupAuth,
		},
		{
			Key:         SettingKeyMaintenanceMode,
			Type:        SettingTypeBool,
			Default:     "false",
			Description: "When true, readiness fails and ordinary API write requests are rejected while health and admin routes remain available.",
			Group:       SettingGroupOperations,
		},
		{
			Key:         SettingKeyAnnouncement,
			Type:        SettingTypeString,
			Default:     "",
			Description: "Operator broadcast message surfaced to clients.",
			Group:       SettingGroupNotifications,
		},
		{
			Key:         SettingKeyPasskeyEnabled,
			Type:        SettingTypeBool,
			Default:     "false",
			Description: "Whether passkey/WebAuthn registration, login, and step-up verification are enabled.",
			Group:       SettingGroupAuth,
		},
		{
			Key:         SettingKeyPasskeyOrigins,
			Type:        SettingTypeString,
			Default:     "",
			Description: "Comma-separated HTTPS origins allowed for passkey/WebAuthn ceremonies. When blank, the request origin is used for localhost only.",
			Group:       SettingGroupSecurity,
		},
		{
			Key:         SettingKeyPasskeyRPID,
			Type:        SettingTypeString,
			Default:     "",
			Description: "Relying party ID for passkey/WebAuthn ceremonies. Defaults to the host of the configured origin.",
			Group:       SettingGroupSecurity,
		},
		{
			Key:         SettingKeyPasskeyRPName,
			Type:        SettingTypeString,
			Default:     "HistorySync",
			Description: "Relying party display name shown by authenticators during passkey/WebAuthn ceremonies.",
			Group:       SettingGroupSecurity,
		},
	}
}
