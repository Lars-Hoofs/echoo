package webhooks

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"

	"echoo/internal/audit"
	"echoo/internal/db"
	"echoo/internal/db/dbq"
	"echoo/internal/jobs"
	"echoo/internal/keyring"
	"echoo/internal/netguard"
)

const (
	requestTimeout = 10 * time.Second
	// MaxAttempts counts the first try, so len(backoff)+1.
	MaxAttempts = 6
	// FailureThreshold is the number of consecutive failed attempts after which a webhook is
	// disabled.
	FailureThreshold = 50
	// Retention is how long the delivery log and processed outbox events are kept.
	Retention = 14 * 24 * time.Hour
	// maxResponseRead bounds what is read (and then thrown away) from a receiver.
	maxResponseRead = 64 << 10
)

var backoff = [MaxAttempts - 1]time.Duration{30 * time.Second, 2 * time.Minute, 10 * time.Minute, time.Hour, 6 * time.Hour}

// Failure codes stored in webhook_deliveries.error. They are deliberately generic: Go's own
// errors contain the request URL, which may carry a secret in its path or query.
const (
	ErrHTTPStatus    = "http_status"
	ErrBlocked       = "blocked_destination"
	ErrTimeout       = "timeout"
	ErrTLS           = "tls_error"
	ErrDNS           = "dns_error"
	ErrConnection    = "connection_failed"
	ErrURLNotAllowed = "url_not_allowed"
	ErrWebhookOff    = "webhook_disabled"
	errBuildPayload  = "payload_failed"
)

// NewHTTPClient returns the client used for deliveries. The dial-time check refuses internal
// addresses on the IP actually connected to (DNS rebinding), redirects are not followed and
// no proxy is used.
func NewHTTPClient() *http.Client { return newHTTPClient(false) }

// newHTTPClient exists so tests can reach a receiver on loopback while keeping every other
// behavior of the production client.
func newHTTPClient(allowInternal bool) *http.Client {
	dialer := netguard.Dialer(allowInternal, 5*time.Second)
	return &http.Client{
		Timeout:       requestTimeout,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		Transport: &http.Transport{
			DialContext:            dialer.DialContext,
			TLSHandshakeTimeout:    5 * time.Second,
			DisableKeepAlives:      true,
			MaxResponseHeaderBytes: 64 << 10,
		},
	}
}

// CheckURL applies the scheme and port policy to a webhook URL and returns a field error
// code, or "" when both are acceptable: https on 443 or 8443, and http (port 80) only when
// allowHTTP is set. The host is judged separately, on the address that is connected to.
func CheckURL(raw string, allowHTTP bool) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.User != nil || u.Opaque != "" {
		return "invalid"
	}
	switch {
	case u.Scheme == "https":
	case u.Scheme == "http" && allowHTTP:
	case u.Scheme == "http":
		return "https_required"
	default:
		return "invalid"
	}
	port := u.Port()
	if port == "" {
		port = map[string]string{"https": "443", "http": "80"}[u.Scheme]
	}
	if !slices.Contains([]string{"443", "8443", "80"}, port) || (port == "80" && u.Scheme == "https") {
		return "port_not_allowed"
	}
	return ""
}

// Sign returns the Echoo-Signature header value: t=<unix>,v1=<hex HMAC-SHA256 of "t.body">.
func Sign(secret []byte, t time.Time, body []byte) string {
	ts := strconv.FormatInt(t.Unix(), 10)
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(ts + "."))
	mac.Write(body)
	return "t=" + ts + ",v1=" + hex.EncodeToString(mac.Sum(nil))
}

// SecretAAD is the additional data the webhook secret is encrypted with.
func SecretAAD(id pgtype.UUID) []byte { return keyring.AAD("webhooks", "secret_enc", id.String()) }

type DeliverDeps struct {
	Pool    *pgxpool.Pool
	Keyring *keyring.Keyring
	// Client defaults to NewHTTPClient.
	Client *http.Client
	// AnyURL skips the scheme and port policy of CheckURL, for tests whose receiver listens
	// on a random loopback port.
	AnyURL bool
	Clock  func() time.Time
}

type DeliverWorker struct {
	river.WorkerDefaults[jobs.WebhookDeliver]
	d DeliverDeps
	q *dbq.Queries
}

