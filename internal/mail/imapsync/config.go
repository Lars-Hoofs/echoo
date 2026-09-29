package imapsync

import (
	"crypto/tls"
	"math/rand/v2"
	"time"
)

const (
	defaultConnectTimeout    = 30 * time.Second
	defaultLogoutTimeout     = 5 * time.Second
	defaultIdleRestart       = 25 * time.Minute
	defaultPollInterval      = 60 * time.Second
	defaultIdleRetryInterval = 10 * time.Minute
	defaultIdleMaxFailures   = 3
	defaultAuthRetry         = 15 * time.Minute
	defaultLockRetry         = 30 * time.Second
	defaultBatchSize         = 50
	defaultMaxMessageBytes   = 50 << 20

	backoffBase = time.Second
	backoffMax  = 5 * time.Minute
)

// Config holds the timing and tuning knobs of the sync. The zero value of every field selects
// the production default, so tests only set what they need to speed up.
type Config struct {
	// ConnectTimeout bounds dialing, the TLS handshake and the server greeting.
	ConnectTimeout time.Duration
	// LogoutTimeout bounds LOGOUT and lock release on shutdown. A session that is still busy
	// this long after its context was cancelled is closed forcibly.
	LogoutTimeout time.Duration
	// IdleRestart is how long one IDLE command runs before it is renewed (RFC 2177: < 29 min).
	IdleRestart time.Duration
	// PollInterval is the polling period while IDLE is unavailable.
	PollInterval time.Duration
	// IdleRetryInterval is how long polling lasts after IDLE failed IdleMaxFailures times
	// before IDLE is tried again.
	IdleRetryInterval time.Duration
	// IdleMaxFailures is the number of consecutive IDLE failures that switches to polling.
	IdleMaxFailures int
	// AuthRetryInterval is the retry period after an authentication failure.
	AuthRetryInterval time.Duration
	// LockRetryInterval is the retry period while another instance holds the mailbox lock.
	LockRetryInterval time.Duration
	// BatchSize is the number of messages fetched per FETCH command.
	BatchSize int
	// MaxMessageBytes is the largest message that is fetched. Larger ones are recorded as
	// skipped with only their headers stored. Keep it in line with the parser's limit.
	MaxMessageBytes int64
	// Backoff returns the reconnect delay for the given zero-based consecutive failure count.
	Backoff func(attempt int) time.Duration
	// TLSConfig is only for tests, to trust a self-signed certificate. Nil means system roots.
	// Certificate verification stays on whatever the config says.
	TLSConfig *tls.Config
}

func (c Config) withDefaults() Config {
	if c.ConnectTimeout <= 0 {
		c.ConnectTimeout = defaultConnectTimeout
	}
	if c.LogoutTimeout <= 0 {
		c.LogoutTimeout = defaultLogoutTimeout
	}
	if c.IdleRestart <= 0 {
		c.IdleRestart = defaultIdleRestart
	}
	if c.PollInterval <= 0 {
		c.PollInterval = defaultPollInterval
	}
	if c.IdleRetryInterval <= 0 {
		c.IdleRetryInterval = defaultIdleRetryInterval
	}
	if c.IdleMaxFailures <= 0 {
		c.IdleMaxFailures = defaultIdleMaxFailures
	}
	if c.AuthRetryInterval <= 0 {
		c.AuthRetryInterval = defaultAuthRetry
	}
	if c.LockRetryInterval <= 0 {
		c.LockRetryInterval = defaultLockRetry
	}
	if c.BatchSize <= 0 {
		c.BatchSize = defaultBatchSize
	}
	if c.MaxMessageBytes <= 0 {
		c.MaxMessageBytes = defaultMaxMessageBytes
	}
	if c.Backoff == nil {
		c.Backoff = exponentialBackoff
	}
	return c
}

// exponentialBackoff doubles from one second up to five minutes, with jitter in the upper half
// so mailboxes on the same server do not reconnect in lockstep.
func exponentialBackoff(attempt int) time.Duration {
	d := backoffMax
	if attempt < 20 {
		d = min(backoffBase<<attempt, backoffMax)
	}
	return d/2 + rand.N(d/2+1) //nolint:gosec // jitter does not need a cryptographic source
}
