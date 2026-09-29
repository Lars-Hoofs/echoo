package api

import (
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"html/template"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"echoo/internal/auth"
	"echoo/internal/csat"
)

// newCSAT returns the survey service, or nil when there is no keyring to sign tokens with.
func (s *Server) newCSAT() *csat.Service {
	if s.keys == nil {
		return nil
	}
	return csat.NewService(csat.Deps{
		Pool: s.pool, Signer: csat.NewSigner(s.keys.DeriveAll("csat-token")...), BaseURL: strings.TrimSuffix(s.cfg.BaseURL.String(), "/"),
	})
}

// The survey page is public and tiny: server-rendered HTML with one inline stylesheet, no
// script, no external requests, no cookies and nothing that identifies the visitor.
const csatCSS = `
@font-face{font-family:Urbanist;font-weight:300;font-display:swap;src:url(/fonts/urbanist-latin-300-normal.woff2) format("woff2")}
@font-face{font-family:Urbanist;font-weight:400;font-display:swap;src:url(/fonts/urbanist-latin-400-normal.woff2) format("woff2")}
:root{color-scheme:light dark;--bg:#ececec;--card:#fff;--soft:#f1f1f0;--ink:#0d0d0d;--muted:#666;--line:rgba(0,0,0,.07);--line-strong:rgba(0,0,0,.14);--alert:#ff4d3a;--font:Urbanist,"Helvetica Neue",Arial,system-ui,sans-serif}
@media (prefers-color-scheme:dark){:root{--bg:#0b0c0d;--card:#17191b;--soft:#202326;--ink:#f2f2f2;--muted:rgba(255,255,255,.62);--line:rgba(255,255,255,.08);--line-strong:rgba(255,255,255,.16)}}
*{box-sizing:border-box}
body{margin:0;min-height:100vh;display:flex;align-items:center;justify-content:center;padding:32px 16px;background:var(--bg);color:var(--ink);font:400 16px/1.5 var(--font);-webkit-font-smoothing:antialiased}
main{width:100%;max-width:480px;background:var(--card);border-radius:24px;padding:24px}
@media (min-width:640px){main{padding:32px}}
h1{margin:0 0 16px;font-size:28px;font-weight:300;line-height:1.1;letter-spacing:-.02em}
p{margin:0 0 16px}
.muted{color:var(--muted);font-size:14px}
.kicker{margin:0 0 8px;font-size:12px}
form+.muted{margin-top:16px}
fieldset{margin:0 0 24px;padding:0;border:0}
legend{padding:0;margin:0 0 16px;font-size:20px;line-height:1.3}
.scale{display:grid;grid-template-columns:repeat(5,1fr);gap:8px}
.scale input{position:absolute;opacity:0;width:1px;height:1px}
.scale label{display:flex;align-items:center;justify-content:center;min-height:56px;border:1px solid var(--line-strong);border-radius:999px;font-size:20px;font-weight:300;cursor:pointer;transition:background-color 160ms cubic-bezier(.55,0,.25,1),color 160ms cubic-bezier(.55,0,.25,1)}
.scale label:hover{background:var(--soft)}
.scale input:checked+label{background:var(--ink);border-color:var(--ink);color:var(--bg)}
.scale input:focus-visible+label{outline:2px solid var(--ink);outline-offset:2px}
.ends{display:flex;justify-content:space-between;margin-top:8px;font-size:12px}
label.field{display:block;margin:0 0 8px;font-size:14px}
textarea{width:100%;min-height:96px;padding:12px 16px;border:1px solid var(--line-strong);border-radius:12px;background:transparent;color:inherit;font:inherit;font-size:14px;resize:vertical}
textarea:focus-visible{outline:0;border-color:var(--ink)}
button:focus-visible{outline:2px solid var(--ink);outline-offset:2px}
button{margin-top:16px;height:44px;padding:0 24px;border:0;border-radius:999px;background:var(--ink);color:var(--bg);font:inherit;font-size:14px;cursor:pointer;transition:opacity 160ms cubic-bezier(.55,0,.25,1)}
button:hover{opacity:.86}
button:active{opacity:.72}
.notice{margin:0 0 16px;padding:12px 16px;background:var(--soft);border-radius:12px;font-size:14px}
.error{box-shadow:inset 0 0 0 1px var(--alert)}
`

var csatCSP = func() string {
	sum := sha256.Sum256([]byte(csatCSS))
	return "default-src 'none'; font-src 'self'; style-src 'sha256-" + base64.StdEncoding.EncodeToString(sum[:]) + "'; form-action 'self'; frame-ancestors 'none'; base-uri 'none'"
}()

