// Package sso implements the OpenID Connect authorization code flow with PKCE against the
// identity provider the owner configured. It talks to the provider and validates its answers;
// which Echoo user a validated identity maps to is decided by the auth package.
package sso

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"

	"echoo/internal/netguard"
)

const (
	// discoveryTTL bounds how long a provider's endpoints are reused before they are fetched
	// again, so a provider that moved its endpoints is picked up without a restart.
	discoveryTTL = 15 * time.Minute
	// fetchTimeout covers one request to the provider, connection setup included.
	fetchTimeout = 10 * time.Second
	dialTimeout  = 5 * time.Second
	// maxResponseBytes caps every answer of the provider; discovery documents, key sets and
	// token responses are a few kilobytes.
	maxResponseBytes = 1 << 20
)

// Failure reasons. They end up in the audit log and in the redirect to the login page, so
// they say what went wrong without carrying anything the provider sent.
const (
	ReasonDiscovery       = "discovery_failed"
	ReasonInsecure        = "insecure_endpoint"
	ReasonExchange        = "exchange_failed"
	ReasonNoIDToken       = "no_id_token"
	ReasonInvalidIDToken  = "invalid_id_token"
	ReasonNonce           = "nonce_mismatch"
	ReasonEmailMissing    = "email_missing"
	ReasonEmailUnverified = "email_unverified"
)

// Error carries a reason code; Cause is for the server log only.
type Error struct {
	Reason string
	Cause  error
}

func (e *Error) Error() string {
	if e.Cause == nil {
		return "sso: " + e.Reason
	}
	return "sso: " + e.Reason + ": " + e.Cause.Error()
}

func (e *Error) Unwrap() error { return e.Cause }

func fail(reason string, cause error) *Error { return &Error{Reason: reason, Cause: cause} }

// Config is what the flow needs from the stored settings.
type Config struct {
	IssuerURL    string
	ClientID     string
	ClientSecret string
	// AllowInternalIssuer lets the provider live on a private network and use plain HTTP.
	AllowInternalIssuer bool
	// TrustMissingEmailVerified accepts identities whose provider sends no email_verified
	// claim at all (Microsoft Entra ID). A claim that says false is always refused.
	TrustMissingEmailVerified bool
}

// Flow is what has to survive the round trip through the browser.
type Flow struct {
	State    string `json:"state"`
	Nonce    string `json:"nonce"`
	Verifier string `json:"verifier"`
}

// Identity is a validated sign-in. MFA is true when the provider reported a second factor.
type Identity struct {
	Subject string
	Email   string
	Name    string
	MFA     bool
}

type Service struct {
	redirectURL string
	now         func() time.Time
	guarded     *http.Client
	internal    *http.Client

	mu    sync.Mutex
	cache map[string]cachedProvider
}

type cachedProvider struct {
	provider *oidc.Provider
	fetched  time.Time
}

// New creates the service; redirectURL is the callback URL registered at the provider.
func New(redirectURL string) *Service {
	return &Service{
		redirectURL: redirectURL, now: time.Now, cache: map[string]cachedProvider{},
		guarded: newClient(false), internal: newClient(true),
	}
}

func newClient(allowInternal bool) *http.Client {
	return &http.Client{
		Timeout: fetchTimeout,
		Transport: &limitedTransport{base: &http.Transport{
			DialContext:         netguard.Dialer(allowInternal, dialTimeout).DialContext,
			TLSHandshakeTimeout: dialTimeout,
			// No proxy: the address check runs on the connection that is really made.
			Proxy: nil,
		}},
		CheckRedirect: func(_ *http.Request, via []*http.Request) error {
			if len(via) >= 3 {
				return errors.New("too many redirects")
			}
			return nil
		},
	}
}

type limitedTransport struct{ base http.RoundTripper }

func (t *limitedTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	resp, err := t.base.RoundTrip(r)
	if err != nil {
		return nil, err
	}
	resp.Body = &limitedBody{Reader: io.LimitReader(resp.Body, maxResponseBytes), Closer: resp.Body}
	return resp, nil
}

type limitedBody struct {
	io.Reader
	io.Closer
}

func (s *Service) client(cfg Config) *http.Client {
	if cfg.AllowInternalIssuer {
		return s.internal
	}
	return s.guarded
}

// ValidIssuerURL checks the shape of an issuer URL. Plain HTTP is only for providers on an
// internal network, which the owner has to allow explicitly.
func ValidIssuerURL(raw string, allowInternal bool) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return false
	}
	return u.Scheme == "https" || (u.Scheme == "http" && allowInternal)
}

func cacheKey(cfg Config) string {
	return fmt.Sprintf("%s|%t", cfg.IssuerURL, cfg.AllowInternalIssuer)
}

// Discover fetches the provider's configuration. It is used when saving the settings, so a
// wrong issuer is found before anybody depends on it; it always refreshes the cache.
func (s *Service) Discover(ctx context.Context, cfg Config) error {
	_, err := s.provider(ctx, cfg, true)
	return err
}

// Forget drops what is cached about the provider, for after the settings changed.
func (s *Service) Forget() {
	s.mu.Lock()
	defer s.mu.Unlock()
	clear(s.cache)
}

