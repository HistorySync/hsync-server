// Package storage implements BlobStorage for S3-compatible object stores
// (AWS S3, MinIO, Cloudflare R2, etc.) using the minio-go SDK.
package storage

import (
	"context"
	"fmt"
	"io"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

// S3Config holds the connection parameters for an S3-compatible service.
type S3Config struct {
	Endpoint  string
	Bucket    string
	AccessKey string
	SecretKey string
	UseSSL    bool
	Region    string // optional, defaults to "us-east-1"
}

// S3Storage implements BlobStorage using the MinIO/S3 API.
type S3Storage struct {
	client *minio.Client
	bucket string
}

// NewS3Storage creates a new S3-backed blob store and verifies the bucket exists.
func NewS3Storage(ctx context.Context, cfg S3Config) (*S3Storage, error) {
	region := cfg.Region
	if region == "" {
		region = "us-east-1"
	}

	client, err := minio.New(cfg.Endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(cfg.AccessKey, cfg.SecretKey, ""),
		Secure: cfg.UseSSL,
		Region: region,
	})
	if err != nil {
		return nil, fmt.Errorf("s3 client: %w", err)
	}

	// Ensure bucket exists (idempotent)
	exists, err := client.BucketExists(ctx, cfg.Bucket)
	if err != nil {
		return nil, fmt.Errorf("s3 bucket check: %w", err)
	}
	if !exists {
		if err := client.MakeBucket(ctx, cfg.Bucket, minio.MakeBucketOptions{Region: region}); err != nil {
			return nil, fmt.Errorf("s3 create bucket: %w", err)
		}
	}

	return &S3Storage{
		client: client,
		bucket: cfg.Bucket,
	}, nil
}

// Put uploads an object with automatic multipart for large files.
func (s *S3Storage) Put(ctx context.Context, key string, reader io.Reader, size int64, contentType string) error {
	if contentType == "" {
		contentType = "application/octet-stream"
	}

	opts := minio.PutObjectOptions{
		ContentType:  contentType,
		PartSize:     5 * 1024 * 1024, // 5 MB parts
		StorageClass: "STANDARD_IA",   // Infrequent access for cost savings
	}

	_, err := s.client.PutObject(ctx, s.bucket, key, reader, size, opts)
	if err != nil {
		return fmt.Errorf("s3 put %s: %w", key, err)
	}
	return nil
}

// Get downloads an object.
func (s *S3Storage) Get(ctx context.Context, key string) (io.ReadCloser, error) {
	obj, err := s.client.GetObject(ctx, s.bucket, key, minio.GetObjectOptions{})
	if err != nil {
		return nil, fmt.Errorf("s3 get %s: %w", key, err)
	}
	return obj, nil
}

// Delete removes an object (no-op if missing).
func (s *S3Storage) Delete(ctx context.Context, key string) error {
	err := s.client.RemoveObject(ctx, s.bucket, key, minio.RemoveObjectOptions{})
	if err != nil {
		return fmt.Errorf("s3 delete %s: %w", key, err)
	}
	return nil
}

// Exists checks for object existence.
func (s *S3Storage) Exists(ctx context.Context, key string) (bool, error) {
	_, err := s.client.StatObject(ctx, s.bucket, key, minio.StatObjectOptions{})
	if err != nil {
		errResp := minio.ToErrorResponse(err)
		if errResp.Code == "NoSuchKey" {
			return false, nil
		}
		return false, fmt.Errorf("s3 stat %s: %w", key, err)
	}
	return true, nil
}

// Size returns the object size.
func (s *S3Storage) Size(ctx context.Context, key string) (int64, bool, error) {
	info, err := s.client.StatObject(ctx, s.bucket, key, minio.StatObjectOptions{})
	if err != nil {
		errResp := minio.ToErrorResponse(err)
		if errResp.Code == "NoSuchKey" {
			return 0, false, nil
		}
		return 0, false, fmt.Errorf("s3 stat %s: %w", key, err)
	}
	return info.Size, true, nil
}

// List returns objects matching a prefix.
func (s *S3Storage) List(ctx context.Context, prefix string) ([]ObjectInfo, error) {
	objects := s.client.ListObjects(ctx, s.bucket, minio.ListObjectsOptions{
		Prefix:    prefix,
		Recursive: true,
		MaxKeys:   1000,
	})

	var result []ObjectInfo
	for obj := range objects {
		if obj.Err != nil {
			return nil, fmt.Errorf("s3 list %s: %w", prefix, obj.Err)
		}
		result = append(result, ObjectInfo{
			Key:          obj.Key,
			Size:         obj.Size,
			LastModified: obj.LastModified,
		})
	}
	return result, nil
}
