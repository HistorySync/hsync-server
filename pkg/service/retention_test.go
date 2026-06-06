package service

import (
	"context"
	"errors"
	"strconv"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/historysync/hsync-server/pkg/model"
	"github.com/historysync/hsync-server/pkg/storage"
)

// fakeRetentionStore is an in-memory stand-in for the bundle repository and blob
// store used by the retention purge loop. It records the order of blob and row
// deletions so tests can assert blobs are removed before rows, and can be made to
// fail specific blobs or rows to exercise the partial-failure paths.
type fakeRetentionStore struct {
	rows      []model.BundleMeta // insertion order stands in for deleted_at ordering
	present   map[string]bool    // metaID -> metadata row still exists
	blobFail  map[string]bool    // blob key -> Delete returns an error
	rowFail   map[string]bool    // metaID -> HardDelete returns an error
	events    []string           // ordered "blob:<key>" / "blobfail:<key>" / "row:<id>"
	listCalls int
}

func newFakeRetentionStore() *fakeRetentionStore {
	return &fakeRetentionStore{
		present:  map[string]bool{},
		blobFail: map[string]bool{},
		rowFail:  map[string]bool{},
	}
}

func metaID(userID uuid.UUID, bundleID string) string {
	return userID.String() + "/" + bundleID
}

// add registers a soft-deleted bundle (its blob is considered present).
func (f *fakeRetentionStore) add(b model.BundleMeta) {
	f.rows = append(f.rows, b)
	f.present[metaID(b.UserID, b.BundleID)] = true
}

func (f *fakeRetentionStore) ListDeletedBefore(ctx context.Context, before time.Time) ([]model.BundleMeta, error) {
	f.listCalls++
	var out []model.BundleMeta
	for _, b := range f.rows {
		if !f.present[metaID(b.UserID, b.BundleID)] {
			continue
		}
		if b.DeletedAt == nil || !b.DeletedAt.Before(before) {
			continue
		}
		out = append(out, b)
		if len(out) == 100 { // mirror the repository's LIMIT 100 page size
			break
		}
	}
	return out, nil
}

func (f *fakeRetentionStore) HardDelete(ctx context.Context, userID uuid.UUID, bundleID string) error {
	id := metaID(userID, bundleID)
	if f.rowFail[id] {
		return errors.New("hard delete failed")
	}
	f.events = append(f.events, "row:"+id)
	f.present[id] = false
	return nil
}

func (f *fakeRetentionStore) Delete(ctx context.Context, key string) error {
	if f.blobFail[key] {
		f.events = append(f.events, "blobfail:"+key)
		return errors.New("blob delete failed")
	}
	f.events = append(f.events, "blob:"+key)
	return nil
}

func deletedBundle(userID uuid.UUID, bundleID string, size int64, deletedAt time.Time) model.BundleMeta {
	return model.BundleMeta{
		BundleID:  bundleID,
		UserID:    userID,
		SizeBytes: size,
		DeletedAt: &deletedAt,
	}
}

func indexOf(events []string, want string) int {
	for i, e := range events {
		if e == want {
			return i
		}
	}
	return -1
}

func TestPurgeExpiredBundlesHappyPath(t *testing.T) {
	f := newFakeRetentionStore()
	user := uuid.New()
	past := time.Now().Add(-48 * time.Hour)
	sizes := map[string]int64{"b1": 100, "b2": 200, "b3": 400}
	for _, id := range []string{"b1", "b2", "b3"} {
		f.add(deletedBundle(user, id, sizes[id], past))
	}

	report, err := purgeExpiredBundles(context.Background(), f, f, time.Now())
	if err != nil {
		t.Fatalf("purgeExpiredBundles() error = %v", err)
	}
	if report.ExpiredBundles != 3 || report.ExpiredBytes != 700 || report.Failed != 0 {
		t.Fatalf("report = %+v, want bundles=3 bytes=700 failed=0", report)
	}

	// Every row must be gone, and each bundle's blob must be deleted before its row.
	for _, id := range []string{"b1", "b2", "b3"} {
		if f.present[metaID(user, id)] {
			t.Fatalf("bundle %q row still present after purge", id)
		}
		key := storage.BundleKey(user.String(), id)
		blobIdx := indexOf(f.events, "blob:"+key)
		rowIdx := indexOf(f.events, "row:"+metaID(user, id))
		if blobIdx < 0 || rowIdx < 0 {
			t.Fatalf("bundle %q missing events (blob=%d row=%d): %v", id, blobIdx, rowIdx, f.events)
		}
		if blobIdx > rowIdx {
			t.Fatalf("bundle %q deleted row before blob: %v", id, f.events)
		}
	}
}

