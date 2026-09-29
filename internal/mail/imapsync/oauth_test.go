package imapsync

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapserver"
	"github.com/emersion/go-sasl"
	"github.com/jackc/pgx/v5/pgtype"

	"echoo/internal/db/dbq"
	"echoo/internal/mailauth"
)

const goodToken = "ya29.good-access-token"

// xoauth2Session accepts XOAUTH2 for goodToken and rejects everything else the way Gmail does:
// a JSON error challenge first, the tagged NO after the client's empty answer.
type xoauth2Session struct {
	imapserver.Session
	logins *[]string
}

func (s xoauth2Session) AuthenticateMechanisms() []string { return []string{"XOAUTH2"} }

func (s xoauth2Session) Authenticate(mech string) (sasl.Server, error) {
	if mech != "XOAUTH2" {
		return nil, &imap.Error{Type: imap.StatusResponseTypeNo, Text: "mechanism not supported"}
	}
	return &xoauth2Server{s: s}, nil
}

type xoauth2Server struct {
	s        xoauth2Session
	rejected bool
}

func (x *xoauth2Server) Next(resp []byte) ([]byte, bool, error) {
	if x.rejected {
		return nil, false, &imap.Error{Type: imap.StatusResponseTypeNo, Code: imap.ResponseCodeAuthenticationFailed, Text: "authentication failed"}
	}
	user, token, ok := parseXOAuth2(string(resp))
	*x.s.logins = append(*x.s.logins, user)
	if ok && user == testUser && token == goodToken {
		return nil, true, x.s.Login(testUser, testPassword)
	}
	x.rejected = true
	return []byte(`{"status":"401","schemes":"Bearer","scope":"https://mail.google.com/"}`), false, nil
}

func parseXOAuth2(s string) (user, token string, ok bool) {
	parts := strings.Split(s, "\x01")
	if len(parts) != 4 || parts[2] != "" || parts[3] != "" {
		return "", "", false
	}
	user, ok1 := strings.CutPrefix(parts[0], "user=")
	token, ok2 := strings.CutPrefix(parts[1], "auth=Bearer ")
	return user, token, ok1 && ok2
}

func startOAuthServer(t *testing.T) (*testServer, *[]string) {
	t.Helper()
	logins := new([]string)
	srv := startServer(t, TLSImplicit, func(s imapserver.Session) imapserver.Session { return xoauth2Session{Session: s, logins: logins} })
	return srv, logins
}

func (s *testServer) oauthConfig(token string) ConnConfig {
	cfg := s.connConfig(TLSImplicit)
	cfg.Password, cfg.AccessToken = "", token
	return cfg
}

func TestTestConnectionAuthenticatesWithXOAuth2(t *testing.T) {
	srv, logins := startOAuthServer(t)
	if err := TestConnection(context.Background(), srv.oauthConfig(goodToken)); err != nil {
		t.Fatalf("good token: %v", err)
	}
	if len(*logins) != 1 || (*logins)[0] != testUser {
		t.Errorf("server saw users %v", *logins)
	}
	if err := TestConnection(context.Background(), srv.oauthConfig("revoked")); !errors.Is(err, ErrAuth) {
		t.Errorf("bad token: got %v, want ErrAuth", err)
	}
}

type fakeTokens struct {
	token string
	err   error
}

func (f fakeTokens) AccessToken(context.Context, pgtype.UUID) (string, error) { return f.token, f.err }

func (h *harness) makeOAuth(tokens TokenSource) {
	h.t.Helper()
	_, err := h.pool.Exec(context.Background(), `UPDATE mailboxes SET auth_type = 'oauth_google', imap_secret_enc = NULL, oauth_connected_at = now() WHERE id = $1`, h.mailboxID)
	if err != nil {
		h.t.Fatal(err)
	}
	h.deps.Tokens = tokens
}

func TestSupervisorSyncsAnOAuthMailbox(t *testing.T) {
	srv, _ := startOAuthServer(t)
	h := newHarness(t, srv, mailboxOpts{}, nil)
	h.makeOAuth(fakeTokens{token: goodToken})
	srv.deliver(t, 2)
	h.run(h.supervisor())
	h.waitRaw(2)
	h.waitState("connected")
}

func TestSupervisorReportsAReconnectNeed(t *testing.T) {
	srv, _ := startOAuthServer(t)
	h := newHarness(t, srv, mailboxOpts{}, nil)
	h.makeOAuth(fakeTokens{err: mailauth.ErrReauthRequired})
	h.run(h.supervisor())
	h.waitState("auth_failed")
	if _, reason := h.syncState(); reason != mailauth.ReasonReauthRequired {
		t.Errorf("sync_error = %q, want %q", reason, mailauth.ReasonReauthRequired)
	}
}

func TestSupervisorTreatsARejectedAccessTokenAsAuthFailure(t *testing.T) {
	srv, _ := startOAuthServer(t)
	h := newHarness(t, srv, mailboxOpts{}, nil)
	h.makeOAuth(fakeTokens{token: "expired"})
	h.run(h.supervisor())
	h.waitState("auth_failed")
}

func TestReconnectRestartsARunningSupervisor(t *testing.T) {
	before := dbq.Mailbox{AuthType: "oauth_google", OauthConnectedAt: pgtype.Timestamptz{Time: time.Unix(1000, 0), Valid: true}}
	same := before
	if settingsOf(before) != settingsOf(same) {
		t.Fatal("identical mailboxes must have equal settings")
	}
	after := before
	after.OauthConnectedAt.Time = time.Unix(2000, 0)
	if settingsOf(before) == settingsOf(after) {
		t.Error("a reconnect must change the settings, or a supervisor sleeping after auth_failed never restarts")
	}
}
