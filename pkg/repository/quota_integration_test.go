//go:build integration

package repository

import (
	"testing"

	"github.com/google/uuid"

	"github.com/historysync/hsync-server/pkg/model"
)

func TestQuotaGetUsageDefaultsToZero(t *testing.T) {
	repos := setupTest(t)
	ctx := testContext(t)

	u := seedUser(t, repos, "usage@example.com")

	// No storage_usage row yet: GetUsage returns a zero-valued usage, not an error.
	usage, err := repos.Quota.GetUsage(ctx, u.ID)
	if err != nil {
		t.Fatalf("GetUsage: %v", err)
	}
	if usage == nil || usage.UserID != u.ID || usage.TotalBytes != 0 || usage.BundleCount != 0 || usage.SnapCount != 0 {
		t.Fatalf("GetUsage default = %+v, want zeroed usage for user", usage)
	}
}

func TestQuotaUpsertAndGetLimits(t *testing.T) {
	repos := setupTest(t)
	ctx := testContext(t)

	u := seedUser(t, repos, "limits@example.com")

	// No row yet: GetLimits surfaces an error.
	if _, err := repos.Quota.GetLimits(ctx, u.ID); err == nil {
		t.Fatal("GetLimits with no row succeeded, want error")
	}

	a := &model.QuotaLimits{
		UserID:              u.ID,
		StorageLimitBytes:   1000,
		MaxDevices:          2,
		MaxBundleSize:       100,
		MaxSnapshots:        3,
		MaxRPM:              50,
		BundleRetentionDays: 30,
	}
	if err := repos.Quota.UpsertLimits(ctx, a); err != nil {
		t.Fatalf("UpsertLimits(insert): %v", err)
	}
	got, err := repos.Quota.GetLimits(ctx, u.ID)
	if err != nil {
		t.Fatalf("GetLimits after insert: %v", err)
	}
	if got.StorageLimitBytes != 1000 || got.MaxDevices != 2 || got.MaxSnapshots != 3 {
		t.Fatalf("GetLimits = %+v, want storage 1000 / devices 2 / snapshots 3", got)
	}

	// Upsert again for the same user must update in place (ON CONFLICT path).
	b := &model.QuotaLimits{
		UserID:              u.ID,
		StorageLimitBytes:   9999,
		MaxDevices:          7,
		MaxBundleSize:       200,
		MaxSnapshots:        8,
		MaxRPM:              60,
		BundleRetentionDays: 90,
	}
	if err := repos.Quota.UpsertLimits(ctx, b); err != nil {
		t.Fatalf("UpsertLimits(update): %v", err)
	}
	got, err = repos.Quota.GetLimits(ctx, u.ID)
	if err != nil {
		t.Fatalf("GetLimits after update: %v", err)
	}
	if got.StorageLimitBytes != 9999 || got.MaxDevices != 7 || got.MaxSnapshots != 8 {
		t.Fatalf("GetLimits after update = %+v, want storage 9999 / devices 7 / snapshots 8", got)
	}
}

func TestQuotaAddRemoveBundleUsage(t *testing.T) {
	repos := setupTest(t)
	ctx := testContext(t)

	u := seedUser(t, repos, "addbundle@example.com")

	if err := repos.Quota.AddBundleUsage(ctx, u.ID, 100); err != nil {
		t.Fatalf("AddBundleUsage: %v", err)
	}
	if err := repos.Quota.AddBundleUsage(ctx, u.ID, 50); err != nil {
		t.Fatalf("AddBundleUsage: %v", err)
	}
	assertUsage(t, repos, u.ID, 150, 2, 0)

	if err := repos.Quota.RemoveBundleUsage(ctx, u.ID, 100); err != nil {
		t.Fatalf("RemoveBundleUsage: %v", err)
	}
	assertUsage(t, repos, u.ID, 50, 1, 0)

	// Removing more than present clamps to zero (GREATEST guard).
	if err := repos.Quota.RemoveBundleUsage(ctx, u.ID, 100); err != nil {
		t.Fatalf("RemoveBundleUsage(over): %v", err)
	}
	assertUsage(t, repos, u.ID, 0, 0, 0)
}

func TestQuotaAddRemoveSnapshotUsage(t *testing.T) {
	repos := setupTest(t)
	ctx := testContext(t)

	u := seedUser(t, repos, "addsnap@example.com")

	if err := repos.Quota.AddSnapshotUsage(ctx, u.ID, 300); err != nil {
		t.Fatalf("AddSnapshotUsage: %v", err)
	}
	assertUsage(t, repos, u.ID, 300, 0, 1)

	if err := repos.Quota.RemoveSnapshotUsage(ctx, u.ID, 300); err != nil {
		t.Fatalf("RemoveSnapshotUsage: %v", err)
	}
	assertUsage(t, repos, u.ID, 0, 0, 0)
}

