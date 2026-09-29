package api

import (
	"context"
	"crypto/tls"
	"errors"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"echoo/internal/audit"
	"echoo/internal/db"
	"echoo/internal/db/dbq"
	"echoo/internal/mail/imapsync"
	"echoo/internal/mail/send"
	"echoo/internal/mailauth"
)

const connectionTestTimeout = 15 * time.Second

type probeResult struct {
	OK   bool   `json:"ok"`
	Code string `json:"code,omitempty"`
}

func (s *Server) mailboxTLS(host string) *tls.Config {
	cfg := &tls.Config{MinVersion: tls.VersionTLS12}
	if s.testTLS != nil {
		cfg = s.testTLS.Clone()
	}
	// The connection goes to a pinned IP, but the certificate must match the configured name.
	cfg.ServerName = host
	return cfg
}

// tryTargets runs fn against each resolved address until one gets past connecting. Only
// connect failures move on to the next address; anything else is the server's answer.
func tryTargets(ctx context.Context, host string, allowInternal bool, connectErr error, fn func(ip string) error) error {
	ips, err := dialTargets(ctx, host, allowInternal)
	if err != nil {
		return errors.Join(connectErr, err)
	}
	for _, ip := range ips {
		if err = fn(ip); err == nil || !errors.Is(err, connectErr) {
			return err
		}
	}
	return err
}

func (s *Server) probeIMAP(ctx context.Context, m mailboxSettings, password, accessToken string) probeResult {
	err := tryTargets(ctx, m.IMAPHost, m.AllowInternalHost, imapsync.ErrConnect, func(ip string) error {
		return imapsync.TestConnection(ctx, imapsync.ConnConfig{
			Host: ip, Port: m.IMAPPort, TLS: imapsync.TLSMode(m.IMAPTLS),
			Username: m.IMAPUsername, Password: password, AccessToken: accessToken,
			Timeout: connectionTestTimeout, AllowInternal: m.AllowInternalHost, TLSConfig: s.mailboxTLS(m.IMAPHost),
		})
	})
	switch {
	case err == nil:
		return probeResult{OK: true}
	case errors.Is(err, imapsync.ErrAuth):
		return probeResult{Code: "imap_auth_failed"}
	case errors.Is(err, imapsync.ErrTLS):
		return probeResult{Code: "imap_tls_failed"}
	case errors.Is(err, imapsync.ErrNoInbox):
		return probeResult{Code: "imap_no_inbox"}
	case errors.Is(err, imapsync.ErrConnect):
		return probeResult{Code: "imap_connect_failed"}
	}
	slog.ErrorContext(ctx, "imap connection test: unclassified error", "err", err)
	return probeResult{Code: "imap_connect_failed"}
}

func (s *Server) probeSMTP(ctx context.Context, m mailboxSettings, password, accessToken string) probeResult {
	err := tryTargets(ctx, m.SMTPHost, m.AllowInternalHost, send.ErrProbeConnect, func(ip string) error {
		return send.Probe(ctx, send.SMTPConfig{
			Host: m.SMTPHost, Port: m.SMTPPort, TLS: send.TLSMode(m.SMTPTLS),
			Username: m.SMTPUsername, Password: password, AccessToken: accessToken, AllowInternal: m.AllowInternalHost, TLSConfig: s.mailboxTLS(m.SMTPHost),
		}, ip)
	})
	switch {
	case err == nil:
		return probeResult{OK: true}
	case errors.Is(err, send.ErrProbeAuth):
		return probeResult{Code: "smtp_auth_failed"}
	case errors.Is(err, send.ErrProbeTLS):
		return probeResult{Code: "smtp_tls_failed"}
	case errors.Is(err, send.ErrProbeConnect):
		return probeResult{Code: "smtp_connect_failed"}
	}
	slog.ErrorContext(ctx, "smtp connection test: unclassified error", "err", err)
	return probeResult{Code: "smtp_connect_failed"}
}

