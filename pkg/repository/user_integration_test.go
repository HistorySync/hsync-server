//go:build integration

package repository

import (
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/historysync/hsync-server/pkg/model"
)

func TestUserCreateAndGet(t *testing.T) {
	repos := setupTest(t)
	ctx := testContext(t)

	u := seedUser(t, repos, "alice@example.com")
	if u.ID == uuid.Nil {
		t.Fatal("Create did not populate ID")
	}
	if u.CreatedAt.IsZero() || u.UpdatedAt.IsZero() {
		t.Fatal("Create did not populate timestamps")
	}

	got, err := repos.Users.GetByID(ctx, u.ID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if got == nil || got.Email != "alice@example.com" {
		t.Fatalf("GetByID = %+v, want email alice@example.com", got)
	}

	byEmail, err := repos.Users.GetByEmail(ctx, "alice@example.com")
	if err != nil {
		t.Fatalf("GetByEmail: %v", err)
	}
	if byEmail == nil || byEmail.ID != u.ID {
		t.Fatalf("GetByEmail = %+v, want ID %s", byEmail, u.ID)
	}

	missing, err := repos.Users.GetByID(ctx, uuid.New())
	if err != nil {
		t.Fatalf("GetByID(missing): %v", err)
	}
	if missing != nil {
		t.Fatalf("GetByID(missing) = %+v, want nil", missing)
	}
}

func TestUserCreateDuplicateEmailRejected(t *testing.T) {
	repos := setupTest(t)
	ctx := testContext(t)

	seedUser(t, repos, "dup@example.com")

	dup := &model.User{
		Email:        "dup@example.com",
		PasswordHash: "x",
		Tier:         model.TierFree,
		Status:       model.StatusActive,
	}
	if err := repos.Users.Create(ctx, dup); err == nil {
		t.Fatal("Create with duplicate email succeeded, want unique-violation error")
	}
}

func TestUserEmailUniquenessIsCaseInsensitive(t *testing.T) {
	repos := setupTest(t)
	ctx := testContext(t)

	u := seedUser(t, repos, "Casey@Example.COM")
	if u.Email != "casey@example.com" {
		t.Fatalf("stored email = %q, want casey@example.com", u.Email)
	}

	byEmail, err := repos.Users.GetByEmail(ctx, " CASEY@example.com ")
	if err != nil {
		t.Fatalf("GetByEmail: %v", err)
	}
	if byEmail == nil || byEmail.ID != u.ID {
		t.Fatalf("GetByEmail = %+v, want ID %s", byEmail, u.ID)
	}

	dup := &model.User{
		Email:        "casey@example.com",
		PasswordHash: "x",
		Tier:         model.TierFree,
		Status:       model.StatusActive,
	}
	if err := repos.Users.Create(ctx, dup); err == nil {
		t.Fatal("Create with case-variant duplicate email succeeded, want unique-violation error")
	}
}

func TestUserSoftDeleteHidesFromReads(t *testing.T) {
	repos := setupTest(t)
	ctx := testContext(t)

	u := seedUser(t, repos, "gone@example.com")

	if err := repos.Users.SoftDelete(ctx, u.ID); err != nil {
		t.Fatalf("SoftDelete: %v", err)
	}

	got, err := repos.Users.GetByID(ctx, u.ID)
	if err != nil {
		t.Fatalf("GetByID after delete: %v", err)
	}
	if got != nil {
		t.Fatalf("GetByID after soft delete = %+v, want nil", got)
	}

	count, err := repos.Users.Count(ctx)
	if err != nil {
		t.Fatalf("Count: %v", err)
	}
	if count != 0 {
		t.Fatalf("Count after soft delete = %d, want 0", count)
	}
}

func TestUserVerifyEmail(t *testing.T) {
	repos := setupTest(t)
	ctx := testContext(t)

	u := seedUser(t, repos, "verify@example.com")
	if u.EmailVerified {
		t.Fatal("new user should not be email-verified")
	}

	if err := repos.Users.VerifyEmail(ctx, u.ID); err != nil {
		t.Fatalf("VerifyEmail: %v", err)
	}

	got, err := repos.Users.GetByID(ctx, u.ID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if got == nil || !got.EmailVerified {
		t.Fatalf("EmailVerified = %v, want true", got)
	}
}

// TestUserUpdatedAtTrigger verifies the trg_users_updated_at trigger bumps
// updated_at on every UPDATE.
func TestUserUpdatedAtTrigger(t *testing.T) {
	repos := setupTest(t)
	ctx := testContext(t)

	u := seedUser(t, repos, "trigger@example.com")
	original := u.UpdatedAt

	// now() is the transaction timestamp; a brief pause guarantees the next
	// transaction observes a strictly later time.
	time.Sleep(5 * time.Millisecond)

	if err := repos.Users.UpdatePassword(ctx, u.ID, "new-hash"); err != nil {
		t.Fatalf("UpdatePassword: %v", err)
	}

	got, err := repos.Users.GetByID(ctx, u.ID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if !got.UpdatedAt.After(original) {
		t.Fatalf("updated_at = %s, want after %s (trigger did not fire)", got.UpdatedAt, original)
	}
}

func TestUserCountByStatus(t *testing.T) {
	repos := setupTest(t)
	ctx := testContext(t)

	mustCreate := func(email string, status model.UserStatus) {
		u := &model.User{
			Email:        email,
			PasswordHash: "x",
			Tier:         model.TierFree,
			Status:       status,
		}
		if err := repos.Users.Create(ctx, u); err != nil {
			t.Fatalf("create %s: %v", email, err)
		}
	}

	mustCreate("a1@example.com", model.StatusActive)
	mustCreate("a2@example.com", model.StatusActive)
	mustCreate("s1@example.com", model.StatusSuspended)

	// A soft-deleted user must be excluded from the counts.
	doomed := seedUser(t, repos, "d1@example.com")
	if err := repos.Users.SoftDelete(ctx, doomed.ID); err != nil {
		t.Fatalf("SoftDelete: %v", err)
	}

	counts, err := repos.Users.CountByStatus(ctx)
	if err != nil {
		t.Fatalf("CountByStatus: %v", err)
	}
	if counts[model.StatusActive] != 2 {
		t.Fatalf("active count = %d, want 2", counts[model.StatusActive])
	}
	if counts[model.StatusSuspended] != 1 {
		t.Fatalf("suspended count = %d, want 1", counts[model.StatusSuspended])
	}
	if _, ok := counts[model.StatusDeleted]; ok {
		t.Fatalf("deleted users should be excluded, got %d", counts[model.StatusDeleted])
	}
}

func TestUserListPagination(t *testing.T) {
	repos := setupTest(t)
	ctx := testContext(t)

	for _, email := range []string{"u1@e.com", "u2@e.com", "u3@e.com"} {
		seedUser(t, repos, email)
	}

	seen := map[uuid.UUID]bool{}
	page1, err := repos.Users.List(ctx, 2, 0)
	if err != nil {
		t.Fatalf("List page1: %v", err)
	}
	if len(page1) != 2 {
		t.Fatalf("page1 len = %d, want 2", len(page1))
	}
	for _, u := range page1 {
		seen[u.ID] = true
	}

	page2, err := repos.Users.List(ctx, 2, 2)
	if err != nil {
		t.Fatalf("List page2: %v", err)
	}
	if len(page2) != 1 {
		t.Fatalf("page2 len = %d, want 1", len(page2))
	}
	for _, u := range page2 {
		if seen[u.ID] {
			t.Fatalf("user %s appeared on both pages", u.ID)
		}
		seen[u.ID] = true
	}
	if len(seen) != 3 {
		t.Fatalf("distinct users across pages = %d, want 3", len(seen))
	}
}
