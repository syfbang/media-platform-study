package storage

import (
	"context"
	"fmt"
	"io"
	"net/url"
	"time"

	"github.com/media-service/media-platform/internal/config"
	"github.com/media-service/media-platform/internal/telemetry"
	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
	"go.opentelemetry.io/otel/attribute"
)

type Storage interface {
	Upload(ctx context.Context, key string, reader io.Reader, size int64, contentType string) error
	Download(ctx context.Context, key string) (io.ReadCloser, error)
	PresignedURL(ctx context.Context, key string, expires time.Duration) (*url.URL, error)
	Delete(ctx context.Context, key string) error
}

type minioStorage struct {
	client *minio.Client
	bucket string
}

func New(cfg config.MinIOConfig) (Storage, error) {
	client, err := minio.New(cfg.Endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(cfg.AccessKey, cfg.SecretKey, ""),
		Secure: cfg.UseSSL,
	})
	if err != nil {
		return nil, fmt.Errorf("minio client: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	exists, err := client.BucketExists(ctx, cfg.Bucket)
	if err != nil {
		return nil, fmt.Errorf("bucket check: %w", err)
	}
	if !exists {
		if err := client.MakeBucket(ctx, cfg.Bucket, minio.MakeBucketOptions{}); err != nil {
			return nil, fmt.Errorf("make bucket: %w", err)
		}
	}

	return &minioStorage{client: client, bucket: cfg.Bucket}, nil
}

func (s *minioStorage) Upload(ctx context.Context, key string, reader io.Reader, size int64, contentType string) error {
	ctx, span := telemetry.Tracer.Start(ctx, "s3.upload")
	defer span.End()
	span.SetAttributes(attribute.String("s3.key", key), attribute.Int64("s3.size_bytes", size))

	_, err := s.client.PutObject(ctx, s.bucket, key, reader, size, minio.PutObjectOptions{
		ContentType: contentType,
	})
	return err
}

func (s *minioStorage) Download(ctx context.Context, key string) (io.ReadCloser, error) {
	_, span := telemetry.Tracer.Start(ctx, "s3.download")
	defer span.End()
	span.SetAttributes(attribute.String("s3.key", key))

	return s.client.GetObject(ctx, s.bucket, key, minio.GetObjectOptions{})
}

func (s *minioStorage) PresignedURL(ctx context.Context, key string, expires time.Duration) (*url.URL, error) {
	return s.client.PresignedGetObject(ctx, s.bucket, key, expires, url.Values{})
}

func (s *minioStorage) Delete(ctx context.Context, key string) error {
	return s.client.RemoveObject(ctx, s.bucket, key, minio.RemoveObjectOptions{})
}