var csatTemplate = template.Must(template.New("csat").Parse(`<!doctype html>
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
{{if .Brand}}<p class="muted kicker">{{.Brand}}</p>{{end}}
<h1>{{.Title}}</h1>
{{if .Message}}<p class="notice{{if .IsError}} error{{end}}" role="{{if .IsError}}alert{{else}}status{{end}}">{{.Message}}</p>{{end}}
{{if .Form}}
<form method="post" action="{{.Action}}">
<input type="hidden" name="csrf" value="{{.CSRF}}">
<fieldset>
<legend>Hoe tevreden bent u over de hulp die u kreeg?</legend>
<div class="scale">
{{range .Ratings}}<input type="radio" name="rating" id="r{{.Value}}" value="{{.Value}}"{{if .Checked}} checked{{end}} required><label for="r{{.Value}}" title="{{.Label}}">{{.Value}}</label>
{{end}}
</div>
<div class="ends muted"><span>1 = zeer ontevreden</span><span>5 = zeer tevreden</span></div>
</fieldset>
<label class="field" for="comment">Wilt u iets toelichten? <span class="muted">(niet verplicht)</span></label>
<textarea id="comment" name="comment" maxlength="1000">{{.Comment}}</textarea>
<button type="submit">{{if .Answered}}Wijzig mijn beoordeling{{else}}Verstuur{{end}}</button>
</form>
{{end}}
{{if .Footer}}<p class="muted">{{.Footer}}</p>{{end}}
</main>
</body>
</html>
`))

type ratingOption struct {
	Value   int
	Label   string
	Checked bool
}

type csatPage struct {
	Title, Brand, Message string
	IsError               bool
	Form, Answered        bool
	Action, CSRF, Comment string
	Ratings               []ratingOption
	Footer                string
}

var dutchMonths = [...]string{"januari", "februari", "maart", "april", "mei", "juni", "juli", "augustus", "september", "oktober", "november", "december"}

func dutchDate(t time.Time) string {
	return fmt.Sprintf("%d %s %d", t.Day(), dutchMonths[t.Month()-1], t.Year())
}

var ratingWords = [5]string{"zeer ontevreden", "ontevreden", "gemiddeld", "tevreden", "zeer tevreden"}

func ratingOptions(selected int) []ratingOption {
	out := make([]ratingOption, 5)
	for i := range out {
		out[i] = ratingOption{Value: i + 1, Label: ratingWords[i], Checked: i+1 == selected}
	}
	return out
}

func writeCSATPage(w http.ResponseWriter, status int, p csatPage) {
	h := w.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Content-Security-Policy", csatCSP)
	h.Set("Cache-Control", "no-store")
	h.Set("X-Robots-Tag", "noindex, nofollow")
	w.WriteHeader(status)
	if err := csatTemplate.Execute(w, p); err != nil {
		slog.Error("render survey page", "err", err, "request_id", h.Get("X-Request-Id"))
	}
}

func csatMessagePage(w http.ResponseWriter, status int, title, message string) {
	writeCSATPage(w, status, csatPage{Title: title, Message: message, IsError: status >= 400})
}

func (s *Server) csatPublicRoutes(r chi.Router) {
	// One person opening a few links stays far below this; a script guessing tokens does not.
	limiter := auth.NewLimiter(30, time.Minute)
	r.With(s.csatGate(limiter)).Get("/tevredenheid/{token}", s.csatShow)
	r.With(s.csatGate(limiter)).Post("/tevredenheid/{token}", s.csatSubmit)
}

// csatGate rate limits per client and answers with a page, since a person reads this response.
func (s *Server) csatGate(l *auth.Limiter) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if s.csat == nil {
				csatMessagePage(w, http.StatusNotFound, "Pagina niet gevonden", "Deze pagina bestaat niet.")
				return
			}
			if !l.Allow(limiterKey(clientFrom(r).IP)) {
				w.Header().Set("Retry-After", "60")
				csatMessagePage(w, http.StatusTooManyRequests, "Even geduld", "Er zijn te veel verzoeken gedaan. Probeer het over een minuut opnieuw.")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func (s *Server) csatLinkError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, csat.ErrExpired):
		csatMessagePage(w, http.StatusGone, "Link verlopen", "Deze link is niet meer geldig. Een beoordeling kan tot 30 dagen na het versturen van de e-mail worden gegeven.")
	case errors.Is(err, csat.ErrInvalid):
		csatMessagePage(w, http.StatusNotFound, "Link ongeldig", "Deze link is niet geldig. Controleer of u de hele link uit de e-mail hebt gebruikt.")
	default:
		slog.ErrorContext(r.Context(), "survey page failed", "err", err, "request_id", requestIDFrom(r.Context()))
		csatMessagePage(w, http.StatusInternalServerError, "Er ging iets mis", "Uw beoordeling kon niet worden verwerkt. Probeer het later opnieuw.")
	}
}

