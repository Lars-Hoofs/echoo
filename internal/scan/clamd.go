// Package scan talks to a clamd daemon over TCP to virus-scan attachments.
package scan

import (
	"bytes"
	"cmp"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"time"
)

// Scan statuses, as stored in attachments.scan_status.
const (
	StatusClean    = "clean"
	StatusInfected = "infected"
	StatusError    = "error"
)

const (
	chunkSize = 64 << 10
	// MaxScanBytes is the largest file sent to clamd. It matches the highest value
	// ECHOO_MAX_ATTACHMENT_MB allows, so no accepted attachment is skipped for size.
	MaxScanBytes = 100 << 20
	// A clamd that hangs must not stall ingest: connecting gets a few seconds, one file half a
	// minute. The zero values of Clamd.DialTimeout and Clamd.ScanTimeout mean these.
	defaultDialTimeout = 5 * time.Second
	defaultScanTimeout = 30 * time.Second
	maxReplySize       = 4 << 10
)

// Result is the outcome of one scan. Detail is the signature name for infected files and a
// short reason for errors.
type Result struct {
	Status string
	Detail string
}

// Scanner checks file contents. It never returns an error: a scanner that cannot decide
// reports StatusError, and callers treat that as "not scanned".
type Scanner interface {
	Scan(ctx context.Context, data []byte) Result
}

// Clamd scans through clamd's INSTREAM command. The address is operator-configured, so
// internal hosts are allowed.
type Clamd struct {
	Addr string
	// DialTimeout and ScanTimeout bound connecting and one whole scan; zero uses the defaults.
	DialTimeout, ScanTimeout time.Duration
}

// ParseAddr accepts "host:port" or "tcp://host:port".
func ParseAddr(raw string) (string, error) {
	addr := strings.TrimPrefix(raw, "tcp://")
	host, port, err := net.SplitHostPort(addr)
	if n, perr := strconv.Atoi(port); err != nil || host == "" || perr != nil || n < 1 || n > 65535 {
		return "", fmt.Errorf("%q is not a tcp host:port address", raw)
	}
	return addr, nil
}

func (c Clamd) Scan(ctx context.Context, data []byte) Result {
	if len(data) > MaxScanBytes {
		return Result{StatusError, "file too large to scan"}
	}
	ctx, cancel := context.WithTimeout(ctx, cmp.Or(c.ScanTimeout, defaultScanTimeout))
	defer cancel()
	reply, err := c.instream(ctx, data)
	if err != nil {
		return Result{StatusError, err.Error()}
	}
	return parseReply(reply)
}

func (c Clamd) instream(ctx context.Context, data []byte) (string, error) {
	d := net.Dialer{Timeout: cmp.Or(c.DialTimeout, defaultDialTimeout)}
	conn, err := d.DialContext(ctx, "tcp", c.Addr)
	if err != nil {
		return "", fmt.Errorf("connect to clamd: %w", err)
	}
	defer func() { _ = conn.Close() }()
	if dl, ok := ctx.Deadline(); ok {
		if err := conn.SetDeadline(dl); err != nil {
			return "", fmt.Errorf("set deadline: %w", err)
		}
	}
	if _, err := conn.Write([]byte("zINSTREAM\x00")); err != nil {
		return "", fmt.Errorf("send command: %w", err)
	}
	var size [4]byte
	for rest := data; len(rest) > 0; {
		n := min(len(rest), chunkSize)
		binary.BigEndian.PutUint32(size[:], uint32(n)) //nolint:gosec // n <= chunkSize
		if _, err := conn.Write(size[:]); err != nil {
			return "", fmt.Errorf("send chunk: %w", err)
		}
		if _, err := conn.Write(rest[:n]); err != nil {
			return "", fmt.Errorf("send chunk: %w", err)
		}
		rest = rest[n:]
	}
	if _, err := conn.Write([]byte{0, 0, 0, 0}); err != nil {
		return "", fmt.Errorf("end stream: %w", err)
	}
	reply, err := io.ReadAll(io.LimitReader(conn, maxReplySize))
	if err != nil {
		return "", fmt.Errorf("read reply: %w", err)
	}
	return string(bytes.TrimRight(reply, "\x00\n")), nil
}

// parseReply reads clamd's "stream: <signature> FOUND", "stream: OK" or "... ERROR" line.
func parseReply(reply string) Result {
	body := strings.TrimSpace(strings.TrimPrefix(reply, "stream:"))
	switch {
	case body == "OK":
		return Result{Status: StatusClean}
	case strings.HasSuffix(body, " FOUND"):
		return Result{StatusInfected, strings.TrimSuffix(body, " FOUND")}
	case body == "":
		return Result{StatusError, "empty reply from clamd"}
	}
	return Result{StatusError, "clamd: " + body}
}