func (s *Service) provider(ctx context.Context, cfg Config, refresh bool) (*oidc.Provider, error) {
	key := cacheKey(cfg)
	s.mu.Lock()
	entry, ok := s.cache[key]
	s.mu.Unlock()
	if ok && !refresh && s.now().Sub(entry.fetched) < discoveryTTL {
		return entry.provider, nil
	}
	if !ValidIssuerURL(cfg.IssuerURL, cfg.AllowInternalIssuer) {
		return nil, fail(ReasonDiscovery, errors.New("invalid issuer URL"))
	}
	p, err := oidc.NewProvider(oidc.ClientContext(ctx, s.client(cfg)), cfg.IssuerURL)
	if err != nil {
		return nil, fail(ReasonDiscovery, err)
	}
	// The client secret goes to the token endpoint, and the user is sent to the authorization
	// endpoint, so a discovery document must not point them at plain HTTP on the internet.
	if !cfg.AllowInternalIssuer {
		ep := p.Endpoint()
		for _, raw := range []string{ep.AuthURL, ep.TokenURL} {
			if u, err := url.Parse(raw); err != nil || u.Scheme != "https" {
				return nil, fail(ReasonInsecure, nil)
			}
		}
	}
	s.mu.Lock()
	s.cache[key] = cachedProvider{provider: p, fetched: s.now()}
	s.mu.Unlock()
	return p, nil
}

func (s *Service) oauthConfig(cfg Config, p *oidc.Provider) *oauth2.Config {
	return &oauth2.Config{
		ClientID: cfg.ClientID, ClientSecret: cfg.ClientSecret, Endpoint: p.Endpoint(),
		RedirectURL: s.redirectURL, Scopes: []string{oidc.ScopeOpenID, "email", "profile"},
	}
}

func randomToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("random token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// Begin starts a sign-in: it returns the provider's authorization URL and the flow values
// the callback needs to see again.
func (s *Service) Begin(ctx context.Context, cfg Config) (string, Flow, error) {
	p, err := s.provider(ctx, cfg, false)
	if err != nil {
		return "", Flow{}, err
	}
	state, err := randomToken()
	if err != nil {
		return "", Flow{}, err
	}
	nonce, err := randomToken()
	if err != nil {
		return "", Flow{}, err
	}
	flow := Flow{State: state, Nonce: nonce, Verifier: oauth2.GenerateVerifier()}
	authURL := s.oauthConfig(cfg, p).AuthCodeURL(state,
		oidc.Nonce(nonce), oauth2.S256ChallengeOption(flow.Verifier))
	return authURL, flow, nil
}

// Complete exchanges the authorization code and validates the ID token: signature, issuer,
// audience, expiry and the nonce of this flow. The email has to be verified.
func (s *Service) Complete(ctx context.Context, cfg Config, flow Flow, code string) (Identity, error) {
	p, err := s.provider(ctx, cfg, false)
	if err != nil {
		return Identity{}, err
	}
	ctx = oidc.ClientContext(ctx, s.client(cfg))
	tok, err := s.oauthConfig(cfg, p).Exchange(ctx, code, oauth2.VerifierOption(flow.Verifier))
	if err != nil {
		return Identity{}, fail(ReasonExchange, err)
	}
	raw, _ := tok.Extra("id_token").(string)
	if raw == "" {
		return Identity{}, fail(ReasonNoIDToken, nil)
	}
	verifier := p.VerifierContext(ctx, &oidc.Config{ClientID: cfg.ClientID, Now: s.now})
	idToken, err := verifier.Verify(ctx, raw)
	if err != nil {
		return Identity{}, fail(ReasonInvalidIDToken, err)
	}
	if idToken.Nonce == "" || idToken.Nonce != flow.Nonce {
		return Identity{}, fail(ReasonNonce, nil)
	}
	var claims struct {
		Email         string   `json:"email"`
		EmailVerified any      `json:"email_verified"`
		Name          string   `json:"name"`
		AMR           []string `json:"amr"`
	}
	if err := idToken.Claims(&claims); err != nil {
		return Identity{}, fail(ReasonInvalidIDToken, err)
	}
	if strings.TrimSpace(claims.Email) == "" {
		return Identity{}, fail(ReasonEmailMissing, nil)
	}
	if !emailVerified(claims.EmailVerified, cfg.TrustMissingEmailVerified) {
		return Identity{}, fail(ReasonEmailUnverified, nil)
	}
	return Identity{Subject: idToken.Subject, Email: claims.Email, Name: claims.Name, MFA: hasMFA(claims.AMR)}, nil
}

// emailVerified reads the claim as providers send it: a boolean, or the string "true".
func emailVerified(claim any, trustMissing bool) bool {
	switch v := claim.(type) {
	case nil:
		return trustMissing
	case bool:
		return v
	case string:
		return strings.EqualFold(v, "true")
	}
	return false
}

// hasMFA reports whether the authentication methods (RFC 8176) include a second factor.
func hasMFA(amr []string) bool {
	return slices.ContainsFunc(amr, func(m string) bool {
		switch strings.ToLower(m) {
		case "mfa", "otp", "hwk":
			return true
		}
		return false
	})
}

// LogFailure writes the cause of a failed provider request to the server log. It holds the
// provider's error text, which the audit log and the browser never see.
func LogFailure(ctx context.Context, err error) {
	var e *Error
	if errors.As(err, &e) && e.Cause != nil {
		slog.WarnContext(ctx, "sso provider request failed", "reason", e.Reason, "error", e.Cause)
	}
}
