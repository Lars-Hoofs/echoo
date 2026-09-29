package realtime

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	// queueSize bounds what a slow client may lag behind. It also bounds the replay buffer,
	// so a replay always fits in a fresh queue.
	queueSize = 256

	maxStreamsPerUser = 10
	maxReconnectDelay = 30 * time.Second
)

var (
	ErrTooManyStreams = errors.New("too many open event streams for this user")
	ErrClosed         = errors.New("realtime hub is shut down")
)

// Options tune timings; zero values select the production defaults.
type Options struct {
	Heartbeat    time.Duration
	ScopeRefresh time.Duration
	WriteTimeout time.Duration
	Now          func() time.Time
}

// Agent is a user with at least one open event stream on this instance.
type Agent struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type frame struct {
	seq  uint64 // 0 for frames outside the replay sequence (presence)
	id   string
	ev   Event
	data []byte
}

// Hub listens on the NOTIFY channel and distributes events to the open streams.
type Hub struct {
	pool   *pgxpool.Pool
	log    *slog.Logger
	opts   Options
	epoch  string
	pres   *presence
	mu     sync.Mutex
	subs   map[*subscriber]struct{}
	byUser map[string]int
	ring   []frame
	head   uint64
	closed bool
}

func NewHub(pool *pgxpool.Pool, log *slog.Logger, opts Options) *Hub {
	if opts.Heartbeat == 0 {
		opts.Heartbeat = 25 * time.Second
	}
	if opts.ScopeRefresh == 0 {
		opts.ScopeRefresh = 60 * time.Second
	}
	if opts.WriteTimeout == 0 {
		opts.WriteTimeout = 10 * time.Second
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	return &Hub{
		pool: pool, log: log, opts: opts,
		// A restart resets the sequence; the epoch makes a stale Last-Event-ID detectable.
		epoch:  strconv.FormatInt(opts.Now().UnixNano(), 36),
		pres:   newPresence(opts.Now),
		subs:   map[*subscriber]struct{}{},
		byUser: map[string]int{},
	}
}

// Run listens until ctx ends, reconnecting with backoff. After every reconnect after the
// first, all clients are told to resync because notifications sent meanwhile are lost.
func (h *Hub) Run(ctx context.Context) {
	delay := time.Second
	first := true
	for ctx.Err() == nil {
		connected, err := h.listen(ctx, !first)
		if ctx.Err() != nil {
			return
		}
		if connected {
			first = false
			delay = time.Second
		}
		h.log.WarnContext(ctx, "realtime listener disconnected", "err", err, "retry_in", delay.String())
		select {
		case <-ctx.Done():
			return
		case <-time.After(delay):
		}
		delay = min(delay*2, maxReconnectDelay)
	}
}

func (h *Hub) listen(ctx context.Context, resync bool) (connected bool, err error) {
	conn, err := pgx.ConnectConfig(ctx, h.pool.Config().ConnConfig)
	if err != nil {
		return false, fmt.Errorf("connect: %w", err)
	}
	defer func() { _ = conn.Close(context.WithoutCancel(ctx)) }()
	if _, err := conn.Exec(ctx, "LISTEN "+Channel); err != nil {
		return false, fmt.Errorf("listen: %w", err)
	}
	if resync {
		h.publish(Event{Type: TypeResync})
	}
	for {
		n, err := conn.WaitForNotification(ctx)
		if err != nil {
			return true, fmt.Errorf("wait for notification: %w", err)
		}
		h.handle(ctx, n.Payload)
	}
}

func (h *Hub) handle(ctx context.Context, payload string) {
	ev, err := decodeEvent(payload)
	if err != nil {
		h.log.WarnContext(ctx, "dropping malformed realtime event", "err", err, "bytes", len(payload))
		return
	}
	h.publish(ev)
}

// publish is also the entry point for tests that bypass Postgres.
func (h *Hub) publish(ev Event) {
	switch ev.Type {
	case TypePresence:
		if ev.ConversationID == "" || ev.UserID == "" {
			return
		}
		h.pres.apply(ev)
	case TypeScopeChanged:
		h.mu.Lock()
		for s := range h.subs {
			s.requestRefresh()
		}
		h.mu.Unlock()
		return
	}

	h.mu.Lock()
	defer h.mu.Unlock()
	f := frame{ev: ev, data: encode(ev)}
	if ev.Type != TypePresence {
		h.head++
		f.seq, f.id = h.head, h.frameID(h.head)
		if len(h.ring) == queueSize {
			h.ring = append(h.ring[:0], h.ring[1:]...)
		}
		h.ring = append(h.ring, f)
	}
	for s := range h.subs {
		if s.sees(ev) {
			s.offer(f)
		}
	}
}

func (h *Hub) frameID(seq uint64) string { return h.epoch + "-" + strconv.FormatUint(seq, 10) }

// replay returns the frames a reconnecting client missed. ok is false when the gap cannot
// be served, in which case the client has to refetch. Callers hold h.mu.
func (h *Hub) replay(lastID string) (frames []frame, ok bool) {
	epoch, seqStr, found := strings.Cut(lastID, "-")
	seq, err := strconv.ParseUint(seqStr, 10, 64)
	if !found || err != nil || epoch != h.epoch || seq > h.head {
		return nil, false
	}
	if seq == h.head {
		return nil, true
	}
	if len(h.ring) == 0 || h.ring[0].seq > seq+1 {
		return nil, false
	}
	return h.ring[seq+1-h.ring[0].seq:], true
}

// Streams is the number of event streams open on this instance.
func (h *Hub) Streams() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.subs)
}

// Viewers returns who has the conversation open right now.
func (h *Hub) Viewers(conversationID string) []Viewer { return h.pres.viewers(conversationID) }

// Online lists users with an open stream on this instance, ordered by name.
func (h *Hub) Online() []Agent {
	h.mu.Lock()
	defer h.mu.Unlock()
	seen := map[string]bool{}
	out := []Agent{}
	for s := range h.subs {
		if !seen[s.userID] {
			seen[s.userID] = true
			out = append(out, Agent{ID: s.userID, Name: s.name})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Name != out[j].Name {
			return out[i].Name < out[j].Name
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// Close ends every open stream and refuses new ones.
func (h *Hub) Close() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.closed = true
	for s := range h.subs {
		s.close()
	}
}
