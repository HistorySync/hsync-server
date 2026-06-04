//go:build integration

package repository

import (
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/historysync/hsync-server/pkg/model"
)

func seedBundle(t *testing.T, repos *Repos, userID, deviceUUID uuid.UUID, bundleID string, lamportLo, lamportHi, size int64) *model.BundleMeta {
	t.Helper()
	b := &model.BundleMeta{
		BundleID:           bundleID,
		UserID:             userID,
		UploaderDeviceUUID: deviceUUID,
		LamportLo:          lamportLo,
		LamportHi:          lamportHi,
		EventCount:         1,
		SizeBytes:          size,
	}
	if err := repos.Bundles.Create(testContext(t), b); err != nil {
		t.Fatalf("seed bundle %q: %v", bundleID, err)
	}
	if b.UploadedAt.IsZero() {
		t.Fatalf("Create did not populate UploadedAt for %q", bundleID)
	}
	return b
}

func TestBundleCreateAndGet(t *testing.T) {
	repos := setupTest(t)
	ctx := testContext(t)

	u := seedUser(t, repos, "bundle@example.com")
	other := seedUser(t, repos, "other@example.com")
	dev := uuid.New()

	seedBundle(t, repos, u.ID, dev, "bundle-1", 1, 10, 500)

	got, err := repos.Bundles.GetByID(ctx, u.ID, "bundle-1")
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if got == nil || got.SizeBytes != 500 {
		t.Fatalf("GetByID = %+v, want size 500", got)
	}

	// Bundles are scoped per user: another user cannot read it.
	scoped, err := repos.Bundles.GetByID(ctx, other.ID, "bundle-1")
	if err != nil {
		t.Fatalf("GetByID(other user): %v", err)
	}
	if scoped != nil {
		t.Fatalf("GetByID for non-owner = %+v, want nil", scoped)
	}

	exists, err := repos.Bundles.ExistsByID(ctx, "bundle-1")
	if err != nil {
		t.Fatalf("ExistsByID: %v", err)
	}
	if !exists {
		t.Fatal("ExistsByID = false, want true")
	}

	exists, err = repos.Bundles.ExistsByID(ctx, "unknown")
	if err != nil {
		t.Fatalf("ExistsByID(unknown): %v", err)
	}
	if exists {
		t.Fatal("ExistsByID(unknown) = true, want false")
	}
}

func TestBundleCreateDuplicateRejected(t *testing.T) {
	repos := setupTest(t)
	ctx := testContext(t)

	u := seedUser(t, repos, "dupbundle@example.com")
	dev := uuid.New()
	seedBundle(t, repos, u.ID, dev, "same-id", 1, 2, 10)

	dup := &model.BundleMeta{
		BundleID:           "same-id",
		UserID:             u.ID,
		UploaderDeviceUUID: dev,
		LamportLo:          3,
		LamportHi:          4,
		EventCount:         1,
		SizeBytes:          10,
	}
	if err := repos.Bundles.Create(ctx, dup); err == nil {
		t.Fatal("duplicate (user_id, bundle_id) Create succeeded, want primary-key violation")
	}
}

func TestBundleListByDeviceCursor(t *testing.T) {
	repos := setupTest(t)
	ctx := testContext(t)

	u := seedUser(t, repos, "listdev@example.com")
	devA := uuid.New()
	devB := uuid.New()

	seedBundle(t, repos, u.ID, devA, "a-10", 10, 19, 1)
	seedBundle(t, repos, u.ID, devA, "a-20", 20, 29, 1)
	seedBundle(t, repos, u.ID, devA, "a-30", 30, 39, 1)
	seedBundle(t, repos, u.ID, devB, "b-15", 15, 19, 1) // different device, must be excluded

	all, err := repos.Bundles.ListByDevice(ctx, u.ID, devA, 0, 10)
	if err != nil {
		t.Fatalf("ListByDevice: %v", err)
	}
	if len(all) != 3 {
		t.Fatalf("ListByDevice len = %d, want 3", len(all))
	}
	// Ordered ascending by lamport_lo.
	if all[0].LamportLo != 10 || all[1].LamportLo != 20 || all[2].LamportLo != 30 {
		t.Fatalf("ListByDevice order = %d,%d,%d, want 10,20,30", all[0].LamportLo, all[1].LamportLo, all[2].LamportLo)
	}

	after, err := repos.Bundles.ListByDevice(ctx, u.ID, devA, 10, 10)
	if err != nil {
		t.Fatalf("ListByDevice(after=10): %v", err)
	}
	if len(after) != 2 || after[0].LamportLo != 20 {
		t.Fatalf("ListByDevice(after=10) = %+v, want lamport_lo 20,30", after)
	}

	limited, err := repos.Bundles.ListByDevice(ctx, u.ID, devA, 0, 2)
	if err != nil {
		t.Fatalf("ListByDevice(limit=2): %v", err)
	}
	if len(limited) != 2 {
		t.Fatalf("ListByDevice(limit=2) len = %d, want 2", len(limited))
	}
}