func NewDeliverWorker(d DeliverDeps) *DeliverWorker {
	if d.Client == nil {
		d.Client = NewHTTPClient()
	}
	if d.Clock == nil {
		d.Clock = time.Now
	}
	return &DeliverWorker{d: d, q: dbq.New(d.Pool)}
}

func (w *DeliverWorker) Timeout(*river.Job[jobs.WebhookDeliver]) time.Duration {
	return requestTimeout + 20*time.Second
}

func (w *DeliverWorker) NextRetry(job *river.Job[jobs.WebhookDeliver]) time.Time {
	i := min(max(job.Attempt-1, 0), len(backoff)-1)
	return w.d.Clock().Add(backoff[i])
}

type outcome struct {
	status   int
	errCode  string
	duration time.Duration
}

func (o outcome) ok() bool { return o.errCode == "" }

func (w *DeliverWorker) Work(ctx context.Context, job *river.Job[jobs.WebhookDeliver]) error {
	var id pgtype.UUID
	if err := id.Scan(job.Args.DeliveryID); err != nil || !id.Valid {
		return river.JobCancel(fmt.Errorf("invalid delivery id %q", job.Args.DeliveryID))
	}
	del, err := w.q.GetWebhookDelivery(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return river.JobCancel(errors.New("delivery no longer exists"))
	}
	if err != nil {
		return fmt.Errorf("load delivery: %w", err)
	}
	if del.Status == "succeeded" {
		return nil
	}
	hook, err := w.q.GetWebhook(ctx, del.WebhookID)
	if errors.Is(err, pgx.ErrNoRows) {
		return river.JobCancel(errors.New("webhook no longer exists"))
	}
	if err != nil {
		return fmt.Errorf("load webhook: %w", err)
	}
	ev, err := w.q.GetWebhookEvent(ctx, del.EventID)
	if err != nil {
		return fmt.Errorf("load event: %w", err)
	}
	attempt := int32(min(job.Attempt, 1<<30)) //nolint:gosec // bounded above
	isTest := ev.Type == Test
	if !hook.Enabled && !isTest {
		return w.finish(ctx, del, "failed", outcome{errCode: ErrWebhookOff}, nil, attempt)
	}
	secret, err := w.d.Keyring.Decrypt(hook.SecretEnc, SecretAAD(hook.ID))
	if err != nil {
		return fmt.Errorf("decrypt webhook secret: %w", err)
	}
	body, err := buildPayload(ctx, w.q, ev, hook.IncludeContent)
	if err != nil {
		return err
	}
	sum := sha256.Sum256(body)
	if !w.d.AnyURL && CheckURL(hook.Url, hook.AllowHttp) != "" {
		// The row may predate the current policy or have been edited in the database.
		return w.failWithoutRetry(ctx, del, hook, isTest, attempt, sum[:])
	}
	res := w.post(ctx, hook.Url, secret, del, ev.Type, body)
	if res.ok() {
		if err := w.finish(ctx, del, "succeeded", res, sum[:], attempt); err != nil {
			return err
		}
		if err := w.q.RecordWebhookSuccess(ctx, hook.ID); err != nil {
			return fmt.Errorf("record webhook success: %w", err)
		}
		return nil
	}

	status := "retrying"
	final := int(attempt) >= MaxAttempts
	if !isTest {
		disabled, err := w.recordFailure(ctx, hook)
		if err != nil {
			return err
		}
		final = final || disabled
	}
	if final {
		status = "failed"
	}
	if err := w.finish(ctx, del, status, res, sum[:], attempt); err != nil {
		return err
	}
	if final {
		return nil
	}
	return fmt.Errorf("webhook delivery failed: %s", res.errCode)
}

// failWithoutRetry ends a delivery to a URL the policy refuses: retrying cannot help.
func (w *DeliverWorker) failWithoutRetry(ctx context.Context, del dbq.WebhookDelivery, hook dbq.Webhook, isTest bool, attempt int32, hash []byte) error {
	if !isTest {
		if _, err := w.recordFailure(ctx, hook); err != nil {
			return err
		}
	}
	return w.finish(ctx, del, "failed", outcome{errCode: ErrURLNotAllowed}, hash, attempt)
}

