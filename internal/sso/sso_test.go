package sso

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"echoo/internal/netguard"
)

func discoveryServer(t *testing.T, tokenScheme string) *httptest.Server {
	t.Helper()
	var srv *httptest.Server
	srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		token := srv.URL + "/token"
		if tokenScheme == "http" {
			token = "http://idp.example.com/token"
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"issuer": srv.URL, "authorization_endpoint": srv.URL + "/authorize", "token_endpoint": token, "jwks_uri": srv.URL + "/jwks",
		})
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestDiscoveryRefusesPlainHTTPEndpoints(t *testing.T) {
	for scheme, want := range map[string]string{"https": "", "http": ReasonInsecure} {
		srv := discoveryServer(t, scheme)
		svc := New("https://echoo.test/auth/sso/callback")
		svc.guarded = srv.Client() // trusts the test certificate; the address guard is covered below
		err := svc.Discover(context.Background(), Config{IssuerURL: srv.URL})
		var e *Error
		switch {
		case want == "" && err != nil:
			t.Errorf("%s token endpoint: %v", scheme, err)
		case want != "" && (!errors.As(err, &e) || e.Reason != want):
			t.Errorf("%s token endpoint: got %v, want %s", scheme, err, want)
		}
	}
}

func TestDiscoveryDoesNotReachInternalAddresses(t *testing.T) {
	srv := discoveryServer(t, "https") // listens on 127.0.0.1
	svc := New("https://echoo.test/auth/sso/callback")
	err := svc.Discover(context.Background(), Config{IssuerURL: srv.URL})
	var e *Error
	if !errors.As(err, &e) || e.Reason != ReasonDiscovery || !errors.Is(err, netguard.ErrInternal) {
		t.Fatalf("got %v", err)
	}
}

func TestValidIssuerURL(t *testing.T) {
	for _, c := range []struct {
		url      string
		internal bool
		want     bool
	}{
		{"https://login.example.com/realms/acme", false, true},
		{"https://login.example.com", false, true},
		{"http://login.example.com", false, false},
		{"http://login.internal:8080", true, true},
		{"ftp://login.example.com", true, false},
		{"https://user:pw@login.example.com", false, false},
		{"https://login.example.com?x=1", false, false},
		{"https://login.example.com#frag", false, false},
		{"https://", false, false},
		{"login.example.com", false, false},
	} {
		if got := ValidIssuerURL(c.url, c.internal); got != c.want {
			t.Errorf("%q (internal %v): got %v", c.url, c.internal, got)
		}
	}
}

func TestEmailVerifiedClaim(t *testing.T) {
	for _, c := range []struct {
		claim        any
		trustMissing bool
		want         bool
	}{
		{true, false, true}, {false, false, false}, {false, true, false},
		{"true", false, true}, {"TRUE", false, true}, {"false", false, false}, {"yes", false, false},
		{nil, false, false}, {nil, true, true}, {1.0, true, false},
	} {
		if got := emailVerified(c.claim, c.trustMissing); got != c.want {
			t.Errorf("claim %v trustMissing %v: got %v", c.claim, c.trustMissing, got)
		}
	}
}

func TestHasMFA(t *testing.T) {
	for amr, want := range map[string]bool{"mfa": true, "otp": true, "hwk": true, "MFA": true, "pwd": false, "swk": false, "": false} {
		if got := hasMFA([]string{"pwd", amr}); got != want {
			t.Errorf("amr %q: got %v", amr, got)
		}
	}
	if hasMFA(nil) {
		t.Error("no amr means no second factor")
	}
}
