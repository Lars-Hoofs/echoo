// Package metrics exposes Prometheus metrics on a listener of its own. Labels are limited to
// values the operator controls (route patterns, states, statuses, job kinds): never user,
// address or subject data.
package metrics

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"

	vm "github.com/VictoriaMetrics/metrics"
	"github.com/jackc/pgx/v5/pgxpool"
)

var durationBuckets = []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10}

// Registry holds the metrics of one server.
type Registry struct {
	set *vm.Set

	mu   sync.Mutex
	snap snapshot
}

func New() *Registry {
	r := &Registry{set: vm.NewSet()}
	r.set.RegisterMetricsWriter(r.writeSnapshot)
	return r
}

// Class turns an HTTP status into its class, "2xx" for 200.
func Class(status int) string { return strconv.Itoa(status/100) + "xx" }

// ObserveRequest records one finished request. route is the router's pattern, never the raw
// path.
func (r *Registry) ObserveRequest(route string, status int, d time.Duration) {
	labels := fmt.Sprintf(`{route=%q,class=%q}`, route, Class(status))
	r.set.GetOrCreateCounter("echoo_http_requests_total" + labels).Inc()
	r.set.GetOrCreatePrometheusHistogramExt("echoo_http_request_duration_seconds"+labels, durationBuckets).Update(d.Seconds())
}

// TrackStreams exposes the number of open server-sent-event streams.
func (r *Registry) TrackStreams(open func() int) {
	r.set.NewGauge("echoo_sse_streams_open", func() float64 { return float64(open()) })
}

// TrackPool exposes the database connection pool.
func (r *Registry) TrackPool(pool *pgxpool.Pool) {
	gauge := func(name string, f func(*pgxpool.Stat) float64) {
		r.set.NewGauge(name, func() float64 { return f(pool.Stat()) })
	}
	gauge("echoo_db_pool_connections_total", func(s *pgxpool.Stat) float64 { return float64(s.TotalConns()) })
	gauge("echoo_db_pool_connections_idle", func(s *pgxpool.Stat) float64 { return float64(s.IdleConns()) })
	gauge("echoo_db_pool_connections_in_use", func(s *pgxpool.Stat) float64 { return float64(s.AcquiredConns()) })
	gauge("echoo_db_pool_connections_max", func(s *pgxpool.Stat) float64 { return float64(s.MaxConns()) })
	gauge("echoo_db_pool_acquires_total", func(s *pgxpool.Stat) float64 { return float64(s.AcquireCount()) })
	gauge("echoo_db_pool_acquires_waited_total", func(s *pgxpool.Stat) float64 { return float64(s.EmptyAcquireCount()) })
	gauge("echoo_db_pool_acquire_wait_seconds_total", func(s *pgxpool.Stat) float64 { return s.AcquireDuration().Seconds() })
}

// Handler serves the metrics in the Prometheus text format, followed by the Go runtime and
// process metrics.
func (r *Registry) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		r.set.WritePrometheus(w)
		vm.WriteProcessMetrics(w)
	})
}

// Serve answers scrapes on ln until ctx ends. The caller opens the listener so a bind failure
// stops startup instead of going unnoticed. It is a listener of its own: metrics are never
// reachable through the public address.
func Serve(ctx context.Context, ln net.Listener, h http.Handler) error {
	mux := http.NewServeMux()
	mux.Handle("GET /metrics", h)
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 30 * time.Second}
	errCh := make(chan error, 1)
	go func() { errCh <- srv.Serve(ln) }()
	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return srv.Shutdown(shutdown)
	}
}

// snapshot is the database-derived part, refreshed by Collect.
type snapshot struct {
	syncStates    map[string]int64
	outbound      map[string]int64
	rawMessages   map[string]int64
	webhooks      map[string]int64
	jobs          map[[2]string]int64
	collectedAt   time.Time
	collectErrors int64
}