func TestBundleListByUserCursor(t *testing.T) {
	repos := setupTest(t)
	ctx := testContext(t)

	u := seedUser(t, repos, "listuser@example.com")
	dev := uuid.New()
	seedBundle(t, repos, u.ID, dev, "b1", 1, 1, 1)
	seedBundle(t, repos, u.ID, dev, "b2", 2, 2, 1)
	seedBundle(t, repos, u.ID, dev, "b3", 3, 3, 1)

	page1, err := repos.Bundles.ListByUser(ctx, u.ID, "", 2)
	if err != nil {
		t.Fatalf("ListByUser page1: %v", err)
	}
	if len(page1) != 2 || page1[0].BundleID != "b1" || page1[1].BundleID != "b2" {
		t.Fatalf("page1 = %+v, want b1,b2", page1)
	}

	page2, err := repos.Bundles.ListByUser(ctx, u.ID, page1[len(page1)-1].BundleID, 2)
	if err != nil {
		t.Fatalf("ListByUser page2: %v", err)
	}
	if len(page2) != 1 || page2[0].BundleID != "b3" {
		t.Fatalf("page2 = %+v, want b3", page2)
	}
}

func TestBundleSoftDeleteAndSums(t *testing.T) {
	repos := setupTest(t)
	ctx := testContext(t)

	u := seedUser(t, repos, "sums@example.com")
	dev := uuid.New()
	seedBundle(t, repos, u.ID, dev, "s1", 1, 1, 100)
	seedBundle(t, repos, u.ID, dev, "s2", 2, 2, 200)

	sum, err := repos.Bundles.SumSizeByUser(ctx, u.ID)
	if err != nil {
		t.Fatalf("SumSizeByUser: %v", err)
	}
	if sum != 300 {
		t.Fatalf("SumSizeByUser = %d, want 300", sum)
	}
	count, err := repos.Bundles.CountByUser(ctx, u.ID)
	if err != nil {
		t.Fatalf("CountByUser: %v", err)
	}
	if count != 2 {
		t.Fatalf("CountByUser = %d, want 2", count)
	}

	deleted, err := repos.Bundles.SoftDelete(ctx, u.ID, "s1")
	if err != nil {
		t.Fatalf("SoftDelete: %v", err)
	}
	if deleted == nil || deleted.DeletedAt == nil {
		t.Fatalf("SoftDelete returned %+v, want DeletedAt set", deleted)
	}

	if got, _ := repos.Bundles.GetByID(ctx, u.ID, "s1"); got != nil {
		t.Fatalf("GetByID after soft delete = %+v, want nil", got)
	}

	sum, err = repos.Bundles.SumSizeByUser(ctx, u.ID)
	if err != nil {
		t.Fatalf("SumSizeByUser after delete: %v", err)
	}
	if sum != 200 {
		t.Fatalf("SumSizeByUser after delete = %d, want 200", sum)
	}

	// Deleting an already-deleted bundle reports not found.
	if _, err := repos.Bundles.SoftDelete(ctx, u.ID, "s1"); err == nil {
		t.Fatal("SoftDelete of already-deleted bundle succeeded, want error")
	}
}

