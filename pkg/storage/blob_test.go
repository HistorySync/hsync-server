package storage

import "testing"

func TestKeyHelpers(t *testing.T) {
	if got, want := BundleKey("user-1", "bundle-1"), "bundles/user-1/bundle-1.hsb"; got != want {
		t.Fatalf("BundleKey() = %q, want %q", got, want)
	}
	if got, want := SnapshotKey("user-1", "snap-1"), "snapshots/user-1/snap-1.hsb"; got != want {
		t.Fatalf("SnapshotKey() = %q, want %q", got, want)
	}
	if got, want := UserPrefix("user-1"), "bundles/user-1/"; got != want {
		t.Fatalf("UserPrefix() = %q, want %q", got, want)
	}
}
