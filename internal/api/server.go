// Package api wires HTTP routes, middleware and handlers.
package api

import (
	"context"
	"crypto/tls"
	"io/fs"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"

	"echoo/internal/auth"
	"echoo/internal/automation"
	"echoo/internal/campaigns"
	"echoo/internal/config"
	"echoo/internal/csat"
	"echoo/internal/db/dbq"
	"echoo/internal/inbox"
	"echoo/internal/keyring"
	"echoo/internal/mailauth"
	"echoo/internal/metrics"
	"echoo/internal/push"
	"echoo/internal/realtime"
	"echoo/internal/reports"
	"echoo/internal/scan"
	"echoo/internal/sso"
	"echoo/internal/storage"
	"echoo/internal/sysmail"
)

// MailboxReloader is told to re-read the mailbox settings after an admin changed them.
type MailboxReloader interface {
	Reload(ctx context.Context) error
}

type Server struct {
	cfg       *config.Config
	pool      *pgxpool.Pool
	q         *dbq.Queries
	auth      *auth.Service
	web       fs.FS
	keys      *keyring.Keyring
	reloader  MailboxReloader
	testTLS   *tls.Config
	hub       *realtime.Hub
	store     storage.Store
	scanner   scan.Scanner
	jobs      *river.Client[pgx.Tx]
	inbox     *inbox.Service
	engine    *automation.Engine
	proxy     *imageProxy
	oauth     *mailauth.Manager
	sysmail   *sysmail.Sender
	reports   *reports.Service
	csat      *csat.Service
	campaigns *campaigns.Service
	kb        *kbState
	sso       *sso.Service
	push      push.Senders

	ready   readiness
	metrics *metrics.Registry

	tokenLimiter    *auth.Limiter
	badTokenLimiter *auth.Limiter

	// Password reset and link pages are public, so they are limited per client and, for reset
	// requests, per account, which stops the form from being used to flood one mailbox.
	resetIPLimiter      *auth.Limiter
	resetAccountLimiter *auth.Limiter
	linkLimiter         *auth.Limiter
}

type Option func(*Server)

// WithKeyring enables storing mailbox passwords; without it, saving one fails.
func WithKeyring(keys *keyring.Keyring) Option { return func(s *Server) { s.keys = keys } }

// WithMailboxReloader makes mailbox changes take effect in the running IMAP sync.
func WithMailboxReloader(r MailboxReloader) Option { return func(s *Server) { s.reloader = r } }

// WithMailboxTestTLS is only for tests, to trust a self-signed mail server certificate.
// Certificate verification stays on whatever the config says.
func WithMailboxTestTLS(c *tls.Config) Option { return func(s *Server) { s.testTLS = c } }

// WithScanner virus-scans uploads before they are stored. Without it uploads are not scanned.
func WithScanner(sc scan.Scanner) Option { return func(s *Server) { s.scanner = sc } }

// WithStorage gives handlers access to the blob store for uploads and attachments.
func WithStorage(store storage.Store) Option { return func(s *Server) { s.store = store } }

// WithJobs lets handlers enqueue background jobs in their own transaction, such as sending mail.
func WithJobs(c *river.Client[pgx.Tx]) Option { return func(s *Server) { s.jobs = c } }

// WithOAuth enables OAuth2 mailboxes (Google, Microsoft). Without it no provider is offered.
func WithOAuth(m *mailauth.Manager) Option { return func(s *Server) { s.oauth = m } }

// WithSysmail enables email for invitations and password resets. Without it those features
// tell the admin that system mail is not configured.
func WithSysmail(m *sysmail.Sender) Option { return func(s *Server) { s.sysmail = m } }

// WithPush sends the test notification and tells clients which push services are set up.
func WithPush(p push.Senders) Option { return func(s *Server) { s.push = p } }

// WithMetrics records request counts and durations by route pattern.
func WithMetrics(m *metrics.Registry) Option { return func(s *Server) { s.metrics = m } }

func New(cfg *config.Config, pool *pgxpool.Pool, authSvc *auth.Service, web fs.FS, opts ...Option) *Server {
	s := &Server{cfg: cfg, pool: pool, q: dbq.New(pool), auth: authSvc, web: web,
		inbox: inbox.NewService(pool),
		hub:   realtime.NewHub(pool, slog.Default(), realtime.Options{}), reports: reports.New(pool)}
	s.tokenLimiter, s.badTokenLimiter = newTokenLimiters()
	s.proxy = newImageProxy()
	s.resetIPLimiter = auth.NewLimiter(10, time.Hour)
	s.resetAccountLimiter = auth.NewLimiter(3, time.Hour)
	s.linkLimiter = auth.NewLimiter(20, time.Minute)
	for _, opt := range opts {
		opt(s)
	}
	if s.jobs != nil {
		s.inbox = s.inbox.WithJobs(s.jobs)
	}
	s.engine = automation.NewEngine(automation.Deps{Pool: pool, Jobs: s.jobs})
	s.csat = s.newCSAT()
	s.kb = s.newKB()
	s.campaigns = s.newCampaigns()
	s.sso = sso.New(s.ssoRedirectURI())
	return s
}

