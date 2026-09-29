package api

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"echoo/internal/audit"
	"echoo/internal/auth"
	"echoo/internal/db"
	"echoo/internal/db/dbq"
	"echoo/internal/keyring"
	"echoo/internal/mail/imapsync"
	"echoo/internal/mailauth"
	"echoo/internal/policy"
)

const (
	maxPasswordBytes = 1024
	reloadTimeout    = 30 * time.Second
)

// mailboxSettings is the writable, non-secret part of a mailbox.
type mailboxSettings struct {
	Name, EmailAddress, DisplayName string
	IMAPHost                        string
	IMAPPort                        int
	IMAPTLS, IMAPUsername           string
	SMTPHost                        string
	SMTPPort                        int
	SMTPTLS, SMTPUsername           string
	SentFolder                      string
	SendDelaySeconds                int
	AllowInternalHost               bool
}

func defaultMailboxSettings() mailboxSettings {
	return mailboxSettings{IMAPPort: 993, IMAPTLS: "implicit", SMTPPort: 465, SMTPTLS: "implicit", SendDelaySeconds: 5}
}

func settingsOfMailbox(mb dbq.Mailbox) mailboxSettings {
	return mailboxSettings{
		Name: mb.Name, EmailAddress: mb.EmailAddress, DisplayName: mb.DisplayName,
		IMAPHost: mb.ImapHost, IMAPPort: int(mb.ImapPort), IMAPTLS: mb.ImapTls, IMAPUsername: mb.ImapUsername,
		SMTPHost: mb.SmtpHost, SMTPPort: int(mb.SmtpPort), SMTPTLS: mb.SmtpTls, SMTPUsername: mb.SmtpUsername,
		SentFolder: mb.SentFolder, SendDelaySeconds: int(mb.SendDelaySeconds), AllowInternalHost: mb.AllowInternalHost,
	}
}

// mailboxInput is the body of create, update and test. A nil field means "not provided".
type mailboxInput struct {
	Name              *string `json:"name"`
	EmailAddress      *string `json:"email_address"`
	DisplayName       *string `json:"display_name"`
	IMAPHost          *string `json:"imap_host"`
	IMAPPort          *int    `json:"imap_port"`
	IMAPTLS           *string `json:"imap_tls"`
	IMAPUsername      *string `json:"imap_username"`
	IMAPPassword      *string `json:"imap_password"`
	SMTPHost          *string `json:"smtp_host"`
	SMTPPort          *int    `json:"smtp_port"`
	SMTPTLS           *string `json:"smtp_tls"`
	SMTPUsername      *string `json:"smtp_username"`
	SMTPPassword      *string `json:"smtp_password"`
	SentFolder        *string `json:"sent_folder"`
	SendDelaySeconds  *int    `json:"send_delay_seconds"`
	AllowInternalHost *bool   `json:"allow_internal_host"`
}

func setIf[T any](dst *T, src *T) {
	if src != nil {
		*dst = *src
	}
}

