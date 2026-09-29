package api

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"html/template"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"echoo/internal/auth"
	"echoo/internal/db/dbq"
	"echoo/internal/kb"
	"echoo/internal/storage"
)

// The public help center is server-rendered and has no script at all, so its CSP allows
// nothing but its own stylesheet and images. Forms post to the same origin only.
const kbCSP = "default-src 'none'; style-src 'self'; font-src 'self'; img-src 'self'; form-action 'self'; base-uri 'none'; frame-ancestors 'none'"

// kbCacheControl lets browsers and shared caches reuse a page for a minute; an unpublished
// article is gone from the public site within that time.
const kbCacheControl = "public, max-age=60, must-revalidate"

const (
	kbSearchLimit   = 20
	kbPopularLimit  = 6
	kbSnippetRunes  = 160
	kbMaxFormBytes  = 4 << 10
	kbSitemapLayout = "2006-01-02T15:04:05Z"
)

type kbState struct {
	site   *kb.Site
	signer *kb.FeedbackSigner
	// Views count once per visitor and article per half hour; feedback is limited per visitor
	// overall and per article; search does real work per request.
	views           *auth.Limiter
	feedbackIP      *auth.Limiter
	feedbackArticle *auth.Limiter
	searches        *auth.Limiter
}

// newKB builds the public site. The templates are embedded, so a parse error is a programming
// error that the tests catch; it stops the process at startup instead of failing per request.
func (s *Server) newKB() *kbState {
	site, err := kb.NewSite()
	if err != nil {
		panic(fmt.Sprintf("knowledge base templates: %v", err))
	}
	st := &kbState{
		site:            site,
		views:           auth.NewLimiter(1, 30*time.Minute),
		feedbackIP:      auth.NewLimiter(30, time.Hour),
		feedbackArticle: auth.NewLimiter(2, time.Hour),
		searches:        auth.NewLimiter(60, time.Minute),
	}
	if s.keys != nil {
		signer := kb.NewFeedbackSigner(s.keys.DeriveAll("kb-feedback")...)
		st.signer = &signer
	}
	return st
}

func kbHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Security-Policy", kbCSP)
		next.ServeHTTP(w, r)
	})
}

func (s *Server) kbPublicRoutes(r chi.Router) {
	pub := r.With(kbHeaders)
	pub.Get("/hulp", s.kbHome)
	pub.Get("/hulp/", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/hulp", http.StatusMovedPermanently)
	})
	pub.Get("/hulp/c/{slug}", s.kbCategory)
	pub.Get("/hulp/a/{slug}", s.kbArticle)
	pub.Post("/hulp/a/{slug}/feedback", s.kbFeedback)
	pub.Get("/hulp/zoeken", s.kbSearch)
	pub.Get("/hulp/sitemap.xml", s.kbSitemap)
	pub.Get("/hulp/robots.txt", s.kbRobots)
	pub.Get("/hulp/static/{name}", s.kbStatic)
	// Images are public once a published article uses them; before that only signed-in agents
	// (the editor) can see them, which is why this route reads the session cookie.
	r.With(s.loadSession).Get("/hulp/i/{id}", s.kbImage)
	pub.Get("/hulp/*", s.kbNotFound)
	// Crawlers only read robots.txt at the root of a host.
	r.Get("/robots.txt", s.kbRobots)
}

func kbFail(w http.ResponseWriter, r *http.Request, err error) {
	slog.ErrorContext(r.Context(), "help center request failed", "err", err, "request_id", requestIDFrom(r.Context()))
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusInternalServerError)
	_, _ = io.WriteString(w, "Er ging iets mis. Probeer het later opnieuw.")
}

// kbBase loads what every page needs: the portal texts and the origin for absolute URLs.
func (s *Server) kbPageBase(r *http.Request) (kb.Page, dbq.KbGetPortalRow, string, error) {
	p, err := s.q.KbGetPortal(r.Context())
	if err != nil {
		return kb.Page{}, p, "", fmt.Errorf("load portal: %w", err)
	}
	_, css := s.kb.site.CSS()
	page := kb.Page{CSS: css, OGType: "website", Portal: kb.Portal{Name: p.Name, Title: p.Title, Intro: p.Intro, LogoText: p.LogoText}}
	return page, p, s.kbBase(p.CustomDomain), nil
}

