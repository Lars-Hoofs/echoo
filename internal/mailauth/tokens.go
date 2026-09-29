package mailauth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/oauth2"

	"echoo/internal/db"
	"echoo/internal/db/dbq"
	"echoo/internal/keyring"
)

// ReasonReauthRequired is stored in mailboxes.sync_error when the provider no longer accepts
// the refresh token; the UI turns it into a request to reconnect.
const ReasonReauthRequired = "oauth_reauth_required"

// refreshSkew makes a token that is about to expire count as expired, so it cannot lapse
// between fetching it and the server checking it.
const refreshSkew = 2 * time.Minute

const refreshTimeout = 20 * time.Second

var (
	// ErrReauthRequired means the account has to be connected again by an admin.
	ErrReauthRequired = errors.New("mail account must be reconnected")
	ErrNotOAuth       = errors.New("mailbox does not use OAuth")
)

// Token is the credential set stored, encrypted, in mailboxes.oauth_token_enc.
type Token struct {
	AccessToken  string    `json:"access_token"`
	RefreshToken string    `json:"refresh_token"`
	Expiry       time.Time `json:"expiry"`
}

// Manager hands out access tokens for OAuth mailboxes and refreshes them when needed.
type Manager struct {
	pool      *pgxpool.Pool
	q         *dbq.Queries
	keys      *keyring.Keyring
	providers *Providers
	baseURL   string
	client    *http.Client
	now       func() time.Time
}

// NewManager creates the token source. baseURL is ECHOO_BASE_URL without a trailing slash;
// providers use it as the redirect URI, which refresh requests do not need but the oauth2
// configuration carries anyway.
func NewManager(pool *pgxpool.Pool, keys *keyring.Keyring, providers *Providers, baseURL string) *Manager {
	return &Manager{
		pool: pool, q: dbq.New(pool), keys: keys, providers: providers, baseURL: baseURL,
		client: &http.Client{Timeout: refreshTimeout}, now: time.Now,
	}
}

func tokenAAD(mailboxID pgtype.UUID) []byte {
	return keyring.AAD("mailboxes", "oauth_token_enc", mailboxID.String())
}

// Seal encrypts a token for the given mailbox row.
func (m *Manager) Seal(mailboxID pgtype.UUID, t Token) ([]byte, error) {
	raw, err := json.Marshal(t) //nolint:gosec // the plaintext only exists to be sealed with the keyring on the next line
	if err != nil {
		return nil, fmt.Errorf("marshal oauth token: %w", err)
	}
	return m.keys.Encrypt(raw, tokenAAD(mailboxID))
}

func (m *Manager) open(mailboxID pgtype.UUID, enc []byte) (Token, error) {
	var t Token
	if len(enc) == 0 {
		return t, ErrReauthRequired
	}
	raw, err := m.keys.Decrypt(enc, tokenAAD(mailboxID))
	if err != nil {
		return t, fmt.Errorf("decrypt oauth token: %w", err)
	}
	if err := json.Unmarshal(raw, &t); err != nil {
		return t, fmt.Errorf("decode oauth token: %w", err)
	}
	return t, nil
}

func (m *Manager) fresh(t Token) bool {
	return t.AccessToken != "" && t.Expiry.After(m.now().Add(refreshSkew))
}

// AccessToken returns a valid access token for the mailbox, refreshing it first when it is
// (nearly) expired. It returns ErrReauthRequired when the provider rejects the refresh token.
func (m *Manager) AccessToken(ctx context.Context, mailboxID pgtype.UUID) (string, error) {
	row, err := m.q.GetMailboxOAuth(ctx, mailboxID)
	if err != nil {
		return "", fmt.Errorf("load mailbox token: %w", err)
	}
	if row.AuthType == "password" {
		return "", ErrNotOAuth
	}
	tok, err := m.open(mailboxID, row.OauthTokenEnc)
	if err != nil {
		return "", err
	}
	if m.fresh(tok) {
		return tok.AccessToken, nil
	}
	return m.refresh(ctx, mailboxID)
}