// merge applies the provided fields on base and validates the result. Values are normalized
// (trimmed, lowercased addresses and hosts), so the returned settings are what gets stored.
func (in mailboxInput) merge(base mailboxSettings) (mailboxSettings, map[string]string) {
	m := base
	setIf(&m.Name, in.Name)
	setIf(&m.EmailAddress, in.EmailAddress)
	setIf(&m.DisplayName, in.DisplayName)
	setIf(&m.IMAPHost, in.IMAPHost)
	setIf(&m.IMAPPort, in.IMAPPort)
	setIf(&m.IMAPTLS, in.IMAPTLS)
	setIf(&m.IMAPUsername, in.IMAPUsername)
	setIf(&m.SMTPHost, in.SMTPHost)
	setIf(&m.SMTPPort, in.SMTPPort)
	setIf(&m.SMTPTLS, in.SMTPTLS)
	setIf(&m.SMTPUsername, in.SMTPUsername)
	setIf(&m.SentFolder, in.SentFolder)
	setIf(&m.SendDelaySeconds, in.SendDelaySeconds)
	setIf(&m.AllowInternalHost, in.AllowInternalHost)

	fields := map[string]string{}
	var ok bool
	if m.Name, ok = cleanText(m.Name, 100); !ok {
		fields["name"] = "invalid"
	}
	m.EmailAddress = auth.NormalizeEmail(m.EmailAddress)
	if !validEmail(m.EmailAddress) {
		fields["email_address"] = "invalid"
	}
	if m.DisplayName != "" {
		if m.DisplayName, ok = cleanText(m.DisplayName, 200); !ok {
			fields["display_name"] = "invalid"
		}
	}
	if m.IMAPHost, ok = normalizeHost(m.IMAPHost); !ok {
		fields["imap_host"] = "invalid"
	}
	if m.SMTPHost, ok = normalizeHost(m.SMTPHost); !ok {
		fields["smtp_host"] = "invalid"
	}
	if m.IMAPPort < 1 || m.IMAPPort > 65535 {
		fields["imap_port"] = "invalid"
	}
	if m.SMTPPort < 1 || m.SMTPPort > 65535 {
		fields["smtp_port"] = "invalid"
	}
	if !validTLSMode(m.IMAPTLS) {
		fields["imap_tls"] = "invalid"
	}
	if !validTLSMode(m.SMTPTLS) {
		fields["smtp_tls"] = "invalid"
	}
	if m.IMAPUsername, ok = cleanText(m.IMAPUsername, 254); !ok {
		fields["imap_username"] = "invalid"
	}
	// An empty SMTP username means the server needs no authentication.
	if m.SMTPUsername != "" {
		if m.SMTPUsername, ok = cleanText(m.SMTPUsername, 254); !ok {
			fields["smtp_username"] = "invalid"
		}
	}
	if m.SentFolder != "" {
		if m.SentFolder, ok = cleanText(m.SentFolder, 200); !ok {
			fields["sent_folder"] = "invalid"
		}
	}
	if m.SendDelaySeconds < 0 || m.SendDelaySeconds > 30 {
		fields["send_delay_seconds"] = "invalid"
	}
	return m, fields
}

// checkOAuthManaged rejects changes to what the OAuth connection determines: passwords, the
// address and the servers. Reconnecting the account is how those change.
func (in mailboxInput) checkOAuthManaged(before, after mailboxSettings, fields map[string]string) {
	if in.IMAPPassword != nil {
		fields["imap_password"] = "managed_by_oauth"
	}
	if in.SMTPPassword != nil {
		fields["smtp_password"] = "managed_by_oauth"
	}
	for _, c := range []struct {
		name string
		diff bool
	}{
		{"email_address", before.EmailAddress != after.EmailAddress},
		{"imap_host", before.IMAPHost != after.IMAPHost},
		{"imap_port", before.IMAPPort != after.IMAPPort},
		{"imap_tls", before.IMAPTLS != after.IMAPTLS},
		{"imap_username", before.IMAPUsername != after.IMAPUsername},
		{"smtp_host", before.SMTPHost != after.SMTPHost},
		{"smtp_port", before.SMTPPort != after.SMTPPort},
		{"smtp_tls", before.SMTPTLS != after.SMTPTLS},
		{"smtp_username", before.SMTPUsername != after.SMTPUsername},
	} {
		if c.diff {
			fields[c.name] = "managed_by_oauth"
		}
	}
}

func validTLSMode(s string) bool { return s == "implicit" || s == "starttls" }

func validPassword(p *string) bool {
	return p == nil || (*p != "" && len(*p) <= maxPasswordBytes && utf8.ValidString(*p))
}

// checkSecrets validates provided passwords and, when required, demands the ones that are
// missing. stored tells which secrets already exist. A stored secret only stays usable while
// the server it belongs to is unchanged (host, port, TLS mode and username, see
// sameIMAPTarget); pointing the mailbox elsewhere needs the password again, so a stored
// password can never be sent to a server chosen by whoever edits the settings.
func (in mailboxInput) checkSecrets(before, m mailboxSettings, storedIMAP, storedSMTP bool, fields map[string]string) {
	if !validPassword(in.IMAPPassword) {
		fields["imap_password"] = "invalid"
	} else if in.IMAPPassword == nil && (!storedIMAP || !sameIMAPTarget(before, m)) {
		fields["imap_password"] = "required"
	}
	if !validPassword(in.SMTPPassword) {
		fields["smtp_password"] = "invalid"
	} else if in.SMTPPassword == nil && (!storedSMTP || !sameSMTPTarget(before, m)) && m.SMTPUsername != "" {
		fields["smtp_password"] = "required"
	}
}

