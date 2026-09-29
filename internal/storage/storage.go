// Package storage stores raw messages and attachments as content-addressed blobs.
package storage

import (
	"context"
	"errors"
	"io"
	"time"
)

var ErrNotFound = errors.New("storage: blob not found")

// Store is implemented by the filesystem and S3 backends. Blobs are write-once and addressed
// by the SHA-256 of their content, so Put is idempotent and safe to retry.
type Store interface {
	// Put stores data and returns its key and SHA-256.
	Put(ctx context.Context, data []byte) (key string, sha256 []byte, err error)
	// Open returns the blob for key, or ErrNotFound.
	Open(ctx context.Context, key string) (io.ReadCloser, error)
}

// Deleter is implemented by stores that can remove a blob. Blobs are content-addressed, so
// callers must first make sure nothing else references the key. Deleting a missing key is not
// an error.
type Deleter interface {
	Delete(ctx context.Context, key string) error
}

// Walker is implemented by stores that can list what they hold, so an operator can find files
// nothing refers to. Only the filesystem store does; listing a bucket is left to the provider's
// own tools.
type Walker interface {
	// Walk calls fn for every stored blob with its key, size and modification time.
	Walk(ctx context.Context, fn func(key string, size int64, modTime time.Time) error) error
}

// Prober is implemented by stores that can show they accept writes without leaving anything
// behind that needs cleaning up; readiness checks use it.
type Prober interface {
	Probe(ctx context.Context) error
}