func TestPurgeExpiredBundlesBlobFailureLeavesRow(t *testing.T) {
	f := newFakeRetentionStore()
	user := uuid.New()
	past := time.Now().Add(-48 * time.Hour)
	f.add(deletedBundle(user, "b1", 100, past))
	f.add(deletedBundle(user, "b2", 200, past))
	f.add(deletedBundle(user, "b3", 400, past))
	f.blobFail[storage.BundleKey(user.String(), "b2")] = true

	report, err := purgeExpiredBundles(context.Background(), f, f, time.Now())
	if err != nil {
		t.Fatalf("purgeExpiredBundles() error = %v", err)
	}
	if report.ExpiredBundles != 2 || report.ExpiredBytes != 500 || report.Failed != 1 {
		t.Fatalf("report = %+v, want bundles=2 bytes=500 failed=1", report)
	}
	// The failed bundle's row (and blob) must remain for a later run.
	if !f.present[metaID(user, "b2")] {
		t.Fatal("b2 row was removed despite blob delete failure")
	}
	// The loop must terminate quickly: one page to purge, one to confirm only the
	// stuck row remains.
	if f.listCalls != 2 {
		t.Fatalf("listCalls = %d, want 2 (no infinite retry)", f.listCalls)
	}
}

func TestPurgeExpiredBundlesRowFailureLeavesRow(t *testing.T) {
	f := newFakeRetentionStore()
	user := uuid.New()
	past := time.Now().Add(-48 * time.Hour)
	f.add(deletedBundle(user, "b1", 100, past))
	f.add(deletedBundle(user, "b2", 200, past))
	f.rowFail[metaID(user, "b1")] = true

	report, err := purgeExpiredBundles(context.Background(), f, f, time.Now())
	if err != nil {
		t.Fatalf("purgeExpiredBundles() error = %v", err)
	}
	if report.ExpiredBundles != 1 || report.ExpiredBytes != 200 || report.Failed != 1 {
		t.Fatalf("report = %+v, want bundles=1 bytes=200 failed=1", report)
	}
	if !f.present[metaID(user, "b1")] {
		t.Fatal("b1 row was removed despite hard-delete failure")
	}
	if f.listCalls != 2 {
		t.Fatalf("listCalls = %d, want 2 (no infinite retry)", f.listCalls)
	}
}

func TestPurgeExpiredBundlesPaging(t *testing.T) {
	f := newFakeRetentionStore()
	user := uuid.New()
	past := time.Now().Add(-48 * time.Hour)
	const total = 250
	for i := 0; i < total; i++ {
		f.add(deletedBundle(user, "b"+strconv.Itoa(i), 1, past))
	}

	report, err := purgeExpiredBundles(context.Background(), f, f, time.Now())
	if err != nil {
		t.Fatalf("purgeExpiredBundles() error = %v", err)
	}
	if report.ExpiredBundles != total || report.ExpiredBytes != total || report.Failed != 0 {
		t.Fatalf("report = %+v, want bundles=%d bytes=%d failed=0", report, total, total)
	}
	// 100 + 100 + 50 purged across three pages, then a fourth empty page ends it.
	if f.listCalls != 4 {
		t.Fatalf("listCalls = %d, want 4 for 250 rows at page size 100", f.listCalls)
	}
}

