//go:build integration

package repository

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/historysync/hsync-server/pkg/model"
)

func TestPasskeyChallengeExpiryAndReplay(t *testing.T) {
	repos := setupTest(t)
	ctx := testContext(t)
	u := seedUser(t, repos, "passkey-challenge@example.com")

	expired := &model.PasskeyChallenge{
		UserID:      &u.ID,
		Type:        "step_up",
		Challenge:   "expired-challenge",
		SessionJSON: []byte(`{"challenge":"expired-challenge"}`),
		ExpiresAt:   time.Now().Add(-time.Minute),
	}
	if err := repos.Passkeys.SaveChallenge(ctx, expired); err != nil {
		t.Fatalf("SaveChallenge(expired): %v", err)
	}
	if got, err := repos.Passkeys.ConsumeChallenge(ctx, expired.ID, "step_up", &u.ID, time.Now()); err != nil || got != nil {
		t.Fatalf("ConsumeChallenge(expired) = (%+v, %v), want (nil, nil)", got, err)
	}

	fresh := &model.PasskeyChallenge{
		UserID:      &u.ID,
		Type:        "step_up",
		Challenge:   "fresh-challenge",
		SessionJSON: []byte(`{"challenge":"fresh-challenge"}`),
		ExpiresAt:   time.Now().Add(time.Minute),
	}
	if err := repos.Passkeys.SaveChallenge(ctx, fresh); err != nil {
		t.Fatalf("SaveChallenge(fresh): %v", err)
	}
	if got, err := repos.Passkeys.ConsumeChallenge(ctx, fresh.ID, "step_up", &u.ID, time.Now()); err != nil || got == nil {
		t.Fatalf("ConsumeChallenge(first) = (%+v, %v), want row", got, err)
	}
	if got, err := repos.Passkeys.ConsumeChallenge(ctx, fresh.ID, "step_up", &u.ID, time.Now()); err != nil || got != nil {
		t.Fatalf("ConsumeChallenge(replay) = (%+v, %v), want (nil, nil)", got, err)
	}
}

func TestPasskeyCredentialDelete(t *testing.T) {
	repos := setupTest(t)
	ctx := testContext(t)
	u := seedUser(t, repos, "passkey-delete@example.com")

	credential := &model.PasskeyCredential{
		UserID:          u.ID,
		Name:            "Laptop",
		CredentialID:    []byte("credential-id"),
		PublicKey:       []byte("public-key"),
		AttestationType: "none",
		TransportsJSON:  []byte(`["internal"]`),
	}
	if err := repos.Passkeys.CreateCredential(ctx, credential); err != nil {
		t.Fatalf("CreateCredential: %v", err)
	}
	if credential.ID == uuid.Nil {
		t.Fatal("CreateCredential did not populate ID")
	}
	deleted, err := repos.Passkeys.DeleteCredentialByUser(ctx, u.ID, credential.ID)
	if err != nil {
		t.Fatalf("DeleteCredentialByUser: %v", err)
	}
	if !deleted {
		t.Fatal("DeleteCredentialByUser = false, want true")
	}
	got, err := repos.Passkeys.GetCredentialByIDForUser(ctx, u.ID, credential.ID)
	if err != nil {
		t.Fatalf("GetCredentialByIDForUser after delete: %v", err)
	}
	if got != nil {
		t.Fatalf("deleted credential = %+v, want nil", got)
	}
}

func TestPasskeyChallengeConsumeIsAtomic(t *testing.T) {
	repos := setupTest(t)
	ctx := testContext(t)
	u := seedUser(t, repos, "passkey-atomic@example.com")
	challenge := &model.PasskeyChallenge{
		UserID:      &u.ID,
		Type:        "login",
		Challenge:   "atomic-challenge",
		SessionJSON: []byte(`{"challenge":"atomic-challenge"}`),
		ExpiresAt:   time.Now().Add(time.Minute),
	}
	if err := repos.Passkeys.SaveChallenge(ctx, challenge); err != nil {
		t.Fatalf("SaveChallenge: %v", err)
	}

	results := make(chan bool, 2)
	for range 2 {
		go func() {
			got, err := repos.Passkeys.ConsumeChallenge(context.Background(), challenge.ID, "login", &u.ID, time.Now())
			results <- err == nil && got != nil
		}()
	}
	successes := 0
	for range 2 {
		if <-results {
			successes++
		}
	}
	if successes != 1 {
		t.Fatalf("atomic consume successes = %d, want 1", successes)
	}
}
