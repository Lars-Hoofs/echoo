package auth

import (
	"sync"
	"time"
)

// Limiter is a fixed-window counter per key (for example a client IP). It is in memory:
// a restart resets the windows, which is acceptable because the per-account lockout in the
// database remains in force.
type Limiter struct {
	limit  int
	window time.Duration
	now    func() time.Time

	mu      sync.Mutex
	entries map[string]*limiterEntry
}

type limiterEntry struct {
	start time.Time
	count int
}

// maxEntries bounds memory under a flood of distinct keys; expired entries are swept first.
const maxEntries = 100_000

func NewLimiter(limit int, window time.Duration) *Limiter {
	return &Limiter{limit: limit, window: window, now: time.Now, entries: map[string]*limiterEntry{}}
}

// Allow records one attempt for key and reports whether it is within the limit.
func (l *Limiter) Allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	e, ok := l.entries[key]
	if !ok || now.Sub(e.start) >= l.window {
		if !ok && len(l.entries) >= maxEntries {
			l.sweep(now)
			if len(l.entries) >= maxEntries {
				return false
			}
		}
		l.entries[key] = &limiterEntry{start: now, count: 1}
		return true
	}
	e.count++
	return e.count <= l.limit
}

func (l *Limiter) sweep(now time.Time) {
	for k, e := range l.entries {
		if now.Sub(e.start) >= l.window {
			delete(l.entries, k)
		}
	}
}
