package imapsync

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"echoo/internal/mailauth"
	"echoo/internal/netguard"
)

func TestSyncReasonClassifiesStoredErrors(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want string
	}{
		{"refused connection", fmt.Errorf("%w: dial tcp 10.0.0.5:993: connection refused", ErrConnect), ReasonConnectFailed},
		{"greeting timeout", fmt.Errorf("%w: greeting: %w", ErrConnect, errors.New("i/o timeout")), ReasonConnectFailed},
		{"handshake", fmt.Errorf("%w: %w", ErrTLS, errors.New("x509: certificate signed by unknown authority")), ReasonTLSFailed},
		{"rejected password", ErrAuth, ReasonAuthFailed},
		{"missing password", errNoCredentials, ReasonAuthFailed},
		{"no inbox", fmt.Errorf("%w: %w", ErrNoInbox, errors.New("mailbox does not exist")), ReasonNoInbox},
		{"internal destination", fmt.Errorf("%w: %w", ErrConnect, netguard.ErrInternal), ReasonInternalDest},
		{"anything else", errors.New("advance cursor: deadlock detected"), ReasonUnknown},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := SyncReason(errorText(tc.err)); got != tc.want {
				t.Errorf("SyncReason(%q) = %q, want %q", errorText(tc.err), got, tc.want)
			}
		})
	}
}

func TestSyncReasonSurvivesTruncationOfTheStoredText(t *testing.T) {
	long := fmt.Errorf("%w: %s", ErrTLS, strings.Repeat("x", 2*maxErrorText))
	if got := SyncReason(errorText(long)); got != ReasonTLSFailed {
		t.Errorf("SyncReason of a truncated error = %q", got)
	}
}

func TestSyncReasonForTheSpecialStoredValues(t *testing.T) {
	if got := SyncReason(""); got != "" {
		t.Errorf("empty sync_error = %q, want no reason", got)
	}
	if got := SyncReason(mailauth.ReasonReauthRequired); got != ReasonReauthRequired {
		t.Errorf("reauth marker = %q", got)
	}
}
