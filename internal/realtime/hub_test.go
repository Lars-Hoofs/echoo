package realtime

import (
	"bufio"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"echoo/internal/db/dbq"
	"echoo/internal/testdb"
)

func TestMain(m *testing.M) { testdb.Main(m) }

const (
	mailboxA = "11111111-1111-1111-1111-111111111111"
	mailboxB = "22222222-2222-2222-2222-222222222222"
	convA    = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
	convB    = "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"
	userSam  = "33333333-3333-3333-3333-333333333333"
	userAnn  = "44444444-4444-4444-4444-444444444444"
)

func quietLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

type refreshState struct {
	mu        sync.Mutex
	mailboxes []string
	err       error
}

func (r *refreshState) set(m []string, err error) {
	r.mu.Lock()
	r.mailboxes, r.err = m, err
	r.mu.Unlock()
}

func (r *refreshState) fn(context.Context) ([]string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.mailboxes, r.err
}

// stream is an open SSE connection; lines arrive on lines until the body closes.
type stream struct {
	resp  *http.Response
	lines chan string
}

// connect opens a stream for the user with the given mailboxes against a test server that
// calls Hub.Serve, translating the hub's sentinel errors like the real handler does.
func connect(t *testing.T, srv *httptest.Server, user string, mailboxes []string, lastID string) *stream {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, srv.URL+"/?user="+user+"&mailboxes="+url.QueryEscape(strings.Join(mailboxes, ",")), nil)
	if err != nil {
		t.Fatal(err)
	}
	if lastID != "" {
		req.Header.Set("Last-Event-ID", lastID)
	}
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	s := &stream{resp: resp, lines: make(chan string, 1024)}
	go func() {
		defer close(s.lines)
		sc := bufio.NewScanner(resp.Body)
		for sc.Scan() {
			s.lines <- sc.Text()
		}
	}()
	return s
}

func (s *stream) next(t *testing.T, timeout time.Duration) (string, bool) {
	t.Helper()
	select {
	case l, ok := <-s.lines:
		return l, ok
	case <-time.After(timeout):
		return "", false
	}
}

// data returns the next data line, skipping ids, retry and comments.
func (s *stream) data(t *testing.T) string {
	t.Helper()
	deadline := time.After(3 * time.Second)
	for {
		select {
		case l, ok := <-s.lines:
			if !ok {
				t.Fatal("stream closed while waiting for data")
			}
			if strings.HasPrefix(l, "data: ") {
				return strings.TrimPrefix(l, "data: ")
			}
		case <-deadline:
			t.Fatal("no data line within 3s")
		}
	}
}

func (s *stream) expectNoData(t *testing.T) {
	t.Helper()
	deadline := time.After(150 * time.Millisecond)
	for {
		select {
		case l := <-s.lines:
			if strings.HasPrefix(l, "data: ") {
				t.Fatalf("unexpected event %s", l)
			}
		case <-deadline:
			return
		}
	}
}

func newServer(t *testing.T, hub *Hub, refresh RefreshFunc, cfg func(*httptest.Server)) *httptest.Server {
	t.Helper()
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		var mailboxes []string
		if v := q.Get("mailboxes"); v != "" {
			mailboxes = strings.Split(v, ",")
		}
		rf := refresh
		if rf == nil {
			rf = func(context.Context) ([]string, error) { return mailboxes, nil }
		}
		err := hub.Serve(w, r, Identity{UserID: q.Get("user"), Name: "N-" + q.Get("user")[:2], Mailboxes: mailboxes}, rf)
		switch {
		case errors.Is(err, ErrTooManyStreams):
			http.Error(w, "too many", http.StatusTooManyRequests)
		case errors.Is(err, ErrClosed):
			http.Error(w, "closed", http.StatusServiceUnavailable)
		}
	}))
	if cfg != nil {
		cfg(srv)
	}
	srv.Start()
	t.Cleanup(srv.Close)
	return srv
}

func newTestHub(opts Options) *Hub { return NewHub(nil, quietLog(), opts) }