// recordFailure counts the failed attempt and reports whether it disabled the webhook.
func (w *DeliverWorker) recordFailure(ctx context.Context, hook dbq.Webhook) (bool, error) {
	var disabled bool
	err := db.InTx(ctx, w.d.Pool, func(q *dbq.Queries) error {
		updated, err := q.RecordWebhookFailure(ctx, dbq.RecordWebhookFailureParams{ID: hook.ID, Threshold: FailureThreshold})
		if err != nil {
			return err
		}
		disabled = hook.Enabled && !updated.Enabled
		if !disabled {
			return nil
		}
		return audit.Write(ctx, q, audit.Entry{
			Action: audit.WebhookAutoDisabled, TargetType: "webhook", TargetID: hook.ID.String(),
			Metadata: map[string]any{"consecutive_failures": updated.ConsecutiveFailures},
		})
	})
	if err != nil {
		return false, fmt.Errorf("record webhook failure: %w", err)
	}
	return disabled, nil
}

func (w *DeliverWorker) finish(ctx context.Context, del dbq.WebhookDelivery, status string, res outcome, hash []byte, attempt int32) error {
	params := dbq.SetWebhookDeliveryResultParams{
		ID: del.ID, Status: status, Error: res.errCode, Attempt: attempt, PayloadHash: hash,
		DurationMs: pgtype.Int4{Int32: int32(min(res.duration.Milliseconds(), 1<<30)), Valid: res.duration > 0}, //nolint:gosec // bounded above
	}
	if res.status != 0 {
		params.StatusCode = pgtype.Int4{Int32: int32(res.status), Valid: true} //nolint:gosec // HTTP status codes are three digits
	}
	if err := w.q.SetWebhookDeliveryResult(ctx, params); err != nil {
		return fmt.Errorf("record delivery result: %w", err)
	}
	return nil
}

func (w *DeliverWorker) post(ctx context.Context, url string, secret []byte, del dbq.WebhookDelivery, event string, body []byte) outcome {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return outcome{errCode: ErrConnection}
	}
	now := w.d.Clock()
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "Echoo-Webhooks/1")
	req.Header.Set("Echoo-Event", event)
	req.Header.Set("Echoo-Delivery", del.ID.String())
	req.Header.Set("Echoo-Signature", Sign(secret, now, body))

	start := time.Now()
	resp, err := w.d.Client.Do(req)
	took := time.Since(start)
	if err != nil {
		return outcome{errCode: classify(err), duration: took}
	}
	defer func() { _ = resp.Body.Close() }()
	// The receiver's answer is not interesting and must not be trusted or stored.
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxResponseRead))
	took = time.Since(start)
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return outcome{status: resp.StatusCode, errCode: ErrHTTPStatus, duration: took}
	}
	return outcome{status: resp.StatusCode, duration: took}
}

func classify(err error) string {
	var (
		dnsErr  *net.DNSError
		certErr *tls.CertificateVerificationError
		recErr  tls.RecordHeaderError
	)
	switch {
	case errors.Is(err, netguard.ErrInternal):
		return ErrBlocked
	case errors.Is(err, context.DeadlineExceeded) || isTimeout(err):
		return ErrTimeout
	case errors.As(err, &certErr) || errors.As(err, &recErr):
		return ErrTLS
	case errors.As(err, &dnsErr):
		return ErrDNS
	}
	return ErrConnection
}

func isTimeout(err error) bool {
	var ne net.Error
	return errors.As(err, &ne) && ne.Timeout()
}

// Enqueuer is the part of the River client used to schedule deliveries.
type Enqueuer interface {
	InsertTx(ctx context.Context, tx pgx.Tx, args river.JobArgs, opts *river.InsertOpts) (*rivertype.JobInsertResult, error)
}

// EnqueueDelivery schedules the job for an existing delivery row. Call it in the transaction
// that created the row.
func EnqueueDelivery(ctx context.Context, enq Enqueuer, tx pgx.Tx, id pgtype.UUID) error {
	_, err := enq.InsertTx(ctx, tx, jobs.WebhookDeliver{DeliveryID: id.String()}, &river.InsertOpts{MaxAttempts: MaxAttempts})
	if err != nil {
		return fmt.Errorf("enqueue webhook delivery: %w", err)
	}
	return nil
}
