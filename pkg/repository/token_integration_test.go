//go:build integration

package repository

import (
	"testing"
	"time"
)

func TestRefreshTokenLifecycle(t *testing.T) {
	repos := setupTest(t)
	ctx := testContext(t)

	u := seedUser(t, repos, "rt@example.com")
	hash := []byte("refresh-token-hash-1")

	if err := repos.RefreshTokens.SaveRefreshToken(ctx, u.ID, hash, "test-device", time.Now().Add(time.Hour)); err != nil {
		t.Fatalf("SaveRefreshToken: %v", err)
	}

	valid, err := repos.RefreshTokens.IsTokenValid(ctx, hash)
	if err != nil {
		t.Fatalf("IsTokenValid: %v", err)
	}
	if !valid {
		t.Fatal("IsTokenValid = false, want true")
	}

	owner, err := repos.RefreshTokens.GetUserIDByTokenHash(ctx, hash)
	if err != nil {
		t.Fatalf("GetUserIDByTokenHash: %v", err)
	}
	if owner == nil || *owner != u.ID {
		t.Fatalf("GetUserIDByTokenHash = %v, want %s", owner, u.ID)
	}

	if err := repos.RefreshTokens.RevokeRefreshToken(ctx, hash); err != nil {
		t.Fatalf("RevokeRefreshToken: %v", err)
	}

	valid, err = repos.RefreshTokens.IsTokenValid(ctx, hash)
	if err != nil {
		t.Fatalf("IsTokenValid after revoke: %v", err)
	}
	if valid {
		t.Fatal("IsTokenValid after revoke = true, want false")
	}
	owner, err = repos.RefreshTokens.GetUserIDByTokenHash(ctx, hash)
	if err != nil {
		t.Fatalf("GetUserIDByTokenHash after revoke: %v", err)
	}
	if owner != nil {
		t.Fatalf("GetUserIDByTokenHash after revoke = %v, want nil", owner)
	}
}

func TestRefreshTokenExpired(t *testing.T) {
	repos := setupTest(t)
	ctx := testContext(t)

	u := seedUser(t, repos, "rtexp@example.com")
	hash := []byte("expired-refresh")

	if err := repos.RefreshTokens.SaveRefreshToken(ctx, u.ID, hash, "test", time.Now().Add(-time.Hour)); err != nil {
		t.Fatalf("SaveRefreshToken: %v", err)
	}

	valid, err := repos.RefreshTokens.IsTokenValid(ctx, hash)
	if err != nil {
		t.Fatalf("IsTokenValid: %v", err)
	}
	if valid {
		t.Fatal("expired token reported valid, want invalid")
	}
}

func TestRevokeAllUserTokens(t *testing.T) {
	repos := setupTest(t)
	ctx := testContext(t)

	u := seedUser(t, repos, "rtall@example.com")
	h1 := []byte("token-a")
	h2 := []byte("token-b")
	for _, h := range [][]byte{h1, h2} {
		if err := repos.RefreshTokens.SaveRefreshToken(ctx, u.ID, h, "test", time.Now().Add(time.Hour)); err != nil {
			t.Fatalf("SaveRefreshToken: %v", err)
		}
	}

	if err := repos.RefreshTokens.RevokeAllUserTokens(ctx, u.ID); err != nil {
		t.Fatalf("RevokeAllUserTokens: %v", err)
	}

	for _, h := range [][]byte{h1, h2} {
		valid, err := repos.RefreshTokens.IsTokenValid(ctx, h)
		if err != nil {
			t.Fatalf("IsTokenValid: %v", err)
		}
		if valid {
			t.Fatal("token still valid after RevokeAllUserTokens, want invalid")
		}
	}
}

