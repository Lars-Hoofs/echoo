package metrics

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"echoo/internal/testdb"
)

func TestMain(m *testing.M) { testdb.Main(m) }

func scrape(t *testing.T, r *Registry) string {
	t.Helper()
	rec := httptest.NewRecorder()
	r.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if rec.Code != 200 {
		t.Fatalf("status %d", rec.Code)
	}
	return rec.Body.String()
}

func TestRequestMetricsUseRoutePatternAndStatusClass(t *testing.T) {
	r := New()
	r.ObserveRequest("/api/v1/conversations/{id}", 200, 30*time.Millisecond)
	r.ObserveRequest("/api/v1/conversations/{id}", 404, time.Millisecond)
	out := scrape(t, r)
	for _, want := range []string{
		`echoo_http_requests_total{route="/api/v1/conversations/{id}",class="2xx"} 1`,
		`echoo_http_requests_total{route="/api/v1/conversations/{id}",class="4xx"} 1`,
		`echoo_http_request_duration_seconds_bucket{route="/api/v1/conversations/{id}",class="2xx",le="0.05"} 1`,
		`echoo_http_request_duration_seconds_count{route="/api/v1/conversations/{id}",class="2xx"} 1`,
		"go_goroutines",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q", want)
		}
	}
}

func TestCollectExposesDatabaseState(t *testing.T) {
	pool := testdb.New(t)
	ctx := context.Background()
	for _, sql := range []string{
		`INSERT INTO mailboxes (name, email_address, sync_state) VALUES ('A', 'a@example.com', 'connected'), ('B', 'b@example.com', 'auth_failed'), ('C', 'c@example.com', 'connected')`,
		`INSERT INTO raw_messages (mailbox_id, source, sha256, size_bytes, blob_key, parse_status)
			SELECT id, 'imap', 'x'::bytea, 1, 'k', 'failed' FROM mailboxes WHERE name = 'A'`,
		`INSERT INTO river_job (kind, state, args, max_attempts, queue, priority) VALUES ('mail.parse', 'available', '{}', 3, 'default', 1)`,
	} {
		if _, err := pool.Exec(ctx, sql); err != nil {
			t.Fatal(err)
		}
	}
	r := New()
	r.TrackPool(pool)
	r.TrackStreams(func() int { return 3 })
	if err := r.NewCollector(pool).Collect(ctx); err != nil {
		t.Fatal(err)
	}
	out := scrape(t, r)
	for _, want := range []string{
		`echoo_imap_mailboxes{sync_state="connected"} 2`,
		`echoo_imap_mailboxes{sync_state="auth_failed"} 1`,
		`echoo_raw_messages{parse_status="failed"} 1`,
		`echoo_jobs{kind="mail.parse",state="available"} 1`,
		`echoo_sse_streams_open 3`,
		`echoo_db_pool_connections_max 10`,
		`echoo_metrics_collect_errors_total 0`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q\n%s", want, out)
		}
	}
	for _, forbidden := range []string{"example.com", "@"} {
		if strings.Contains(out, forbidden) {
			t.Errorf("output must not contain %q", forbidden)
		}
	}
}

func TestFailedCollectionKeepsOldNumbersAndCountsTheError(t *testing.T) {
	pool := testdb.New(t)
	r := New()
	c := r.NewCollector(pool)
	if err := c.Collect(context.Background()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := c.Collect(ctx); err == nil {
		t.Fatal("collect with a cancelled context should fail")
	}
	if out := scrape(t, r); !strings.Contains(out, "echoo_metrics_collect_errors_total 1") {
		t.Errorf("error not counted:\n%s", out)
	}
}
