package api

import (
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"golang.org/x/oauth2"

	"echoo/internal/audit"
	"echoo/internal/db"
	"echoo/internal/db/dbq"
	"echoo/internal/mailauth"
	"echoo/internal/policy"
)

const mailboxSettingsPath = "/instellingen/mailboxen"

var errOAuthUnavailable = &apiError{Status: http.StatusNotFound, Code: "oauth_provider_unavailable", Message: "this provider is not configured"}

func (s *Server) mailAccountAdminRoutes(r chi.Router) {
	r.Get("/mailboxes/oauth/providers", s.listOAuthProviders)
	r.With(sessionOnly).Get("/mailboxes/oauth/{provider}/start", s.startOAuth)
	r.Get("/settings/system-mail", s.getSystemMail)
	r.Post("/invitations", s.createInvitation)
	r.Post("/users/{id}/invitation/resend", s.resendInvitation)
	r.Delete("/users/{id}/invitation", s.revokeInvitation)
}

func (s *Server) oauthProviders() []*mailauth.Provider {
	if s.oauth == nil {
		return nil
	}
	return s.oauth.Providers().List()
}

func (s *Server) listOAuthProviders(w http.ResponseWriter, r *http.Request) {
	type providerJSON struct {
		ID          string `json:"id"`
		Name        string `json:"name"`
		RedirectURI string `json:"redirect_uri"`
	}
	out := []providerJSON{}
	for _, p := range s.oauthProviders() {
		out = append(out, providerJSON{ID: p.ID, Name: p.Name, RedirectURI: p.RedirectURL(s.baseURL())})
	}
	writeJSON(w, http.StatusOK, map[string]any{"providers": out})
}

func (s *Server) baseURL() string { return s.cfg.BaseURL.String() }

// startOAuth returns the provider's authorization URL. The state is bound to this session, so
// only the browser that asked for it can finish the flow.
func (s *Server) startOAuth(w http.ResponseWriter, r *http.Request) {
	provider, ok := s.providerParam(r)
	if !ok {
		writeError(w, r, errOAuthUnavailable)
		return
	}
	st := mailauth.State{Provider: provider.ID, SessionID: uuidStr(sessionFrom(r.Context()).ID)}
	hint := ""
	if raw := r.URL.Query().Get("mailbox_id"); raw != "" {
		id, ok := parseUUID(raw)
		if !ok {
			writeError(w, r, errNotFound)
			return
		}
		mb, err := s.q.GetMailbox(r.Context(), id)
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, r, errNotFound)
			return
		}
		if err != nil {
			writeError(w, r, err)
			return
		}
		st.MailboxID, hint = uuidStr(mb.ID), mb.EmailAddress
	} else if raw := r.URL.Query().Get("name"); raw != "" {
		name, ok := cleanText(raw, 100)
		if !ok {
			writeError(w, r, errValidation(map[string]string{"name": "invalid"}))
			return
		}
		st.Name = name
	}
	st.Verifier = oauth2.GenerateVerifier()
	param, err := mailauth.SealState(s.oauth.Keys(), st, time.Now())
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"url": s.oauth.AuthURL(provider, param, st.Verifier, hint)})
}

func (s *Server) providerParam(r *http.Request) (*mailauth.Provider, bool) {
	if s.oauth == nil {
		return nil, false
	}
	return s.oauth.Providers().ByID(chi.URLParam(r, "provider"))
}

// oauthCallback completes the authorization: it checks the state against the session, trades
// the code for tokens, and creates or updates the mailbox. Every outcome is a redirect to the
// mailbox settings, where the UI shows the result.
func (s *Server) oauthCallback(w http.ResponseWriter, r *http.Request) {
	sess := sessionFrom(r.Context())
	if sess == nil || sess.MfaPending || bearerFrom(r.Context()) != nil {
		http.Redirect(w, r, "/inloggen", http.StatusSeeOther)
		return
	}
	fail := func(code string) {
		writeOAuthAudit(r, s, audit.MailboxOAuthFailed, pgtype.UUID{}, map[string]any{"reason": code})
		http.Redirect(w, r, mailboxSettingsPath+"?"+url.Values{"oauth_error": {code}}.Encode(), http.StatusSeeOther)
	}
	enroll, err := s.mfaEnrollmentRequired(r.Context(), sess)
	if err != nil {
		writeError(w, r, err)
		return
	}
	if !policy.Has(sess.User, policy.MailboxesManage) || sess.User.PasswordMustChange || enroll {
		fail("forbidden")
		return
	}
	provider, ok := s.providerParam(r)
	if !ok {
		fail("provider_unavailable")
		return
	}
	st, err := mailauth.OpenState(s.oauth.Keys(), r.URL.Query().Get("state"), time.Now())
	switch {
	case errors.Is(err, mailauth.ErrStateExpired):
		fail("state_expired")
		return
	case err != nil, st.Provider != provider.ID, st.SessionID != uuidStr(sess.ID):
		fail("state_invalid")
		return
	}
	if r.URL.Query().Get("error") != "" {
		fail("provider_denied")
		return
	}
	code := r.URL.Query().Get("code")
	if code == "" {
		fail("state_invalid")
		return
	}
	tok, email, err := s.oauth.Exchange(r.Context(), provider, code, st.Verifier)
	if errors.Is(err, mailauth.ErrNoRefreshToken) {
		fail("no_refresh_token")
		return
	}
	if err != nil {
		slog.WarnContext(r.Context(), "oauth code exchange failed", "provider", provider.ID, "err", err, "request_id", requestIDFrom(r.Context()))
		fail("exchange_failed")
		return
	}

	mb, reason, err := s.connectOAuthMailbox(r, provider, st, email, tok)
	if err != nil {
		writeError(w, r, err)
		return
	}
	if reason != "" {
		fail(reason)
		return
	}
	s.reloadMailboxes(r)
	http.Redirect(w, r, mailboxSettingsPath+"?"+url.Values{"oauth": {"connected"}, "mailbox": {uuidStr(mb.ID)}}.Encode(), http.StatusSeeOther)
}