func (r *Registry) writeSnapshot(w io.Writer) {
	r.mu.Lock()
	s := r.snap
	r.mu.Unlock()
	writeByLabel := func(name, label string, m map[string]int64) {
		for k, v := range m {
			vm.WriteGaugeUint64(w, fmt.Sprintf(`%s{%s=%q}`, name, label, k), uint64(v)) //nolint:gosec // counts are never negative
		}
	}
	writeByLabel("echoo_imap_mailboxes", "sync_state", s.syncStates)
	writeByLabel("echoo_outbound_messages", "status", s.outbound)
	writeByLabel("echoo_raw_messages", "parse_status", s.rawMessages)
	writeByLabel("echoo_webhook_deliveries", "status", s.webhooks)
	for k, v := range s.jobs {
		vm.WriteGaugeUint64(w, fmt.Sprintf(`echoo_jobs{kind=%q,state=%q}`, k[0], k[1]), uint64(v)) //nolint:gosec // see above
	}
	vm.WriteCounterUint64(w, "echoo_metrics_collect_errors_total", uint64(s.collectErrors)) //nolint:gosec // see above
	if !s.collectedAt.IsZero() {
		vm.WriteGaugeFloat64(w, "echoo_metrics_collected_timestamp_seconds", float64(s.collectedAt.Unix()))
	}
}

// Collector refreshes the database-derived metrics.
type Collector struct {
	r    *Registry
	pool *pgxpool.Pool
}

func (r *Registry) NewCollector(pool *pgxpool.Pool) *Collector { return &Collector{r: r, pool: pool} }

// Run collects now and then every interval until ctx ends. A failed collection keeps the
// previous numbers and counts an error, so a database hiccup shows up as a metric.
func (c *Collector) Run(ctx context.Context, interval time.Duration) {
	c.collectAndLog(ctx)
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			c.collectAndLog(ctx)
		}
	}
}

func (c *Collector) collectAndLog(ctx context.Context) {
	if err := c.Collect(ctx); err != nil && ctx.Err() == nil {
		slog.WarnContext(ctx, "collect metrics", "err", err)
	}
}

// Collect reads the current counts once.
func (c *Collector) Collect(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	next := snapshot{collectedAt: time.Now()}
	var err error
	if next.syncStates, err = c.counts(ctx, `SELECT sync_state, count(*) FROM mailboxes WHERE disabled_at IS NULL GROUP BY 1`); err != nil {
		return c.fail(err)
	}
	if next.outbound, err = c.counts(ctx, `SELECT status, count(*) FROM outbound GROUP BY 1`); err != nil {
		return c.fail(err)
	}
	if next.rawMessages, err = c.counts(ctx, `SELECT parse_status, count(*) FROM raw_messages WHERE parse_status IN ('pending', 'failed') GROUP BY 1`); err != nil {
		return c.fail(err)
	}
	if next.webhooks, err = c.counts(ctx, `SELECT status, count(*) FROM webhook_deliveries GROUP BY 1`); err != nil {
		return c.fail(err)
	}
	next.jobs = map[[2]string]int64{}
	rows, err := c.pool.Query(ctx, `SELECT kind, state::text, count(*) FROM river_job GROUP BY 1, 2`)
	if err != nil {
		return c.fail(err)
	}
	defer rows.Close()
	for rows.Next() {
		var kind, state string
		var n int64
		if err := rows.Scan(&kind, &state, &n); err != nil {
			return c.fail(err)
		}
		next.jobs[[2]string{kind, state}] = n
	}
	if err := rows.Err(); err != nil {
		return c.fail(err)
	}
	c.r.mu.Lock()
	next.collectErrors = c.r.snap.collectErrors
	c.r.snap = next
	c.r.mu.Unlock()
	return nil
}

func (c *Collector) fail(err error) error {
	c.r.mu.Lock()
	c.r.snap.collectErrors++
	c.r.mu.Unlock()
	return err
}

func (c *Collector) counts(ctx context.Context, sql string) (map[string]int64, error) {
	rows, err := c.pool.Query(ctx, sql)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int64{}
	for rows.Next() {
		var k string
		var n int64
		if err := rows.Scan(&k, &n); err != nil {
			return nil, err
		}
		out[k] = n
	}
	return out, rows.Err()
}
