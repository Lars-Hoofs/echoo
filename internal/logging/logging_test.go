package logging

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"
)

func logger(buf *bytes.Buffer) *slog.Logger {
	return slog.New(NewHandler(slog.NewJSONHandler(buf, nil)))
}

func decode(t *testing.T, buf *bytes.Buffer) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(buf.Bytes(), &m); err != nil {
		t.Fatalf("%v: %s", err, buf)
	}
	return m
}

func TestRequestIDIsAddedFromContext(t *testing.T) {
	var buf bytes.Buffer
	ctx := WithRequestID(context.Background(), "abc123")
	logger(&buf).WarnContext(ctx, "something", "count", 3)
	if m := decode(t, &buf); m["request_id"] != "abc123" || m["count"] != float64(3) {
		t.Fatalf("record = %v", m)
	}

	buf.Reset()
	logger(&buf).WarnContext(ctx, "something", "request_id", "explicit")
	if m := decode(t, &buf); m["request_id"] != "explicit" || strings.Count(buf.String(), "request_id") != 1 {
		t.Fatalf("an explicit request_id must win and not repeat: %s", buf.String())
	}

	buf.Reset()
	logger(&buf).Warn("no request")
	if strings.Contains(buf.String(), "request_id") {
		t.Fatalf("no request, no id: %s", buf.String())
	}
}

func TestAddressesAreRemovedFromValuesAndErrors(t *testing.T) {
	var buf bytes.Buffer
	l := logger(&buf).With("component", "x", "who", "sanne@example.org")
	l.Error("delivery failed",
		"error", errors.New("550 5.1.1 <jan.de-vries+tag@mail.example.com> user unknown"),
		"detail", "sent to a@b.nl and c@d.nl",
		slog.Group("g", slog.String("to", "x@y.com")),
		"count", 2)
	out := buf.String()
	for _, leaked := range []string{"jan.de-vries", "example.com", "a@b.nl", "c@d.nl", "x@y.com", "sanne@"} {
		if strings.Contains(out, leaked) {
			t.Errorf("log line leaks %q: %s", leaked, out)
		}
	}
	if m := decode(t, &buf); m["count"] != float64(2) || !strings.Contains(m["error"].(string), "user unknown") {
		t.Errorf("the rest of the line must survive: %v", m)
	}
}

type recipient struct{ addr string }

func (r recipient) String() string { return "to " + r.addr }

func TestAddressesAreRemovedFromMessagesStringersAndStringSlices(t *testing.T) {
	var buf bytes.Buffer
	logger(&buf).Error("no mailbox for lena@example.org",
		"who", recipient{"pim@example.net"},
		"all", []string{"a@b.nl", "plain"})
	out := buf.String()
	for _, leaked := range []string{"lena@", "pim@", "a@b.nl", "example.org", "example.net"} {
		if strings.Contains(out, leaked) {
			t.Errorf("log line leaks %q: %s", leaked, out)
		}
	}
	if m := decode(t, &buf); !strings.Contains(m["who"].(string), "to [address]") || m["all"].([]any)[1] != "plain" {
		t.Errorf("the rest of the line must survive: %v", m)
	}
}
