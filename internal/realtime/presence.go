package realtime

import (
	"sort"
	"sync"
	"time"
)

const (
	// viewingTTL is refreshed by the client's 15 s heartbeat, so one missed beat is tolerated.
	viewingTTL = 30 * time.Second
	// typingTTL is short because typing has no explicit stop: it simply expires.
	typingTTL     = 8 * time.Second
	sweepInterval = time.Minute
)

// Viewer is one user with a conversation open.
type Viewer struct {
	UserID string `json:"user_id"`
	Name   string `json:"name"`
	Typing bool   `json:"typing"`
}

type presenceEntry struct {
	name       string
	viewingEnd time.Time
	typingEnd  time.Time
}

// presence is ephemeral by design: nothing is written to the database, and every instance
// rebuilds its own view from the same NOTIFY stream.
type presence struct {
	now       func() time.Time
	mu        sync.Mutex
	byConv    map[string]map[string]*presenceEntry
	lastSweep time.Time
}

func newPresence(now func() time.Time) *presence {
	return &presence{now: now, byConv: map[string]map[string]*presenceEntry{}, lastSweep: now()}
}

func (p *presence) apply(ev Event) {
	now := p.now()
	p.mu.Lock()
	defer p.mu.Unlock()
	if now.Sub(p.lastSweep) >= sweepInterval {
		p.sweep(now)
	}
	users := p.byConv[ev.ConversationID]
	if ev.State == StateLeft {
		delete(users, ev.UserID)
		if len(users) == 0 {
			delete(p.byConv, ev.ConversationID)
		}
		return
	}
	if users == nil {
		users = map[string]*presenceEntry{}
		p.byConv[ev.ConversationID] = users
	}
	e := users[ev.UserID]
	if e == nil {
		e = &presenceEntry{}
		users[ev.UserID] = e
	}
	e.name = ev.Name
	e.viewingEnd = now.Add(viewingTTL)
	if ev.State == StateTyping {
		e.typingEnd = now.Add(typingTTL)
	}
}

func (p *presence) sweep(now time.Time) {
	for conv, users := range p.byConv {
		for id, e := range users {
			if !now.Before(e.viewingEnd) {
				delete(users, id)
			}
		}
		if len(users) == 0 {
			delete(p.byConv, conv)
		}
	}
	p.lastSweep = now
}

// viewers lists the users with the conversation open, ordered by name.
func (p *presence) viewers(conversationID string) []Viewer {
	now := p.now()
	p.mu.Lock()
	defer p.mu.Unlock()
	out := []Viewer{}
	for id, e := range p.byConv[conversationID] {
		if now.Before(e.viewingEnd) {
			out = append(out, Viewer{UserID: id, Name: e.name, Typing: now.Before(e.typingEnd)})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Name != out[j].Name {
			return out[i].Name < out[j].Name
		}
		return out[i].UserID < out[j].UserID
	})
	return out
}
