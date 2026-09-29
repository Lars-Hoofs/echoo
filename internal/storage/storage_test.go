package storage

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

// exercise runs the behaviour every Store must have.
func exercise(t *testing.T, s Store) {
	t.Helper()
	ctx := context.Background()
	data := []byte("From: a@example.com\r\n\r\nhallo\r\n")
	key, sum, err := s.Put(ctx, data)
	if err != nil {
		t.Fatal(err)
	}
	want := sha256.Sum256(data)
	if !bytes.Equal(sum, want[:]) || !keyPattern.MatchString(key) {
		t.Fatalf("key %q sum %x", key, sum)
	}
	again, _, err := s.Put(ctx, data)
	if err != nil || again != key {
		t.Fatalf("second put: %q %v", again, err)
	}
	r, err := s.Open(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(r)
	_ = r.Close()
	if err != nil || !bytes.Equal(got, data) {
		t.Fatalf("read back %q %v", got, err)
	}
	missing := keyFor(make([]byte, 32))
	if _, err := s.Open(ctx, missing); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing key: %v", err)
	}
	for _, bad := range []string{"../../etc/passwd", "sha256/aa/bb/../../x", "/etc/passwd", ""} {
		if _, err := s.Open(ctx, bad); !errors.Is(err, ErrNotFound) {
			t.Errorf("key %q must be rejected, got %v", bad, err)
		}
	}
}

func TestFS(t *testing.T) {
	root := filepath.Join(t.TempDir(), "blobs")
	s, err := NewFS(root)
	if err != nil {
		t.Fatal(err)
	}
	exercise(t, s)

	leftovers, err := filepath.Glob(filepath.Join(root, "sha256", "*", "*", ".tmp-*"))
	if err != nil || len(leftovers) != 0 {
		t.Fatalf("temp files left behind: %v %v", leftovers, err)
	}
	st, err := os.Stat(root)
	if err != nil || st.Mode().Perm() != 0o700 {
		t.Fatalf("root permissions: %v %v", st.Mode(), err)
	}
	if _, err := NewFS("relative/path"); err == nil {
		t.Fatal("relative root accepted")
	}
}

func TestFromURL(t *testing.T) {
	if _, err := FromURL("fs:///"+filepath.ToSlash(t.TempDir())[1:], "", ""); err != nil {
		t.Fatalf("fs url: %v", err)
	}
	for _, bad := range []string{"fs://relative/path", "ftp://x", "s3://?endpoint=x", "s3://bucket"} {
		if _, err := FromURL(bad, "", ""); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestS3(t *testing.T) {
	if testing.Short() {
		t.Skip("starts a container")
	}
	ctx := context.Background()
	c, err := testcontainers.Run(ctx, "chrislusf/seaweedfs:latest",
		testcontainers.WithCmd("server", "-s3", "-s3.config=/etc/s3.json", "-dir=/data"),
		testcontainers.WithFiles(testcontainers.ContainerFile{
			Reader:            strings.NewReader(`{"identities":[{"name":"test","credentials":[{"accessKey":"key","secretKey":"secret"}],"actions":["Admin","Read","Write","List"]}]}`),
			ContainerFilePath: "/etc/s3.json",
			FileMode:          0o644,
		}),
		testcontainers.WithExposedPorts("8333/tcp"),
		testcontainers.WithWaitStrategy(wait.ForListeningPort("8333/tcp")),
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = testcontainers.TerminateContainer(c) })
	endpoint, err := c.PortEndpoint(ctx, "8333/tcp", "")
	if err != nil {
		t.Fatal(err)
	}

	s, err := FromURL(fmt.Sprintf("s3://echoo-test?endpoint=%s&region=us-east-1&insecure=true", endpoint), "key", "secret")
	if err != nil {
		t.Fatal(err)
	}
	s3 := s.(*S3)
	// The S3 gateway may accept connections before it serves requests.
	var mkErr error
	for range 50 {
		if mkErr = s3.client.MakeBucket(ctx, "echoo-test", minio.MakeBucketOptions{Region: "us-east-1"}); mkErr == nil {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if mkErr != nil {
		t.Fatal(mkErr)
	}
	exercise(t, s)
}