// refresh runs under a transaction-scoped advisory lock, so the IMAP sync, the send job and
// the connection test never refresh the same mailbox at once: providers that rotate refresh
// tokens invalidate the old one on use, and a lost race would lock the account out.
func (m *Manager) refresh(ctx context.Context, mailboxID pgtype.UUID) (string, error) {
	var access string
	var reauth bool
	err := db.InTx(ctx, m.pool, func(q *dbq.Queries) error {
		if err := q.LockMailboxOAuth(ctx, mailboxID.String()); err != nil {
			return fmt.Errorf("lock mailbox token: %w", err)
		}
		row, err := q.GetMailboxOAuth(ctx, mailboxID)
		if err != nil {
			return fmt.Errorf("reload mailbox token: %w", err)
		}
		tok, err := m.open(mailboxID, row.OauthTokenEnc)
		if err != nil {
			return err
		}
		if m.fresh(tok) {
			access = tok.AccessToken
			return nil
		}
		provider, ok := m.providers.ByAuthType(row.AuthType)
		if !ok {
			return fmt.Errorf("oauth provider for %s is not configured", row.AuthType)
		}
		next, err := m.exchangeRefreshToken(ctx, provider, tok.RefreshToken)
		var rerr *oauth2.RetrieveError
		if errors.As(err, &rerr) && rerr.ErrorCode == "invalid_grant" {
			reauth = true
			return q.MarkMailboxReauthRequired(ctx, dbq.MarkMailboxReauthRequiredParams{ID: mailboxID, Reason: ReasonReauthRequired})
		}
		if err != nil {
			return fmt.Errorf("refresh oauth token: %w", err)
		}
		enc, err := m.Seal(mailboxID, next)
		if err != nil {
			return err
		}
		if err := q.StoreRefreshedOAuthToken(ctx, dbq.StoreRefreshedOAuthTokenParams{
			ID: mailboxID, OauthTokenEnc: enc, OauthExpiresAt: pgtype.Timestamptz{Time: next.Expiry, Valid: true},
		}); err != nil {
			return fmt.Errorf("store refreshed token: %w", err)
		}
		access = next.AccessToken
		return nil
	})
	if err != nil {
		return "", err
	}
	if reauth {
		return "", ErrReauthRequired
	}
	return access, nil
}

func (m *Manager) exchangeRefreshToken(ctx context.Context, p *Provider, refreshToken string) (Token, error) {
	ctx = context.WithValue(ctx, oauth2.HTTPClient, m.client)
	got, err := p.Config(m.baseURL).TokenSource(ctx, &oauth2.Token{RefreshToken: refreshToken}).Token()
	if err != nil {
		return Token{}, err
	}
	next := Token{AccessToken: got.AccessToken, RefreshToken: got.RefreshToken, Expiry: got.Expiry}
	// Google keeps the refresh token; Microsoft rotates it. Either way the newest one wins.
	if next.RefreshToken == "" {
		next.RefreshToken = refreshToken
	}
	return next, nil
}

// Exchange trades an authorization code for tokens. verifier is the PKCE code verifier. The
// returned email comes from the id_token.
func (m *Manager) Exchange(ctx context.Context, p *Provider, code, verifier string) (Token, string, error) {
	ctx = context.WithValue(ctx, oauth2.HTTPClient, m.client)
	got, err := p.Config(m.baseURL).Exchange(ctx, code, oauth2.VerifierOption(verifier))
	if err != nil {
		return Token{}, "", fmt.Errorf("exchange authorization code: %w", err)
	}
	if got.RefreshToken == "" {
		return Token{}, "", ErrNoRefreshToken
	}
	idToken, _ := got.Extra("id_token").(string)
	email, err := AccountEmail(idToken, p.ClientID)
	if err != nil {
		return Token{}, "", fmt.Errorf("read account: %w", err)
	}
	return Token{AccessToken: got.AccessToken, RefreshToken: got.RefreshToken, Expiry: got.Expiry}, email, nil
}

// ErrNoRefreshToken means the provider granted access without offline access, which cannot
// keep a mailbox connected.
var ErrNoRefreshToken = errors.New("the provider returned no refresh token")

// AuthURL returns the provider's authorization URL for an attempt with the given state.
func (m *Manager) AuthURL(p *Provider, state, verifier, loginHint string) string {
	opts := append([]oauth2.AuthCodeOption{oauth2.S256ChallengeOption(verifier)}, p.AuthParams...)
	if loginHint != "" {
		opts = append(opts, oauth2.SetAuthURLParam("login_hint", loginHint))
	}
	return p.Config(m.baseURL).AuthCodeURL(state, opts...)
}

// Providers returns the configured providers.
func (m *Manager) Providers() *Providers { return m.providers }

// Keys returns the keyring, for sealing the authorization state.
func (m *Manager) Keys() *keyring.Keyring { return m.keys }