func sameIMAPTarget(a, b mailboxSettings) bool {
	return a.IMAPHost == b.IMAPHost && a.IMAPPort == b.IMAPPort && a.IMAPTLS == b.IMAPTLS && a.IMAPUsername == b.IMAPUsername
}

func sameSMTPTarget(a, b mailboxSettings) bool {
	return a.SMTPHost == b.SMTPHost && a.SMTPPort == b.SMTPPort && a.SMTPTLS == b.SMTPTLS && a.SMTPUsername == b.SMTPUsername
}

// checkInternalHosts rejects hosts that point at internal addresses unless allowed, and the
// attempt to allow them by anyone but the owner.
func checkInternalHosts(ctx context.Context, actor dbq.User, before, after mailboxSettings, fields map[string]string) error {
	if after.AllowInternalHost != before.AllowInternalHost && actor.Role != policy.RoleOwner {
		return errForbidden
	}
	if after.AllowInternalHost {
		return nil
	}
	for field, host := range map[string]string{"imap_host": after.IMAPHost, "smtp_host": after.SMTPHost} {
		if _, bad := fields[field]; !bad && checkHostAtSave(ctx, host) {
			fields[field] = "internal_host"
		}
	}
	return nil
}

type mailboxJSON struct {
	ID                string     `json:"id"`
	Name              string     `json:"name"`
	EmailAddress      string     `json:"email_address"`
	DisplayName       string     `json:"display_name"`
	IMAPHost          string     `json:"imap_host"`
	IMAPPort          int32      `json:"imap_port"`
	IMAPTLS           string     `json:"imap_tls"`
	IMAPUsername      string     `json:"imap_username"`
	IMAPPasswordSet   bool       `json:"imap_password_set"`
	SMTPHost          string     `json:"smtp_host"`
	SMTPPort          int32      `json:"smtp_port"`
	SMTPTLS           string     `json:"smtp_tls"`
	SMTPUsername      string     `json:"smtp_username"`
	SMTPPasswordSet   bool       `json:"smtp_password_set"`
	SentFolder        string     `json:"sent_folder"`
	SendDelaySeconds  int32      `json:"send_delay_seconds"`
	AllowInternalHost bool       `json:"allow_internal_host"`
	AuthType          string     `json:"auth_type"`
	OAuthConnectedAt  *time.Time `json:"oauth_connected_at"`
	// NeedsReconnect is set when the provider rejected the stored token and an admin must
	// connect the account again.
	NeedsReconnect bool `json:"needs_reconnect"`
	// SyncReason is a code from a closed set for why the last sync failed, null when it did not.
	// The server's own error text stays in the database.
	SyncReason         *string    `json:"sync_reason"`
	SyncState          string     `json:"sync_state"`
	SyncStateChangedAt time.Time  `json:"sync_state_changed_at"`
	LastSyncedAt       *time.Time `json:"last_synced_at"`
	DisabledAt         *time.Time `json:"disabled_at"`
	CreatedAt          time.Time  `json:"created_at"`
	UpdatedAt          time.Time  `json:"updated_at"`
}

func toMailboxJSON(mb dbq.Mailbox) mailboxJSON {
	return mailboxJSON{
		ID: uuidStr(mb.ID), Name: mb.Name, EmailAddress: mb.EmailAddress, DisplayName: mb.DisplayName,
		IMAPHost: mb.ImapHost, IMAPPort: mb.ImapPort, IMAPTLS: mb.ImapTls, IMAPUsername: mb.ImapUsername,
		IMAPPasswordSet: len(mb.ImapSecretEnc) > 0,
		SMTPHost:        mb.SmtpHost, SMTPPort: mb.SmtpPort, SMTPTLS: mb.SmtpTls, SMTPUsername: mb.SmtpUsername,
		SMTPPasswordSet: len(mb.SmtpSecretEnc) > 0,
		SentFolder:      mb.SentFolder, SendDelaySeconds: mb.SendDelaySeconds, AllowInternalHost: mb.AllowInternalHost,
		AuthType: mb.AuthType, OAuthConnectedAt: timeOrNil(mb.OauthConnectedAt),
		NeedsReconnect: mb.AuthType != "password" && mb.SyncState == "auth_failed" && mb.SyncError == mailauth.ReasonReauthRequired,
		SyncReason:     syncReason(mb.SyncError),
		SyncState:      mb.SyncState, SyncStateChangedAt: mb.SyncStateChangedAt.Time.UTC(),
		LastSyncedAt: timeOrNil(mb.LastSyncedAt), DisabledAt: timeOrNil(mb.DisabledAt),
		CreatedAt: mb.CreatedAt.Time.UTC(), UpdatedAt: mb.UpdatedAt.Time.UTC(),
	}
}