func TestBundleHardDelete(t *testing.T) {
	repos := setupTest(t)
	ctx := testContext(t)

	u := seedUser(t, repos, "hard@example.com")
	dev := uuid.New()
	seedBundle(t, repos, u.ID, dev, "h1", 1, 1, 50)

	if err := repos.Bundles.HardDelete(ctx, u.ID, "h1"); err != nil {
		t.Fatalf("HardDelete: %v", err)
	}
	exists, err := repos.Bundles.ExistsByID(ctx, "h1")
	if err != nil {
		t.Fatalf("ExistsByID: %v", err)
	}
	if exists {
		t.Fatal("ExistsByID after hard delete = true, want false")
	}
}

// TestBundleCountDeletedBefore verifies the retention-report query counts only
// bundles soft-deleted before the cutoff, summing their bytes and ignoring live
// rows.
func TestBundleCountDeletedBefore(t *testing.T) {
	repos := setupTest(t)
	ctx := testContext(t)

	u := seedUser(t, repos, "countdel@example.com")
	dev := uuid.New()
	seedBundle(t, repos, u.ID, dev, "c1", 1, 1, 100)
	seedBundle(t, repos, u.ID, dev, "c2", 2, 2, 200)
	seedBundle(t, repos, u.ID, dev, "c3", 3, 3, 400) // stays live

	if _, err := repos.Bundles.SoftDelete(ctx, u.ID, "c1"); err != nil {
		t.Fatalf("SoftDelete c1: %v", err)
	}
	if _, err := repos.Bundles.SoftDelete(ctx, u.ID, "c2"); err != nil {
		t.Fatalf("SoftDelete c2: %v", err)
	}

	// Cutoff in the future: both soft-deleted bundles qualify, the live one does not.
	count, bytes, err := repos.Bundles.CountDeletedBefore(ctx, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatalf("CountDeletedBefore(future): %v", err)
	}
	if count != 2 || bytes != 300 {
		t.Fatalf("CountDeletedBefore(future) = (%d, %d), want (2, 300)", count, bytes)
	}

	// Cutoff in the past: nothing was soft-deleted that long ago.
	count, bytes, err = repos.Bundles.CountDeletedBefore(ctx, time.Now().Add(-time.Hour))
	if err != nil {
		t.Fatalf("CountDeletedBefore(past): %v", err)
	}
	if count != 0 || bytes != 0 {
		t.Fatalf("CountDeletedBefore(past) = (%d, %d), want (0, 0)", count, bytes)
	}
}

// TestBundleListDeletedBefore verifies the retention purge query returns only the
// bundles soft-deleted before the cutoff, excluding live rows, so the purge loop
// pages over exactly the rows it is allowed to remove.
func TestBundleListDeletedBefore(t *testing.T) {
	repos := setupTest(t)
	ctx := testContext(t)

	u := seedUser(t, repos, "listdel@example.com")
	dev := uuid.New()
	seedBundle(t, repos, u.ID, dev, "d1", 1, 1, 100)
	seedBundle(t, repos, u.ID, dev, "d2", 2, 2, 200)
	seedBundle(t, repos, u.ID, dev, "live", 3, 3, 400) // stays live

	if _, err := repos.Bundles.SoftDelete(ctx, u.ID, "d1"); err != nil {
		t.Fatalf("SoftDelete d1: %v", err)
	}
	if _, err := repos.Bundles.SoftDelete(ctx, u.ID, "d2"); err != nil {
		t.Fatalf("SoftDelete d2: %v", err)
	}

	// Future cutoff: both soft-deleted bundles are returned, the live one is not.
	deleted, err := repos.Bundles.ListDeletedBefore(ctx, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatalf("ListDeletedBefore(future): %v", err)
	}
	got := map[string]int64{}
	for _, b := range deleted {
		got[b.BundleID] = b.SizeBytes
	}
	if len(got) != 2 || got["d1"] != 100 || got["d2"] != 200 {
		t.Fatalf("ListDeletedBefore(future) = %+v, want d1=100,d2=200 only", got)
	}
	if _, ok := got["live"]; ok {
		t.Fatal("ListDeletedBefore returned a live bundle")
	}

	// Past cutoff: nothing qualifies.
	none, err := repos.Bundles.ListDeletedBefore(ctx, time.Now().Add(-time.Hour))
	if err != nil {
		t.Fatalf("ListDeletedBefore(past): %v", err)
	}
	if len(none) != 0 {
		t.Fatalf("ListDeletedBefore(past) = %+v, want empty", none)
	}
}
