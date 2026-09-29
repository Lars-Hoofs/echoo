package api

import (
	"errors"
	"html/template"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"echoo/internal/auth"
	"echoo/internal/campaigns"
)

// The unsubscribe page is public and tiny, like the survey page, and shares its stylesheet and
// content security policy: no script, no external requests, no cookies.
var unsubscribeTemplate = template.Must(template.New("unsubscribe").Parse(`<!doctype html>
<html lang="nl">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<meta name="robots" content="noindex,nofollow">
<title>{{.Title}}</title>
<style>` + csatCSS + `</style>
</head>
<body>
<main>
<h1>{{.Title}}</h1>
{{if .Message}}<p class="notice{{if .IsError}} error{{end}}" role="{{if .IsError}}alert{{else}}status{{end}}">{{.Message}}</p>{{end}}
{{if .Form}}
<p>Wil je geen e-mail meer ontvangen van {{.Brand}}?{{if .Email}} Dit geldt voor {{.Email}}.{{end}}</p>
<form method="post" action="{{.Action}}">
<input type="hidden" name="List-Unsubscribe" value="One-Click">
<button type="submit">Afmelden</button>
</form>
{{end}}
</main>
</body>
</html>
`))

type unsubscribePage struct {
	Title, Message string
	IsError        bool
	Form           bool
	Brand, Email   string
	Action         string
}

func writeUnsubscribePage(w http.ResponseWriter, status int, p unsubscribePage) {
	h := w.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Content-Security-Policy", csatCSP)
	h.Set("Cache-Control", "no-store")
	h.Set("X-Robots-Tag", "noindex, nofollow")
	w.WriteHeader(status)
	if err := unsubscribeTemplate.Execute(w, p); err != nil {
		slog.Error("render unsubscribe page", "err", err)
	}
}

func unsubscribeMessage(w http.ResponseWriter, status int, title, message string) {
	writeUnsubscribePage(w, status, unsubscribePage{Title: title, Message: message, IsError: status >= 400})
}

func (s *Server) campaignPublicRoutes(r chi.Router) {
	// One person opening a few links stays far below this; a script guessing tokens does not.
	// Mail providers post one-click requests from shared addresses, hence not lower.
	limiter := auth.NewLimiter(60, time.Minute)
	r.With(s.unsubscribeGate(limiter)).Get("/afmelden/{token}", s.unsubscribeShow)
	r.With(s.unsubscribeGate(limiter)).Post("/afmelden/{token}", s.unsubscribeSubmit)
}

func (s *Server) unsubscribeGate(l *auth.Limiter) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !l.Allow(limiterKey(clientFrom(r).IP)) {
				w.Header().Set("Retry-After", "60")
				unsubscribeMessage(w, http.StatusTooManyRequests, "Even geduld", "Er zijn te veel verzoeken gedaan. Probeer het over een minuut opnieuw.")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func unsubscribeLinkError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, campaigns.ErrExpiredToken):
		unsubscribeMessage(w, http.StatusGone, "Link verlopen", "Deze link is niet meer geldig. Antwoord op de e-mail om je af te melden.")
	case errors.Is(err, campaigns.ErrInvalidToken):
		unsubscribeMessage(w, http.StatusNotFound, "Link ongeldig", "Deze link is niet geldig. Controleer of je de hele link uit de e-mail hebt gebruikt.")
	default:
		slog.ErrorContext(r.Context(), "unsubscribe page failed", "err", err, "request_id", requestIDFrom(r.Context()))
		unsubscribeMessage(w, http.StatusInternalServerError, "Er ging iets mis", "Afmelden is niet gelukt. Probeer het later opnieuw.")
	}
}

func unsubscribeDone(w http.ResponseWriter, u campaigns.Unsubscription) {
	unsubscribeMessage(w, http.StatusOK, "Je bent afgemeld", "Je ontvangt geen e-mail meer van "+u.Brand+".")
}

// Opening the link only shows the confirmation form: mail scanners and link previews open
// links, and must not unsubscribe anyone.
func (s *Server) unsubscribeShow(w http.ResponseWriter, r *http.Request) {
	token := chi.URLParam(r, "token")
	u, err := s.campaigns.LookupUnsubscribe(r.Context(), token)
	if err != nil {
		unsubscribeLinkError(w, r, err)
		return
	}
	if u.Done {
		unsubscribeDone(w, u)
		return
	}
	writeUnsubscribePage(w, http.StatusOK, unsubscribePage{
		Title: "Afmelden", Form: true, Brand: u.Brand, Email: u.Email, Action: "/afmelden/" + token,
	})
}

const maxUnsubscribeBody = 4 << 10

// The POST is both the confirmation of the page and the one-click request of RFC 8058, which
// mail providers send without a session, origin or cookies. The token is the credential, so
// there is no CSRF check: a forged request needs the token, and does what its holder wants.
func (s *Server) unsubscribeSubmit(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxUnsubscribeBody)
	if err := r.ParseForm(); err != nil {
		unsubscribeMessage(w, http.StatusBadRequest, "Ongeldige aanvraag", "Afmelden kon niet worden gelezen. Probeer het opnieuw.")
		return
	}
	u, err := s.campaigns.Unsubscribe(r.Context(), chi.URLParam(r, "token"))
	if err != nil {
		unsubscribeLinkError(w, r, err)
		return
	}
	unsubscribeDone(w, u)
}