// kbWrite sends a rendered page. Successful pages carry a content ETag and, when the caller
// knows one, Last-Modified; http.ServeContent answers conditional requests with 304.
func kbWrite(w http.ResponseWriter, r *http.Request, status int, body []byte, modified time.Time, cache string) {
	h := w.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Cache-Control", cache)
	if status != http.StatusOK {
		w.WriteHeader(status)
		_, _ = w.Write(body)
		return
	}
	sum := sha256.Sum256(body)
	h.Set("ETag", `"`+hex.EncodeToString(sum[:16])+`"`)
	http.ServeContent(w, r, "", modified, bytes.NewReader(body))
}

func (s *Server) kbRenderMessage(w http.ResponseWriter, r *http.Request, status int, heading, text string) {
	page, p, _, err := s.kbPageBase(r)
	if err != nil {
		kbFail(w, r, err)
		return
	}
	page.Title = heading + " - " + p.Name
	page.NoIndex = true
	body, err := s.kb.site.Render("message", kb.MessageData{Page: page, Heading: heading, Text: text})
	if err != nil {
		kbFail(w, r, err)
		return
	}
	kbWrite(w, r, status, body, time.Time{}, "no-store")
}

func (s *Server) kbNotFound(w http.ResponseWriter, r *http.Request) {
	s.kbRenderMessage(w, r, http.StatusNotFound, "Pagina niet gevonden", "Deze pagina bestaat niet of is niet meer beschikbaar.")
}

func (s *Server) kbHome(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	page, p, base, err := s.kbPageBase(r)
	if err != nil {
		kbFail(w, r, err)
		return
	}
	cats, err := s.q.KbPublicCategories(ctx)
	if err != nil {
		kbFail(w, r, fmt.Errorf("list categories: %w", err))
		return
	}
	popular, err := s.q.KbPopularArticles(ctx, kbPopularLimit)
	if err != nil {
		kbFail(w, r, fmt.Errorf("list popular articles: %w", err))
		return
	}
	page.Title = p.Name
	page.Description = p.Intro
	page.Canonical = base + "/hulp"
	data := kb.HomeData{Page: page}
	for _, c := range cats {
		if c.ArticleCount > 0 {
			data.Categories = append(data.Categories, kb.CategoryCard{Slug: c.Slug, Name: c.Name, Description: c.Description, Count: int(c.ArticleCount)})
		}
	}
	for _, a := range popular {
		data.Popular = append(data.Popular, kb.ArticleLink{Slug: a.Slug, Title: a.Title, Excerpt: kb.Excerpt(a.Excerpt, kbSnippetRunes)})
	}
	body, err := s.kb.site.Render("home", data)
	if err != nil {
		kbFail(w, r, err)
		return
	}
	kbWrite(w, r, http.StatusOK, body, time.Time{}, kbCacheControl)
}

func (s *Server) kbCategory(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	c, err := s.q.KbPublicCategoryBySlug(ctx, chi.URLParam(r, "slug"))
	if errors.Is(err, pgx.ErrNoRows) {
		s.kbNotFound(w, r)
		return
	}
	if err != nil {
		kbFail(w, r, fmt.Errorf("load category: %w", err))
		return
	}
	page, p, base, err := s.kbPageBase(r)
	if err != nil {
		kbFail(w, r, err)
		return
	}
	children, err := s.q.KbPublicChildCategories(ctx, c.ID)
	if err != nil {
		kbFail(w, r, fmt.Errorf("list subcategories: %w", err))
		return
	}
	articles, err := s.q.KbPublicCategoryArticles(ctx, c.ID)
	if err != nil {
		kbFail(w, r, fmt.Errorf("list category articles: %w", err))
		return
	}
	page.Title = c.Name + " - " + p.Name
	page.Description = c.Description
	page.Canonical = base + "/hulp/c/" + c.Slug
	data := kb.CategoryData{Page: page, Category: kb.CategoryCard{Slug: c.Slug, Name: c.Name, Description: c.Description}}
	if c.ParentSlug.Valid {
		data.Parent = &kb.CategoryCard{Slug: c.ParentSlug.String, Name: c.ParentName.String}
	}
	for _, ch := range children {
		if ch.ArticleCount > 0 {
			data.Children = append(data.Children, kb.CategoryCard{Slug: ch.Slug, Name: ch.Name, Description: ch.Description, Count: int(ch.ArticleCount)})
		}
	}
	for _, a := range articles {
		data.Articles = append(data.Articles, kb.ArticleLink{Slug: a.Slug, Title: a.Title, Excerpt: kb.Excerpt(a.Excerpt, kbSnippetRunes)})
	}
	if len(data.Articles) == 0 && len(data.Children) == 0 {
		s.kbNotFound(w, r)
		return
	}
	body, err := s.kb.site.Render("category", data)
	if err != nil {
		kbFail(w, r, err)
		return
	}
	kbWrite(w, r, http.StatusOK, body, time.Time{}, kbCacheControl)
}

