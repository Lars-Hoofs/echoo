package webhooks

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"

	"echoo/internal/db/dbq"
	"echoo/internal/jobs"
	"echoo/internal/keyring"
	"echoo/internal/testdb"
)

func TestMain(m *testing.M) { testdb.Main(m) }

type fakeEnqueuer struct {
	mu  sync.Mutex
	ids []string
}

func (f *fakeEnqueuer) InsertTx(_ context.Context, _ pgx.Tx, args river.JobArgs, _ *river.InsertOpts) (*rivertype.JobInsertResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ids = append(f.ids, args.(jobs.WebhookDeliver).DeliveryID)
	return &rivertype.JobInsertResult{}, nil
}

type receiver struct {
	mu       sync.Mutex
	requests []received
	status   int
	delay    time.Duration
	location string
}

type received struct {
	header http.Header
	body   []byte
}

func (r *receiver) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	body, _ := io.ReadAll(req.Body)
	r.mu.Lock()
	r.requests = append(r.requests, received{header: req.Header.Clone(), body: body})
	status, delay, location := r.status, r.delay, r.location
	r.mu.Unlock()
	time.Sleep(delay)
	if location != "" {
		w.Header().Set("Location", location)
	}
	w.WriteHeader(status)
}

func (r *receiver) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.requests)
}

type env struct {
	t        *testing.T
	pool     *pgxpool.Pool
	q        *dbq.Queries
	keys     *keyring.Keyring
	recv     *receiver
	srv      *httptest.Server
	worker   *DeliverWorker
	clock    time.Time
	hookID   pgtype.UUID
	secret   string
	enqueuer *fakeEnqueuer
}

func newEnv(t *testing.T) *env {
	t.Helper()
	pool := testdb.New(t)
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	keys, err := keyring.Parse("k1:" + base64.StdEncoding.EncodeToString(key))
	if err != nil {
		t.Fatal(err)
	}
	recv := &receiver{status: http.StatusOK}
	srv := httptest.NewServer(recv)
	t.Cleanup(srv.Close)
	e := &env{t: t, pool: pool, q: dbq.New(pool), keys: keys, recv: recv, srv: srv, clock: time.Now(), secret: "whsec_test", enqueuer: &fakeEnqueuer{}}
	// The default client refuses the loopback address the test receiver listens on.
	e.worker = NewDeliverWorker(DeliverDeps{Pool: pool, Keyring: keys, Client: srv.Client(), AnyURL: true, Clock: func() time.Time { return e.clock }})
	e.hookID = e.addWebhook(srv.URL, false, Events...)
	return e
}

func (e *env) addWebhook(url string, includeContent bool, events ...string) pgtype.UUID {
	e.t.Helper()
	id, err := e.q.NewUUID(context.Background())
	if err != nil {
		e.t.Fatal(err)
	}
	enc, err := e.keys.Encrypt([]byte(e.secret), SecretAAD(id))
	if err != nil {
		e.t.Fatal(err)
	}
	_, err = e.q.InsertWebhook(context.Background(), dbq.InsertWebhookParams{
		ID: id, Url: url, SecretEnc: enc, Events: events, IncludeContent: includeContent, AllowHttp: true,
	})
	if err != nil {
		e.t.Fatal(err)
	}
	return id
}

func (e *env) exec(sql string, args ...any) {
	e.t.Helper()
	if _, err := e.pool.Exec(context.Background(), sql, args...); err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) scan(dst any, sql string, args ...any) {
	e.t.Helper()
	if err := e.pool.QueryRow(context.Background(), sql, args...).Scan(dst); err != nil {
		e.t.Fatal(err)
	}
}

type fixture struct {
	mailbox, conversation, message, contact pgtype.UUID
}