// TestTryAddBundleUsageBoundary verifies the conditional UPDATE that enforces
// the storage limit: it must succeed up to and including the limit and reject
// the first byte that would exceed it, leaving usage unchanged on rejection.
func TestTryAddBundleUsageBoundary(t *testing.T) {
	repos := setupTest(t)
	ctx := testContext(t)

	u := seedUser(t, repos, "trybundle@example.com")
	const limit = 1000

	ok, err := repos.Quota.TryAddBundleUsage(ctx, u.ID, 600, limit)
	if err != nil {
		t.Fatalf("TryAddBundleUsage(600): %v", err)
	}
	if !ok {
		t.Fatal("TryAddBundleUsage(600, limit 1000) = false, want true")
	}

	// Lands exactly on the limit: must be allowed (<= is inclusive).
	ok, err = repos.Quota.TryAddBundleUsage(ctx, u.ID, 400, limit)
	if err != nil {
		t.Fatalf("TryAddBundleUsage(400): %v", err)
	}
	if !ok {
		t.Fatal("TryAddBundleUsage reaching exactly the limit = false, want true")
	}
	assertUsage(t, repos, u.ID, 1000, 2, 0)

	// One byte over: must be rejected and usage must not change.
	ok, err = repos.Quota.TryAddBundleUsage(ctx, u.ID, 1, limit)
	if err != nil {
		t.Fatalf("TryAddBundleUsage(1 over): %v", err)
	}
	if ok {
		t.Fatal("TryAddBundleUsage exceeding the limit = true, want false")
	}
	assertUsage(t, repos, u.ID, 1000, 2, 0)
}

func TestTryAddSnapshotUsageBoundary(t *testing.T) {
	repos := setupTest(t)
	ctx := testContext(t)

	u := seedUser(t, repos, "trysnap@example.com")
	const limit = 500

	ok, err := repos.Quota.TryAddSnapshotUsage(ctx, u.ID, 500, limit)
	if err != nil {
		t.Fatalf("TryAddSnapshotUsage(500): %v", err)
	}
	if !ok {
		t.Fatal("TryAddSnapshotUsage reaching exactly the limit = false, want true")
	}
	assertUsage(t, repos, u.ID, 500, 0, 1)

	ok, err = repos.Quota.TryAddSnapshotUsage(ctx, u.ID, 1, limit)
	if err != nil {
		t.Fatalf("TryAddSnapshotUsage(1 over): %v", err)
	}
	if ok {
		t.Fatal("TryAddSnapshotUsage exceeding the limit = true, want false")
	}
	assertUsage(t, repos, u.ID, 500, 0, 1)
}

// TestRecalculateUsage verifies the reconcile recomputes counters from the live
// (non-soft-deleted) bundle and snapshot rows, overwriting any drift.
func TestRecalculateUsage(t *testing.T) {
	repos := setupTest(t)
	ctx := testContext(t)

	u := seedUser(t, repos, "recalc@example.com")
	dev := uuid.New()

	// Two live bundles (100 + 200) plus one that gets soft-deleted (50).
	seedBundle(t, repos, u.ID, dev, "rb1", 1, 1, 100)
	seedBundle(t, repos, u.ID, dev, "rb2", 2, 2, 200)
	seedBundle(t, repos, u.ID, dev, "rb-del", 3, 3, 50)
	if _, err := repos.Bundles.SoftDelete(ctx, u.ID, "rb-del"); err != nil {
		t.Fatalf("SoftDelete bundle: %v", err)
	}

	// One live snapshot (300) plus one soft-deleted (40).
	for _, s := range []*model.SnapshotMeta{
		{SnapshotID: "rs1", UserID: u.ID, BaseHLC: 1, SizeBytes: 300, CipherID: 1},
		{SnapshotID: "rs-del", UserID: u.ID, BaseHLC: 2, SizeBytes: 40, CipherID: 1},
	} {
		if err := repos.Snapshots.Create(ctx, s); err != nil {
			t.Fatalf("create snapshot %s: %v", s.SnapshotID, err)
		}
	}
	if _, err := repos.Snapshots.SoftDelete(ctx, u.ID, "rs-del"); err != nil {
		t.Fatalf("SoftDelete snapshot: %v", err)
	}

	// Corrupt the counters to simulate drift (e.g. a crash mid-upload).
	if err := repos.Quota.AddBundleUsage(ctx, u.ID, 9999); err != nil {
		t.Fatalf("seed drift: %v", err)
	}

	usage, err := repos.Quota.RecalculateUsage(ctx, u.ID)
	if err != nil {
		t.Fatalf("RecalculateUsage: %v", err)
	}
	// Live totals only: bytes 100+200+300 = 600, 2 bundles, 1 snapshot.
	if usage.TotalBytes != 600 || usage.BundleCount != 2 || usage.SnapCount != 1 {
		t.Fatalf("RecalculateUsage = {bytes:%d bundles:%d snaps:%d}, want {600 2 1}",
			usage.TotalBytes, usage.BundleCount, usage.SnapCount)
	}
	// The persisted row must equal the returned value.
	assertUsage(t, repos, u.ID, 600, 2, 1)
}

