package api

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"runtime/debug"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"echoo/internal/auth"
	"echoo/internal/logging"
)

type ctxKey int

const (
	ctxRequestID ctxKey = iota
	ctxClientIP
	ctxSession
)

func requestIDFrom(ctx context.Context) string {
	id, _ := ctx.Value(ctxRequestID).(string)
	return id
}

func sessionFrom(ctx context.Context) *auth.Session {
	s, _ := ctx.Value(ctxSession).(*auth.Session)
	return s
}

func clientFrom(r *http.Request) auth.Client {
	c := auth.Client{UserAgent: r.UserAgent()}
	if ip, ok := r.Context().Value(ctxClientIP).(netip.Addr); ok {
		c.IP = &ip
	}
	return c
}

func requestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b := make([]byte, 8)
		if _, err := rand.Read(b); err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		id := hex.EncodeToString(b)
		w.Header().Set("X-Request-Id", id)
		ctx := logging.WithRequestID(context.WithValue(r.Context(), ctxRequestID, id), id)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// clientIP uses X-Forwarded-For only when the direct peer is a trusted proxy, and then takes
// the right-most address that is not itself a trusted proxy.
func clientIP(trusted []netip.Prefix) func(http.Handler) http.Handler {
	isTrusted := func(a netip.Addr) bool {
		for _, p := range trusted {
			if p.Contains(a) {
				return true
			}
		}
		return false
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			host, _, err := net.SplitHostPort(r.RemoteAddr)
			if err != nil {
				host = r.RemoteAddr
			}
			ip, err := netip.ParseAddr(host)
			if err == nil {
				ip = ip.Unmap()
				if isTrusted(ip) {
					hops := strings.Split(r.Header.Get("X-Forwarded-For"), ",")
					for i := len(hops) - 1; i >= 0; i-- {
						hop, err := netip.ParseAddr(strings.TrimSpace(hops[i]))
						if err != nil {
							break
						}
						ip = hop.Unmap()
						if !isTrusted(ip) {
							break
						}
					}
				}
				r = r.WithContext(context.WithValue(r.Context(), ctxClientIP, ip))
			}
			next.ServeHTTP(w, r)
		})
	}
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (s *statusRecorder) WriteHeader(code int) {
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusRecorder) Unwrap() http.ResponseWriter { return s.ResponseWriter }

// accessLog logs the route pattern, never the raw path or query string, which may contain
// identifiers or search terms.
func accessLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		route := "unmatched"
		if rc := chi.RouteContext(r.Context()); rc != nil && rc.RoutePattern() != "" {
			route = rc.RoutePattern()
		}
		slog.InfoContext(r.Context(), "http",
			"method", r.Method, "route", route, "status", rec.status,
			"duration_ms", time.Since(start).Milliseconds(), "request_id", requestIDFrom(r.Context()))
	})
}

func recoverer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if v := recover(); v != nil {
				if err, ok := v.(error); ok && errors.Is(err, http.ErrAbortHandler) {
					panic(v)
				}
				slog.ErrorContext(r.Context(), "panic", "value", v, "stack", string(debug.Stack()), "request_id", requestIDFrom(r.Context()))
				writeError(w, r, errors.New("panic"))
			}
		}()
		next.ServeHTTP(w, r)
	})
}

func securityHeaders(hsts bool) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			h := w.Header()
			h.Set("X-Content-Type-Options", "nosniff")
			h.Set("Referrer-Policy", "no-referrer")
			h.Set("X-Frame-Options", "DENY")
			h.Set("Cross-Origin-Opener-Policy", "same-origin")
			h.Set("Cross-Origin-Resource-Policy", "same-origin")
			h.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=(), payment=(), usb=()")
			h.Set("Content-Security-Policy", "default-src 'none'; frame-ancestors 'none'; base-uri 'none'; form-action 'none'")
			if hsts {
				h.Set("Strict-Transport-Security", "max-age=63072000; includeSubDomains")
			}
			next.ServeHTTP(w, r)
		})
	}
}

func noStore(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		next.ServeHTTP(w, r)
	})
}

func isSafeMethod(m string) bool {
	return m == http.MethodGet || m == http.MethodHead || m == http.MethodOptions
}

// checkOrigin rejects cross-site state-changing requests. Browsers always send Origin on
// POST/PUT/PATCH/DELETE; requests without it must at least be marked same-origin.
func (s *Server) checkOrigin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// A bearer token is not ambient authority, so there is nothing to forge (see serveBearer).
		if !isSafeMethod(r.Method) && r.Header.Get("Authorization") == "" {
			origin := r.Header.Get("Origin")
			switch {
			case origin != "":
				if origin != s.cfg.Origin() {
					writeError(w, r, errCSRF)
					return
				}
			case r.Header.Get("Sec-Fetch-Site") != "same-origin":
				writeError(w, r, errCSRF)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// loadSession attaches the session if the cookie is present and valid. It does not reject.
func (s *Server) loadSession(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			s.serveBearer(w, r, next)
			return
		}
		c, err := r.Cookie(s.cookieName())
		if err == nil {
			sess, err := s.auth.Authenticate(r.Context(), c.Value)
			switch {
			case err == nil:
				r = r.WithContext(context.WithValue(r.Context(), ctxSession, sess))
			case !errors.Is(err, auth.ErrNoSession):
				writeError(w, r, err)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// requireSession rejects requests without a session; pending 2FA sessions are only accepted
// where allowPending is set.
func requireSession(allowPending bool) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			sess := sessionFrom(r.Context())
			if sess == nil || (sess.MfaPending && !allowPending) {
				writeError(w, r, errUnauthenticated)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// requireCSRF checks the per-session token on state-changing requests.
func requireCSRF(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !isSafeMethod(r.Method) && bearerFrom(r.Context()) == nil {
			sess := sessionFrom(r.Context())
			got := r.Header.Get("X-CSRF-Token")
			if sess == nil || got == "" || subtle.ConstantTimeCompare([]byte(got), []byte(sess.CsrfToken)) != 1 {
				writeError(w, r, errCSRF)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// requireAccountReady blocks everything except account setup while the user still has to
// change a temporary password or enroll in 2FA.
func (s *Server) requireAccountReady(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sess := sessionFrom(r.Context())
		if sess.User.PasswordMustChange {
			writeError(w, r, &apiError{Status: http.StatusForbidden, Code: "password_change_required", Message: "change your temporary password first"})
			return
		}
		required, err := s.mfaEnrollmentRequired(r.Context(), sess)
		if err != nil {
			writeError(w, r, err)
			return
		}
		if required {
			writeError(w, r, &apiError{Status: http.StatusForbidden, Code: "mfa_enrollment_required", Message: "set up two-factor authentication first"})
			return
		}
		next.ServeHTTP(w, r)
	})
}

// limiterKey buckets IPv6 clients by /64: a single subscriber usually controls a whole /64,
// so per-address buckets would give an attacker practically unlimited fresh keys.
func limiterKey(ip *netip.Addr) string {
	switch {
	case ip == nil:
		return "unknown"
	case ip.Is6():
		return netip.PrefixFrom(*ip, 64).Masked().String()
	}
	return ip.String()
}

func limitByIP(l *auth.Limiter) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			key := limiterKey(clientFrom(r).IP)
			if !l.Allow(key) {
				writeError(w, r, errRateLimited)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