func TestFanOutIsFilteredByMailboxScope(t *testing.T) {
	hub := newTestHub(Options{})
	srv := newServer(t, hub, nil, nil)
	withA := connect(t, srv, userSam, []string{mailboxA}, "")
	withB := connect(t, srv, userAnn, []string{mailboxB}, "")
	waitForStreams(t, hub, 2)

	hub.publish(Event{Type: TypeMessageCreated, ConversationID: convB, MailboxID: mailboxB})
	hub.publish(Event{Type: TypeMessageCreated, ConversationID: convA, MailboxID: mailboxA})

	if got := withA.data(t); !strings.Contains(got, convA) {
		t.Fatalf("user with mailbox A got %s", got)
	}
	withA.expectNoData(t)
	if got := withB.data(t); !strings.Contains(got, convB) {
		t.Fatalf("user with mailbox B got %s", got)
	}
	withB.expectNoData(t)
}

func TestEventsWithoutMailboxNeverReachAnyone(t *testing.T) {
	hub := newTestHub(Options{})
	srv := newServer(t, hub, nil, nil)
	s := connect(t, srv, userSam, []string{mailboxA}, "")
	waitForStreams(t, hub, 1)

	hub.publish(Event{Type: "conversation.updated", ConversationID: convA})
	hub.publish(Event{Type: "something.new", MailboxID: mailboxB})
	s.expectNoData(t)
}

func TestNotificationsGoOnlyToTheirUser(t *testing.T) {
	hub := newTestHub(Options{})
	srv := newServer(t, hub, nil, nil)
	sam := connect(t, srv, userSam, []string{mailboxA}, "")
	ann := connect(t, srv, userAnn, []string{mailboxA}, "")
	waitForStreams(t, hub, 2)

	hub.publish(Event{Type: TypeNotification, UserID: userAnn})

	if got := ann.data(t); !strings.Contains(got, `"notification"`) {
		t.Fatalf("addressee got %s", got)
	}
	sam.expectNoData(t)
}

func TestStreamLimitPerUser(t *testing.T) {
	hub := newTestHub(Options{})
	srv := newServer(t, hub, nil, nil)
	for range maxStreamsPerUser {
		connect(t, srv, userSam, []string{mailboxA}, "")
	}
	waitForStreams(t, hub, maxStreamsPerUser)

	over := connect(t, srv, userSam, []string{mailboxA}, "")
	if over.resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("status %d, want 429", over.resp.StatusCode)
	}
	other := connect(t, srv, userAnn, []string{mailboxA}, "")
	if other.resp.StatusCode != http.StatusOK {
		t.Fatalf("another user got status %d", other.resp.StatusCode)
	}
}

func TestHeadersRetryAndHeartbeat(t *testing.T) {
	hub := newTestHub(Options{Heartbeat: 40 * time.Millisecond})
	srv := newServer(t, hub, nil, nil)
	s := connect(t, srv, userSam, []string{mailboxA}, "")

	if ct := s.resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("content type %q", ct)
	}
	if cc := s.resp.Header.Get("Cache-Control"); !strings.Contains(cc, "no-cache") {
		t.Fatalf("cache control %q", cc)
	}
	if first, _ := s.next(t, time.Second); first != "retry: 3000" {
		t.Fatalf("first line %q", first)
	}
	for {
		l, ok := s.next(t, time.Second)
		if !ok {
			t.Fatal("no heartbeat")
		}
		if l == ": ping" {
			return
		}
	}
}

func TestWriteTimeoutDoesNotKillTheStream(t *testing.T) {
	hub := newTestHub(Options{Heartbeat: 100 * time.Millisecond, WriteTimeout: time.Second})
	srv := newServer(t, hub, nil, func(s *httptest.Server) {
		s.Config.WriteTimeout = 300 * time.Millisecond
	})
	s := connect(t, srv, userSam, []string{mailboxA}, "")
	waitForStreams(t, hub, 1)

	time.Sleep(900 * time.Millisecond)
	hub.publish(Event{Type: TypeMessageCreated, ConversationID: convA, MailboxID: mailboxA})
	if got := s.data(t); !strings.Contains(got, convA) {
		t.Fatalf("event after the server write timeout: %s", got)
	}
}

