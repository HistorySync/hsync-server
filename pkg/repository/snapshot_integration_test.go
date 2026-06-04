//go:build integration

package repository

import (
	"testing"
	"time"

	"github.com/historysync/hsync-server/pkg/model"
)

func TestSnapshotCreateAndGetLatest(t *testing.T) {
	repos := setupTest(t)
	ctx := testContext(t)

	u := seedUser(t, repos, "snap@example.com")

	var last string
	for _, id := range []string{"snap-1", "snap-2", "snap-3"} {
		s := &model.SnapshotMeta{SnapshotID: id, UserID: u.ID, BaseHLC: 1, SizeBytes: 10, CipherID: 1}
		if err := repos.Snapshots.Create(ctx, s); err != nil {
			t.Fatalf("create %s: %v", id, err)
		}
		last = id
		// Ensure strictly increasing created_at so ordering is deterministic.
		time.Sleep(3 * time.Millisecond)
	}

	latest, err := repos.Snapshots.GetLatest(ctx, u.ID)
	if err != nil {
		t.Fatalf("GetLatest: %v", err)
	}
	if latest == nil || latest.SnapshotID != last {
		t.Fatalf("GetLatest = %+v, want %s", latest, last)
	}

	byID, err := repos.Snapshots.GetByID(ctx, u.ID, "snap-2")
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if byID == nil || byID.SnapshotID != "snap-2" {
		t.Fatalf("GetByID = %+v, want snap-2", byID)
	}

	missing, err := repos.Snapshots.GetByID(ctx, u.ID, "nope")
	if err != nil {
		t.Fatalf("GetByID(missing): %v", err)
	}
	if missing != nil {
		t.Fatalf("GetByID(missing) = %+v, want nil", missing)
	}
}

func TestSnapshotSoftDelete(t *testing.T) {
	repos := setupTest(t)
	ctx := testContext(t)

	u := seedUser(t, repos, "snapdel@example.com")
	s := &model.SnapshotMeta{SnapshotID: "d1", UserID: u.ID, BaseHLC: 1, SizeBytes: 10, CipherID: 1}
	if err := repos.Snapshots.Create(ctx, s); err != nil {
		t.Fatalf("Create: %v", err)
	}

	deleted, err := repos.Snapshots.SoftDelete(ctx, u.ID, "d1")
	if err != nil {
		t.Fatalf("SoftDelete: %v", err)
	}
	if deleted == nil || deleted.DeletedAt == nil {
		t.Fatalf("SoftDelete returned %+v, want DeletedAt set", deleted)
	}

	if got, _ := repos.Snapshots.GetByID(ctx, u.ID, "d1"); got != nil {
		t.Fatalf("GetByID after delete = %+v, want nil", got)
	}
	count, err := repos.Snapshots.CountByUser(ctx, u.ID)
	if err != nil {
		t.Fatalf("CountByUser: %v", err)
	}
	if count != 0 {
		t.Fatalf("CountByUser = %d, want 0", count)
	}

	if _, err := repos.Snapshots.SoftDelete(ctx, u.ID, "d1"); err == nil {
		t.Fatal("SoftDelete of already-deleted snapshot succeeded, want error")
	}
}

// TestSnapshotPruneOldest exercises the CTE that keeps the newest N snapshots
// and soft-deletes the rest.
func TestSnapshotPruneOldest(t *testing.T) {
	repos := setupTest(t)
	ctx := testContext(t)

	u := seedUser(t, repos, "prune@example.com")

	ids := []string{"p1", "p2", "p3", "p4", "p5"} // created oldest -> newest
	for _, id := range ids {
		s := &model.SnapshotMeta{SnapshotID: id, UserID: u.ID, BaseHLC: 1, SizeBytes: 10, CipherID: 1}
		if err := repos.Snapshots.Create(ctx, s); err != nil {
			t.Fatalf("create %s: %v", id, err)
		}
		time.Sleep(3 * time.Millisecond)
	}

	pruned, err := repos.Snapshots.PruneOldest(ctx, u.ID, 2)
	if err != nil {
		t.Fatalf("PruneOldest: %v", err)
	}
	if len(pruned) != 3 {
		t.Fatalf("pruned len = %d, want 3", len(pruned))
	}
	prunedIDs := map[string]bool{}
	for _, s := range pruned {
		if s.DeletedAt == nil {
			t.Fatalf("pruned snapshot %s has nil DeletedAt", s.SnapshotID)
		}
		prunedIDs[s.SnapshotID] = true
	}
	for _, id := range []string{"p1", "p2", "p3"} {
		if !prunedIDs[id] {
			t.Fatalf("expected %s to be pruned; pruned set = %v", id, prunedIDs)
		}
	}

	count, err := repos.Snapshots.CountByUser(ctx, u.ID)
	if err != nil {
		t.Fatalf("CountByUser: %v", err)
	}
	if count != 2 {
		t.Fatalf("CountByUser after prune = %d, want 2", count)
	}

	// The two newest survive; an oldest is gone.
	if got, _ := repos.Snapshots.GetByID(ctx, u.ID, "p5"); got == nil {
		t.Fatal("newest snapshot p5 was pruned, want kept")
	}
	if got, _ := repos.Snapshots.GetByID(ctx, u.ID, "p1"); got != nil {
		t.Fatal("oldest snapshot p1 still readable, want pruned")
	}
}

func TestSnapshotPruneOldestKeepsAllWhenUnderLimit(t *testing.T) {
	repos := setupTest(t)
	ctx := testContext(t)

	u := seedUser(t, repos, "prunenoop@example.com")
	for _, id := range []string{"k1", "k2"} {
		s := &model.SnapshotMeta{SnapshotID: id, UserID: u.ID, BaseHLC: 1, SizeBytes: 10, CipherID: 1}
		if err := repos.Snapshots.Create(ctx, s); err != nil {
			t.Fatalf("create %s: %v", id, err)
		}
	}

	pruned, err := repos.Snapshots.PruneOldest(ctx, u.ID, 5)
	if err != nil {
		t.Fatalf("PruneOldest: %v", err)
	}
	if len(pruned) != 0 {
		t.Fatalf("pruned len = %d, want 0", len(pruned))
	}
	count, err := repos.Snapshots.CountByUser(ctx, u.ID)
	if err != nil {
		t.Fatalf("CountByUser: %v", err)
	}
	if count != 2 {
		t.Fatalf("CountByUser = %d, want 2", count)
	}
}