func syncReason(syncError string) *string {
	reason := imapsync.SyncReason(syncError)
	if reason == "" {
		return nil
	}
	return &reason
}

func (s *Server) reloadMailboxes(r *http.Request) {
	if s.reloader == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), reloadTimeout)
	defer cancel()
	if err := s.reloader.Reload(ctx); err != nil {
		slog.ErrorContext(r.Context(), "reload mailboxes", "err", err, "request_id", requestIDFrom(r.Context()))
	}
}

// secretParams encrypts the provided passwords for the given mailbox row.
func (s *Server) secretParams(id pgtype.UUID, in mailboxInput) (dbq.SetMailboxSecretsParams, error) {
	params := dbq.SetMailboxSecretsParams{ID: id}
	if s.keys == nil {
		return params, errors.New("no keyring configured for mailbox secrets")
	}
	enc := func(column string, pw *string) ([]byte, error) {
		if pw == nil {
			return nil, nil
		}
		return s.keys.Encrypt([]byte(*pw), keyring.AAD("mailboxes", column, uuidStr(id)))
	}
	var err error
	if params.ImapSecretEnc, err = enc("imap_secret_enc", in.IMAPPassword); err != nil {
		return params, err
	}
	params.SmtpSecretEnc, err = enc("smtp_secret_enc", in.SMTPPassword)
	return params, err
}

func (s *Server) decryptSecret(id pgtype.UUID, column string, enc []byte) (string, error) {
	if s.keys == nil {
		return "", errors.New("no keyring configured for mailbox secrets")
	}
	pt, err := s.keys.Decrypt(enc, keyring.AAD("mailboxes", column, uuidStr(id)))
	return string(pt), err
}

func mailboxAudit(r *http.Request, action string, id pgtype.UUID, meta map[string]any) audit.Entry {
	return audit.Entry{Actor: sessionFrom(r.Context()).User.ID, IP: clientFrom(r).IP, Action: action, TargetType: "mailbox", TargetID: uuidStr(id), Metadata: meta}
}

func (s *Server) listMailboxes(w http.ResponseWriter, r *http.Request) {
	rows, err := s.q.ListMailboxes(r.Context())
	if err != nil {
		writeError(w, r, err)
		return
	}
	out := make([]mailboxJSON, len(rows))
	for i, mb := range rows {
		out[i] = toMailboxJSON(mb)
	}
	writeJSON(w, http.StatusOK, map[string]any{"mailboxes": out})
}

func (s *Server) loadMailbox(w http.ResponseWriter, r *http.Request) (dbq.Mailbox, bool) {
	id, ok := parseUUID(chi.URLParam(r, "id"))
	if !ok {
		writeError(w, r, errNotFound)
		return dbq.Mailbox{}, false
	}
	mb, err := s.q.GetMailbox(r.Context(), id)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, r, errNotFound)
		return dbq.Mailbox{}, false
	}
	if err != nil {
		writeError(w, r, err)
		return dbq.Mailbox{}, false
	}
	return mb, true
}

func (s *Server) getMailbox(w http.ResponseWriter, r *http.Request) {
	if mb, ok := s.loadMailbox(w, r); ok {
		writeJSON(w, http.StatusOK, map[string]any{"mailbox": toMailboxJSON(mb)})
	}
}

