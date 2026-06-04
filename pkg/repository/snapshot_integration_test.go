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

// TestSnapshotListDeletedBefore verifies the retention purge query returns only
// snapshots soft-deleted before the cutoff, excluding live rows.
func TestSnapshotListDeletedBefore(t *testing.T) {
	repos := setupTest(t)
	ctx := testContext(t)

	u := seedUser(t, repos, "snaplistdel@example.com")

	ids := []string{"ld1", "ld2", "ld3", "live"}
	for _, id := range ids {
		s := &model.SnapshotMeta{SnapshotID: id, UserID: u.ID, BaseHLC: 1, SizeBytes: 100, CipherID: 1}
		if err := repos.Snapshots.Create(ctx, s); err != nil {
			t.Fatalf("create %s: %v", id, err)
		}
	}
	if _, err := repos.Snapshots.SoftDelete(ctx, u.ID, "ld1"); err != nil {
		t.Fatalf("SoftDelete ld1: %v", err)
	}
	if _, err := repos.Snapshots.SoftDelete(ctx, u.ID, "ld2"); err != nil {
		t.Fatalf("SoftDelete ld2: %v", err)
	}
	if _, err := repos.Snapshots.SoftDelete(ctx, u.ID, "ld3"); err != nil {
		t.Fatalf("SoftDelete ld3: %v", err)
	}

	// Future cutoff: all three soft-deleted snapshots qualify.
	deleted, err := repos.Snapshots.ListDeletedBefore(ctx, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatalf("ListDeletedBefore(future): %v", err)
	}
	got := map[string]int64{}
	for _, s := range deleted {
		got[s.SnapshotID] = s.SizeBytes
	}
	if len(got) != 3 || got["ld1"] != 100 || got["ld2"] != 100 || got["ld3"] != 100 {
		t.Fatalf("ListDeletedBefore(future) = %+v, want ld1,ld2,ld3=100", got)
	}
	if _, ok := got["live"]; ok {
		t.Fatal("ListDeletedBefore returned a live snapshot")
	}

	// Past cutoff: nothing qualifies.
	none, err := repos.Snapshots.ListDeletedBefore(ctx, time.Now().Add(-time.Hour))
	if err != nil {
		t.Fatalf("ListDeletedBefore(past): %v", err)
	}
	if len(none) != 0 {
		t.Fatalf("ListDeletedBefore(past) = %+v, want empty", none)
	}

	// CountDeletedBefore should agree with ListDeletedBefore.
	count, bytes, err := repos.Snapshots.CountDeletedBefore(ctx, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatalf("CountDeletedBefore(future): %v", err)
	}
	if count != 3 || bytes != 300 {
		t.Fatalf("CountDeletedBefore(future) = (%d, %d), want (3, 300)", count, bytes)
	}
}

// TestSnapshotHardDelete verifies physical removal.
func TestSnapshotHardDelete(t *testing.T) {
	repos := setupTest(t)
	ctx := testContext(t)

	u := seedUser(t, repos, "snaphard@example.com")
	s := &model.SnapshotMeta{SnapshotID: "hd1", UserID: u.ID, BaseHLC: 1, SizeBytes: 50, CipherID: 1}
	if err := repos.Snapshots.Create(ctx, s); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := repos.Snapshots.SoftDelete(ctx, u.ID, "hd1"); err != nil {
		t.Fatalf("SoftDelete: %v", err)
	}
	if err := repos.Snapshots.HardDelete(ctx, u.ID, "hd1"); err != nil {
		t.Fatalf("HardDelete: %v", err)
	}

	// After hard delete, the row must not appear in ListDeletedBefore.
	deleted, err := repos.Snapshots.ListDeletedBefore(ctx, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatalf("ListDeletedBefore after hard delete: %v", err)
	}
	for _, d := range deleted {
		if d.SnapshotID == "hd1" {
			t.Fatal("hard-deleted snapshot still returned by ListDeletedBefore")
		}
	}
}