// connectOAuthMailbox stores the tokens on the mailbox being reconnected or on a new one. A
// non-empty failure code means the attempt was turned down and nothing changed.
func (s *Server) connectOAuthMailbox(r *http.Request, p *mailauth.Provider, st mailauth.State, email string, tok mailauth.Token) (dbq.Mailbox, string, error) {
	ctx := r.Context()
	var mb dbq.Mailbox
	failure := ""
	err := db.InTx(ctx, s.pool, func(q *dbq.Queries) error {
		reconnect := st.MailboxID != ""
		if reconnect {
			id, ok := parseUUID(st.MailboxID)
			if !ok {
				failure = "state_invalid"
				return nil
			}
			var err error
			if mb, err = q.GetMailboxForUpdate(ctx, id); errors.Is(err, pgx.ErrNoRows) {
				failure = "mailbox_missing"
				return nil
			} else if err != nil {
				return err
			}
			// Signing in as someone else must not point an existing mailbox at another account.
			if mb.EmailAddress != email {
				failure = "email_mismatch"
				return nil
			}
		} else {
			name := st.Name
			if name == "" {
				name = email
			}
			var err error
			mb, err = q.InsertOAuthMailbox(ctx, dbq.InsertOAuthMailboxParams{
				Name: name, EmailAddress: email, AuthType: p.AuthType,
				ImapHost: p.IMAP.Host, ImapPort: int32(p.IMAP.Port), ImapTls: p.IMAP.TLS, //nolint:gosec // provider constants
				SmtpHost: p.SMTP.Host, SmtpPort: int32(p.SMTP.Port), SmtpTls: p.SMTP.TLS, //nolint:gosec // provider constants
			})
			if err != nil {
				return err
			}
			if err := audit.Write(ctx, q, mailboxAudit(r, audit.MailboxCreated, mb.ID, map[string]any{"name": name, "auth": p.AuthType})); err != nil {
				return err
			}
		}
		// The row id is part of the AAD, so the token can only be sealed once the row exists.
		enc, err := s.oauth.Seal(mb.ID, tok)
		if err != nil {
			return err
		}
		mb, err = q.ConnectMailboxOAuth(ctx, dbq.ConnectMailboxOAuthParams{
			ID: mb.ID, AuthType: p.AuthType, EmailAddress: email,
			ImapHost: p.IMAP.Host, ImapPort: int32(p.IMAP.Port), ImapTls: p.IMAP.TLS, //nolint:gosec // provider constants
			SmtpHost: p.SMTP.Host, SmtpPort: int32(p.SMTP.Port), SmtpTls: p.SMTP.TLS, //nolint:gosec // provider constants
			OauthTokenEnc: enc, OauthExpiresAt: pgtype.Timestamptz{Time: tok.Expiry, Valid: true},
		})
		if err != nil {
			return err
		}
		if err := audit.Write(ctx, q, mailboxAudit(r, audit.MailboxOAuthConnected, mb.ID, map[string]any{"provider": p.ID, "reconnect": reconnect})); err != nil {
			return err
		}
		return notifyScopeChanged(ctx, q)
	})
	if isUniqueViolation(err) {
		return dbq.Mailbox{}, "email_taken", nil
	}
	return mb, failure, err
}

func writeOAuthAudit(r *http.Request, s *Server, action string, target pgtype.UUID, meta map[string]any) {
	if err := db.InTx(r.Context(), s.pool, func(q *dbq.Queries) error {
		return audit.Write(r.Context(), q, mailboxAudit(r, action, target, meta))
	}); err != nil {
		slog.ErrorContext(r.Context(), "audit oauth failure", "err", err, "request_id", requestIDFrom(r.Context()))
	}
}
