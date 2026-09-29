package imapsync

import (
	"strings"

	"echoo/internal/mailauth"
	"echoo/internal/netguard"
)

// The closed set of reasons SyncReason returns.
const (
	ReasonConnectFailed  = "connect_failed"
	ReasonTLSFailed      = "tls_failed"
	ReasonAuthFailed     = "auth_failed"
	ReasonReauthRequired = "oauth_reauth_required"
	ReasonInternalDest   = "internal_destination"
	ReasonNoInbox        = "no_inbox"
	ReasonUnknown        = "unknown"
)

// SyncReason classifies a stored sync_error for the API. The stored text starts with the message
// of the failure class (see errorText), so the class survives truncation, and the text itself
// never leaves the server: the API only exposes the code. An empty sync_error has no reason.
func SyncReason(syncError string) string {
	switch {
	case syncError == "":
		return ""
	case syncError == mailauth.ReasonReauthRequired:
		return ReasonReauthRequired
	case strings.Contains(syncError, netguard.ErrInternal.Error()):
		return ReasonInternalDest
	case strings.HasPrefix(syncError, ErrAuth.Error()), strings.HasPrefix(syncError, errNoCredentials.Error()):
		return ReasonAuthFailed
	case strings.HasPrefix(syncError, ErrTLS.Error()):
		return ReasonTLSFailed
	case strings.HasPrefix(syncError, ErrNoInbox.Error()):
		return ReasonNoInbox
	case strings.HasPrefix(syncError, ErrConnect.Error()):
		return ReasonConnectFailed
	}
	return ReasonUnknown
}