func (s *Server) kbArticle(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	slug := chi.URLParam(r, "slug")
	a, err := s.q.KbPublicArticleBySlug(ctx, slug)
	if errors.Is(err, pgx.ErrNoRows) {
		current, rerr := s.q.KbFindRedirect(ctx, slug)
		if errors.Is(rerr, pgx.ErrNoRows) {
			s.kbNotFound(w, r)
			return
		}
		if rerr != nil {
			kbFail(w, r, fmt.Errorf("find redirect: %w", rerr))
			return
		}
		w.Header().Set("Cache-Control", "public, max-age=300")
		//nolint:gosec // G710: current is a slug from the database, and the target is always under /hulp/a/.
		http.Redirect(w, r, "/hulp/a/"+current, http.StatusMovedPermanently)
		return
	}
	if err != nil {
		kbFail(w, r, fmt.Errorf("load article: %w", err))
		return
	}
	page, p, base, err := s.kbPageBase(r)
	if err != nil {
		kbFail(w, r, err)
		return
	}
	related, err := s.q.KbPublicRelatedArticles(ctx, dbq.KbPublicRelatedArticlesParams{CategoryID: a.CategoryID, ID: a.ID})
	if err != nil {
		kbFail(w, r, fmt.Errorf("list related articles: %w", err))
		return
	}
	excerpt := kb.Excerpt(a.Excerpt, kbSnippetRunes)
	page.Title = a.Title + " - " + p.Name
	page.Description = excerpt
	page.Canonical = base + "/hulp/a/" + a.Slug
	page.OGType = "article"
	thanks := r.URL.Query().Get("bedankt") == "1"
	data := kb.ArticleData{
		Page: page, Article: kb.ArticleLink{Slug: a.Slug, Title: a.Title, Excerpt: excerpt},
		//nolint:gosec // G203: the body is sanitized by kb.Sanitize on write and again here.
		Body:         template.HTML(kb.Sanitize(a.BodyHtml)),
		Updated:      dutchDate(a.UpdatedAt.Time.UTC()),
		CategoryName: a.CategoryName, CategorySlug: a.CategorySlug, ParentName: a.ParentName.String, ParentSlug: a.ParentSlug.String,
		Thanks: thanks,
	}
	if s.kb.signer != nil {
		data.FeedbackToken = s.kb.signer.Token(uuidStr(a.ID), time.Now())
	}
	for _, rel := range related {
		data.Related = append(data.Related, kb.ArticleLink{Slug: rel.Slug, Title: rel.Title})
	}
	body, err := s.kb.site.Render("article", data)
	if err != nil {
		kbFail(w, r, err)
		return
	}
	if !thanks {
		s.kbCountView(r, slug)
	}
	modified := latest(a.UpdatedAt.Time, a.CategoryUpdatedAt.Time, p.UpdatedAt.Time)
	if thanks {
		kbWrite(w, r, http.StatusOK, body, time.Time{}, "no-store")
		return
	}
	kbWrite(w, r, http.StatusOK, body, modified, kbCacheControl)
}

func latest(times ...time.Time) time.Time {
	var out time.Time
	for _, t := range times {
		if t.After(out) {
			out = t
		}
	}
	return out
}