func lastEventID(t *testing.T, s *stream) string {
	t.Helper()
	deadline := time.After(3 * time.Second)
	var id string
	for {
		select {
		case l, ok := <-s.lines:
			if !ok {
				t.Fatal("stream closed")
			}
			if v, found := strings.CutPrefix(l, "id: "); found {
				id = v
			}
			if strings.HasPrefix(l, "data: ") {
				return id
			}
		case <-deadline:
			t.Fatal("no event")
		}
	}
}

func TestLastEventIDReplaysWhatWasMissed(t *testing.T) {
	hub := newTestHub(Options{})
	srv := newServer(t, hub, nil, nil)
	first := connect(t, srv, userSam, []string{mailboxA}, "")
	waitForStreams(t, hub, 1)
	hub.publish(Event{Type: TypeMessageCreated, ConversationID: convA, MailboxID: mailboxA})
	seen := lastEventID(t, first)
	_ = first.resp.Body.Close()

	hub.publish(Event{Type: TypeMessageUpdated, ConversationID: convA, MailboxID: mailboxA})
	hub.publish(Event{Type: TypeMessageCreated, ConversationID: convB, MailboxID: mailboxB})
	hub.publish(Event{Type: TypeConversationUpdated, ConversationID: convA, MailboxID: mailboxA})

	again := connect(t, srv, userSam, []string{mailboxA}, seen)
	if got := again.data(t); !strings.Contains(got, TypeMessageUpdated) {
		t.Fatalf("first replayed event %s", got)
	}
	if got := again.data(t); !strings.Contains(got, TypeConversationUpdated) {
		t.Fatalf("second replayed event %s (mailbox B must stay hidden)", got)
	}
	again.expectNoData(t)
}

func TestLastEventIDThatCannotBeServedResyncs(t *testing.T) {
	hub := newTestHub(Options{})
	srv := newServer(t, hub, nil, nil)
	for i := range queueSize + 10 {
		hub.publish(Event{Type: TypeMessageCreated, ConversationID: convA, MailboxID: mailboxA, Version: int64(i)})
	}
	cases := map[string]string{
		"fell out of the buffer": hub.frameID(1),
		"other server epoch":     "zzzz-5",
		"garbage":                "nonsense",
		"from the future":        hub.frameID(hub.head + 5),
	}
	for name, id := range cases {
		t.Run(name, func(t *testing.T) {
			s := connect(t, srv, userSam, []string{mailboxA}, id)
			if got := s.data(t); !strings.Contains(got, TypeResync) {
				t.Fatalf("got %s, want resync", got)
			}
		})
	}
}

func TestScopeRefreshFiltersAndAnnouncesChange(t *testing.T) {
	hub := newTestHub(Options{ScopeRefresh: time.Hour})
	state := &refreshState{mailboxes: []string{mailboxA}}
	srv := newServer(t, hub, state.fn, nil)
	s := connect(t, srv, userSam, []string{mailboxA}, "")
	waitForStreams(t, hub, 1)

	state.set([]string{mailboxB}, nil)
	hub.publish(Event{Type: TypeScopeChanged})
	if got := s.data(t); !strings.Contains(got, TypeResync) {
		t.Fatalf("scope change must trigger a resync, got %s", got)
	}
	hub.publish(Event{Type: TypeMessageCreated, ConversationID: convA, MailboxID: mailboxA})
	hub.publish(Event{Type: TypeMessageCreated, ConversationID: convB, MailboxID: mailboxB})
	if got := s.data(t); !strings.Contains(got, convB) {
		t.Fatalf("after losing mailbox A got %s", got)
	}
	s.expectNoData(t)
}

func TestRefreshErrorEndsTheStream(t *testing.T) {
	hub := newTestHub(Options{ScopeRefresh: 30 * time.Millisecond})
	state := &refreshState{mailboxes: []string{mailboxA}, err: errors.New("session revoked")}
	srv := newServer(t, hub, state.fn, nil)
	s := connect(t, srv, userSam, []string{mailboxA}, "")
	deadline := time.After(2 * time.Second)
	for {
		select {
		case _, ok := <-s.lines:
			if !ok {
				return
			}
		case <-deadline:
			t.Fatal("stream stayed open after the refresh failed")
		}
	}
}