func TestPurgeExpiredBundlesEmpty(t *testing.T) {
	f := newFakeRetentionStore()

	report, err := purgeExpiredBundles(context.Background(), f, f, time.Now())
	if err != nil {
		t.Fatalf("purgeExpiredBundles() error = %v", err)
	}
	if report.ExpiredBundles != 0 || report.ExpiredBytes != 0 || report.Failed != 0 {
		t.Fatalf("report = %+v, want all zero", report)
	}
	if f.listCalls != 1 {
		t.Fatalf("listCalls = %d, want 1", f.listCalls)
	}
}

func TestPurgeExpiredBundlesRespectsCutoff(t *testing.T) {
	f := newFakeRetentionStore()
	user := uuid.New()
	cutoff := time.Now()
	f.add(deletedBundle(user, "old", 100, cutoff.Add(-time.Hour))) // eligible
	f.add(deletedBundle(user, "new", 200, cutoff.Add(time.Hour)))  // too recent

	report, err := purgeExpiredBundles(context.Background(), f, f, cutoff)
	if err != nil {
		t.Fatalf("purgeExpiredBundles() error = %v", err)
	}
	if report.ExpiredBundles != 1 || report.ExpiredBytes != 100 {
		t.Fatalf("report = %+v, want bundles=1 bytes=100", report)
	}
	if !f.present[metaID(user, "new")] {
		t.Fatal("recently-deleted bundle was purged before its grace period")
	}
}

func TestPurgeExpiredBundlesCancelledContext(t *testing.T) {
	f := newFakeRetentionStore()
	user := uuid.New()
	f.add(deletedBundle(user, "b1", 100, time.Now().Add(-time.Hour)))

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	report, err := purgeExpiredBundles(ctx, f, f, time.Now())
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
	if report.ExpiredBundles != 0 {
		t.Fatalf("report.ExpiredBundles = %d, want 0", report.ExpiredBundles)
	}
	if f.listCalls != 0 {
		t.Fatalf("listCalls = %d, want 0 (cancelled before any query)", f.listCalls)
	}
}

// Snapshot Retention Tests
// fakeSnapshotStore is the snapshot counterpart of fakeRetentionStore.
type fakeSnapshotStore struct {
	rows      []model.SnapshotMeta
	present   map[string]bool // metaID -> metadata row still exists
	blobFail  map[string]bool
	rowFail   map[string]bool
	events    []string
	listCalls int
}

func newFakeSnapshotStore() *fakeSnapshotStore {
	return &fakeSnapshotStore{
		present:  map[string]bool{},
		blobFail: map[string]bool{},
		rowFail:  map[string]bool{},
	}
}

func snapID(userID uuid.UUID, snapshotID string) string {
	return userID.String() + "/" + snapshotID
}

func (f *fakeSnapshotStore) add(s model.SnapshotMeta) {
	f.rows = append(f.rows, s)
	f.present[snapID(s.UserID, s.SnapshotID)] = true
}

func (f *fakeSnapshotStore) ListDeletedBefore(ctx context.Context, before time.Time) ([]model.SnapshotMeta, error) {
	f.listCalls++
	var out []model.SnapshotMeta
	for _, s := range f.rows {
		if !f.present[snapID(s.UserID, s.SnapshotID)] {
			continue
		}
		if s.DeletedAt == nil || !s.DeletedAt.Before(before) {
			continue
		}
		out = append(out, s)
		if len(out) == 100 {
			break
		}
	}
	return out, nil
}

func (f *fakeSnapshotStore) HardDelete(ctx context.Context, userID uuid.UUID, snapshotID string) error {
	id := snapID(userID, snapshotID)
	if f.rowFail[id] {
		return errors.New("hard delete failed")
	}
	f.events = append(f.events, "row:"+id)
	f.present[id] = false
	return nil
}

func (f *fakeSnapshotStore) Delete(ctx context.Context, key string) error {
	if f.blobFail[key] {
		f.events = append(f.events, "blobfail:"+key)
		return errors.New("blob delete failed")
	}
	f.events = append(f.events, "blob:"+key)
	return nil
}

