package storage

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"net/url"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

// S3 stores blobs in an S3-compatible bucket (AWS S3, MinIO, SeaweedFS, Garage, ...).
type S3 struct {
	client *minio.Client
	bucket string
}

type S3Config struct {
	Endpoint  string // host[:port]
	Bucket    string
	Region    string
	AccessKey string
	SecretKey string
	Insecure  bool // plain HTTP; only for a bucket on the same private network
}

func NewS3(cfg S3Config) (*S3, error) {
	if cfg.Endpoint == "" || cfg.Bucket == "" {
		return nil, errors.New("storage: s3 needs an endpoint and a bucket")
	}
	client, err := minio.New(cfg.Endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(cfg.AccessKey, cfg.SecretKey, ""),
		Secure: !cfg.Insecure,
		Region: cfg.Region,
	})
	if err != nil {
		return nil, fmt.Errorf("storage: s3 client: %w", err)
	}
	return &S3{client: client, bucket: cfg.Bucket}, nil
}

func (s *S3) Put(ctx context.Context, data []byte) (string, []byte, error) {
	sum := sha256.Sum256(data)
	key := keyFor(sum[:])
	// Content-addressed keys make an overwrite write identical bytes, so no existence check.
	_, err := s.client.PutObject(ctx, s.bucket, key, bytes.NewReader(data), int64(len(data)),
		minio.PutObjectOptions{ContentType: "application/octet-stream"})
	if err != nil {
		return "", nil, fmt.Errorf("storage: s3 put: %w", err)
	}
	return key, sum[:], nil
}

func (s *S3) Open(ctx context.Context, key string) (io.ReadCloser, error) {
	if !keyPattern.MatchString(key) {
		return nil, ErrNotFound
	}
	obj, err := s.client.GetObject(ctx, s.bucket, key, minio.GetObjectOptions{})
	if err != nil {
		return nil, fmt.Errorf("storage: s3 get: %w", err)
	}
	// GetObject is lazy; Stat surfaces a missing key before the caller starts reading.
	if _, err := obj.Stat(); err != nil {
		_ = obj.Close()
		if minio.ToErrorResponse(err).Code == minio.NoSuchKey {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("storage: s3 stat: %w", err)
	}
	return obj, nil
}

// probeKey is overwritten by every probe, so probing never accumulates objects.
const probeKey = ".echoo-probe"

func (s *S3) Probe(ctx context.Context) error {
	_, err := s.client.PutObject(ctx, s.bucket, probeKey, bytes.NewReader([]byte("probe")), 5,
		minio.PutObjectOptions{ContentType: "application/octet-stream"})
	if err != nil {
		return fmt.Errorf("storage: s3 probe: %w", err)
	}
	return nil
}

func (s *S3) Delete(ctx context.Context, key string) error {
	if !keyPattern.MatchString(key) {
		return ErrNotFound
	}
	if err := s.client.RemoveObject(ctx, s.bucket, key, minio.RemoveObjectOptions{}); err != nil {
		return fmt.Errorf("storage: s3 delete: %w", err)
	}
	return nil
}

// FromURL builds a store from ECHOO_STORAGE: "fs:///absolute/path" or
// "s3://bucket?endpoint=host:port&region=eu-west-1[&insecure=true]".
func FromURL(raw, accessKey, secretKey string) (Store, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("storage: invalid URL: %w", err)
	}
	switch u.Scheme {
	case "fs":
		if u.Host != "" {
			return nil, fmt.Errorf("storage: use fs:///absolute/path (three slashes)")
		}
		return NewFS(u.Path)
	case "s3":
		q := u.Query()
		return NewS3(S3Config{
			Endpoint: q.Get("endpoint"), Bucket: u.Host, Region: q.Get("region"),
			AccessKey: accessKey, SecretKey: secretKey, Insecure: q.Get("insecure") == "true",
		})
	default:
		return nil, fmt.Errorf("storage: unsupported scheme %q (use fs or s3)", u.Scheme)
	}
}
