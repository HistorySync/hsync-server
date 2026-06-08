package repository

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/historysync/hsync-server/pkg/model"
)

// PasskeyRepo manages WebAuthn credentials and one-time ceremony sessions.
type PasskeyRepo struct {
	pool *pgxpool.Pool
}

func (r *PasskeyRepo) CreateCredential(ctx context.Context, credential *model.PasskeyCredential) error {
	const q = `
		INSERT INTO passkey_credentials (
			user_id, name, credential_id, public_key, attestation_type, aaguid,
			sign_count, clone_warning, user_present, user_verified,
			backup_eligible, backup_state, transports, attachment
		)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, COALESCE(NULLIF($13, '')::jsonb, '[]'::jsonb), $14)
		RETURNING id, created_at, updated_at`
	return r.pool.QueryRow(ctx, q,
		credential.UserID,
		credential.Name,
		credential.CredentialID,
		credential.PublicKey,
		credential.AttestationType,
		credential.AAGUID,
		credential.SignCount,
		credential.CloneWarning,
		credential.UserPresent,
		credential.UserVerified,
		credential.BackupEligible,
		credential.BackupState,
		string(credential.TransportsJSON),
		credential.Attachment,
	).Scan(&credential.ID, &credential.CreatedAt, &credential.UpdatedAt)
}

func (r *PasskeyRepo) ListCredentialsByUser(ctx context.Context, userID uuid.UUID) ([]model.PasskeyCredential, error) {
	const q = `
		SELECT id, user_id, name, credential_id, public_key, attestation_type, aaguid,
		       sign_count, clone_warning, user_present, user_verified,
		       backup_eligible, backup_state, transports, attachment,
		       last_used_at, created_at, updated_at
		FROM passkey_credentials
		WHERE user_id = $1
		ORDER BY created_at DESC`
	rows, err := r.pool.Query(ctx, q, userID)
	if err != nil {
		return nil, fmt.Errorf("list passkey credentials: %w", err)
	}
	defer rows.Close()
	return scanPasskeyCredentials(rows)
}

func (r *PasskeyRepo) GetCredentialByIDForUser(ctx context.Context, userID, id uuid.UUID) (*model.PasskeyCredential, error) {
	const q = `
		SELECT id, user_id, name, credential_id, public_key, attestation_type, aaguid,
		       sign_count, clone_warning, user_present, user_verified,
		       backup_eligible, backup_state, transports, attachment,
		       last_used_at, created_at, updated_at
		FROM passkey_credentials
		WHERE user_id = $1 AND id = $2`
	credential := &model.PasskeyCredential{}
	err := scanPasskeyCredential(r.pool.QueryRow(ctx, q, userID, id), credential)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get passkey credential: %w", err)
	}
	return credential, nil
}

func (r *PasskeyRepo) GetCredentialByCredentialID(ctx context.Context, credentialID []byte) (*model.PasskeyCredential, error) {
	const q = `
		SELECT id, user_id, name, credential_id, public_key, attestation_type, aaguid,
		       sign_count, clone_warning, user_present, user_verified,
		       backup_eligible, backup_state, transports, attachment,
		       last_used_at, created_at, updated_at
		FROM passkey_credentials
		WHERE credential_id = $1`
	credential := &model.PasskeyCredential{}
	err := scanPasskeyCredential(r.pool.QueryRow(ctx, q, credentialID), credential)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get passkey credential by credential id: %w", err)
	}
	return credential, nil
}

func (r *PasskeyRepo) UpdateCredentialAfterUse(ctx context.Context, credential *model.PasskeyCredential, now time.Time) error {
	const q = `
		UPDATE passkey_credentials
		SET public_key = $3,
		    attestation_type = $4,
		    aaguid = $5,
		    sign_count = $6,
		    clone_warning = $7,
		    user_present = $8,
		    user_verified = $9,
		    backup_eligible = $10,
		    backup_state = $11,
		    transports = COALESCE(NULLIF($12, '')::jsonb, '[]'::jsonb),
		    attachment = $13,
		    last_used_at = $14,
		    updated_at = now()
		WHERE user_id = $1 AND id = $2`
	_, err := r.pool.Exec(ctx, q,
		credential.UserID,
		credential.ID,
		credential.PublicKey,
		credential.AttestationType,
		credential.AAGUID,
		credential.SignCount,
		credential.CloneWarning,
		credential.UserPresent,
		credential.UserVerified,
		credential.BackupEligible,
		credential.BackupState,
		string(credential.TransportsJSON),
		credential.Attachment,
		now,
	)
	if err != nil {
		return fmt.Errorf("update passkey credential: %w", err)
	}
	credential.LastUsedAt = &now
	return nil
}