func TestSlowClientIsDisconnectedNotBlocking(t *testing.T) {
	hub := newTestHub(Options{})
	sub := newSubscriber(Identity{UserID: userSam, Mailboxes: []string{mailboxA}})
	if err := hub.attach(sub, ""); err != nil {
		t.Fatal(err)
	}
	for range queueSize + 1 {
		hub.publish(Event{Type: TypeMessageCreated, ConversationID: convA, MailboxID: mailboxA})
	}
	select {
	case <-sub.done:
	default:
		t.Fatal("subscriber with a full queue was not closed")
	}
}

func TestCloseEndsStreamsAndRefusesNewOnes(t *testing.T) {
	hub := newTestHub(Options{})
	srv := newServer(t, hub, nil, nil)
	s := connect(t, srv, userSam, []string{mailboxA}, "")
	waitForStreams(t, hub, 1)

	hub.Close()
	deadline := time.After(2 * time.Second)
	for open := true; open; {
		select {
		case _, open = <-s.lines:
		case <-deadline:
			t.Fatal("stream still open after Close")
		}
	}
	if late := connect(t, srv, userSam, []string{mailboxA}, ""); late.resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status %d after close", late.resp.StatusCode)
	}
}

func TestOnlineListsUsersOnce(t *testing.T) {
	hub := newTestHub(Options{})
	srv := newServer(t, hub, nil, nil)
	connect(t, srv, userAnn, nil, "")
	connect(t, srv, userAnn, nil, "")
	connect(t, srv, userSam, nil, "")
	waitForStreams(t, hub, 3)

	got := hub.Online()
	if len(got) != 2 || got[0].ID != userSam || got[1].ID != userAnn {
		t.Fatalf("online = %+v", got)
	}
}

type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

func TestPresenceExpires(t *testing.T) {
	clock := &fakeClock{t: time.Unix(1_700_000_000, 0)}
	hub := newTestHub(Options{Now: clock.now})
	viewing := Event{Type: TypePresence, ConversationID: convA, MailboxID: mailboxA, UserID: userSam, Name: "Sam", State: StateViewing}

	hub.publish(viewing)
	if got := hub.Viewers(convA); len(got) != 1 || got[0].Name != "Sam" || got[0].Typing {
		t.Fatalf("viewers = %+v", got)
	}

	clock.advance(29 * time.Second)
	if len(hub.Viewers(convA)) != 1 {
		t.Fatal("viewer expired before the 30 s TTL")
	}
	hub.publish(viewing)
	clock.advance(29 * time.Second)
	if len(hub.Viewers(convA)) != 1 {
		t.Fatal("heartbeat did not extend the TTL")
	}
	clock.advance(2 * time.Second)
	if got := hub.Viewers(convA); len(got) != 0 {
		t.Fatalf("viewer outlived the TTL: %+v", got)
	}
}

func TestPresenceTypingExpiresBeforeViewing(t *testing.T) {
	clock := &fakeClock{t: time.Unix(1_700_000_000, 0)}
	hub := newTestHub(Options{Now: clock.now})
	hub.publish(Event{Type: TypePresence, ConversationID: convA, UserID: userSam, Name: "Sam", State: StateTyping})
	if got := hub.Viewers(convA); len(got) != 1 || !got[0].Typing {
		t.Fatalf("viewers = %+v", got)
	}
	clock.advance(typingTTL + time.Second)
	if got := hub.Viewers(convA); len(got) != 1 || got[0].Typing {
		t.Fatalf("typing should have lapsed while viewing stays: %+v", got)
	}
}