func (s *Server) surveyForm(token string, sv csat.Survey, rating int, comment string) csatPage {
	p := csatPage{
		Title: "Uw mening", Brand: sv.Brand, Form: true, Answered: sv.Rating > 0,
		Action: "/tevredenheid/" + token, CSRF: s.csat.CSRF(token), Comment: comment, Ratings: ratingOptions(rating),
	}
	if sv.Rating > 0 {
		p.Footer = "U kunt uw beoordeling nog wijzigen tot " + dutchDate(sv.ChangeUntil) + "."
	}
	return p
}

func (s *Server) csatShow(w http.ResponseWriter, r *http.Request) {
	token := chi.URLParam(r, "token")
	sv, err := s.csat.Lookup(r.Context(), token)
	if err != nil {
		s.csatLinkError(w, r, err)
		return
	}
	if sv.Locked {
		csatMessagePage(w, http.StatusOK, "Bedankt voor uw beoordeling", "Uw beoordeling is ontvangen. Wijzigen kan niet meer.")
		return
	}
	rating, comment := sv.Rating, sv.Comment
	if sv.Rating == 0 {
		if n, err := strconv.Atoi(r.URL.Query().Get("r")); err == nil && n >= 1 && n <= 5 {
			rating = n
		}
	}
	p := s.surveyForm(token, sv, rating, comment)
	if r.URL.Query().Get("opgeslagen") == "1" && sv.Rating > 0 {
		p.Title, p.Message = "Bedankt voor uw beoordeling", "Uw beoordeling is opgeslagen."
	}
	writeCSATPage(w, http.StatusOK, p)
}

// sameOrigin accepts a submit that comes from our own page. Browsers that send Sec-Fetch-Site
// are trusted on it: the survey page sets Referrer-Policy no-referrer, and under that policy
// browsers send Origin: null on a form post, so Origin alone would refuse real customers. Older
// browsers must send our own Origin.
func (s *Server) sameOrigin(r *http.Request) bool {
	switch r.Header.Get("Sec-Fetch-Site") {
	case "same-origin":
		return true
	case "":
		return r.Header.Get("Origin") == s.cfg.Origin()
	}
	return false
}

const maxSurveyBody = 16 << 10

func (s *Server) csatSubmit(w http.ResponseWriter, r *http.Request) {
	token := chi.URLParam(r, "token")
	if !s.sameOrigin(r) {
		csatMessagePage(w, http.StatusForbidden, "Niet toegestaan", "Deze aanvraag kwam niet van de beoordelingspagina. Open de link uit de e-mail opnieuw.")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxSurveyBody)
	if err := r.ParseForm(); err != nil {
		csatMessagePage(w, http.StatusBadRequest, "Ongeldige aanvraag", "Uw beoordeling kon niet worden gelezen. Probeer het opnieuw.")
		return
	}
	if !s.csat.CheckCSRF(token, r.PostForm.Get("csrf")) {
		csatMessagePage(w, http.StatusForbidden, "Niet toegestaan", "Deze aanvraag kwam niet van de beoordelingspagina. Open de link uit de e-mail opnieuw.")
		return
	}
	rating, _ := strconv.Atoi(r.PostForm.Get("rating"))
	comment := r.PostForm.Get("comment")
	_, err := s.csat.Submit(r.Context(), token, rating, comment)
	switch {
	case err == nil:
		// The path is fixed and token passed verification, so it is base64url and nothing else.
		http.Redirect(w, r, "/tevredenheid/"+token+"?opgeslagen=1", http.StatusSeeOther) //nolint:gosec // see above
	case errors.Is(err, csat.ErrInvalidInput):
		sv, lerr := s.csat.Lookup(r.Context(), token)
		if lerr != nil {
			s.csatLinkError(w, r, lerr)
			return
		}
		p := s.surveyForm(token, sv, rating, comment)
		p.Message, p.IsError = "Kies een cijfer van 1 tot 5. Een toelichting mag maximaal 1000 tekens lang zijn.", true
		writeCSATPage(w, http.StatusUnprocessableEntity, p)
	case errors.Is(err, csat.ErrLocked):
		csatMessagePage(w, http.StatusConflict, "Wijzigen niet meer mogelijk", "U kunt uw beoordeling niet meer wijzigen. Het is maximaal 7 dagen na uw eerste beoordeling mogelijk.")
	default:
		s.csatLinkError(w, r, err)
	}
}
