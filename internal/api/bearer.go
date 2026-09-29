package api

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"echoo/internal/auth"
)

const (
	// tokenRateLimit is per API token; a runaway integration must not starve the agents.
	tokenRateLimit = 600
	// badTokenRateLimit bounds failed token lookups per client, so guessing costs the caller
	// more than it costs the database.
	badTokenRateLimit = 60
)

type bearerCtxKey struct{}

var errTokenReadOnly = &apiError{Status: http.StatusForbidden, Code: "token_read_only", Message: "this token has the read scope only"}

var errSessionRequired = &apiError{Status: http.StatusForbidden, Code: "session_required", Message: "this action needs a signed-in browser session, not an API token"}

func newTokenLimiters() (perToken, badPerClient *auth.Limiter) {
	return auth.NewLimiter(tokenRateLimit, time.Minute), auth.NewLimiter(badTokenRateLimit, time.Minute)
}

func bearerFrom(ctx context.Context) *auth.APIPrincipal {
	p, _ := ctx.Value(bearerCtxKey{}).(*auth.APIPrincipal)
	return p
}

// serveBearer authenticates a request that carries an Authorization header. From here on the
// cookie is ignored, and because nothing ambient (a cookie) authorizes the request, neither the
// Origin nor the CSRF checks apply: a cross-site page cannot know the token.
func (s *Server) serveBearer(w http.ResponseWriter, r *http.Request, next http.Handler) {
	scheme, token, _ := strings.Cut(r.Header.Get("Authorization"), " ")
	if !strings.EqualFold(scheme, "Bearer") {
		s.bearerFailed(w, r)
		return
	}
	p, err := s.auth.AuthenticateAPIToken(r.Context(), strings.TrimSpace(token))
	if errors.Is(err, auth.ErrInvalidAPIToken) {
		s.bearerFailed(w, r)
		return
	}
	if err != nil {
		writeError(w, r, err)
		return
	}
	if !s.tokenLimiter.Allow(uuidStr(p.Token.ID)) {
		w.Header().Set("Retry-After", "60")
		writeError(w, r, errRateLimited)
		return
	}
	if !isSafeMethod(r.Method) && !p.CanWrite() {
		writeError(w, r, errTokenReadOnly)
		return
	}
	ctx := context.WithValue(r.Context(), ctxSession, &auth.Session{User: p.User})
	ctx = context.WithValue(ctx, bearerCtxKey{}, p)
	next.ServeHTTP(w, r.WithContext(ctx))
}

func (s *Server) bearerFailed(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("WWW-Authenticate", "Bearer")
	if !s.badTokenLimiter.Allow(limiterKey(clientFrom(r).IP)) {
		writeError(w, r, errRateLimited)
		return
	}
	writeError(w, r, errUnauthenticated)
}

// sessionOnly keeps credential and session management out of reach of API tokens, so a leaked
// token cannot be used to mint a longer-lived one or to change how the account signs in.
func sessionOnly(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if bearerFrom(r.Context()) != nil {
			writeError(w, r, errSessionRequired)
			return
		}
		next.ServeHTTP(w, r)
	})
}
