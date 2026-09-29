package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"echoo/internal/metrics"
	"echoo/internal/storage"
)

type probeStore struct {
	storage.Store
	err error
}

func (p *probeStore) Probe(context.Context) error { return p.err }

func get(t *testing.T, srv *Server, path string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec
}

func TestReadyzChecksSchemaAndStorage(t *testing.T) {
	h := newHarness(t)
	if rec := get(t, h.srv, "/readyz"); rec.Code != 200 {
		t.Fatalf("healthy: %d %s", rec.Code, rec.Body)
	}

	broken := &probeStore{err: errors.New("read-only file system")}
	srv := New(h.srv.cfg, h.pool, h.auth, h.srv.web, WithStorage(broken))
	rec := get(t, srv, "/readyz")
	if rec.Code != 503 || !strings.Contains(rec.Body.String(), "storage not writable") {
		t.Fatalf("read-only storage: %d %s", rec.Code, rec.Body)
	}
	if strings.Contains(rec.Body.String(), "read-only file system") {
		t.Error("the cause belongs in the log, not in a public response")
	}
	if rec := get(t, srv, "/healthz"); rec.Code != 200 {
		t.Fatalf("healthz must stay trivial: %d", rec.Code)
	}

	srv = New(h.srv.cfg, h.pool, h.auth, h.srv.web, WithStorage(&probeStore{}))
	if _, err := h.pool.Exec(context.Background(), `UPDATE goose_db_version SET is_applied = false WHERE version_id = (SELECT max(version_id) FROM goose_db_version)`); err != nil {
		t.Fatal(err)
	}
	rec = get(t, srv, "/readyz")
	if rec.Code != 503 || !strings.Contains(rec.Body.String(), "schema out of date") {
		t.Fatalf("behind schema: %d %s", rec.Code, rec.Body)
	}
}

func TestReadinessCachesChecks(t *testing.T) {
	var rd readiness
	calls := 0
	run := func(problem string) func() string {
		return func() string { calls++; return problem }
	}
	now := time.Now()
	if rd.result(now, run("")) != "" || rd.result(now.Add(29*time.Second), run("x")) != "" || calls != 1 {
		t.Fatalf("a healthy result is cached for 30 s (calls %d)", calls)
	}
	if rd.result(now.Add(31*time.Second), run("down")) != "down" || calls != 2 {
		t.Fatal("the cache expires after 30 s")
	}
	if rd.result(now.Add(33*time.Second), run("x")) != "down" || calls != 2 {
		t.Fatal("a failure is cached briefly")
	}
	if rd.result(now.Add(37*time.Second), run("")) != "" || calls != 3 {
		t.Fatal("a failure is rechecked after 5 s so recovery shows quickly")
	}
}

func TestRequestsAreCountedByRoutePattern(t *testing.T) {
	h := newHarness(t)
	reg := metrics.New()
	srv := New(h.srv.cfg, h.pool, h.auth, h.srv.web, WithMetrics(reg))
	get(t, srv, "/healthz")
	get(t, srv, "/api/v1/conversations/0199a000-0000-7000-8000-000000000000")
	get(t, srv, "/nonexistent/customer@example.com")

	rec := httptest.NewRecorder()
	reg.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	out := rec.Body.String()
	for _, want := range []string{
		`echoo_http_requests_total{route="/healthz",class="2xx"} 1`,
		`echoo_http_requests_total{route="/api/v1/conversations/{id}",class="4xx"} 1`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("metrics lack %q", want)
		}
	}
	if strings.Contains(out, "customer@example.com") || strings.Contains(out, "0199a000") {
		t.Error("raw paths must never become labels")
	}
}