func (e *env) fixture() fixture {
	e.t.Helper()
	var f fixture
	e.scan(&f.mailbox, `INSERT INTO mailboxes (name, email_address) VALUES ('Support', 'help@example.com') RETURNING id`)
	e.scan(&f.contact, `INSERT INTO contacts (name) VALUES ('Jane Doe') RETURNING id`)
	e.exec(`INSERT INTO contact_addresses (contact_id, email, is_primary) VALUES ($1, 'jane@example.org', true)`, f.contact)
	e.scan(&f.conversation, `INSERT INTO conversations (mailbox_id, subject, contact_id) VALUES ($1, 'Order 1234', $2) RETURNING id`, f.mailbox, f.contact)
	e.scan(&f.message, `INSERT INTO messages (conversation_id, mailbox_id, kind, direction, subject, body_text)
		VALUES ($1, $2, 'email', 'in', 'Order 1234', 'Where   is my
		parcel?') RETURNING id`, f.conversation, f.mailbox)
	return f
}

// record writes the event and runs the fan-out, returning the delivery IDs it scheduled.
func (e *env) record(ev Event) []pgtype.UUID {
	e.t.Helper()
	if err := Record(context.Background(), e.q, ev); err != nil {
		e.t.Fatal(err)
	}
	e.enqueuer.ids = nil
	if err := NewFanoutWorker(e.pool).Run(context.Background(), e.enqueuer); err != nil {
		e.t.Fatal(err)
	}
	out := make([]pgtype.UUID, len(e.enqueuer.ids))
	for i, s := range e.enqueuer.ids {
		if err := out[i].Scan(s); err != nil {
			e.t.Fatal(err)
		}
	}
	return out
}

func (e *env) work(id pgtype.UUID, attempt int) error {
	return e.worker.Work(context.Background(), &river.Job[jobs.WebhookDeliver]{
		JobRow: &rivertype.JobRow{Attempt: attempt}, Args: jobs.WebhookDeliver{DeliveryID: id.String()},
	})
}

func (e *env) delivery(id pgtype.UUID) dbq.WebhookDelivery {
	e.t.Helper()
	d, err := e.q.GetWebhookDelivery(context.Background(), id)
	if err != nil {
		e.t.Fatal(err)
	}
	return d
}

func (e *env) webhook() dbq.Webhook {
	e.t.Helper()
	h, err := e.q.GetWebhook(context.Background(), e.hookID)
	if err != nil {
		e.t.Fatal(err)
	}
	return h
}

func TestSignature(t *testing.T) {
	ts := time.Unix(1_700_000_000, 0)
	got := Sign([]byte("secret"), ts, []byte(`{"a":1}`))
	mac := hmac.New(sha256.New, []byte("secret"))
	mac.Write([]byte(`1700000000.{"a":1}`))
	want := "t=1700000000,v1=" + hex.EncodeToString(mac.Sum(nil))
	if got != want {
		t.Errorf("signature = %s, want %s", got, want)
	}
}

func TestRecordOnlyForSubscribedEvents(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	if err := Record(ctx, e.q, Event{Type: "conversation.exploded"}); !errors.Is(err, ErrUnknownEvent) {
		t.Fatalf("unknown type: err = %v", err)
	}
	e.exec(`UPDATE webhooks SET events = ARRAY['message.created']`)
	if err := Record(ctx, e.q, Event{Type: ContactCreated}); err != nil {
		t.Fatal(err)
	}
	var n int
	e.scan(&n, `SELECT count(*) FROM webhook_events`)
	if n != 0 {
		t.Errorf("events without subscriber = %d, want 0", n)
	}
	e.exec(`UPDATE webhooks SET enabled = false`)
	if err := Record(ctx, e.q, Event{Type: MessageCreated}); err != nil {
		t.Fatal(err)
	}
	e.scan(&n, `SELECT count(*) FROM webhook_events`)
	if n != 0 {
		t.Errorf("events for a disabled webhook = %d, want 0", n)
	}
}

func TestRecordIsPartOfTheTransaction(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	tx, err := e.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := Record(ctx, dbq.New(tx), Event{Type: ContactCreated}); err != nil {
		t.Fatal(err)
	}
	if err := tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	var n int
	e.scan(&n, `SELECT count(*) FROM webhook_events`)
	if n != 0 {
		t.Errorf("events after rollback = %d, want 0", n)
	}
}