// TestEmailVerificationFlow exercises the email_verifications table introduced by
// migration 002 (previously created only by the Enterprise migrations).
func TestEmailVerificationFlow(t *testing.T) {
	repos := setupTest(t)
	ctx := testContext(t)

	u := seedUser(t, repos, "ev@example.com")
	hash := []byte("email-verify-hash")

	if err := repos.EmailVerifications.Save(ctx, u.ID, hash, time.Now().Add(time.Hour)); err != nil {
		t.Fatalf("Save: %v", err)
	}

	owner, err := repos.EmailVerifications.GetUserIDByToken(ctx, hash)
	if err != nil {
		t.Fatalf("GetUserIDByToken: %v", err)
	}
	if owner == nil || *owner != u.ID {
		t.Fatalf("GetUserIDByToken = %v, want %s", owner, u.ID)
	}

	unknown, err := repos.EmailVerifications.GetUserIDByToken(ctx, []byte("nope"))
	if err != nil {
		t.Fatalf("GetUserIDByToken(unknown): %v", err)
	}
	if unknown != nil {
		t.Fatalf("GetUserIDByToken(unknown) = %v, want nil", unknown)
	}

	if err := repos.EmailVerifications.DeleteByUser(ctx, u.ID); err != nil {
		t.Fatalf("DeleteByUser: %v", err)
	}
	owner, err = repos.EmailVerifications.GetUserIDByToken(ctx, hash)
	if err != nil {
		t.Fatalf("GetUserIDByToken after delete: %v", err)
	}
	if owner != nil {
		t.Fatalf("GetUserIDByToken after delete = %v, want nil", owner)
	}
}

func TestEmailVerificationExpired(t *testing.T) {
	repos := setupTest(t)
	ctx := testContext(t)

	u := seedUser(t, repos, "evexp@example.com")
	hash := []byte("expired-verify")
	if err := repos.EmailVerifications.Save(ctx, u.ID, hash, time.Now().Add(-time.Minute)); err != nil {
		t.Fatalf("Save: %v", err)
	}

	owner, err := repos.EmailVerifications.GetUserIDByToken(ctx, hash)
	if err != nil {
		t.Fatalf("GetUserIDByToken: %v", err)
	}
	if owner != nil {
		t.Fatalf("expired verification token resolved to %v, want nil", owner)
	}
}

// TestPasswordResetFlow exercises the password_resets table introduced by
// migration 002, including the used_at single-use guard.
func TestPasswordResetFlow(t *testing.T) {
	repos := setupTest(t)
	ctx := testContext(t)

	u := seedUser(t, repos, "pr@example.com")
	hash := []byte("password-reset-hash")

	if err := repos.PasswordResets.Save(ctx, u.ID, hash, time.Now().Add(time.Hour)); err != nil {
		t.Fatalf("Save: %v", err)
	}

	owner, err := repos.PasswordResets.GetUserIDByToken(ctx, hash)
	if err != nil {
		t.Fatalf("GetUserIDByToken: %v", err)
	}
	if owner == nil || *owner != u.ID {
		t.Fatalf("GetUserIDByToken = %v, want %s", owner, u.ID)
	}

	// Once used, the token must no longer resolve.
	if err := repos.PasswordResets.MarkUsed(ctx, hash); err != nil {
		t.Fatalf("MarkUsed: %v", err)
	}
	owner, err = repos.PasswordResets.GetUserIDByToken(ctx, hash)
	if err != nil {
		t.Fatalf("GetUserIDByToken after MarkUsed: %v", err)
	}
	if owner != nil {
		t.Fatalf("GetUserIDByToken after MarkUsed = %v, want nil (single-use)", owner)
	}
}

func TestPasswordResetExpired(t *testing.T) {
	repos := setupTest(t)
	ctx := testContext(t)

	u := seedUser(t, repos, "prexp@example.com")
	hash := []byte("expired-reset")
	if err := repos.PasswordResets.Save(ctx, u.ID, hash, time.Now().Add(-time.Minute)); err != nil {
		t.Fatalf("Save: %v", err)
	}

	owner, err := repos.PasswordResets.GetUserIDByToken(ctx, hash)
	if err != nil {
		t.Fatalf("GetUserIDByToken: %v", err)
	}
	if owner != nil {
		t.Fatalf("expired reset token resolved to %v, want nil", owner)
	}
}
