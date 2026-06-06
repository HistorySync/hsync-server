// Package storage defines the abstraction layer for blob storage backends.
//
// Implementations include S3/MinIO (production), local filesystem (simple
// self-hosted), and in-memory (testing).
package storage

import (
	"context"
	"io"
	"time"
)

// Interface
// BlobStorage is the abstract interface for storing and retrieving opaque
// binary blobs (bundle files). All operations are context-aware for
// cancellation and tracing.
type BlobStorage interface {
	// Put uploads an object. If key already exists it is overwritten.
	Put(ctx context.Context, key string, reader io.Reader, size int64, contentType string) error

	// Get downloads an object. The caller MUST close the returned reader.
	Get(ctx context.Context, key string) (io.ReadCloser, error)

	// Delete removes an object. Deleting a non-existent key is not an error.
	Delete(ctx context.Context, key string) error

	// Exists checks whether an object exists at the given key.
	Exists(ctx context.Context, key string) (bool, error)

	// Size returns the object size in bytes.
	// Returns (0, false, nil) if the object does not exist.
	Size(ctx context.Context, key string) (int64, bool, error)

	// List returns object metadata for keys with the given prefix.
	// At most 1000 objects are returned.
	List(ctx context.Context, prefix string) ([]ObjectInfo, error)
}

// ObjectInfo is lightweight metadata about a stored object.
type ObjectInfo struct {
	Key          string
	Size         int64
	LastModified time.Time
}

// Key Helpers
// BundleKey builds the S3 key for a bundle file.
// Format: bundles/{userID}/{bundleID}.hsb
func BundleKey(userID, bundleID string) string {
	return "bundles/" + userID + "/" + bundleID + ".hsb"
}

// SnapshotKey builds the S3 key for a snapshot file.
// Format: snapshots/{userID}/{snapshotID}.hsb
func SnapshotKey(userID, snapshotID string) string {
	return "snapshots/" + userID + "/" + snapshotID + ".hsb"
}

// UserPrefix returns the prefix for all objects belonging to a user.
func UserPrefix(userID string) string {
	return "bundles/" + userID + "/"
}
