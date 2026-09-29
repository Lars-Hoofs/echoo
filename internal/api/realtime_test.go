package api

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"
)

type sse struct {
	resp  *http.Response
	lines chan string
}

func (h *harness) startHub(t *testing.T) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { h.srv.Realtime().Run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
}

func (c *client) events(t *testing.T) *sse {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, c.h.ts.URL+"/api/v1/events", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-Forwarded-For", c.ip)
	resp, err := c.http.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	s := &sse{resp: resp, lines: make(chan string, 256)}
	go func() {
		defer close(s.lines)
		sc := bufio.NewScanner(resp.Body)
		for sc.Scan() {
			s.lines <- sc.Text()
		}
	}()
	return s
}

func (s *sse) waitFor(t *testing.T, substr string) bool {
	t.Helper()
	deadline := time.After(3 * time.Second)
	for {
		select {
		case l, ok := <-s.lines:
			if !ok {
				return false
			}
			if strings.HasPrefix(l, "data: ") && strings.Contains(l, substr) {
				return true
			}
		case <-deadline:
			return false
		}
	}
}

func (s *sse) expectSilence(t *testing.T, substr string) {
	t.Helper()
	deadline := time.After(300 * time.Millisecond)
	for {
		select {
		case l := <-s.lines:
			if strings.HasPrefix(l, "data: ") && strings.Contains(l, substr) {
				t.Fatalf("received %s", l)
			}
		case <-deadline:
			return
		}
	}
}

func (f *inboxFixture) notify(payload string) {
	f.h.t.Helper()
	f.exec(`SELECT pg_notify('echoo_events', $1)`, payload)
}

func waitOnline(t *testing.T, c *client, id string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(string(c.do("GET", "/api/v1/agents/online", nil).raw), id) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("user %s never showed up as online", id)
}

func TestEventStreamHeadersAndScope(t *testing.T) {
	f := newInboxFixture(t)
	f.h.startHub(t)
	s := f.agent.events(t)
	if s.resp.StatusCode != 200 || s.resp.Header.Get("Content-Type") != "text/event-stream" {
		t.Fatalf("status %d, type %q", s.resp.StatusCode, s.resp.Header.Get("Content-Type"))
	}
	waitOnline(t, f.agent, f.agentID)
	time.Sleep(200 * time.Millisecond) // the hub's LISTEN connection is separate from the stream

	convA := f.conversation("a", convOpt{mailbox: f.mailboxA})
	convB := f.conversation("b", convOpt{mailbox: f.mailboxB})
	for i := 0; i < 20; i++ {
		f.notify(`{"type":"message.created","conversation_id":"` + convB + `","mailbox_id":"` + f.mailboxB + `"}`)
		f.notify(`{"type":"message.created","conversation_id":"` + convA + `","mailbox_id":"` + f.mailboxA + `"}`)
		if s.waitFor(t, convA) {
			s.expectSilence(t, convB)
			return
		}
	}
	t.Fatal("no event for the readable mailbox")
}

func TestEventStreamRequiresSession(t *testing.T) {
	h := newHarness(t)
	r := h.client().do("GET", "/api/v1/events", nil)
	expect(t, r, 401, "unauthenticated")
}

func TestEventStreamLimitPerUser(t *testing.T) {
	f := newInboxFixture(t)
	for i := 0; i < 10; i++ {
		s := f.agent.events(t)
		if s.resp.StatusCode != 200 {
			t.Fatalf("stream %d: status %d", i, s.resp.StatusCode)
		}
	}
	if s := f.agent.events(t); s.resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("11th stream: status %d", s.resp.StatusCode)
	}
	if s := f.admin.events(t); s.resp.StatusCode != 200 {
		t.Fatalf("other user: status %d", s.resp.StatusCode)
	}
}

