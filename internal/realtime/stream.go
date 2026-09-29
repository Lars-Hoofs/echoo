package realtime

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"net/http"
	"sync"
	"time"
)

// Identity is who a stream belongs to and which mailboxes they may read.
type Identity struct {
	UserID    string
	Name      string
	Mailboxes []string
}

// RefreshFunc reloads the mailboxes the user may read. An error ends the stream, which
// fails closed: the client reconnects and is authenticated again.
type RefreshFunc func(ctx context.Context) ([]string, error)

type subscriber struct {
	userID  string
	name    string
	mu      sync.RWMutex
	scope   map[string]struct{}
	queue   chan frame
	refresh chan struct{}
	done    chan struct{}
	once    sync.Once
}

func newSubscriber(id Identity) *subscriber {
	s := &subscriber{
		userID: id.UserID, name: id.Name,
		queue:   make(chan frame, queueSize),
		refresh: make(chan struct{}, 1),
		done:    make(chan struct{}),
	}
	s.setScope(id.Mailboxes)
	return s
}

func (s *subscriber) setScope(mailboxes []string) {
	set := make(map[string]struct{}, len(mailboxes))
	for _, m := range mailboxes {
		set[m] = struct{}{}
	}
	s.mu.Lock()
	s.scope = set
	s.mu.Unlock()
}

func (s *subscriber) close() { s.once.Do(func() { close(s.done) }) }

func (s *subscriber) requestRefresh() {
	select {
	case s.refresh <- struct{}{}:
	default:
	}
}

// offer never blocks the hub: a client that falls a full queue behind is disconnected and
// catches up through Last-Event-ID replay or a resync.
func (s *subscriber) offer(f frame) {
	select {
	case s.queue <- f:
	default:
		s.close()
	}
}

// sees decides whether an event may reach this user. Anything that is neither global nor
// addressed to the user must name a mailbox in scope, so unknown shapes fail closed.
func (s *subscriber) sees(ev Event) bool {
	switch ev.Type {
	case TypeResync:
		return true
	case TypeNotification:
		return ev.UserID == s.userID
	}
	if ev.MailboxID == "" {
		return false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	_, ok := s.scope[ev.MailboxID]
	return ok
}

func (h *Hub) attach(s *subscriber, lastEventID string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return ErrClosed
	}
	if h.byUser[s.userID] >= maxStreamsPerUser {
		return ErrTooManyStreams
	}
	if lastEventID != "" {
		if frames, ok := h.replay(lastEventID); ok {
			for _, f := range frames {
				if s.sees(f.ev) {
					s.offer(f)
				}
			}
		} else {
			ev := Event{Type: TypeResync}
			s.offer(frame{id: h.frameID(h.head), ev: ev, data: encode(ev)})
		}
	}
	h.subs[s] = struct{}{}
	h.byUser[s.userID]++
	return nil
}

func (h *Hub) detach(s *subscriber) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.subs, s)
	if h.byUser[s.userID]--; h.byUser[s.userID] <= 0 {
		delete(h.byUser, s.userID)
	}
}

func encode(ev Event) []byte {
	// Event has only string and integer fields, which cannot fail to encode.
	b, _ := json.Marshal(ev)
	return b
}

// Serve streams events to one client until it disconnects, the hub closes or the user's
// access ends. It returns ErrTooManyStreams or ErrClosed before writing anything; after the
// headers are out, a write error only means the client went away and ends the stream quietly.
func (h *Hub) Serve(w http.ResponseWriter, r *http.Request, id Identity, refresh RefreshFunc) error {
	s := newSubscriber(id)
	if err := h.attach(s, r.Header.Get("Last-Event-ID")); err != nil {
		return err
	}
	defer h.detach(s)

	rc := http.NewResponseController(w)
	// The server's WriteTimeout would kill a long-lived stream, so every write gets its own
	// deadline instead of removing the timeout for the whole server.
	write := func(b []byte) bool {
		if err := rc.SetWriteDeadline(h.opts.Now().Add(h.opts.WriteTimeout)); err != nil && !errors.Is(err, http.ErrNotSupported) {
			return false
		}
		if _, err := w.Write(b); err != nil {
			return false
		}
		return rc.Flush() == nil
	}

	hd := w.Header()
	hd.Set("Content-Type", "text/event-stream")
	hd.Set("Cache-Control", "no-cache, no-store")
	hd.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	if !write([]byte("retry: 3000\n\n")) {
		return nil
	}

	heartbeat := time.NewTicker(h.opts.Heartbeat)
	defer heartbeat.Stop()
	scopeTick := time.NewTicker(h.opts.ScopeRefresh)
	defer scopeTick.Stop()

	for {
		select {
		case <-r.Context().Done():
			return nil
		case <-s.done:
			return nil
		case f := <-s.queue:
			if !write(formatFrame(f)) {
				return nil
			}
		case <-heartbeat.C:
			if !write([]byte(": ping\n\n")) {
				return nil
			}
		case <-s.refresh:
			if err := h.refreshScope(r.Context(), s, refresh, write); err != nil {
				return err
			}
		case <-scopeTick.C:
			if err := h.refreshScope(r.Context(), s, refresh, write); err != nil {
				return err
			}
		}
	}
}

func (h *Hub) refreshScope(ctx context.Context, s *subscriber, refresh RefreshFunc, write func([]byte) bool) error {
	mailboxes, err := refresh(ctx)
	if err != nil {
		if ctx.Err() != nil {
			return nil
		}
		return err
	}
	next := make(map[string]struct{}, len(mailboxes))
	for _, m := range mailboxes {
		next[m] = struct{}{}
	}
	s.mu.RLock()
	same := maps.Equal(s.scope, next)
	s.mu.RUnlock()
	if same {
		return nil
	}
	s.setScope(mailboxes)
	// Lists the client already holds may now contain or miss conversations.
	ev := Event{Type: TypeResync}
	write(formatFrame(frame{ev: ev, data: encode(ev)}))
	return nil
}

func formatFrame(f frame) []byte {
	b := make([]byte, 0, len(f.data)+64)
	if f.id != "" {
		b = append(b, "id: "...)
		b = append(b, f.id...)
		b = append(b, '\n')
	}
	b = append(b, "data: "...)
	b = append(b, f.data...)
	return append(b, '\n', '\n')
}
