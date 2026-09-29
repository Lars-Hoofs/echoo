package scan

import (
	"bytes"
	"context"
	"encoding/binary"
	"io"
	"net"
	"strings"
	"testing"
	"time"
)

// fakeClamd accepts one connection at a time, records the streamed bytes and answers reply.
func fakeClamd(t *testing.T, reply string) (addr string, received func() []byte) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	got := make(chan []byte, 8)
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer func() { _ = conn.Close() }()
				cmd := make([]byte, len("zINSTREAM\x00"))
				if _, err := io.ReadFull(conn, cmd); err != nil || string(cmd) != "zINSTREAM\x00" {
					return
				}
				var body bytes.Buffer
				for {
					var n [4]byte
					if _, err := io.ReadFull(conn, n[:]); err != nil {
						return
					}
					size := binary.BigEndian.Uint32(n[:])
					if size == 0 {
						break
					}
					if _, err := io.CopyN(&body, conn, int64(size)); err != nil {
						return
					}
				}
				got <- body.Bytes()
				_, _ = conn.Write([]byte(reply))
			}()
		}
	}()
	return ln.Addr().String(), func() []byte { return <-got }
}

func TestClamdScan(t *testing.T) {
	big := bytes.Repeat([]byte("x"), 3*chunkSize+17)
	tests := []struct {
		name   string
		reply  string
		data   []byte
		status string
		detail string
	}{
		{"clean", "stream: OK\x00", []byte("hello"), StatusClean, ""},
		{"clean multi chunk", "stream: OK\x00", big, StatusClean, ""},
		{"infected", "stream: Eicar-Test-Signature FOUND\x00", []byte("eicar"), StatusInfected, "Eicar-Test-Signature"},
		{"clamd error", "INSTREAM size limit exceeded. ERROR\x00", []byte("x"), StatusError, "clamd: INSTREAM size limit exceeded. ERROR"},
		{"empty reply", "", []byte("x"), StatusError, "empty reply from clamd"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			addr, received := fakeClamd(t, tc.reply)
			res := Clamd{Addr: addr}.Scan(context.Background(), tc.data)
			if res.Status != tc.status || res.Detail != tc.detail {
				t.Fatalf("got %+v, want %s %q", res, tc.status, tc.detail)
			}
			if got := received(); !bytes.Equal(got, tc.data) {
				t.Fatalf("clamd received %d bytes, want %d", len(got), len(tc.data))
			}
		})
	}
}

func TestClamdUnreachableIsError(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	res := Clamd{Addr: addr}.Scan(context.Background(), []byte("x"))
	if res.Status != StatusError || !strings.Contains(res.Detail, "connect to clamd") {
		t.Fatalf("got %+v", res)
	}
}

func TestClamdSkipsOversizedFile(t *testing.T) {
	res := Clamd{Addr: "127.0.0.1:1"}.Scan(context.Background(), make([]byte, MaxScanBytes+1))
	if res.Status != StatusError || res.Detail != "file too large to scan" {
		t.Fatalf("got %+v", res)
	}
}

func TestParseAddr(t *testing.T) {
	for in, want := range map[string]string{"clamav:3310": "clamav:3310", "tcp://clamav:3310": "clamav:3310", "tcp://[::1]:3310": "[::1]:3310"} {
		got, err := ParseAddr(in)
		if err != nil || got != want {
			t.Errorf("ParseAddr(%q) = %q, %v", in, got, err)
		}
	}
	for _, bad := range []string{"", "clamav", "tcp://:3310", "unix:///run/clamd.sock", "http://clamav:3310"} {
		if _, err := ParseAddr(bad); err == nil {
			t.Errorf("ParseAddr(%q) accepted", bad)
		}
	}
}

// A clamd that accepts the connection and never answers must end in an error within the scan
// timeout instead of holding the caller.
func TestClamdThatNeverAnswersTimesOut(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	t.Cleanup(func() { close(done); _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() { <-done; _ = c.Close() }()
		}
	}()
	started := time.Now()
	res := Clamd{Addr: ln.Addr().String(), ScanTimeout: 200 * time.Millisecond}.Scan(context.Background(), []byte("data"))
	if res.Status != StatusError {
		t.Fatalf("result = %+v, want error", res)
	}
	if took := time.Since(started); took > 3*time.Second {
		t.Errorf("scan took %s, want about the 200ms timeout", took)
	}
}