func TestFanoutCreatesOneDeliveryPerSubscribedWebhook(t *testing.T) {
	e := newEnv(t)
	e.addWebhook(e.srv.URL+"/other", false, MessageCreated)
	e.addWebhook(e.srv.URL+"/unrelated", false, ContactCreated)
	e.exec(`INSERT INTO webhooks (url, secret_enc, events, enabled) VALUES ('https://off.example.com', '\x00', ARRAY['message.created'], false)`)
	f := e.fixture()

	ids := e.record(Event{Type: MessageCreated, MailboxID: f.mailbox, ConversationID: f.conversation, MessageID: f.message})
	if len(ids) != 2 {
		t.Fatalf("deliveries = %d, want 2 (subscribed and enabled)", len(ids))
	}
	var pending int
	e.scan(&pending, `SELECT count(*) FROM webhook_events WHERE fanned_out_at IS NULL`)
	if pending != 0 {
		t.Errorf("event still pending after fan-out")
	}
	// Running again finds nothing to do.
	e.enqueuer.ids = nil
	if err := NewFanoutWorker(e.pool).Run(context.Background(), e.enqueuer); err != nil || len(e.enqueuer.ids) != 0 {
		t.Errorf("second run: err = %v, deliveries = %d", err, len(e.enqueuer.ids))
	}
}

func TestDeliveryPayloadIsMinimalByDefault(t *testing.T) {
	e := newEnv(t)
	f := e.fixture()
	ids := e.record(Event{Type: MessageCreated, MailboxID: f.mailbox, ConversationID: f.conversation, MessageID: f.message})
	if err := e.work(ids[0], 1); err != nil {
		t.Fatal(err)
	}

	if e.recv.count() != 1 {
		t.Fatalf("requests = %d", e.recv.count())
	}
	req := e.recv.requests[0]
	if req.header.Get("Content-Type") != "application/json" || req.header.Get("Echoo-Event") != "message.created" ||
		req.header.Get("Echoo-Delivery") != ids[0].String() {
		t.Errorf("headers = %v", req.header)
	}
	sig := req.header.Get("Echoo-Signature")
	tPart, v1, _ := strings.Cut(sig, ",")
	unix := strings.TrimPrefix(tPart, "t=")
	mac := hmac.New(sha256.New, []byte(e.secret))
	mac.Write([]byte(unix + "."))
	mac.Write(req.body)
	if strings.TrimPrefix(v1, "v1=") != hex.EncodeToString(mac.Sum(nil)) {
		t.Errorf("signature does not verify: %s", sig)
	}
	if n, err := strconv.ParseInt(unix, 10, 64); err != nil || n != e.clock.Unix() {
		t.Errorf("timestamp = %s, want %d", unix, e.clock.Unix())
	}

	var p struct {
		ID        string    `json:"id"`
		Event     string    `json:"event"`
		CreatedAt time.Time `json:"created_at"`
		Data      struct {
			Message map[string]any `json:"message"`
		} `json:"data"`
	}
	if err := json.Unmarshal(req.body, &p); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(p.ID, "evt_") || p.Event != "message.created" || p.CreatedAt.IsZero() {
		t.Errorf("payload = %s", req.body)
	}
	if p.Data.Message["id"] != f.message.String() || p.Data.Message["direction"] != "inbound" || p.Data.Message["conversation_id"] != f.conversation.String() {
		t.Errorf("message = %v", p.Data.Message)
	}
	for _, private := range []string{"Order 1234", "parcel", "Jane", "jane@example.org"} {
		if strings.Contains(string(req.body), private) {
			t.Errorf("payload leaks %q without include_content: %s", private, req.body)
		}
	}

	d := e.delivery(ids[0])
	if d.Status != "succeeded" || !d.StatusCode.Valid || d.StatusCode.Int32 != 200 || d.Attempt != 1 || d.Error != "" || len(d.PayloadHash) != 32 {
		t.Errorf("delivery = %+v", d)
	}
}