func TestChangingMailboxAccessMakesStreamsResync(t *testing.T) {
	f := newInboxFixture(t)
	f.h.startHub(t)
	s := f.agent.events(t)
	waitOnline(t, f.agent, f.agentID)
	time.Sleep(200 * time.Millisecond)

	for i := 0; i < 20; i++ {
		r := f.admin.do("PUT", "/api/v1/mailboxes/"+f.mailboxA+"/access", map[string]any{"access": []any{}})
		expect(t, r, 204, "")
		if s.waitFor(t, `"resync"`) {
			return
		}
		r = f.admin.do("PUT", "/api/v1/mailboxes/"+f.mailboxA+"/access", map[string]any{"access": []map[string]string{{"team_id": f.team, "level": "write"}}})
		expect(t, r, 204, "")
	}
	t.Fatal("stream never resynced after the access change")
}

func TestPresenceRoundTripAndScope(t *testing.T) {
	f := newInboxFixture(t)
	f.h.startHub(t)
	conv := f.conversation("a", convOpt{mailbox: f.mailboxA})
	hidden := f.conversation("b", convOpt{mailbox: f.mailboxB})
	path := "/api/v1/conversations/" + conv + "/presence"

	expect(t, f.agent.do("POST", path, map[string]string{"state": "dancing"}), 422, "validation_failed")
	expect(t, f.agent.do("POST", "/api/v1/conversations/"+hidden+"/presence", map[string]string{"state": "viewing"}), 404, "not_found")
	expect(t, f.agent.do("GET", "/api/v1/conversations/"+hidden+"/presence", nil), 404, "not_found")
	expect(t, f.agent.do("POST", "/api/v1/conversations/not-a-uuid/presence", map[string]string{"state": "viewing"}), 404, "not_found")
	expect(t, f.h.client().do("GET", path, nil), 401, "")

	type viewers struct {
		Viewers []struct {
			UserID string `json:"user_id"`
			Name   string `json:"name"`
			Typing bool   `json:"typing"`
		} `json:"viewers"`
	}
	read := func(c *client) viewers {
		var v viewers
		if err := json.Unmarshal(c.do("GET", path, nil).raw, &v); err != nil {
			t.Fatal(err)
		}
		return v
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		expect(t, f.agent.do("POST", path, map[string]string{"state": "typing"}), 204, "")
		time.Sleep(50 * time.Millisecond)
		if v := read(f.readonly); len(v.Viewers) == 1 {
			if v.Viewers[0].UserID != f.agentID || !v.Viewers[0].Typing || v.Viewers[0].Name == "" {
				t.Fatalf("viewers = %+v", v)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("presence never arrived")
		}
	}

	expect(t, f.agent.do("POST", path, map[string]string{"state": "left"}), 204, "")
	deadline = time.Now().Add(3 * time.Second)
	for len(read(f.readonly).Viewers) != 0 {
		if time.Now().After(deadline) {
			t.Fatal("left did not clear presence")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestPresenceIsRateLimitedPerUser(t *testing.T) {
	f := newInboxFixture(t)
	conv := f.conversation("a", convOpt{mailbox: f.mailboxA})
	path := "/api/v1/conversations/" + conv + "/presence"
	limited := false
	for i := 0; i < 130; i++ {
		if f.agent.do("POST", path, map[string]string{"state": "viewing"}).status == http.StatusTooManyRequests {
			limited = true
			break
		}
	}
	if !limited {
		t.Fatal("no rate limit after 130 presence posts")
	}
	expect(t, f.admin.do("POST", path, map[string]string{"state": "viewing"}), 204, "")
}

func TestCreatingAMailboxMakesAdminStreamsResync(t *testing.T) {
	f := newInboxFixture(t)
	f.h.startHub(t)
	s := f.admin.events(t)
	deadline := time.Now().Add(3 * time.Second)
	for len(f.h.srv.Realtime().Online()) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("stream never attached")
		}
		time.Sleep(10 * time.Millisecond)
	}
	time.Sleep(200 * time.Millisecond)

	for i := 0; i < 20; i++ {
		r := f.admin.do("POST", "/api/v1/mailboxes", mailboxBody(map[string]any{"email_address": "new" + strings.Repeat("x", i+1) + "@example.com"}))
		expect(t, r, 201, "")
		if s.waitFor(t, `"resync"`) {
			return
		}
	}
	t.Fatal("admin stream never resynced after a mailbox was created")
}
