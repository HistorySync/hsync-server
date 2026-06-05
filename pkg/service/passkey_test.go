package service

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/go-webauthn/webauthn/webauthn"
	"github.com/google/uuid"

	"github.com/historysync/hsync-server/pkg/model"
)

func TestResolvePasskeyOriginsRequiresHTTPSForConfiguredOrigins(t *testing.T) {
	req, _ := http.NewRequest("POST", "https://ignored.example/api", nil)

	if _, err := resolvePasskeyOrigins(req, "http://app.example.com"); !errors.Is(err, ErrPasskeyVerification) {
		t.Fatalf("resolvePasskeyOrigins(insecure) err = %v, want ErrPasskeyVerification", err)
	}
	origins, err := resolvePasskeyOrigins(req, "https://app.example.com, https://admin.example.com")
	if err != nil {
		t.Fatalf("resolvePasskeyOrigins(https): %v", err)
	}
	if len(origins) != 2 || origins[0] != "https://app.example.com" || origins[1] != "https://admin.example.com" {
		t.Fatalf("origins = %#v", origins)
	}
}

func TestResolvePasskeyOriginsAutoDetectsOnlyLocalhost(t *testing.T) {
	req, _ := http.NewRequest("POST", "http://localhost:3000/api", nil)
	req.Host = "localhost:3000"
	origins, err := resolvePasskeyOrigins(req, "")
	if err != nil {
		t.Fatalf("resolvePasskeyOrigins(localhost): %v", err)
	}
	if len(origins) != 1 || origins[0] != "http://localhost:3000" {
		t.Fatalf("origins = %#v", origins)
	}

	remote, _ := http.NewRequest("POST", "http://app.example.com/api", nil)
	remote.Host = "app.example.com"
	if _, err := resolvePasskeyOrigins(remote, ""); !errors.Is(err, ErrPasskeyVerification) {
		t.Fatalf("resolvePasskeyOrigins(remote) err = %v, want ErrPasskeyVerification", err)
	}
}

func TestResolvePasskeyRPIDMustMatchOrigins(t *testing.T) {
	origins := []string{"https://app.example.com", "https://admin.example.com"}
	if _, err := resolvePasskeyRPID("evil.example.com", origins); !errors.Is(err, ErrPasskeyVerification) {
		t.Fatalf("resolvePasskeyRPID(wrong) err = %v, want ErrPasskeyVerification", err)
	}
	rpID, err := resolvePasskeyRPID("example.com", origins)
	if err != nil {
		t.Fatalf("resolvePasskeyRPID(parent domain): %v", err)
	}
	if rpID != "example.com" {
		t.Fatalf("rpID = %q, want example.com", rpID)
	}
}

func TestPasskeyConsumeSessionRejectsWrongUserAndReplay(t *testing.T) {
	owner := uuid.New()
	other := uuid.New()
	challengeID := uuid.New()
	sessionJSON, _ := json.Marshal(&webauthn.SessionData{Challenge: "challenge", Expires: time.Now().Add(time.Minute)})
	store := &fakePasskeyChallengeStore{
		challenges: map[uuid.UUID]*model.PasskeyChallenge{
			challengeID: {
				ID:          challengeID,
				UserID:      &owner,
				Type:        passkeyChallengeRegistration,
				SessionJSON: sessionJSON,
				ExpiresAt:   time.Now().Add(time.Minute),
			},
		},
	}
	svc := &PasskeyService{passkeys: store, now: time.Now}

	if _, err := svc.consumeSession(context.Background(), challengeID, passkeyChallengeRegistration, &other); !errors.Is(err, ErrPasskeyChallenge) {
		t.Fatalf("consumeSession(wrong user) err = %v, want ErrPasskeyChallenge", err)
	}
	if _, err := svc.consumeSession(context.Background(), challengeID, passkeyChallengeRegistration, &owner); err != nil {
		t.Fatalf("consumeSession(first): %v", err)
	}
	if _, err := svc.consumeSession(context.Background(), challengeID, passkeyChallengeRegistration, &owner); !errors.Is(err, ErrPasskeyChallenge) {
		t.Fatalf("consumeSession(replay) err = %v, want ErrPasskeyChallenge", err)
	}
}

func TestPasskeyStepUpChallengeConsumeBranches(t *testing.T) {
	owner := uuid.New()
	challengeID := uuid.New()
	sessionJSON, _ := json.Marshal(&webauthn.SessionData{Challenge: "step-up-challenge", Expires: time.Now().Add(time.Minute)})
	store := &fakePasskeyChallengeStore{
		challenges: map[uuid.UUID]*model.PasskeyChallenge{
			challengeID: {
				ID:          challengeID,
				UserID:      &owner,
				Type:        passkeyChallengeStepUp,
				SessionJSON: sessionJSON,
				ExpiresAt:   time.Now().Add(time.Minute),
			},
		},
	}
	svc := &PasskeyService{passkeys: store, now: time.Now}

	if _, err := svc.consumeSession(context.Background(), uuid.New(), passkeyChallengeStepUp, &owner); !errors.Is(err, ErrPasskeyChallenge) {
		t.Fatalf("consumeSession(missing step-up) err = %v, want ErrPasskeyChallenge", err)
	}
	if _, err := svc.consumeSession(context.Background(), challengeID, passkeyChallengeStepUp, &owner); err != nil {
		t.Fatalf("consumeSession(valid step-up): %v", err)
	}
}

type fakePasskeyChallengeStore struct {
	challenges map[uuid.UUID]*model.PasskeyChallenge
}

func (f *fakePasskeyChallengeStore) CreateCredential(context.Context, *model.PasskeyCredential) error {
	return nil
}

func (f *fakePasskeyChallengeStore) ListCredentialsByUser(context.Context, uuid.UUID) ([]model.PasskeyCredential, error) {
	return nil, nil
}

func (f *fakePasskeyChallengeStore) GetCredentialByIDForUser(context.Context, uuid.UUID, uuid.UUID) (*model.PasskeyCredential, error) {
	return nil, nil
}

func (f *fakePasskeyChallengeStore) GetCredentialByCredentialID(context.Context, []byte) (*model.PasskeyCredential, error) {
	return nil, nil
}

func (f *fakePasskeyChallengeStore) UpdateCredentialAfterUse(context.Context, *model.PasskeyCredential, time.Time) error {
	return nil
}

func (f *fakePasskeyChallengeStore) DeleteCredentialByUser(context.Context, uuid.UUID, uuid.UUID) (bool, error) {
	return false, nil
}

func (f *fakePasskeyChallengeStore) SaveChallenge(context.Context, *model.PasskeyChallenge) error {
	return nil
}

func (f *fakePasskeyChallengeStore) ConsumeChallenge(_ context.Context, id uuid.UUID, challengeType string, userID *uuid.UUID, now time.Time) (*model.PasskeyChallenge, error) {
	challenge := f.challenges[id]
	if challenge == nil || challenge.Type != challengeType || challenge.ConsumedAt != nil || !challenge.ExpiresAt.After(now) {
		return nil, nil
	}
	if userID == nil {
		if challenge.UserID != nil {
			return nil, nil
		}
	} else if challenge.UserID == nil || *challenge.UserID != *userID {
		return nil, nil
	}
	consumedAt := now
	challenge.ConsumedAt = &consumedAt
	return challenge, nil
}