// testMailboxConnection checks IMAP and SMTP settings before they are saved. With an id, the
// body is applied on top of that mailbox and its stored passwords are used for the ones not
// provided, as long as the server they belong to is unchanged (checkSecrets); such a test is
// audited.
func (s *Server) testMailboxConnection(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID *string `json:"id"`
		mailboxInput
	}
	if err := decode(r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	in := req.mailboxInput
	base := defaultMailboxSettings()
	var existing *dbq.Mailbox
	if req.ID != nil {
		id, ok := parseUUID(*req.ID)
		if !ok {
			writeError(w, r, errValidation(map[string]string{"id": "invalid"}))
			return
		}
		mb, err := s.q.GetMailbox(r.Context(), id)
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, r, errValidation(map[string]string{"id": "unknown"}))
			return
		}
		if err != nil {
			writeError(w, r, err)
			return
		}
		existing, base = &mb, settingsOfMailbox(mb)
	}
	m, fields := in.merge(base)
	viaOAuth := existing != nil && existing.AuthType != "password"
	if viaOAuth {
		in.checkOAuthManaged(base, m, fields)
	} else {
		in.checkSecrets(base, m, existing != nil && len(existing.ImapSecretEnc) > 0, existing != nil && len(existing.SmtpSecretEnc) > 0, fields)
	}
	if err := checkInternalHosts(r.Context(), sessionFrom(r.Context()).User, base, m, fields); err != nil {
		writeError(w, r, err)
		return
	}
	if len(fields) > 0 {
		writeError(w, r, errValidation(fields))
		return
	}

	var imapPW, smtpPW, token string
	var err error
	if viaOAuth {
		if token, err = s.oauthToken(r.Context(), existing.ID); errors.Is(err, mailauth.ErrReauthRequired) {
			writeJSON(w, http.StatusOK, map[string]any{"imap": probeResult{Code: "oauth_reauth_required"}, "smtp": probeResult{Code: "oauth_reauth_required"}})
			return
		} else if err != nil {
			writeError(w, r, err)
			return
		}
	} else if in.IMAPPassword != nil {
		imapPW = *in.IMAPPassword
	} else if imapPW, err = s.decryptSecret(existing.ID, "imap_secret_enc", existing.ImapSecretEnc); err != nil {
		writeError(w, r, err)
		return
	}
	switch {
	case viaOAuth:
	case in.SMTPPassword != nil:
		smtpPW = *in.SMTPPassword
	case m.SMTPUsername != "":
		if smtpPW, err = s.decryptSecret(existing.ID, "smtp_secret_enc", existing.SmtpSecretEnc); err != nil {
			writeError(w, r, err)
			return
		}
	}

	ctx, cancel := context.WithTimeout(r.Context(), connectionTestTimeout)
	defer cancel()
	var imapRes, smtpRes probeResult
	var wg sync.WaitGroup
	wg.Go(func() { imapRes = s.probeIMAP(ctx, m, imapPW, token) })
	wg.Go(func() { smtpRes = s.probeSMTP(ctx, m, smtpPW, token) })
	wg.Wait()
	usedIMAP, usedSMTP := !viaOAuth && in.IMAPPassword == nil, !viaOAuth && in.SMTPPassword == nil && smtpPW != ""
	if existing != nil && (usedIMAP || usedSMTP) {
		err := db.InTx(r.Context(), s.pool, func(q *dbq.Queries) error {
			return audit.Write(r.Context(), q, mailboxAudit(r, audit.MailboxStoredSecretTest, existing.ID, map[string]any{"imap": usedIMAP, "smtp": usedSMTP}))
		})
		if err != nil {
			writeError(w, r, err)
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"imap": imapRes, "smtp": smtpRes})
}

func (s *Server) oauthToken(ctx context.Context, id pgtype.UUID) (string, error) {
	if s.oauth == nil {
		return "", errOAuthUnavailable
	}
	return s.oauth.AccessToken(ctx, id)
}
