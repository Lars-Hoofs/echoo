package api

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"echoo/internal/db"
	"echoo/internal/storage"
)

func (s *Server) healthz(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

const (
	readyCacheOK   = 30 * time.Second
	readyCacheFail = 5 * time.Second
)

// readiness caches the checks that touch storage and read the schema version, so a load
// balancer polling /readyz every second does not turn into a write per second.
type readiness struct {
	mu        sync.Mutex
	checkedAt time.Time
	problem   string
}

func (rd *readiness) result(now time.Time, run func() string) string {
	rd.mu.Lock()
	defer rd.mu.Unlock()
	ttl := readyCacheOK
	if rd.problem != "" {
		ttl = readyCacheFail
	}
	if rd.checkedAt.IsZero() || now.Sub(rd.checkedAt) >= ttl {
		rd.problem = run()
		rd.checkedAt = now
	}
	return rd.problem
}

// readyz is for load balancers: the database answers, the schema is the one this binary
// expects, and the blob store accepts writes. healthz stays trivial so a slow dependency never
// gets the container killed.
func (s *Server) readyz(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	if err := s.pool.Ping(ctx); err != nil {
		slog.WarnContext(r.Context(), "readiness: database unreachable", "err", err, "request_id", requestIDFrom(r.Context()))
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "database unavailable"})
		return
	}
	problem := s.ready.result(time.Now(), func() string { return s.checkDependencies(ctx) })
	if problem != "" {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"status": problem})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// checkDependencies returns a short reason when something other than the database is wrong.
func (s *Server) checkDependencies(ctx context.Context) string {
	want, err := db.ExpectedSchemaVersion()
	if err != nil {
		slog.ErrorContext(ctx, "readiness: embedded migrations", "err", err)
		return "schema check failed"
	}
	got, err := db.AppliedSchemaVersion(ctx, s.pool)
	if err != nil {
		slog.WarnContext(ctx, "readiness: schema version", "err", err)
		return "schema unreadable"
	}
	if got != want {
		slog.WarnContext(ctx, "readiness: schema version mismatch", "applied", fmt.Sprint(got), "expected", fmt.Sprint(want))
		return "schema out of date"
	}
	if p, ok := s.store.(storage.Prober); ok {
		if err := p.Probe(ctx); err != nil {
			slog.WarnContext(ctx, "readiness: storage not writable", "err", err)
			return "storage not writable"
		}
	}
	return ""
}