func (s *Server) cookieName() string {
	// The __Host- prefix makes the browser enforce Secure, Path=/ and no Domain attribute.
	if s.cfg.SecureCookies() {
		return "__Host-echoo_session"
	}
	return "echoo_session"
}

func (s *Server) setSessionCookie(w http.ResponseWriter, sess *auth.Session) {
	// Secure is only false for http://localhost development (enforced by config).
	http.SetCookie(w, &http.Cookie{ //nolint:gosec // see above
		Name: s.cookieName(), Value: sess.Token, Path: "/",
		Expires:  sess.ExpiresAt.Time,
		HttpOnly: true, Secure: s.cfg.SecureCookies(), SameSite: http.SameSiteLaxMode,
	})
}

func (s *Server) clearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{ //nolint:gosec // same attributes as setSessionCookie
		Name: s.cookieName(), Value: "", Path: "/", MaxAge: -1,
		HttpOnly: true, Secure: s.cfg.SecureCookies(), SameSite: http.SameSiteLaxMode,
	})
}

func (s *Server) Handler() *chi.Mux {
	r := chi.NewRouter()
	r.Use(requestID, clientIP(s.cfg.TrustedProxies), accessLog, s.instrument, recoverer, securityHeaders(s.cfg.SecureCookies()))

	r.Get("/healthz", s.healthz)
	r.Get("/readyz", s.readyz)

	loginLimiter := auth.NewLimiter(10, time.Minute)
	presenceLimiter := auth.NewLimiter(120, time.Minute)
	ssoLimiter := auth.NewLimiter(30, time.Minute)

	r.Route("/api/v1", func(r chi.Router) {
		r.Use(noStore, s.checkOrigin, s.loadSession)
		r.NotFound(func(w http.ResponseWriter, r *http.Request) { writeError(w, r, errNotFound) })
		r.MethodNotAllowed(func(w http.ResponseWriter, r *http.Request) {
			writeError(w, r, &apiError{Status: http.StatusMethodNotAllowed, Code: "method_not_allowed", Message: "method not allowed"})
		})

		r.With(limitByIP(loginLimiter)).Post("/auth/login", s.login)
		r.Get("/auth/sso", s.ssoInfo)
		s.accountLinkRoutes(r)

		r.Group(func(r chi.Router) {
			r.Use(requireSession(true), requireCSRF)
			r.With(sessionOnly, limitByIP(loginLimiter)).Post("/auth/mfa", s.verifyMFA)
			r.With(sessionOnly).Post("/auth/logout", s.logout)
		})

		r.Group(func(r chi.Router) {
			r.Use(requireSession(false), requireCSRF)

			// Account setup stays reachable while a password change or 2FA enrollment is pending.
			r.Get("/me", s.me)
			r.With(sessionOnly).Post("/me/password", s.changePassword)
			r.With(sessionOnly).Post("/me/totp/setup", s.beginTOTP)
			r.With(sessionOnly).Post("/me/totp/enable", s.enableTOTP)

			r.Group(func(r chi.Router) {
				r.Use(s.requireAccountReady)
				r.Patch("/me", s.updateMe)
				r.With(sessionOnly).Post("/me/totp/disable", s.disableTOTP)
				r.With(sessionOnly).Post("/me/recovery-codes", s.regenerateRecoveryCodes)
				r.Get("/inbox/summary", s.inboxSummary)
				r.Get("/conversations", s.listConversations)
				r.Get("/conversations/{id}", s.getConversation)
				r.Post("/conversations/{id}/messages/{mid}/allow-images", s.allowImages)
				r.Get("/attachments/{id}/download", s.downloadAttachment)
				r.Patch("/conversations/{id}", s.patchConversation)
				r.Put("/conversations/{id}/labels", s.putConversationLabels)
				r.Post("/conversations/bulk", s.bulkConversations)
				r.Get("/conversations/{id}/events", s.listConversationEvents)
				r.Get("/assignees", s.listAssignees)
				r.Get("/labels", s.listLabels)
				r.With(sessionOnly).Get("/events", s.streamEvents)
				r.With(sessionOnly, limitByUser(presenceLimiter)).Post("/conversations/{id}/presence", s.postPresence)
				r.Get("/conversations/{id}/presence", s.getPresence)
				r.Get("/agents/online", s.onlineAgents)
				r.With(sessionOnly).Get("/me/sessions", s.listSessions)
				r.With(sessionOnly).Delete("/me/sessions/{id}", s.revokeSession)
				r.With(sessionOnly).Post("/me/sessions/revoke-others", s.revokeOtherSessions)
				r.Get("/me/tokens", s.listMyTokens)
				r.With(sessionOnly).Post("/me/tokens", s.createMyToken)
				r.With(sessionOnly).Delete("/me/tokens/{id}", s.revokeMyToken)
				r.Get("/openapi.yaml", s.openAPI)
				r.Get("/me/notification-settings", s.getNotificationSettings)
				r.Put("/me/notification-settings", s.putNotificationSettings)
				s.composerRoutes(r)
				s.unreadRoutes(r)
				s.automationRoutes(r)
				s.searchRoutes(r)
				s.contactRoutes(r)
				s.reportRoutes(r)
				s.kbRoutes(r)
				s.trashRoutes(r)
				s.blocklistRoutes(r)
				s.pushRoutes(r)

				r.Group(func(r chi.Router) {
					r.Use(requirePermission)
					r.Post("/labels", s.createLabel)
					r.Patch("/labels/{id}", s.updateLabel)
					r.Delete("/labels/{id}", s.deleteLabel)
					r.Get("/users", s.listUsers)
					r.Post("/users", s.createUser)
					r.Patch("/users/{id}", s.updateUser)
					r.Post("/users/{id}/reset-password", s.resetUserPassword)
					r.Post("/users/{id}/reset-mfa", s.resetUserMFA)

					r.Get("/teams", s.listTeams)
					r.Post("/teams", s.createTeam)
					r.Patch("/teams/{id}", s.renameTeam)
					r.Delete("/teams/{id}", s.deleteTeam)
					r.Get("/teams/{id}/members", s.listTeamMembers)
					r.Put("/teams/{id}/members", s.setTeamMembers)

					s.mailAccountAdminRoutes(r)
					r.Get("/mailboxes", s.listMailboxes)
					r.Post("/mailboxes", s.createMailbox)
					r.Post("/mailboxes/test", s.testMailboxConnection)
					r.Get("/mailboxes/{id}", s.getMailbox)
					r.Patch("/mailboxes/{id}", s.updateMailbox)
					r.Post("/mailboxes/{id}/disable", s.disableMailbox)
					r.Post("/mailboxes/{id}/enable", s.enableMailbox)
					r.Get("/mailboxes/{id}/access", s.getMailboxAccess)
					r.Put("/mailboxes/{id}/access", s.putMailboxAccess)

					r.Get("/audit", s.listAudit)
					r.Get("/settings/security", s.getSecuritySettings)
					r.Put("/settings/security", s.putSecuritySettings)
					s.composerAdminRoutes(r)
					s.automationAdminRoutes(r)
					s.contactAdminRoutes(r)
					s.reportAdminRoutes(r)
					s.kbAdminRoutes(r)
					s.campaignRoutes(r)
					s.opsAdminRoutes(r)
					r.Get("/audit/export", s.exportAudit)

					r.Get("/tokens", s.listAllTokens)
					r.With(sessionOnly).Delete("/tokens/{id}", s.revokeAnyToken)

					r.Get("/webhooks", s.listWebhooks)
					r.Post("/webhooks", s.createWebhook)
					r.Patch("/webhooks/{id}", s.updateWebhook)
					r.Delete("/webhooks/{id}", s.deleteWebhook)
					r.Post("/webhooks/{id}/test", s.testWebhook)
					r.Get("/webhooks/{id}/deliveries", s.listWebhookDeliveries)
					r.Post("/webhooks/{id}/deliveries/{deliveryId}/resend", s.resendWebhookDelivery)

					r.Get("/jobs", s.listJobs)
					r.Post("/jobs/{id}/retry", s.retryJob)
					r.Get("/raw-messages", s.listRawMessageProblems)
					r.Post("/raw-messages/{id}/retry", s.retryRawMessage)
					s.roleRoutes(r)
				})

				r.Group(func(r chi.Router) {
					r.Use(requireOwner)
					s.ssoRoutes(r)
					s.opsOwnerRoutes(r)
				})
			})
		})
	})

	// The mail render surface lives outside /api: its documents are framed by the app and its
	// subresources are fetched by a sandboxed, cookie-less origin. See render_sign.go.
	r.Route("/render", func(r chi.Router) {
		r.With(s.loadSession, requireSession(false), s.requireAccountReady).Get("/messages/{id}", s.renderMessage)
		r.Get("/attachments/{id}", s.renderAttachment)
		r.Get("/proxy", s.renderProxy)
	})

	// Single sign-on is a browser round trip through the identity provider: it navigates to
	// these two routes and is answered with redirects, so they live outside /api.
	r.With(noStore, limitByIP(ssoLimiter)).Get("/auth/sso/start", s.ssoStart)
	r.With(noStore, limitByIP(ssoLimiter)).Get("/auth/sso/callback", s.ssoCallback)

	// The provider redirects the admin's browser here, so it lives outside /api and answers with
	// redirects to the settings page, not JSON.
	r.With(noStore, s.loadSession).Get("/oauth/callback/{provider}", s.oauthCallback)

	s.publicFontRoutes(r)
	s.csatPublicRoutes(r)
	s.kbPublicRoutes(r)
	s.campaignPublicRoutes(r)

	r.NotFound(s.spa().ServeHTTP)
	return r
}