func TestDeliveryPayloadWithContent(t *testing.T) {
	e := newEnv(t)
	e.exec(`UPDATE webhooks SET include_content = true`)
	f := e.fixture()

	msgIDs := e.record(Event{Type: MessageCreated, MailboxID: f.mailbox, ConversationID: f.conversation, MessageID: f.message})
	convIDs := e.record(Event{Type: ConversationCreated, MailboxID: f.mailbox, ConversationID: f.conversation})
	contactIDs := e.record(Event{Type: ContactCreated, ContactID: f.contact})
	for _, id := range append(append(msgIDs, convIDs...), contactIDs...) {
		if err := e.work(id, 1); err != nil {
			t.Fatal(err)
		}
	}
	bodies := ""
	for _, r := range e.recv.requests {
		bodies += string(r.body) + "\n"
	}
	for _, want := range []string{`"subject":"Order 1234"`, `"preview":"Where is my parcel?"`, `"email":"jane@example.org"`, `"name":"Jane Doe"`, `"status":"open"`} {
		if !strings.Contains(bodies, want) {
			t.Errorf("payloads lack %s:\n%s", want, bodies)
		}
	}
}

func TestPayloadOfDeletedEntityHasOnlyItsID(t *testing.T) {
	e := newEnv(t)
	f := e.fixture()
	ids := e.record(Event{Type: MessageCreated, MailboxID: f.mailbox, ConversationID: f.conversation, MessageID: f.message})
	e.exec(`DELETE FROM messages`)
	if err := e.work(ids[0], 1); err != nil {
		t.Fatal(err)
	}
	if body := string(e.recv.requests[0].body); !strings.Contains(body, `"message":{"id":"`+f.message.String()+`"}`) {
		t.Errorf("payload = %s", body)
	}
}

func TestPayloadMarksATrashedConversation(t *testing.T) {
	e := newEnv(t)
	f := e.fixture()
	ids := e.record(Event{Type: ConversationUpdated, MailboxID: f.mailbox, ConversationID: f.conversation})
	ids = append(ids, e.record(Event{Type: ConversationUpdated, MailboxID: f.mailbox, ConversationID: f.conversation})...)
	if err := e.work(ids[0], 1); err != nil {
		t.Fatal(err)
	}
	e.exec(`UPDATE conversations SET deleted_at = now()`)
	if err := e.work(ids[1], 1); err != nil {
		t.Fatal(err)
	}
	if body := string(e.recv.requests[0].body); strings.Contains(body, `"deleted"`) {
		t.Errorf("payload of a live conversation = %s", body)
	}
	if body := string(e.recv.requests[1].body); !strings.Contains(body, `"deleted":true`) {
		t.Errorf("payload of a trashed conversation = %s", body)
	}
}

func TestFailureRetriesThenGivesUp(t *testing.T) {
	e := newEnv(t)
	e.recv.status = http.StatusInternalServerError
	ids := e.record(Event{Type: ContactCreated, ContactID: e.fixture().contact})

	for attempt := 1; attempt < MaxAttempts; attempt++ {
		if err := e.work(ids[0], attempt); err == nil {
			t.Fatalf("attempt %d: want an error so River retries", attempt)
		}
		d := e.delivery(ids[0])
		if d.Status != "retrying" || d.StatusCode.Int32 != 500 || d.Error != ErrHTTPStatus || int(d.Attempt) != attempt {
			t.Fatalf("attempt %d: delivery = %+v", attempt, d)
		}
	}
	if err := e.work(ids[0], MaxAttempts); err != nil {
		t.Fatalf("last attempt: %v, want nil so the job is not retried", err)
	}
	if d := e.delivery(ids[0]); d.Status != "failed" || int(d.Attempt) != MaxAttempts {
		t.Errorf("delivery = %+v", d)
	}
	if got := e.webhook().ConsecutiveFailures; int(got) != MaxAttempts {
		t.Errorf("consecutive failures = %d, want %d", got, MaxAttempts)
	}

	e.recv.status = http.StatusNoContent
	if err := e.work(ids[0], 1); err != nil {
		t.Fatal(err)
	}
	if d := e.delivery(ids[0]); d.Status != "succeeded" {
		t.Errorf("delivery after resend = %+v", d)
	}
	if got := e.webhook().ConsecutiveFailures; got != 0 {
		t.Errorf("consecutive failures after success = %d, want 0", got)
	}
}

