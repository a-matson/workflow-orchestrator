package storage

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/a-matson/workflow-orchestrator/backend/internal/models"
	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
	"github.com/rs/zerolog/log"
)

const DefaultBucket = "fluxor-artifacts"

// Client wraps the MinIO SDK for artifact storage operations.
type Client struct {
	mc *minio.Client
	bucket string
}

// NewClient creates and verifies a MinIO connection.
func NewClient(ctx context.Context, endpoint, accessKey, secretKey, bucket string, useSSL bool) (*Client, error) {
	mc, err := minio.New(endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(accessKey, secretKey, ""),
		Secure: useSSL,
	})
	if err != nil {
		return nil, fmt.Errorf("minio: create client: %w", err)
	}

	if bucket == "" {
		bucket = DefaultBucket
	}

	c := &Client{mc: mc, bucket: bucket}
	if err := c.ensureBucket(ctx); err != nil {
		return nil, err
	}

	log.Info().Str("endpoint", endpoint).Str("bucket", bucket).Msg("MinIO connected")
	return c, nil
}

func (c *Client) ensureBucket(ctx context.Context) error {
	exists, err := c.mc.BucketExists(ctx, c.bucket)
	if err != nil {
		return fmt.Errorf("minio: check bucket: %w", err)
	}
	if !exists {
		if err := c.mc.MakeBucket(ctx, c.bucket, minio.MakeBucketOptions{}); err != nil {
			return fmt.Errorf("minio: create bucket %q: %w", c.bucket, err)
		}
		log.Info().Str("bucket", c.bucket).Msg("MinIO bucket created")
	}
	return nil
}

// ArtifactKey builds the canonical MinIO object key for an artifact.
//   artifacts/{workflowExecID}/{taskDefID}/{relativePath}
func ArtifactKey(workflowExecID, taskDefID, relativePath string) string {
	return fmt.Sprintf("artifacts/%s/%s/%s", workflowExecID, taskDefID, relativePath)
}

// Upload streams a reader to MinIO and returns the resolved artifact.
func (c *Client) Upload(ctx context.Context, key string, r io.Reader, size int64, contentType string) (models.ResolvedArtifact, error) {
	if contentType == "" {
		contentType = "application/octet-stream"
	}

	info, err := c.mc.PutObject(ctx, c.bucket, key, r, size, minio.PutObjectOptions{
		ContentType: contentType,
	})
	if err != nil {
		return models.ResolvedArtifact{}, fmt.Errorf("minio: upload %q: %w", key, err)
	}

	log.Debug().Str("key", key).Int64("bytes", info.Size).Msg("artifact uploaded")
	return models.ResolvedArtifact{
		MinioKey: key,
		Size:     info.Size,
	}, nil
}

// Download fetches an object from MinIO and returns a ReadCloser.
// The caller must close the returned reader.
func (c *Client) Download(ctx context.Context, key string) (io.ReadCloser, int64, error) {
	obj, err := c.mc.GetObject(ctx, c.bucket, key, minio.GetObjectOptions{})
	if err != nil {
		return nil, 0, fmt.Errorf("minio: download %q: %w", key, err)
	}
	info, err := obj.Stat()
	if err != nil {
		obj.Close()
		return nil, 0, fmt.Errorf("minio: stat %q: %w", key, err)
	}
	return obj, info.Size, nil
}

// PresignURL returns a time-limited download URL for an artifact.
func (c *Client) PresignURL(ctx context.Context, key string, expires time.Duration) (string, error) {
	u, err := c.mc.PresignedGetObject(ctx, c.bucket, key, expires, nil)
	if err != nil {
		return "", fmt.Errorf("minio: presign %q: %w", key, err)
	}
	return u.String(), nil
}

// Exists returns true if the object key exists in the bucket.
func (c *Client) Exists(ctx context.Context, key string) (bool, error) {
	_, err := c.mc.StatObject(ctx, c.bucket, key, minio.StatObjectOptions{})
	if err != nil {
		if minio.ToErrorResponse(err).Code == "NoSuchKey" {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// ListByPrefix returns all artifact keys that start with the given prefix.
func (c *Client) ListByPrefix(ctx context.Context, prefix string) ([]models.ResolvedArtifact, error) {
	var results []models.ResolvedArtifact
	for obj := range c.mc.ListObjects(ctx, c.bucket, minio.ListObjectsOptions{
		Prefix:    prefix,
		Recursive: true,
	}) {
		if obj.Err != nil {
			return nil, obj.Err
		}
		results = append(results, models.ResolvedArtifact{
			MinioKey: obj.Key,
			Size:     obj.Size,
		})
	}
	return results, nil
}