func TestPresenceLeftRemovesImmediatelyAndSweepFreesMemory(t *testing.T) {
	clock := &fakeClock{t: time.Unix(1_700_000_000, 0)}
	hub := newTestHub(Options{Now: clock.now})
	hub.publish(Event{Type: TypePresence, ConversationID: convA, UserID: userSam, Name: "Sam", State: StateViewing})
	hub.publish(Event{Type: TypePresence, ConversationID: convB, UserID: userAnn, Name: "Ann", State: StateViewing})
	hub.publish(Event{Type: TypePresence, ConversationID: convA, UserID: userSam, State: StateLeft})
	if len(hub.Viewers(convA)) != 0 {
		t.Fatal("left did not remove the viewer")
	}

	clock.advance(2 * time.Minute)
	hub.publish(Event{Type: TypePresence, ConversationID: convA, UserID: userSam, Name: "Sam", State: StateViewing})
	hub.pres.mu.Lock()
	n := len(hub.pres.byConv)
	hub.pres.mu.Unlock()
	if n != 1 {
		t.Fatalf("%d conversations tracked after the sweep, want 1", n)
	}
}

func waitForStreams(t *testing.T, hub *Hub, n int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		hub.mu.Lock()
		got := len(hub.subs)
		hub.mu.Unlock()
		if got == n {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%d streams attached, want %d", got, n)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestDecodeEventCanonicalizesAndRejects(t *testing.T) {
	ev, err := decodeEvent(`{"type":"message.created","mailbox_id":"11111111-1111-1111-1111-11111111111A","conversation_id":"` + convA + `","extra":"ignored"}`)
	if err != nil {
		t.Fatal(err)
	}
	if ev.MailboxID != strings.ToLower(ev.MailboxID) {
		t.Fatalf("mailbox id not canonical: %s", ev.MailboxID)
	}
	for _, bad := range []string{`{`, `{"type":""}`, `{"type":"x","mailbox_id":"not-a-uuid"}`} {
		if _, err := decodeEvent(bad); err == nil {
			t.Errorf("decodeEvent(%s) accepted", bad)
		}
	}
}

func TestNotifyRejectsOversizedPayload(t *testing.T) {
	err := Notify(t.Context(), failingNotifier{}, Event{Type: strings.Repeat("x", 8000)})
	if err == nil || !strings.Contains(err.Error(), "limit") {
		t.Fatalf("err = %v", err)
	}
}

type failingNotifier struct{}

func (failingNotifier) RealtimeNotify(context.Context, string) error {
	return errors.New("must not be called")
}

func TestListenerDeliversNotifyAndResyncsAfterReconnect(t *testing.T) {
	pool := testdb.New(t)
	hub := NewHub(pool, quietLog(), Options{})
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() { hub.Run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })

	srv := newServer(t, hub, nil, nil)
	s := connect(t, srv, userSam, []string{mailboxA}, "")
	waitForStreams(t, hub, 1)
	q := dbq.New(pool)

	notifyUntilSeen(t, q, s, Event{Type: TypeMessageCreated, ConversationID: convA, MailboxID: mailboxA}, convA)
	// Retries above may have queued duplicates; let them arrive before checking silence.
	time.Sleep(200 * time.Millisecond)
	for len(s.lines) > 0 {
		<-s.lines
	}
	if err := Notify(ctx, q, Event{Type: TypeMessageCreated, ConversationID: convB, MailboxID: mailboxB}); err != nil {
		t.Fatal(err)
	}
	s.expectNoData(t)

	terminateListener(t, pool)
	deadline := time.After(10 * time.Second)
	for {
		select {
		case l := <-s.lines:
			if strings.Contains(l, TypeResync) {
				return
			}
		case <-deadline:
			t.Fatal("no resync after the listener reconnected")
		}
	}
}

// notifyUntilSeen tolerates the small window before the LISTEN is active.
func notifyUntilSeen(t *testing.T, q *dbq.Queries, s *stream, ev Event, want string) {
	t.Helper()
	for range 50 {
		if err := Notify(t.Context(), q, ev); err != nil {
			t.Fatal(err)
		}
		if l, ok := s.next(t, 100*time.Millisecond); ok && strings.Contains(l, want) {
			return
		}
	}
	t.Fatalf("event for %s never arrived", want)
}

func terminateListener(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	_, err := pool.Exec(t.Context(), `SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE query = 'LISTEN `+Channel+`' AND pid <> pg_backend_pid()`)
	if err != nil {
		t.Fatal(err)
	}
}