func TestBackoffSchedule(t *testing.T) {
	e := newEnv(t)
	want := []time.Duration{30 * time.Second, 2 * time.Minute, 10 * time.Minute, time.Hour, 6 * time.Hour, 6 * time.Hour}
	for i, d := range want {
		job := &river.Job[jobs.WebhookDeliver]{JobRow: &rivertype.JobRow{Attempt: i + 1}}
		if got := e.worker.NextRetry(job).Sub(e.clock); got != d {
			t.Errorf("after attempt %d: %v, want %v", i+1, got, d)
		}
	}
}

func TestAutoDisableAfterFiftyConsecutiveFailures(t *testing.T) {
	e := newEnv(t)
	e.recv.status = http.StatusBadGateway
	e.exec(`UPDATE webhooks SET consecutive_failures = $1`, FailureThreshold-1)
	f := e.fixture()
	ids := e.record(Event{Type: ContactCreated, ContactID: f.contact})

	if err := e.work(ids[0], 1); err != nil {
		t.Fatalf("the failure that disables the webhook must not schedule a retry: %v", err)
	}
	h := e.webhook()
	if h.Enabled || h.DisabledReason != "too_many_failures" || h.ConsecutiveFailures != FailureThreshold {
		t.Errorf("webhook = enabled %v reason %q failures %d", h.Enabled, h.DisabledReason, h.ConsecutiveFailures)
	}
	if d := e.delivery(ids[0]); d.Status != "failed" {
		t.Errorf("delivery status = %s, want failed", d.Status)
	}
	var audited int
	e.scan(&audited, `SELECT count(*) FROM audit_log WHERE action = 'webhook.auto_disabled' AND target_id = $1`, h.ID.String())
	if audited != 1 {
		t.Errorf("audit entries = %d, want 1", audited)
	}

	// A queued delivery for a disabled webhook is not sent.
	before := e.recv.count()
	e.exec(`INSERT INTO webhook_events (type, contact_id, fanned_out_at) VALUES ('contact.created', $1, now())`, f.contact)
	var evID int64
	e.scan(&evID, `SELECT max(id) FROM webhook_events`)
	del, err := e.q.InsertWebhookDelivery(context.Background(), dbq.InsertWebhookDeliveryParams{WebhookID: e.hookID, EventID: evID, Event: ContactCreated})
	if err != nil {
		t.Fatal(err)
	}
	if err := e.work(del.ID, 1); err != nil {
		t.Fatal(err)
	}
	if got := e.delivery(del.ID); got.Status != "failed" || got.Error != ErrWebhookOff || e.recv.count() != before {
		t.Errorf("delivery = %+v, requests %d -> %d", got, before, e.recv.count())
	}
}