func (s *Server) createMailbox(w http.ResponseWriter, r *http.Request) {
	var in mailboxInput
	if err := decode(r, &in); err != nil {
		writeError(w, r, err)
		return
	}
	actor := sessionFrom(r.Context()).User
	base := defaultMailboxSettings()
	m, fields := in.merge(base)
	in.checkSecrets(base, m, false, false, fields)
	if err := checkInternalHosts(r.Context(), actor, base, m, fields); err != nil {
		writeError(w, r, err)
		return
	}
	if len(fields) > 0 {
		writeError(w, r, errValidation(fields))
		return
	}

	var mb dbq.Mailbox
	err := db.InTx(r.Context(), s.pool, func(q *dbq.Queries) error {
		var err error
		mb, err = q.InsertMailbox(r.Context(), dbq.InsertMailboxParams{
			Name: m.Name, EmailAddress: m.EmailAddress, DisplayName: m.DisplayName,
			ImapHost: m.IMAPHost, ImapPort: int32(m.IMAPPort), ImapTls: m.IMAPTLS, ImapUsername: m.IMAPUsername, //nolint:gosec // range checked by merge
			SmtpHost: m.SMTPHost, SmtpPort: int32(m.SMTPPort), SmtpTls: m.SMTPTLS, SmtpUsername: m.SMTPUsername, //nolint:gosec // range checked by merge
			SentFolder: m.SentFolder, SendDelaySeconds: int32(m.SendDelaySeconds), AllowInternalHost: m.AllowInternalHost, //nolint:gosec // range checked by merge
		})
		if err != nil {
			return err
		}
		// The row ID is part of the AAD, so the secrets can only be encrypted once it exists.
		params, err := s.secretParams(mb.ID, in)
		if err != nil {
			return err
		}
		if mb, err = q.SetMailboxSecrets(r.Context(), params); err != nil {
			return err
		}
		if err := audit.Write(r.Context(), q, mailboxAudit(r, audit.MailboxCreated, mb.ID, map[string]any{"name": m.Name})); err != nil {
			return err
		}
		if err := audit.Write(r.Context(), q, mailboxAudit(r, audit.MailboxSecretChanged, mb.ID, secretMeta(in))); err != nil {
			return err
		}
		if m.AllowInternalHost {
			if err := audit.Write(r.Context(), q, mailboxAudit(r, audit.MailboxInternalHost, mb.ID, map[string]any{"allowed": true})); err != nil {
				return err
			}
		}
		// Admins read every mailbox, so their open streams need to learn about the new one.
		return notifyScopeChanged(r.Context(), q)
	})
	if isUniqueViolation(err) {
		writeError(w, r, errValidation(map[string]string{"email_address": "taken"}))
		return
	}
	if err != nil {
		writeError(w, r, err)
		return
	}
	s.reloadMailboxes(r)
	writeJSON(w, http.StatusCreated, map[string]any{"mailbox": toMailboxJSON(mb)})
}

func secretMeta(in mailboxInput) map[string]any {
	return map[string]any{"imap": in.IMAPPassword != nil, "smtp": in.SMTPPassword != nil}
}

func changedSettings(a, b mailboxSettings) []string {
	changed := []string{}
	for _, c := range []struct {
		name string
		diff bool
	}{
		{"name", a.Name != b.Name},
		{"email_address", a.EmailAddress != b.EmailAddress},
		{"display_name", a.DisplayName != b.DisplayName},
		{"imap_host", a.IMAPHost != b.IMAPHost},
		{"imap_port", a.IMAPPort != b.IMAPPort},
		{"imap_tls", a.IMAPTLS != b.IMAPTLS},
		{"imap_username", a.IMAPUsername != b.IMAPUsername},
		{"smtp_host", a.SMTPHost != b.SMTPHost},
		{"smtp_port", a.SMTPPort != b.SMTPPort},
		{"smtp_tls", a.SMTPTLS != b.SMTPTLS},
		{"smtp_username", a.SMTPUsername != b.SMTPUsername},
		{"sent_folder", a.SentFolder != b.SentFolder},
		{"send_delay_seconds", a.SendDelaySeconds != b.SendDelaySeconds},
		{"allow_internal_host", a.AllowInternalHost != b.AllowInternalHost},
	} {
		if c.diff {
			changed = append(changed, c.name)
		}
	}
	return changed
}

