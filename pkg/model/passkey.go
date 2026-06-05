package model

import (
	"encoding/base64"
	"encoding/json"
	"time"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"
	"github.com/google/uuid"
)

type PasskeyCredential struct {
	ID              uuid.UUID  `json:"id" db:"id"`
	UserID          uuid.UUID  `json:"user_id" db:"user_id"`
	Name            string     `json:"name" db:"name"`
	CredentialID    []byte     `json:"-" db:"credential_id"`
	PublicKey       []byte     `json:"-" db:"public_key"`
	AttestationType string     `json:"attestation_type" db:"attestation_type"`
	AAGUID          []byte     `json:"-" db:"aaguid"`
	SignCount       uint32     `json:"sign_count" db:"sign_count"`
	CloneWarning    bool       `json:"clone_warning" db:"clone_warning"`
	UserPresent     bool       `json:"user_present" db:"user_present"`
	UserVerified    bool       `json:"user_verified" db:"user_verified"`
	BackupEligible  bool       `json:"backup_eligible" db:"backup_eligible"`
	BackupState     bool       `json:"backup_state" db:"backup_state"`
	TransportsJSON  []byte     `json:"-" db:"transports"`
	Attachment      string     `json:"attachment" db:"attachment"`
	LastUsedAt      *time.Time `json:"last_used_at,omitempty" db:"last_used_at"`
	CreatedAt       time.Time  `json:"created_at" db:"created_at"`
	UpdatedAt       time.Time  `json:"updated_at" db:"updated_at"`
}

type PasskeyChallenge struct {
	ID          uuid.UUID  `json:"id" db:"id"`
	UserID      *uuid.UUID `json:"user_id,omitempty" db:"user_id"`
	Type        string     `json:"type" db:"type"`
	Challenge   string     `json:"challenge" db:"challenge"`
	SessionJSON []byte     `json:"-" db:"session_json"`
	ExpiresAt   time.Time  `json:"expires_at" db:"expires_at"`
	ConsumedAt  *time.Time `json:"consumed_at,omitempty" db:"consumed_at"`
	CreatedAt   time.Time  `json:"created_at" db:"created_at"`
}

type PasskeyCredentialView struct {
	ID             uuid.UUID  `json:"id"`
	Name           string     `json:"name"`
	Attachment     string     `json:"attachment,omitempty"`
	Transports     []string   `json:"transports,omitempty"`
	BackupEligible bool       `json:"backup_eligible"`
	BackupState    bool       `json:"backup_state"`
	LastUsedAt     *time.Time `json:"last_used_at,omitempty"`
	CreatedAt      time.Time  `json:"created_at"`
}

func NewPasskeyCredentialFromWebAuthn(userID uuid.UUID, name string, credential *webauthn.Credential) *PasskeyCredential {
	if credential == nil {
		return nil
	}
	p := &PasskeyCredential{
		UserID:          userID,
		Name:            name,
		CredentialID:    credential.ID,
		PublicKey:       credential.PublicKey,
		AttestationType: credential.AttestationType,
		AAGUID:          credential.Authenticator.AAGUID,
		SignCount:       credential.Authenticator.SignCount,
		CloneWarning:    credential.Authenticator.CloneWarning,
		UserPresent:     credential.Flags.UserPresent,
		UserVerified:    credential.Flags.UserVerified,
		BackupEligible:  credential.Flags.BackupEligible,
		BackupState:     credential.Flags.BackupState,
		Attachment:      string(credential.Authenticator.Attachment),
	}
	p.SetTransports(credential.Transport)
	return p
}

func (p *PasskeyCredential) ApplyValidatedCredential(credential *webauthn.Credential) {
	if p == nil || credential == nil {
		return
	}
	p.PublicKey = credential.PublicKey
	p.AttestationType = credential.AttestationType
	p.AAGUID = credential.Authenticator.AAGUID
	p.SignCount = credential.Authenticator.SignCount
	p.CloneWarning = credential.Authenticator.CloneWarning
	p.UserPresent = credential.Flags.UserPresent
	p.UserVerified = credential.Flags.UserVerified
	p.BackupEligible = credential.Flags.BackupEligible
	p.BackupState = credential.Flags.BackupState
	p.Attachment = string(credential.Authenticator.Attachment)
	p.SetTransports(credential.Transport)
}

func (p *PasskeyCredential) ToWebAuthnCredential() webauthn.Credential {
	if p == nil {
		return webauthn.Credential{}
	}
	return webauthn.Credential{
		ID:              p.CredentialID,
		PublicKey:       p.PublicKey,
		AttestationType: p.AttestationType,
		Transport:       p.TransportList(),
		Flags: webauthn.CredentialFlags{
			UserPresent:    p.UserPresent,
			UserVerified:   p.UserVerified,
			BackupEligible: p.BackupEligible,
			BackupState:    p.BackupState,
		},
		Authenticator: webauthn.Authenticator{
			AAGUID:       p.AAGUID,
			SignCount:    p.SignCount,
			CloneWarning: p.CloneWarning,
			Attachment:   protocol.AuthenticatorAttachment(p.Attachment),
		},
	}
}

func (p *PasskeyCredential) TransportList() []protocol.AuthenticatorTransport {
	if p == nil || len(p.TransportsJSON) == 0 {
		return nil
	}
	var raw []string
	if err := json.Unmarshal(p.TransportsJSON, &raw); err != nil {
		return nil
	}
	out := make([]protocol.AuthenticatorTransport, 0, len(raw))
	for _, item := range raw {
		out = append(out, protocol.AuthenticatorTransport(item))
	}
	return out
}

func (p *PasskeyCredential) SetTransports(transports []protocol.AuthenticatorTransport) {
	if p == nil {
		return
	}
	if len(transports) == 0 {
		p.TransportsJSON = nil
		return
	}
	raw := make([]string, 0, len(transports))
	for _, transport := range transports {
		raw = append(raw, string(transport))
	}
	p.TransportsJSON, _ = json.Marshal(raw)
}

func (p *PasskeyCredential) View() PasskeyCredentialView {
	view := PasskeyCredentialView{
		ID:             p.ID,
		Name:           p.Name,
		Attachment:     p.Attachment,
		BackupEligible: p.BackupEligible,
		BackupState:    p.BackupState,
		LastUsedAt:     p.LastUsedAt,
		CreatedAt:      p.CreatedAt,
	}
	for _, transport := range p.TransportList() {
		view.Transports = append(view.Transports, string(transport))
	}
	return view
}

func CredentialIDString(id []byte) string {
	return base64.RawURLEncoding.EncodeToString(id)
}
