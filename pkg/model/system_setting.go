package model

import "time"

// SystemSetting is one row of the database-driven dynamic system settings. It
// stores an override value for a code-declared, whitelisted key. ValueType and
// Description are persisted copies of the code definition so the table is
// self-describing when inspected directly; the code registry stays authoritative
// for the whitelist, defaults, and which values are sensitive.
//
// A missing row means "use the code default", so the table is never required
// for the server to boot. Sensitive values are never returned in plaintext over
// the API (see the settings service); the model itself carries the raw value
// for internal use only.
type SystemSetting struct {
	Key         string    `json:"key"         db:"key"`
	Value       string    `json:"value"       db:"value"`
	ValueType   string    `json:"value_type"  db:"value_type"`
	Description string    `json:"description" db:"description"`
	UpdatedAt   time.Time `json:"updated_at"  db:"updated_at"`
}