func (s *Server) updateMailbox(w http.ResponseWriter, r *http.Request) {
	var in mailboxInput
	if err := decode(r, &in); err != nil {
		writeError(w, r, err)
		return
	}
	current, ok := s.loadMailbox(w, r)
	if !ok {
		return
	}
	// Validation, including DNS lookups, happens before the row is locked so a slow resolver
	// cannot stall the sync that updates the same row.
	before := settingsOfMailbox(current)
	m, fields := in.merge(before)
	if current.AuthType != "password" {
		in.checkOAuthManaged(before, m, fields)
	} else {
		in.checkSecrets(before, m, len(current.ImapSecretEnc) > 0, len(current.SmtpSecretEnc) > 0, fields)
	}
	if err := checkInternalHosts(r.Context(), sessionFrom(r.Context()).User, before, m, fields); err != nil {
		writeError(w, r, err)
		return
	}
	if len(fields) > 0 {
		writeError(w, r, errValidation(fields))
		return
	}

	var mb dbq.Mailbox
	changedAny := false
	err := db.InTx(r.Context(), s.pool, func(q *dbq.Queries) error {
		locked, err := q.GetMailboxForUpdate(r.Context(), current.ID)
		if err != nil {
			return err
		}
		lockedBefore := settingsOfMailbox(locked)
		merged, invalid := in.merge(lockedBefore)
		if len(invalid) > 0 {
			return errValidation(invalid)
		}
		if locked.AuthType == "password" {
			// The row may have changed since the check above; a stored password still must not
			// follow a changed server.
			stale := map[string]string{}
			in.checkSecrets(lockedBefore, merged, len(locked.ImapSecretEnc) > 0, len(locked.SmtpSecretEnc) > 0, stale)
			if len(stale) > 0 {
				return errValidation(stale)
			}
		}
		mb = locked
		changed := changedSettings(lockedBefore, merged)
		if len(changed) > 0 {
			mb, err = q.UpdateMailbox(r.Context(), dbq.UpdateMailboxParams{
				ID: locked.ID, Name: merged.Name, EmailAddress: merged.EmailAddress, DisplayName: merged.DisplayName,
				ImapHost: merged.IMAPHost, ImapPort: int32(merged.IMAPPort), ImapTls: merged.IMAPTLS, ImapUsername: merged.IMAPUsername, //nolint:gosec // range checked by merge
				SmtpHost: merged.SMTPHost, SmtpPort: int32(merged.SMTPPort), SmtpTls: merged.SMTPTLS, SmtpUsername: merged.SMTPUsername, //nolint:gosec // range checked by merge
				SentFolder: merged.SentFolder, SendDelaySeconds: int32(merged.SendDelaySeconds), AllowInternalHost: merged.AllowInternalHost, //nolint:gosec // range checked by merge
			})
			if err != nil {
				return err
			}
			if err := audit.Write(r.Context(), q, mailboxAudit(r, audit.MailboxUpdated, mb.ID, map[string]any{"fields": changed})); err != nil {
				return err
			}
		}
		if merged.AllowInternalHost != lockedBefore.AllowInternalHost {
			if err := audit.Write(r.Context(), q, mailboxAudit(r, audit.MailboxInternalHost, mb.ID, map[string]any{"allowed": merged.AllowInternalHost})); err != nil {
				return err
			}
		}
		if in.IMAPPassword != nil || in.SMTPPassword != nil {
			params, err := s.secretParams(mb.ID, in)
			if err != nil {
				return err
			}
			if mb, err = q.SetMailboxSecrets(r.Context(), params); err != nil {
				return err
			}
			if err := audit.Write(r.Context(), q, mailboxAudit(r, audit.MailboxSecretChanged, mb.ID, secretMeta(in))); err != nil {
				return err
			}
			changed = append(changed, "secret")
		}
		changedAny = len(changed) > 0
		return nil
	})
	switch {
	case isUniqueViolation(err):
		writeError(w, r, errValidation(map[string]string{"email_address": "taken"}))
		return
	case errors.Is(err, pgx.ErrNoRows):
		writeError(w, r, errNotFound)
		return
	case err != nil:
		writeError(w, r, err)
		return
	}
	if changedAny {
		s.reloadMailboxes(r)
	}
	writeJSON(w, http.StatusOK, map[string]any{"mailbox": toMailboxJSON(mb)})
}

func (s *Server) disableMailbox(w http.ResponseWriter, r *http.Request) {
	s.setMailboxDisabled(w, r, true)
}
func (s *Server) enableMailbox(w http.ResponseWriter, r *http.Request) {
	s.setMailboxDisabled(w, r, false)
}