func deletedSnapshot(userID uuid.UUID, snapshotID string, size int64, deletedAt time.Time) model.SnapshotMeta {
	return model.SnapshotMeta{
		SnapshotID: snapshotID,
		UserID:     userID,
		SizeBytes:  size,
		DeletedAt:  &deletedAt,
	}
}

func TestPurgeExpiredSnapshotsHappyPath(t *testing.T) {
	f := newFakeSnapshotStore()
	user := uuid.New()
	past := time.Now().Add(-48 * time.Hour)
	sizes := map[string]int64{"s1": 100, "s2": 200, "s3": 400}
	for _, id := range []string{"s1", "s2", "s3"} {
		f.add(deletedSnapshot(user, id, sizes[id], past))
	}

	report, err := purgeExpiredSnapshots(context.Background(), f, f, time.Now())
	if err != nil {
		t.Fatalf("purgeExpiredSnapshots() error = %v", err)
	}
	if report.ExpiredSnapshots != 3 || report.ExpiredBytes != 700 || report.Failed != 0 {
		t.Fatalf("report = %+v, want snapshots=3 bytes=700 failed=0", report)
	}

	for _, id := range []string{"s1", "s2", "s3"} {
		if f.present[snapID(user, id)] {
			t.Fatalf("snapshot %q row still present after purge", id)
		}
		key := storage.SnapshotKey(user.String(), id)
		blobIdx := indexOf(f.events, "blob:"+key)
		rowIdx := indexOf(f.events, "row:"+snapID(user, id))
		if blobIdx < 0 || rowIdx < 0 {
			t.Fatalf("snapshot %q missing events (blob=%d row=%d): %v", id, blobIdx, rowIdx, f.events)
		}
		if blobIdx > rowIdx {
			t.Fatalf("snapshot %q deleted row before blob: %v", id, f.events)
		}
	}
}

func TestPurgeExpiredSnapshotsBlobFailureLeavesRow(t *testing.T) {
	f := newFakeSnapshotStore()
	user := uuid.New()
	past := time.Now().Add(-48 * time.Hour)
	f.add(deletedSnapshot(user, "s1", 100, past))
	f.add(deletedSnapshot(user, "s2", 200, past))
	f.add(deletedSnapshot(user, "s3", 400, past))
	f.blobFail[storage.SnapshotKey(user.String(), "s2")] = true

	report, err := purgeExpiredSnapshots(context.Background(), f, f, time.Now())
	if err != nil {
		t.Fatalf("purgeExpiredSnapshots() error = %v", err)
	}
	if report.ExpiredSnapshots != 2 || report.ExpiredBytes != 500 || report.Failed != 1 {
		t.Fatalf("report = %+v, want snapshots=2 bytes=500 failed=1", report)
	}
	if !f.present[snapID(user, "s2")] {
		t.Fatal("s2 row was removed despite blob delete failure")
	}
	if f.listCalls != 2 {
		t.Fatalf("listCalls = %d, want 2 (no infinite retry)", f.listCalls)
	}
}

func TestPurgeExpiredSnapshotsEmpty(t *testing.T) {
	f := newFakeSnapshotStore()

	report, err := purgeExpiredSnapshots(context.Background(), f, f, time.Now())
	if err != nil {
		t.Fatalf("purgeExpiredSnapshots() error = %v", err)
	}
	if report.ExpiredSnapshots != 0 || report.ExpiredBytes != 0 || report.Failed != 0 {
		t.Fatalf("report = %+v, want all zero", report)
	}
}

func TestPurgeExpiredSnapshotsCancelledContext(t *testing.T) {
	f := newFakeSnapshotStore()
	user := uuid.New()
	f.add(deletedSnapshot(user, "s1", 100, time.Now().Add(-time.Hour)))

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	report, err := purgeExpiredSnapshots(ctx, f, f, time.Now())
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
	if report.ExpiredSnapshots != 0 {
		t.Fatalf("report.ExpiredSnapshots = %d, want 0", report.ExpiredSnapshots)
	}
}