// TestRecalculateUsageEmpty verifies a user with no live rows reconciles to zero
// and a storage_usage row is materialized.
func TestRecalculateUsageEmpty(t *testing.T) {
	repos := setupTest(t)
	ctx := testContext(t)

	u := seedUser(t, repos, "recalc-empty@example.com")

	usage, err := repos.Quota.RecalculateUsage(ctx, u.ID)
	if err != nil {
		t.Fatalf("RecalculateUsage: %v", err)
	}
	if usage.TotalBytes != 0 || usage.BundleCount != 0 || usage.SnapCount != 0 {
		t.Fatalf("RecalculateUsage(empty) = %+v, want zeroed", usage)
	}
	assertUsage(t, repos, u.ID, 0, 0, 0)
}

// TestRecalculateAllUsage verifies the bulk reconcile fixes every user's
// counters in one pass, excluding soft-deleted rows.
func TestRecalculateAllUsage(t *testing.T) {
	repos := setupTest(t)
	ctx := testContext(t)

	u1 := seedUser(t, repos, "all1@example.com")
	u2 := seedUser(t, repos, "all2@example.com")
	dev := uuid.New()

	// u1: 2 bundles (100+200) + 1 snapshot (300) -> 600 / 2 / 1.
	seedBundle(t, repos, u1.ID, dev, "a1", 1, 1, 100)
	seedBundle(t, repos, u1.ID, dev, "a2", 2, 2, 200)
	if err := repos.Snapshots.Create(ctx, &model.SnapshotMeta{
		SnapshotID: "as1", UserID: u1.ID, BaseHLC: 1, SizeBytes: 300, CipherID: 1,
	}); err != nil {
		t.Fatalf("create snapshot: %v", err)
	}

	// u2: 1 live bundle (50) plus a soft-deleted bundle (999) that must be excluded.
	seedBundle(t, repos, u2.ID, dev, "b1", 1, 1, 50)
	seedBundle(t, repos, u2.ID, dev, "b-del", 2, 2, 999)
	if _, err := repos.Bundles.SoftDelete(ctx, u2.ID, "b-del"); err != nil {
		t.Fatalf("SoftDelete: %v", err)
	}

	// Drift u1's counters to confirm the reconcile overwrites them.
	if err := repos.Quota.AddBundleUsage(ctx, u1.ID, 12345); err != nil {
		t.Fatalf("seed drift: %v", err)
	}

	n, err := repos.Quota.RecalculateAllUsage(ctx)
	if err != nil {
		t.Fatalf("RecalculateAllUsage: %v", err)
	}
	if n != 2 {
		t.Fatalf("RecalculateAllUsage reconciled %d rows, want 2", n)
	}
	assertUsage(t, repos, u1.ID, 600, 2, 1)
	assertUsage(t, repos, u2.ID, 50, 1, 0)
}

func assertUsage(t *testing.T, repos *Repos, userID uuid.UUID, wantBytes int64, wantBundles, wantSnaps int32) {
	t.Helper()
	usage, err := repos.Quota.GetUsage(testContext(t), userID)
	if err != nil {
		t.Fatalf("GetUsage: %v", err)
	}
	if usage.TotalBytes != wantBytes || usage.BundleCount != wantBundles || usage.SnapCount != wantSnaps {
		t.Fatalf("usage = {bytes:%d bundles:%d snaps:%d}, want {bytes:%d bundles:%d snaps:%d}",
			usage.TotalBytes, usage.BundleCount, usage.SnapCount, wantBytes, wantBundles, wantSnaps)
	}
}