func (s *Server) setMailboxDisabled(w http.ResponseWriter, r *http.Request, disabled bool) {
	id, ok := parseUUID(chi.URLParam(r, "id"))
	if !ok {
		writeError(w, r, errNotFound)
		return
	}
	var mb dbq.Mailbox
	changed := false
	err := db.InTx(r.Context(), s.pool, func(q *dbq.Queries) error {
		var err error
		if mb, err = q.GetMailboxForUpdate(r.Context(), id); err != nil {
			return err
		}
		if mb.DisabledAt.Valid == disabled {
			return nil
		}
		if mb, err = q.SetMailboxDisabled(r.Context(), dbq.SetMailboxDisabledParams{ID: id, Disabled: disabled}); err != nil {
			return err
		}
		changed = true
		action := audit.MailboxEnabled
		if disabled {
			action = audit.MailboxDisabled
		}
		return audit.Write(r.Context(), q, mailboxAudit(r, action, id, nil))
	})
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, r, errNotFound)
		return
	}
	if err != nil {
		writeError(w, r, err)
		return
	}
	if changed {
		s.reloadMailboxes(r)
	}
	writeJSON(w, http.StatusOK, map[string]any{"mailbox": toMailboxJSON(mb)})
}

type accessEntry struct {
	TeamID string `json:"team_id"`
	Level  string `json:"level"`
}

func toAccessEntries(rows []dbq.MailboxAccess) []accessEntry {
	out := make([]accessEntry, len(rows))
	for i, a := range rows {
		out[i] = accessEntry{TeamID: uuidStr(a.TeamID), Level: a.Level}
	}
	return out
}

func (s *Server) getMailboxAccess(w http.ResponseWriter, r *http.Request) {
	mb, ok := s.loadMailbox(w, r)
	if !ok {
		return
	}
	rows, err := s.q.ListMailboxAccess(r.Context(), mb.ID)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"access": toAccessEntries(rows)})
}

func (s *Server) putMailboxAccess(w http.ResponseWriter, r *http.Request) {
	id, ok := parseUUID(chi.URLParam(r, "id"))
	if !ok {
		writeError(w, r, errNotFound)
		return
	}
	var req struct {
		Access []accessEntry `json:"access"`
	}
	if err := decode(r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	if len(req.Access) > 1000 {
		writeError(w, r, errValidation(map[string]string{"access": "too_many"}))
		return
	}
	seen := map[pgtype.UUID]bool{}
	teamIDs := make([]pgtype.UUID, len(req.Access))
	levels := make([]string, len(req.Access))
	for i, e := range req.Access {
		team, ok := parseUUID(e.TeamID)
		if !ok || seen[team] || (e.Level != "read" && e.Level != "write") {
			writeError(w, r, errValidation(map[string]string{"access": "invalid"}))
			return
		}
		seen[team] = true
		teamIDs[i], levels[i] = team, e.Level
	}
	err := db.InTx(r.Context(), s.pool, func(q *dbq.Queries) error {
		var before []dbq.MailboxAccess
		// keepScope locks the actor first, then the mailbox: the other order deadlocks with a
		// concurrent request of the same actor.
		err := keepScope(r.Context(), q, sessionFrom(r.Context()).User, func() error {
			if _, err := q.GetMailboxForUpdate(r.Context(), id); err != nil {
				return err
			}
			var err error
			if before, err = q.ListMailboxAccess(r.Context(), id); err != nil {
				return err
			}
			if err := q.DeleteMailboxAccess(r.Context(), id); err != nil {
				return err
			}
			if len(teamIDs) == 0 {
				return nil
			}
			return q.AddMailboxAccess(r.Context(), dbq.AddMailboxAccessParams{MailboxID: id, TeamIds: teamIDs, Levels: levels})
		})
		if err != nil {
			return err
		}
		meta := map[string]any{"before": toAccessEntries(before), "after": req.Access}
		if err := audit.Write(r.Context(), q, mailboxAudit(r, audit.MailboxAccessChanged, id, meta)); err != nil {
			return err
		}
		return notifyScopeChanged(r.Context(), q)
	})
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		writeError(w, r, errNotFound)
	case isForeignKeyViolation(err):
		writeError(w, r, errValidation(map[string]string{"access": "unknown_team"}))
	case err != nil:
		writeError(w, r, err)
	default:
		w.WriteHeader(http.StatusNoContent)
	}
}