// kbCountView counts a view without cookies: one per visitor address and article per half hour.
// A failed count must not break the page, so it is logged and dropped.
func (s *Server) kbCountView(r *http.Request, slug string) {
	if !s.kb.views.Allow(limiterKey(clientFrom(r).IP) + "|" + slug) {
		return
	}
	if err := s.q.KbCountView(r.Context(), slug); err != nil {
		slog.WarnContext(r.Context(), "count article view", "err", err, "request_id", requestIDFrom(r.Context()))
	}
}

func (s *Server) kbFeedback(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	ip := limiterKey(clientFrom(r).IP)
	if !s.kb.feedbackIP.Allow(ip) {
		s.kbRenderMessage(w, r, http.StatusTooManyRequests, "Te veel reacties", "Je hebt kort geleden al veel reacties gegeven. Probeer het later opnieuw.")
		return
	}
	slug := chi.URLParam(r, "slug")
	a, err := s.q.KbPublicArticleBySlug(ctx, slug)
	if errors.Is(err, pgx.ErrNoRows) {
		s.kbNotFound(w, r)
		return
	}
	if err != nil {
		kbFail(w, r, fmt.Errorf("load article: %w", err))
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, kbMaxFormBytes)
	if err := r.ParseForm(); err != nil {
		s.kbRenderMessage(w, r, http.StatusBadRequest, "Reactie niet verwerkt", "Je reactie kon niet worden gelezen. Ga terug naar het artikel en probeer het opnieuw.")
		return
	}
	helpful := r.PostForm.Get("helpful")
	id := uuidStr(a.ID)
	if s.kb.signer == nil || !s.kb.signer.Valid(id, r.PostForm.Get("token"), time.Now()) || (helpful != "yes" && helpful != "no") {
		s.kbRenderMessage(w, r, http.StatusBadRequest, "Reactie niet verwerkt", "Deze pagina is verlopen. Open het artikel opnieuw en probeer het nog eens.")
		return
	}
	if !s.kb.feedbackArticle.Allow(ip + "|" + id) {
		s.kbRenderMessage(w, r, http.StatusTooManyRequests, "Al bedankt", "Je hebt al een reactie op dit artikel gegeven. Bedankt.")
		return
	}
	if _, err := s.q.KbRecordFeedback(ctx, dbq.KbRecordFeedbackParams{ID: a.ID, Helpful: helpful == "yes"}); err != nil {
		kbFail(w, r, fmt.Errorf("record feedback: %w", err))
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	http.Redirect(w, r, "/hulp/a/"+a.Slug+"?bedankt=1", http.StatusSeeOther)
}

func (s *Server) kbSearch(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if !s.kb.searches.Allow(limiterKey(clientFrom(r).IP)) {
		s.kbRenderMessage(w, r, http.StatusTooManyRequests, "Even geduld", "Er zijn veel zoekopdrachten gedaan. Probeer het over een minuut opnieuw.")
		return
	}
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if runes := []rune(q); len(runes) > kbMaxQueryRunes {
		q = string(runes[:kbMaxQueryRunes])
	}
	page, p, _, err := s.kbPageBase(r)
	if err != nil {
		kbFail(w, r, err)
		return
	}
	page.Title = "Zoeken - " + p.Name
	page.NoIndex = true
	data := kb.SearchData{Page: page, Query: q}
	if q != "" {
		rows, err := s.q.KbSearchPublished(ctx, dbq.KbSearchPublishedParams{Text: q, HeadlineOptions: kb.HeadlineOptions, PageSize: kbSearchLimit})
		if err != nil {
			kbFail(w, r, fmt.Errorf("search articles: %w", err))
			return
		}
		for _, row := range rows {
			data.Results = append(data.Results, kb.Result{Slug: row.Slug, Title: row.Title, Snippet: kb.ParseSnippet(string(row.Snippet))})
		}
	}
	body, err := s.kb.site.Render("search", data)
	if err != nil {
		kbFail(w, r, err)
		return
	}
	kbWrite(w, r, http.StatusOK, body, time.Time{}, "no-store")
}

type sitemapURL struct {
	Loc     string `xml:"loc"`
	LastMod string `xml:"lastmod,omitempty"`
}

type sitemapSet struct {
	XMLName xml.Name     `xml:"http://www.sitemaps.org/schemas/sitemap/0.9 urlset"`
	URLs    []sitemapURL `xml:"url"`
}

func (s *Server) kbSitemap(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	base, err := s.kbPublicBase(ctx, s.q)
	if err != nil {
		kbFail(w, r, err)
		return
	}
	cats, err := s.q.KbSitemapCategories(ctx)
	if err != nil {
		kbFail(w, r, fmt.Errorf("list sitemap categories: %w", err))
		return
	}
	articles, err := s.q.KbSitemapArticles(ctx)
	if err != nil {
		kbFail(w, r, fmt.Errorf("list sitemap articles: %w", err))
		return
	}
	set := sitemapSet{URLs: []sitemapURL{{Loc: base + "/hulp"}}}
	for _, c := range cats {
		set.URLs = append(set.URLs, sitemapURL{Loc: base + "/hulp/c/" + c.Slug, LastMod: sitemapTime(c.UpdatedAt)})
	}
	for _, a := range articles {
		set.URLs = append(set.URLs, sitemapURL{Loc: base + "/hulp/a/" + a.Slug, LastMod: sitemapTime(a.UpdatedAt)})
	}
	var buf bytes.Buffer
	buf.WriteString(xml.Header)
	if err := xml.NewEncoder(&buf).Encode(set); err != nil {
		kbFail(w, r, fmt.Errorf("encode sitemap: %w", err))
		return
	}
	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	w.Header().Set("Cache-Control", kbCacheControl)
	_, _ = w.Write(buf.Bytes())
}

func sitemapTime(t pgtype.Timestamptz) string { return t.Time.UTC().Format(kbSitemapLayout) }

func (s *Server) kbRobots(w http.ResponseWriter, r *http.Request) {
	base, err := s.kbPublicBase(r.Context(), s.q)
	if err != nil {
		kbFail(w, r, err)
		return
	}
	// The app behind the login is not for crawlers; only the help center is open. The longest
	// matching rule wins, so the search results stay excluded inside /hulp.
	lines := []string{"User-agent: *", "Disallow: /hulp/zoeken", "Allow: /hulp", "Disallow: /", "Sitemap: " + base + "/hulp/sitemap.xml", ""}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", kbCacheControl)
	_, _ = io.WriteString(w, strings.Join(lines, "\n"))
}

func (s *Server) kbStatic(w http.ResponseWriter, r *http.Request) {
	css, path := s.kb.site.CSS()
	if "/hulp/static/"+chi.URLParam(r, "name") != path {
		s.kbNotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/css; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	_, _ = w.Write(css)
}

func (s *Server) kbImage(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id, ok := parseUUID(chi.URLParam(r, "id"))
	if !ok {
		s.kbNotFound(w, r)
		return
	}
	img, err := s.q.KbGetImage(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		s.kbNotFound(w, r)
		return
	}
	if err != nil {
		kbFail(w, r, fmt.Errorf("load image: %w", err))
		return
	}
	published, err := s.q.KbImageIsPublished(ctx, id)
	if err != nil {
		kbFail(w, r, fmt.Errorf("check image: %w", err))
		return
	}
	cache := "public, max-age=3600"
	if !published {
		if sess := sessionFrom(ctx); sess == nil || sess.MfaPending {
			s.kbNotFound(w, r)
			return
		}
		cache = "private, no-store"
	}
	if s.store == nil {
		kbFail(w, r, errors.New("blob storage is not configured"))
		return
	}
	rc, err := s.store.Open(ctx, img.BlobKey)
	if errors.Is(err, storage.ErrNotFound) {
		s.kbNotFound(w, r)
		return
	}
	if err != nil {
		kbFail(w, r, fmt.Errorf("open image: %w", err))
		return
	}
	defer func() { _ = rc.Close() }()
	h := w.Header()
	h.Set("Content-Type", img.ContentType)
	h.Set("Cache-Control", cache)
	if _, err := io.Copy(w, rc); err != nil {
		slog.WarnContext(ctx, "stream image", "err", err, "request_id", requestIDFrom(ctx))
	}
}