func TestTestMessageBypassesDisabledAndDoesNotCountAsFailure(t *testing.T) {
	e := newEnv(t)
	e.exec(`UPDATE webhooks SET enabled = false, disabled_reason = 'too_many_failures'`)
	evID, err := e.q.InsertTestWebhookEvent(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	del, err := e.q.InsertWebhookDelivery(context.Background(), dbq.InsertWebhookDeliveryParams{WebhookID: e.hookID, EventID: evID, Event: Test})
	if err != nil {
		t.Fatal(err)
	}

	if err := e.work(del.ID, 1); err != nil {
		t.Fatal(err)
	}
	if d := e.delivery(del.ID); d.Status != "succeeded" {
		t.Errorf("test delivery = %+v", d)
	}
	if !strings.Contains(string(e.recv.requests[0].body), `"event":"webhook.test"`) {
		t.Errorf("body = %s", e.recv.requests[0].body)
	}

	e.recv.status = http.StatusInternalServerError
	failing, err := e.q.InsertWebhookDelivery(context.Background(), dbq.InsertWebhookDeliveryParams{WebhookID: e.hookID, EventID: evID, Event: Test})
	if err != nil {
		t.Fatal(err)
	}
	if err := e.work(failing.ID, 1); err == nil {
		t.Fatal("failing test delivery should still be retried by River")
	}
	if got := e.webhook().ConsecutiveFailures; got != 0 {
		t.Errorf("a failing test counted towards the failure streak: %d", got)
	}
}

func TestDefaultClientRefusesInternalDestinations(t *testing.T) {
	e := newEnv(t)
	e.worker = NewDeliverWorker(DeliverDeps{Pool: e.pool, Keyring: e.keys, AnyURL: true, Clock: func() time.Time { return e.clock }})
	ids := e.record(Event{Type: ContactCreated, ContactID: e.fixture().contact})

	if err := e.work(ids[0], 1); err == nil {
		t.Fatal("want an error")
	}
	d := e.delivery(ids[0])
	if d.Error != ErrBlocked || e.recv.count() != 0 {
		t.Errorf("delivery error = %q, receiver got %d requests", d.Error, e.recv.count())
	}

	// A hostname that resolves to loopback is refused on the connected address as well.
	e.exec(`UPDATE webhooks SET url = 'http://localhost:' || split_part($1, ':', 3)`, e.srv.URL)
	if err := e.work(ids[0], 2); err == nil {
		t.Fatal("want an error")
	}
	if d := e.delivery(ids[0]); d.Error != ErrBlocked || e.recv.count() != 0 {
		t.Errorf("localhost: error = %q, requests %d", d.Error, e.recv.count())
	}
}

func TestDeliveryRechecksSchemeAndPort(t *testing.T) {
	cases := []struct {
		name, url string
		allowHTTP bool
	}{
		{"plain http without the flag", "http://receiver.example.com/hook", false},
		{"odd port", "https://receiver.example.com:9999/hook", true},
		{"other scheme", "ftp://receiver.example.com/hook", true},
		{"userinfo", "https://user:pw@receiver.example.com/hook", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			e.worker = NewDeliverWorker(DeliverDeps{Pool: e.pool, Keyring: e.keys, Client: e.srv.Client(), Clock: func() time.Time { return e.clock }})
			e.exec(`UPDATE webhooks SET url = $1, allow_http = $2`, tc.url, tc.allowHTTP)
			ids := e.record(Event{Type: ContactCreated, ContactID: e.fixture().contact})

			if err := e.work(ids[0], 1); err != nil {
				t.Fatalf("a refused URL must not be retried: %v", err)
			}
			d := e.delivery(ids[0])
			if d.Status != "failed" || d.Error != ErrURLNotAllowed || e.recv.count() != 0 {
				t.Errorf("status %q, error %q, receiver got %d requests", d.Status, d.Error, e.recv.count())
			}
		})
	}
}

func TestCheckURL(t *testing.T) {
	for raw, want := range map[string]string{
		"https://a.example.com/x":      "",
		"https://a.example.com:8443/x": "",
		"http://a.example.com/x":       "https_required",
		"https://a.example.com:80/x":   "port_not_allowed",
		"https://a.example.com:22/x":   "port_not_allowed",
		"gopher://a.example.com/x":     "invalid",
		"https://u@a.example.com/x":    "invalid",
	} {
		if got := CheckURL(raw, false); got != want {
			t.Errorf("CheckURL(%q) = %q, want %q", raw, got, want)
		}
	}
	if got := CheckURL("http://a.example.com/x", true); got != "" {
		t.Errorf("http with allow_http = %q", got)
	}
}

