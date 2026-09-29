package send

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync/atomic"
)

// ErrRecipientNotAllowed marks a delivery refused because a recipient is outside
// ECHOO_OUTBOUND_ALLOWED_DOMAINS.
var ErrRecipientNotAllowed = errors.New("recipient domain is not in ECHOO_OUTBOUND_ALLOWED_DOMAINS")

var allowedDomains atomic.Pointer[[]string]

// RestrictRecipients limits every delivery of this process to recipients in domains; nil or
// empty lifts the limit. It lives here, not with the callers, so that no sending path (replies,
// system mail, campaigns) can skip it. It must be called once at startup, before any job runs.
func RestrictRecipients(domains []string) {
	if len(domains) == 0 {
		allowedDomains.Store(nil)
		return
	}
	stored := make([]string, len(domains))
	for i, d := range domains {
		stored[i] = strings.ToLower(d)
	}
	allowedDomains.Store(&stored)
}

func checkRecipients(rcpt []string) error {
	domains := allowedDomains.Load()
	if domains == nil {
		return nil
	}
	for _, r := range rcpt {
		at := strings.LastIndexByte(r, '@')
		if at < 0 || !slices.Contains(*domains, strings.ToLower(r[at+1:])) {
			return fmt.Errorf("%w: %s", ErrRecipientNotAllowed, r)
		}
	}
	return nil
}