func (r *PasskeyRepo) DeleteCredentialByUser(ctx context.Context, userID, id uuid.UUID) (bool, error) {
	tag, err := r.pool.Exec(ctx, `DELETE FROM passkey_credentials WHERE user_id = $1 AND id = $2`, userID, id)
	if err != nil {
		return false, fmt.Errorf("delete passkey credential: %w", err)
	}
	return tag.RowsAffected() == 1, nil
}

// DeleteCredentialsByUser removes every passkey credential for a user.
func (r *PasskeyRepo) DeleteCredentialsByUser(ctx context.Context, userID uuid.UUID) error {
	if _, err := r.pool.Exec(ctx, `DELETE FROM passkey_credentials WHERE user_id = $1`, userID); err != nil {
		return fmt.Errorf("delete passkey credentials: %w", err)
	}
	return nil
}

func (r *PasskeyRepo) SaveChallenge(ctx context.Context, challenge *model.PasskeyChallenge) error {
	const q = `
		INSERT INTO passkey_challenges (user_id, type, challenge, session_json, expires_at)
		VALUES ($1, $2, $3, $4, $5)
		RETURNING id, created_at`
	return r.pool.QueryRow(ctx, q,
		challenge.UserID,
		challenge.Type,
		challenge.Challenge,
		challenge.SessionJSON,
		challenge.ExpiresAt,
	).Scan(&challenge.ID, &challenge.CreatedAt)
}

func (r *PasskeyRepo) ConsumeChallenge(ctx context.Context, id uuid.UUID, challengeType string, userID *uuid.UUID, now time.Time) (*model.PasskeyChallenge, error) {
	const q = `
		UPDATE passkey_challenges
		SET consumed_at = $3
		WHERE id = $1
		  AND type = $2
		  AND consumed_at IS NULL
		  AND expires_at > $3
		  AND (
			($4::uuid IS NULL AND user_id IS NULL)
			OR user_id = $4
		  )
		RETURNING id, user_id, type, challenge, session_json, expires_at, consumed_at, created_at`
	challenge := &model.PasskeyChallenge{}
	err := r.pool.QueryRow(ctx, q, id, challengeType, now, userID).Scan(
		&challenge.ID,
		&challenge.UserID,
		&challenge.Type,
		&challenge.Challenge,
		&challenge.SessionJSON,
		&challenge.ExpiresAt,
		&challenge.ConsumedAt,
		&challenge.CreatedAt,
	)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("consume passkey challenge: %w", err)
	}
	return challenge, nil
}

// ExpireChallengesByUser invalidates any unconsumed passkey sessions for a user.
func (r *PasskeyRepo) ExpireChallengesByUser(ctx context.Context, userID uuid.UUID, now time.Time) error {
	const q = `
		UPDATE passkey_challenges
		SET consumed_at = $2, expires_at = LEAST(expires_at, $2)
		WHERE user_id = $1 AND consumed_at IS NULL`
	if _, err := r.pool.Exec(ctx, q, userID, now); err != nil {
		return fmt.Errorf("expire passkey challenges: %w", err)
	}
	return nil
}

func scanPasskeyCredentials(rows pgx.Rows) ([]model.PasskeyCredential, error) {
	var credentials []model.PasskeyCredential
	for rows.Next() {
		var credential model.PasskeyCredential
		if err := scanPasskeyCredential(rows, &credential); err != nil {
			return nil, fmt.Errorf("scan passkey credential: %w", err)
		}
		credentials = append(credentials, credential)
	}
	return credentials, rows.Err()
}

type passkeyCredentialScanner interface {
	Scan(dest ...any) error
}

func scanPasskeyCredential(row passkeyCredentialScanner, credential *model.PasskeyCredential) error {
	var signCount int64
	if err := row.Scan(
		&credential.ID,
		&credential.UserID,
		&credential.Name,
		&credential.CredentialID,
		&credential.PublicKey,
		&credential.AttestationType,
		&credential.AAGUID,
		&signCount,
		&credential.CloneWarning,
		&credential.UserPresent,
		&credential.UserVerified,
		&credential.BackupEligible,
		&credential.BackupState,
		&credential.TransportsJSON,
		&credential.Attachment,
		&credential.LastUsedAt,
		&credential.CreatedAt,
		&credential.UpdatedAt,
	); err != nil {
		return err
	}
	credential.SignCount = uint32(signCount)
	return nil
}