func TestRedirectsAreNotFollowed(t *testing.T) {
	e := newEnv(t)
	target := &receiver{status: http.StatusOK}
	targetSrv := httptest.NewServer(target)
	t.Cleanup(targetSrv.Close)
	e.recv.status, e.recv.location = http.StatusFound, targetSrv.URL
	e.worker = NewDeliverWorker(DeliverDeps{Pool: e.pool, Keyring: e.keys, Client: newHTTPClient(true), AnyURL: true, Clock: func() time.Time { return e.clock }})
	ids := e.record(Event{Type: ContactCreated, ContactID: e.fixture().contact})

	if err := e.work(ids[0], 1); err == nil {
		t.Fatal("want an error")
	}
	if d := e.delivery(ids[0]); d.Status != "retrying" || d.StatusCode.Int32 != 302 {
		t.Errorf("delivery = %+v", d)
	}
	if target.count() != 0 {
		t.Error("the redirect target was contacted")
	}
}

func TestSlowReceiverTimesOut(t *testing.T) {
	e := newEnv(t)
	e.recv.delay = 500 * time.Millisecond
	client := *e.srv.Client()
	client.Timeout = 100 * time.Millisecond
	e.worker = NewDeliverWorker(DeliverDeps{Pool: e.pool, Keyring: e.keys, Client: &client, AnyURL: true, Clock: func() time.Time { return e.clock }})
	ids := e.record(Event{Type: ContactCreated, ContactID: e.fixture().contact})

	if err := e.work(ids[0], 1); err == nil {
		t.Fatal("want an error")
	}
	if d := e.delivery(ids[0]); d.Error != ErrTimeout {
		t.Errorf("error = %q, want %q", d.Error, ErrTimeout)
	}
}

func TestErrorTextNeverHoldsTheURL(t *testing.T) {
	e := newEnv(t)
	e.exec(`UPDATE webhooks SET url = 'http://127.0.0.1:1/secret-path?token=hunter2'`)
	ids := e.record(Event{Type: ContactCreated, ContactID: e.fixture().contact})
	_ = e.work(ids[0], 1)
	d := e.delivery(ids[0])
	if d.Error == "" || strings.Contains(d.Error, "hunter2") || strings.Contains(d.Error, "127.0.0.1") {
		t.Errorf("error = %q", d.Error)
	}
}

func TestPurgeRemovesOldLogsOnly(t *testing.T) {
	e := newEnv(t)
	f := e.fixture()
	old := e.record(Event{Type: ContactCreated, ContactID: f.contact})
	recent := e.record(Event{Type: ContactCreated, ContactID: f.contact})
	e.exec(`UPDATE webhook_deliveries SET created_at = now() - interval '15 days' WHERE id = $1`, old[0])
	e.exec(`UPDATE webhook_events SET occurred_at = now() - interval '15 days' WHERE id = (SELECT event_id FROM webhook_deliveries WHERE id = $1)`, old[0])

	if err := NewPurgeWorker(e.pool).Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	var oldN, recentN, events int
	e.scan(&oldN, `SELECT count(*) FROM webhook_deliveries WHERE id = $1`, old[0])
	e.scan(&recentN, `SELECT count(*) FROM webhook_deliveries WHERE id = $1`, recent[0])
	e.scan(&events, `SELECT count(*) FROM webhook_events`)
	if oldN != 0 || recentN != 1 || events != 1 {
		t.Errorf("old = %d, recent = %d, events = %d, want 0, 1, 1", oldN, recentN, events)
	}
}

func TestPurgeKeepsUnprocessedEvents(t *testing.T) {
	e := newEnv(t)
	if err := Record(context.Background(), e.q, Event{Type: ContactCreated, ContactID: e.fixture().contact}); err != nil {
		t.Fatal(err)
	}
	e.exec(`UPDATE webhook_events SET occurred_at = now() - interval '30 days'`)
	if err := NewPurgeWorker(e.pool).Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	var n int
	e.scan(&n, `SELECT count(*) FROM webhook_events`)
	if n != 1 {
		t.Errorf("unprocessed events after purge = %d, want 1", n)
	}
}
