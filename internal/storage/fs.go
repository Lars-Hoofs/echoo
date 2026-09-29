package storage

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"time"
)

// keyPattern is the only accepted key shape; it keeps Open from ever touching paths outside
// the store, whatever a caller passes in.
var keyPattern = regexp.MustCompile(`^sha256/[0-9a-f]{2}/[0-9a-f]{2}/[0-9a-f]{64}$`)

func keyFor(sum []byte) string {
	h := hex.EncodeToString(sum)
	return "sha256/" + h[0:2] + "/" + h[2:4] + "/" + h
}

// FS stores blobs on a local filesystem, e.g. a Docker volume.
type FS struct {
	root string
}

func NewFS(root string) (*FS, error) {
	if !filepath.IsAbs(root) {
		return nil, fmt.Errorf("storage: filesystem root must be absolute, got %q", root)
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, fmt.Errorf("storage: create root: %w", err)
	}
	return &FS{root: root}, nil
}

func (s *FS) Put(_ context.Context, data []byte) (string, []byte, error) {
	sum := sha256.Sum256(data)
	key := keyFor(sum[:])
	path := filepath.Join(s.root, filepath.FromSlash(key))
	if _, err := os.Stat(path); err == nil {
		return key, sum[:], nil
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", nil, fmt.Errorf("storage: create dir: %w", err)
	}
	// Write to a temp file and rename, so a crash never leaves a partial blob under its key.
	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return "", nil, fmt.Errorf("storage: create temp: %w", err)
	}
	tmpName := tmp.Name()
	if err := writeAndSync(tmp, data); err != nil {
		_ = os.Remove(tmpName)
		return "", nil, err
	}
	if err := os.Rename(tmpName, path); err != nil {
		_ = os.Remove(tmpName)
		return "", nil, fmt.Errorf("storage: rename: %w", err)
	}
	if err := syncDir(dir); err != nil {
		return "", nil, err
	}
	return key, sum[:], nil
}

func writeAndSync(f *os.File, data []byte) error {
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return fmt.Errorf("storage: write: %w", err)
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return fmt.Errorf("storage: sync: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("storage: close: %w", err)
	}
	return nil
}

func syncDir(dir string) error {
	d, err := os.Open(dir) //nolint:gosec // dir is derived from the store root and a validated key
	if err != nil {
		return fmt.Errorf("storage: open dir: %w", err)
	}
	defer func() { _ = d.Close() }()
	if err := d.Sync(); err != nil {
		return fmt.Errorf("storage: sync dir: %w", err)
	}
	return nil
}

func (s *FS) Open(_ context.Context, key string) (io.ReadCloser, error) {
	if !keyPattern.MatchString(key) {
		return nil, ErrNotFound
	}
	f, err := os.Open(filepath.Join(s.root, filepath.FromSlash(key))) //nolint:gosec // key validated above
	if errors.Is(err, fs.ErrNotExist) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("storage: open: %w", err)
	}
	return f, nil
}

func (s *FS) Delete(_ context.Context, key string) error {
	if !keyPattern.MatchString(key) {
		return ErrNotFound
	}
	err := os.Remove(filepath.Join(s.root, filepath.FromSlash(key))) //nolint:gosec // key validated above
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("storage: delete: %w", err)
	}
	return nil
}

// Walk lists every blob under the root. Temporary files of writes in progress are skipped.
func (s *FS) Walk(ctx context.Context, fn func(key string, size int64, modTime time.Time) error) error {
	return filepath.WalkDir(s.root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(s.root, path)
		if err != nil {
			return err
		}
		key := filepath.ToSlash(rel)
		if !keyPattern.MatchString(key) {
			return nil
		}
		info, err := d.Info()
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		return fn(key, info.Size(), info.ModTime())
	})
}

// Probe writes and removes a small temporary file under the root, which fails on a read-only
// mount, a full disk or a missing volume.
func (s *FS) Probe(_ context.Context) error {
	f, err := os.CreateTemp(s.root, ".probe-*")
	if err != nil {
		return fmt.Errorf("storage: probe: %w", err)
	}
	name := f.Name()
	if err := writeAndSync(f, []byte("probe")); err != nil {
		_ = os.Remove(name)
		return err
	}
	if err := os.Remove(name); err != nil {
		return fmt.Errorf("storage: probe: %w", err)
	}
	return nil
}
