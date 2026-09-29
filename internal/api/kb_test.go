package api

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"echoo/internal/storage"
)

type kbEnv struct {
	h                      *harness
	admin, agent, readonly *client
	anon                   *http.Client
}

func newKBEnv(t *testing.T) *kbEnv {
	t.Helper()
	h := newHarness(t)
	store, err := storage.NewFS(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	h.srv.store = store
	e := &kbEnv{h: h}
	e.admin, _ = h.loggedIn("admin")
	e.agent, _ = h.loggedIn("agent")
	e.readonly, _ = h.loggedIn("readonly")
	tr := h.ts.Client().Transport.(*http.Transport).Clone()
	tr.TLSClientConfig = &tls.Config{InsecureSkipVerify: true} //nolint:gosec // test server certificate
	e.anon = &http.Client{Transport: tr, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	return e
}

type pubResponse struct {
	status int
	header http.Header
	body   string
}

// pub requests a public page without a session, as a visitor with the given address.
func (e *kbEnv) pub(method, path, ip string, form url.Values, hdr map[string]string) pubResponse {
	e.h.t.Helper()
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	req, err := http.NewRequest(method, e.h.ts.URL+path, body)
	if err != nil {
		e.h.t.Fatal(err)
	}
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	if ip == "" {
		ip = "203.0.113.9"
	}
	req.Header.Set("X-Forwarded-For", ip)
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := e.anon.Do(req)
	if err != nil {
		e.h.t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		e.h.t.Fatal(err)
	}
	return pubResponse{status: resp.StatusCode, header: resp.Header, body: string(raw)}
}

func (e *kbEnv) get(path string) pubResponse { return e.pub("GET", path, "", nil, nil) }

func (e *kbEnv) category(name string) string {
	e.h.t.Helper()
	r := e.admin.do("POST", "/api/v1/kb/categories", map[string]any{"name": name})
	expect(e.h.t, r, 201, "")
	return r.body["id"].(string)
}

func (e *kbEnv) article(c *client, fields map[string]any) response {
	e.h.t.Helper()
	if _, ok := fields["body_html"]; !ok {
		fields["body_html"] = "<p>Inhoud van het artikel.</p>"
	}
	return c.do("POST", "/api/v1/kb/articles", fields)
}

func (e *kbEnv) publishedArticle(title, body, category string) (id, slug string) {
	e.h.t.Helper()
	r := e.article(e.agent, map[string]any{"title": title, "body_html": body, "category_id": category})
	expect(e.h.t, r, 201, "")
	id, slug = r.body["id"].(string), r.body["slug"].(string)
	expect(e.h.t, e.agent.do("POST", "/api/v1/kb/articles/"+id+"/status", map[string]string{"status": "published"}), 200, "")
	return id, slug
}

func TestKBPermissions(t *testing.T) {
	e := newKBEnv(t)
	cat := e.category("Facturen")

	// Agents write articles but cannot manage categories or the portal.
	expect(t, e.agent.do("POST", "/api/v1/kb/categories", map[string]any{"name": "X"}), 403, "forbidden")
	expect(t, e.agent.do("PATCH", "/api/v1/kb/categories/"+cat, map[string]any{"name": "X"}), 403, "forbidden")
	expect(t, e.agent.do("DELETE", "/api/v1/kb/categories/"+cat, nil), 403, "forbidden")
	expect(t, e.agent.do("PUT", "/api/v1/kb/portal", map[string]any{"name": "X", "title": "X"}), 403, "forbidden")

	r := e.article(e.agent, map[string]any{"title": "Concept", "category_id": cat})
	expect(t, r, 201, "")
	draft := r.body["id"].(string)
	pubID, _ := e.publishedArticle("Gepubliceerd", "<p>tekst</p>", cat)

	// Readonly users never write and never see drafts.
	expect(t, e.article(e.readonly, map[string]any{"title": "Nee"}), 403, "forbidden")
	expect(t, e.readonly.do("PATCH", "/api/v1/kb/articles/"+draft, map[string]any{"title": "Nee", "slug": "nee", "body_html": "<p>x</p>", "version": 1}), 403, "forbidden")
	expect(t, e.readonly.do("POST", "/api/v1/kb/articles/"+pubID+"/status", map[string]string{"status": "draft"}), 403, "forbidden")
	expect(t, e.readonly.do("DELETE", "/api/v1/kb/articles/"+pubID, nil), 403, "forbidden")
	expect(t, e.readonly.do("GET", "/api/v1/kb/articles/"+pubID+"/revisions", nil), 403, "forbidden")
	expect(t, e.readonly.do("GET", "/api/v1/kb/articles/"+draft, nil), 404, "not_found")
	expect(t, e.readonly.do("GET", "/api/v1/kb/articles/"+pubID, nil), 200, "")
	expect(t, e.readonly.do("POST", "/api/v1/kb/images", nil), 403, "forbidden")

	list := e.readonly.do("GET", "/api/v1/kb/articles", nil)
	expect(t, list, 200, "")
	items := list.body["articles"].([]any)
	if len(items) != 1 || items[0].(map[string]any)["id"] != pubID {
		t.Fatalf("readonly list = %s", list.raw)
	}
	if n := len(e.agent.do("GET", "/api/v1/kb/articles", nil).body["articles"].([]any)); n != 2 {
		t.Fatalf("agent sees %d articles, want 2", n)
	}
	// Everyone signed in can read the structure.
	expect(t, e.readonly.do("GET", "/api/v1/kb/categories", nil), 200, "")
	expect(t, e.readonly.do("GET", "/api/v1/kb/portal", nil), 200, "")
}

func TestKBDraftsAreNotPublic(t *testing.T) {
	e := newKBEnv(t)
	cat := e.category("Start")
	r := e.article(e.agent, map[string]any{"title": "Geheim concept", "category_id": cat, "body_html": "<p>nog niet klaar</p>"})
	expect(t, r, 201, "")
	id, slug := r.body["id"].(string), r.body["slug"].(string)
	set := func(status string) response {
		return e.agent.do("POST", "/api/v1/kb/articles/"+id+"/status", map[string]string{"status": status})
	}
	for _, path := range []string{"/hulp/a/" + slug, "/hulp/zoeken?q=Geheim", "/hulp", "/hulp/c/start", "/hulp/sitemap.xml"} {
		if got := e.get(path); strings.Contains(got.body, "Geheim concept") || (strings.HasPrefix(path, "/hulp/a/") && got.status != 404) {
			t.Errorf("draft visible at %s (%d)", path, got.status)
		}
	}
	expect(t, set("published"), 200, "")
	if got := e.get("/hulp/a/" + slug); got.status != 200 || !strings.Contains(got.body, "Geheim concept") {
		t.Fatalf("published article: %d", got.status)
	}
	expect(t, set("draft"), 200, "")
	if got := e.get("/hulp/a/" + slug); got.status != 404 {
		t.Fatalf("unpublished article: %d", got.status)
	}
	expect(t, set("published"), 200, "")
	expect(t, set("archived"), 200, "")
	if got := e.get("/hulp/a/" + slug); got.status != 404 {
		t.Fatalf("archived article: %d", got.status)
	}
	// A category with only unpublished articles is not on the site either.
	if got := e.get("/hulp/c/start"); got.status != 404 {
		t.Fatalf("empty category: %d", got.status)
	}

	// Publishing needs a category and a body.
	r = e.article(e.agent, map[string]any{"title": "Zonder categorie"})
	expect(t, r, 201, "")
	expect(t, e.agent.do("POST", "/api/v1/kb/articles/"+r.body["id"].(string)+"/status", map[string]string{"status": "published"}), 422, "validation_failed")
	r = e.article(e.agent, map[string]any{"title": "Leeg", "category_id": cat, "body_html": "<p></p>"})
	expect(t, r, 201, "")
	expect(t, e.agent.do("POST", "/api/v1/kb/articles/"+r.body["id"].(string)+"/status", map[string]string{"status": "published"}), 422, "validation_failed")
}

func TestKBArticleHTMLIsSanitized(t *testing.T) {
	e := newKBEnv(t)
	cat := e.category("Veilig")
	body := `<p onclick="x()">Tekst</p><script>alert(1)</script><img src=x onerror=alert(2)><a href="javascript:alert(3)">klik</a>` +
		`<iframe src="https://evil.example"></iframe><img src="https://evil.example/t.gif"><a href="https://ok.example/">ok</a>`
	r := e.article(e.agent, map[string]any{"title": `Titel "><script>alert(4)</script> & <b>vet</b>`, "body_html": body, "category_id": cat})
	expect(t, r, 201, "")
	stored := r.body["body_html"].(string)
	for _, bad := range []string{"<script", "onerror", "onclick", "javascript:", "<iframe", "evil.example/t.gif"} {
		if strings.Contains(stored, bad) {
			t.Errorf("stored body contains %q: %s", bad, stored)
		}
	}
	id := r.body["id"].(string)
	expect(t, e.agent.do("POST", "/api/v1/kb/articles/"+id+"/status", map[string]string{"status": "published"}), 200, "")
	page := e.get("/hulp/a/" + r.body["slug"].(string))
	if page.status != 200 {
		t.Fatalf("status %d", page.status)
	}
	if strings.Contains(page.body, "<script") || strings.Contains(page.body, "<b>vet") || strings.Contains(page.body, "onerror") {
		t.Errorf("public page has unsafe markup: %s", page.body)
	}
	if !strings.Contains(page.body, "&lt;b&gt;vet&lt;/b&gt;") {
		t.Errorf("title not escaped: %s", page.body)
	}
	csp := page.header.Get("Content-Security-Policy")
	if !strings.Contains(csp, "default-src 'none'") || strings.Contains(csp, "script-src") || strings.Contains(csp, "unsafe") {
		t.Errorf("CSP = %q", csp)
	}
	if page.header.Get("Set-Cookie") != "" {
		t.Error("the public site must not set cookies")
	}

	// A row changed behind the API's back is sanitized again when it is rendered.
	if _, err := e.h.pool.Exec(context.Background(), `UPDATE kb_articles SET body_html = '<p>ok</p><script>alert(9)</script>' WHERE id = $1`, id); err != nil {
		t.Fatal(err)
	}
	if page = e.get("/hulp/a/" + r.body["slug"].(string)); strings.Contains(page.body, "<script>alert(9)") {
		t.Errorf("render did not sanitize: %s", page.body)
	}

	// An image that was never uploaded is refused.
	r = e.article(e.agent, map[string]any{"title": "Plaatje", "body_html": `<img src="/hulp/i/0199a000-0000-7000-8000-000000000000">`})
	expect(t, r, 422, "validation_failed")
	if r.body["error"].(map[string]any)["fields"].(map[string]any)["body_html"] != "unknown_image" {
		t.Errorf("fields = %s", r.raw)
	}
}

func TestKBSlugsAndRedirects(t *testing.T) {
	e := newKBEnv(t)
	cat := e.category("Slugs")
	r := e.article(e.agent, map[string]any{"title": "Wachtwoord vergeten?", "category_id": cat})
	expect(t, r, 201, "")
	if r.body["slug"] != "wachtwoord-vergeten" {
		t.Fatalf("generated slug = %v", r.body["slug"])
	}
	first := r.body["id"].(string)
	r2 := e.article(e.agent, map[string]any{"title": "Wachtwoord vergeten?", "category_id": cat})
	expect(t, r2, 201, "")
	if r2.body["slug"] != "wachtwoord-vergeten-2" {
		t.Fatalf("second slug = %v", r2.body["slug"])
	}
	expect(t, e.article(e.agent, map[string]any{"title": "Anders", "slug": "wachtwoord-vergeten"}), 422, "validation_failed")
	expect(t, e.article(e.agent, map[string]any{"title": "Anders", "slug": "Geen Slug!"}), 422, "validation_failed")

	// Renaming an unpublished article leaves no redirect behind.
	patch := func(id string, version int, slug string) response {
		return e.agent.do("PATCH", "/api/v1/kb/articles/"+id, map[string]any{
			"title": "Wachtwoord vergeten?", "slug": slug, "category_id": cat, "body_html": "<p>Inhoud van het artikel.</p>", "version": version,
		})
	}
	r = patch(first, 1, "draft-naam")
	expect(t, r, 200, "")
	if got := e.get("/hulp/a/wachtwoord-vergeten"); got.status != 404 {
		t.Fatalf("old slug of a draft: %d", got.status)
	}
	expect(t, e.agent.do("POST", "/api/v1/kb/articles/"+first+"/status", map[string]string{"status": "published"}), 200, "")
	r = e.agent.do("GET", "/api/v1/kb/articles/"+first, nil)
	version := int(r.body["version"].(float64))

	r = patch(first, version, "inloggen-lukt-niet")
	expect(t, r, 200, "")
	if got := e.get("/hulp/a/inloggen-lukt-niet"); got.status != 200 {
		t.Fatalf("new slug: %d", got.status)
	}
	got := e.get("/hulp/a/draft-naam")
	if got.status != 301 || got.header.Get("Location") != "/hulp/a/inloggen-lukt-niet" {
		t.Fatalf("old slug: %d %q", got.status, got.header.Get("Location"))
	}
	// The redirect follows later renames, and the old address stays reserved.
	r = patch(first, version+1, "nieuwste-naam")
	expect(t, r, 200, "")
	for _, old := range []string{"draft-naam", "inloggen-lukt-niet"} {
		if got := e.get("/hulp/a/" + old); got.status != 301 || got.header.Get("Location") != "/hulp/a/nieuwste-naam" {
			t.Errorf("%s: %d %q", old, got.status, got.header.Get("Location"))
		}
		if r := patch(r2.body["id"].(string), 1, old); r.status != 422 {
			t.Errorf("another article took the old slug %s: %d", old, r.status)
		}
	}
	// The article itself may go back to an old slug.
	expect(t, patch(first, version+2, "draft-naam"), 200, "")
	if got := e.get("/hulp/a/draft-naam"); got.status != 200 {
		t.Fatalf("reclaimed slug: %d", got.status)
	}
	// Unpublishing turns the redirects into 404s too.
	expect(t, e.agent.do("POST", "/api/v1/kb/articles/"+first+"/status", map[string]string{"status": "draft"}), 200, "")
	if got := e.get("/hulp/a/inloggen-lukt-niet"); got.status != 404 {
		t.Fatalf("redirect of unpublished article: %d", got.status)
	}
}

func TestKBVersionConflictAndRevisions(t *testing.T) {
	e := newKBEnv(t)
	cat := e.category("Versies")
	r := e.article(e.agent, map[string]any{"title": "Begin", "category_id": cat, "body_html": "<p>versie 0</p>"})
	expect(t, r, 201, "")
	id := r.body["id"].(string)
	save := func(version int, n int) response {
		return e.agent.do("PATCH", "/api/v1/kb/articles/"+id, map[string]any{
			"title": fmt.Sprintf("Titel %d", n), "slug": "begin", "category_id": cat, "body_html": fmt.Sprintf("<p>versie %d</p>", n), "version": version,
		})
	}
	version := 1
	for n := 1; n <= 25; n++ {
		r = save(version, n)
		expect(t, r, 200, "")
		version = int(r.body["version"].(float64))
	}
	stale := save(1, 99)
	expect(t, stale, 409, "version_conflict")
	if cur := stale.body["error"].(map[string]any)["current_version"].(float64); int(cur) != version {
		t.Errorf("current_version = %v, want %d", cur, version)
	}

	revs := e.agent.do("GET", "/api/v1/kb/articles/"+id+"/revisions", nil)
	expect(t, revs, 200, "")
	list := revs.body["revisions"].([]any)
	if len(list) != 20 {
		t.Fatalf("kept %d revisions, want 20", len(list))
	}
	newest := list[0].(map[string]any)
	if newest["title"] != "Titel 24" {
		t.Fatalf("newest revision = %v", newest["title"])
	}
	restored := e.agent.do("POST", "/api/v1/kb/articles/"+id+"/revisions/"+newest["id"].(string)+"/restore", nil)
	expect(t, restored, 200, "")
	if restored.body["title"] != "Titel 24" || !strings.Contains(restored.body["body_html"].(string), "versie 24") {
		t.Fatalf("restored = %s", restored.raw)
	}
	// The state before the restore became a revision itself.
	revs = e.agent.do("GET", "/api/v1/kb/articles/"+id+"/revisions", nil)
	if top := revs.body["revisions"].([]any)[0].(map[string]any)["title"]; top != "Titel 25" || len(revs.body["revisions"].([]any)) != 20 {
		t.Fatalf("after restore: %s", revs.raw)
	}
	expect(t, e.agent.do("POST", "/api/v1/kb/articles/"+id+"/revisions/0199a000-0000-7000-8000-000000000000/restore", nil), 404, "not_found")
}

func TestKBCategories(t *testing.T) {
	e := newKBEnv(t)
	parent := e.category("Account")
	child := e.admin.do("POST", "/api/v1/kb/categories", map[string]any{"name": "Wachtwoord", "parent_id": parent})
	expect(t, child, 201, "")
	childID := child.body["id"].(string)
	// One level only.
	expect(t, e.admin.do("POST", "/api/v1/kb/categories", map[string]any{"name": "Te diep", "parent_id": childID}), 422, "validation_failed")
	expect(t, e.admin.do("PATCH", "/api/v1/kb/categories/"+parent, map[string]any{"name": "Account", "parent_id": childID}), 422, "validation_failed")
	other := e.category("Los")
	expect(t, e.admin.do("PATCH", "/api/v1/kb/categories/"+parent, map[string]any{"name": "Account", "parent_id": other}), 422, "validation_failed")
	expect(t, e.admin.do("PATCH", "/api/v1/kb/categories/"+parent, map[string]any{"name": "Account", "parent_id": parent}), 422, "validation_failed")
	expect(t, e.admin.do("POST", "/api/v1/kb/categories", map[string]any{"name": "Account"}), 422, "validation_failed")

	e.publishedArticle("Wachtwoord wijzigen", "<p>ga naar instellingen</p>", childID)
	// The parent shows the child's article in its count and lists the child.
	home := e.get("/hulp")
	if !strings.Contains(home.body, "Account") || !strings.Contains(home.body, "1 artikel") {
		t.Errorf("home: %s", home.body)
	}
	page := e.get("/hulp/c/account")
	if page.status != 200 || !strings.Contains(page.body, "/hulp/c/wachtwoord") {
		t.Errorf("parent page: %d %s", page.status, page.body)
	}
	if sub := e.get("/hulp/c/wachtwoord"); sub.status != 200 || !strings.Contains(sub.body, "Wachtwoord wijzigen") || !strings.Contains(sub.body, "/hulp/c/account") {
		t.Errorf("child page: %d %s", sub.status, sub.body)
	}

	expect(t, e.admin.do("DELETE", "/api/v1/kb/categories/"+parent, nil), 409, "category_not_empty")
	expect(t, e.admin.do("DELETE", "/api/v1/kb/categories/"+childID, nil), 409, "category_not_empty")
	expect(t, e.admin.do("DELETE", "/api/v1/kb/categories/"+other, nil), 204, "")
	expect(t, e.admin.do("DELETE", "/api/v1/kb/categories/"+other, nil), 404, "not_found")
	cats := e.agent.do("GET", "/api/v1/kb/categories", nil).body["categories"].([]any)
	if len(cats) != 2 {
		t.Fatalf("categories = %v", cats)
	}
}

func TestKBSearchRankingAndEscaping(t *testing.T) {
	e := newKBEnv(t)
	cat := e.category("Zoeken")
	e.publishedArticle("Algemene voorwaarden", "<p>Lees hier over de factuur en meer over andere dingen die ertoe doen.</p>", cat)
	e.publishedArticle("Uw factuur betalen", "<p>Betalen kan met iDEAL.</p>", cat)
	e.publishedArticle("Retourneren", "<p>Stuur het pakket terug. Bewaar de <b>factuur</b> &lt;img src=x onerror=alert(1)&gt; als bewijs van aankoop, de factuur is nodig.</p>", cat)
	r := e.article(e.agent, map[string]any{"title": "Interne factuur notitie", "category_id": cat, "body_html": "<p>factuur factuur factuur</p>"})
	expect(t, r, 201, "")

	res := e.get("/hulp/zoeken?q=factuur")
	if res.status != 200 {
		t.Fatalf("status %d", res.status)
	}
	if strings.Contains(res.body, "Interne factuur") {
		t.Error("a draft shows up in search")
	}
	iTitle := strings.Index(res.body, "Uw factuur betalen")
	iBody := strings.Index(res.body, "Retourneren")
	iWeak := strings.Index(res.body, "Algemene voorwaarden")
	if iTitle < 0 || iBody < 0 || iWeak < 0 || iTitle >= iBody || iBody >= iWeak {
		t.Errorf("ranking wrong (title=%d body=%d weak=%d): %s", iTitle, iBody, iWeak, res.body)
	}
	if !strings.Contains(res.body, "<mark>factuur</mark>") {
		t.Errorf("no highlighted match: %s", res.body)
	}
	if strings.Contains(res.body, "<img") || strings.Contains(res.body, "onerror=alert(1)>") || strings.Contains(res.body, "<b>") {
		t.Errorf("snippet is not escaped: %s", res.body)
	}
	if !strings.Contains(res.body, `content="noindex"`) {
		t.Error("search results must not be indexed")
	}

	evil := e.get("/hulp/zoeken?q=" + url.QueryEscape(`"><script>alert(1)</script>`))
	if strings.Contains(evil.body, "<script>alert") || !strings.Contains(evil.body, "&#34;&gt;&lt;script&gt;") {
		t.Errorf("query not escaped: %s", evil.body)
	}
	if empty := e.get("/hulp/zoeken?q=nietbestaandwoord"); empty.status != 200 || !strings.Contains(empty.body, "Geen artikelen gevonden") {
		t.Errorf("empty result page: %d", empty.status)
	}
	if blank := e.get("/hulp/zoeken"); blank.status != 200 {
		t.Errorf("blank search: %d", blank.status)
	}
	// Whatever operators a visitor types, the query never errors.
	for _, q := range []string{`"`, `-`, `a & | ! ( )`, `:*`, strings.Repeat("x", 500)} {
		if got := e.get("/hulp/zoeken?q=" + url.QueryEscape(q)); got.status != 200 {
			t.Errorf("q=%q: %d", q, got.status)
		}
	}
}

var tokenPattern = regexp.MustCompile(`name="token" value="([^"]+)"`)

func TestKBFeedback(t *testing.T) {
	e := newKBEnv(t)
	cat := e.category("Feedback")
	id, slug := e.publishedArticle("Nuttig artikel", "<p>tekst</p>", cat)
	page := e.get("/hulp/a/" + slug)
	m := tokenPattern.FindStringSubmatch(page.body)
	if m == nil {
		t.Fatalf("no feedback token on the page: %s", page.body)
	}
	token := m[1]
	if again := tokenPattern.FindStringSubmatch(e.get("/hulp/a/" + slug).body); again == nil || again[1] != token {
		t.Error("the token must be the same for every render, so caches stay valid")
	}
	post := func(ip, tok, helpful string) pubResponse {
		return e.pub("POST", "/hulp/a/"+slug+"/feedback", ip, url.Values{"token": {tok}, "helpful": {helpful}}, nil)
	}
	counts := func() (int, int) {
		a := e.agent.do("GET", "/api/v1/kb/articles/"+id, nil)
		return int(a.body["helpful_yes"].(float64)), int(a.body["helpful_no"].(float64))
	}

	if got := post("198.51.100.1", "kapot", "yes"); got.status != 400 {
		t.Errorf("bad token: %d", got.status)
	}
	otherID, otherSlug := e.publishedArticle("Ander artikel", "<p>x</p>", cat)
	_ = otherID
	otherToken := tokenPattern.FindStringSubmatch(e.get("/hulp/a/" + otherSlug).body)[1]
	if got := post("198.51.100.1", otherToken, "yes"); got.status != 400 {
		t.Errorf("token of another article: %d", got.status)
	}
	if got := post("198.51.100.1", token, "maybe"); got.status != 400 {
		t.Errorf("bad value: %d", got.status)
	}
	if y, n := counts(); y != 0 || n != 0 {
		t.Fatalf("rejected feedback was counted: %d/%d", y, n)
	}

	got := post("198.51.100.2", token, "yes")
	if got.status != 303 || got.header.Get("Location") != "/hulp/a/"+slug+"?bedankt=1" {
		t.Fatalf("feedback: %d %q", got.status, got.header.Get("Location"))
	}
	if thanks := e.get("/hulp/a/" + slug + "?bedankt=1"); !strings.Contains(thanks.body, "Bedankt voor je reactie") || strings.Contains(thanks.body, `name="helpful"`) || thanks.header.Get("Cache-Control") != "no-store" {
		t.Errorf("thanks page: %s", thanks.body)
	}
	expect(t, response{status: post("198.51.100.3", token, "no").status}, 303, "")
	if y, n := counts(); y != 1 || n != 1 {
		t.Fatalf("counts = %d/%d, want 1/1", y, n)
	}

	// The same visitor may vote twice on an article per hour, not more.
	post("198.51.100.4", token, "yes")
	post("198.51.100.4", token, "yes")
	if got := post("198.51.100.4", token, "yes"); got.status != 429 {
		t.Errorf("third vote from one address: %d", got.status)
	}
	if y, _ := counts(); y != 3 {
		t.Errorf("yes = %d, want 3", y)
	}
	// And is limited overall, across articles.
	limited := 0
	for range 40 {
		if post("198.51.100.5", "kapot", "yes").status == 429 {
			limited++
		}
	}
	if limited == 0 {
		t.Error("no per-address limit on feedback posts")
	}
	if got := e.pub("POST", "/hulp/a/bestaat-niet/feedback", "198.51.100.6", url.Values{"token": {token}, "helpful": {"yes"}}, nil); got.status != 404 {
		t.Errorf("unknown article: %d", got.status)
	}

	// The list for agents shows the totals.
	list := e.agent.do("GET", "/api/v1/kb/articles?status=published&q=Nuttig", nil).body["articles"].([]any)
	if len(list) != 1 || list[0].(map[string]any)["helpful_yes"].(float64) != 3 {
		t.Errorf("list = %v", list)
	}
}

func TestKBViewCounts(t *testing.T) {
	e := newKBEnv(t)
	cat := e.category("Views")
	id, slug := e.publishedArticle("Veel gelezen", "<p>tekst</p>", cat)
	views := func() int { return int(e.agent.do("GET", "/api/v1/kb/articles/"+id, nil).body["view_count"].(float64)) }
	for range 3 {
		e.pub("GET", "/hulp/a/"+slug, "198.51.100.10", nil, nil)
	}
	if v := views(); v != 1 {
		t.Fatalf("views from one address = %d, want 1", v)
	}
	e.pub("GET", "/hulp/a/"+slug, "198.51.100.11", nil, nil)
	if v := views(); v != 2 {
		t.Fatalf("views = %d, want 2", v)
	}
	if e.get("/hulp/a/onbekend-artikel"); views() != 2 {
		t.Error("a 404 counted as a view")
	}
	// The most read article leads the popular list.
	e.publishedArticle("Weinig gelezen", "<p>tekst</p>", cat)
	home := e.get("/hulp")
	if strings.Index(home.body, "Veel gelezen") > strings.Index(home.body, "Weinig gelezen") {
		t.Errorf("popular order: %s", home.body)
	}
}

func TestKBCachingHeaders(t *testing.T) {
	e := newKBEnv(t)
	cat := e.category("Cache")
	_, slug := e.publishedArticle("Gecachet", "<p>tekst</p>", cat)
	first := e.get("/hulp/a/" + slug)
	etag, lm := first.header.Get("ETag"), first.header.Get("Last-Modified")
	if etag == "" || lm == "" || !strings.Contains(first.header.Get("Cache-Control"), "max-age=60") {
		t.Fatalf("headers = %v", first.header)
	}
	cond := e.pub("GET", "/hulp/a/"+slug, "", nil, map[string]string{"If-None-Match": etag})
	if cond.status != 304 || cond.body != "" {
		t.Errorf("If-None-Match: %d", cond.status)
	}
	cond = e.pub("GET", "/hulp/a/"+slug, "", nil, map[string]string{"If-Modified-Since": lm})
	if cond.status != 304 {
		t.Errorf("If-Modified-Since: %d", cond.status)
	}
	if home := e.get("/hulp"); home.header.Get("ETag") == "" {
		t.Error("home has no ETag")
	}
	css := regexp.MustCompile(`/hulp/static/kb\.[0-9a-f]+\.css`).FindString(first.body)
	if css == "" {
		t.Fatalf("no stylesheet link: %s", first.body)
	}
	got := e.get(css)
	if got.status != 200 || !strings.Contains(got.header.Get("Cache-Control"), "immutable") || !strings.HasPrefix(got.header.Get("Content-Type"), "text/css") || !strings.Contains(got.body, "prefers-color-scheme: dark") {
		t.Errorf("stylesheet: %d %v", got.status, got.header)
	}
	if e.get("/hulp/static/kb.0000.css").status != 404 {
		t.Error("an unknown stylesheet name must 404")
	}
	font := e.get("/fonts/urbanist-latin-400-normal.woff2")
	if font.status != 200 || font.header.Get("Content-Type") != "font/woff2" || !strings.Contains(got.body, "/fonts/urbanist-latin-400-normal.woff2") {
		t.Errorf("font: %d %v", font.status, font.header)
	}
	if !strings.Contains(first.header.Get("Content-Security-Policy"), "font-src 'self'") {
		t.Errorf("the help center CSP must allow fonts from its own origin: %q", first.header.Get("Content-Security-Policy"))
	}
	if e.get("/fonts/nergens.woff2").status != 404 {
		t.Error("an unknown font name must 404")
	}
	if e.get("/hulp/nergens").status != 404 || !strings.Contains(e.get("/hulp/nergens").body, "Pagina niet gevonden") {
		t.Error("unknown help center path must give the Dutch 404 page")
	}
	// Changing the article changes the ETag.
	list := e.agent.do("GET", "/api/v1/kb/articles?q=Gecachet", nil).body["articles"].([]any)[0].(map[string]any)
	expect(t, e.agent.do("PATCH", "/api/v1/kb/articles/"+list["id"].(string), map[string]any{
		"title": "Gecachet", "slug": slug, "category_id": list["category_id"], "body_html": "<p>nieuwe tekst</p>", "version": list["version"],
	}), 200, "")
	if second := e.get("/hulp/a/" + slug); second.header.Get("ETag") == etag || !strings.Contains(second.body, "nieuwe tekst") {
		t.Error("ETag did not change with the content")
	}
}

func TestKBSitemapRobotsAndMeta(t *testing.T) {
	e := newKBEnv(t)
	cat := e.category("Sitemap")
	_, slug := e.publishedArticle("Zichtbaar", "<p>Een <b>korte</b> uitleg &amp; meer.</p>", cat)
	r := e.article(e.agent, map[string]any{"title": "Onzichtbaar", "category_id": cat})
	expect(t, r, 201, "")
	hidden := r.body["slug"].(string)

	sm := e.get("/hulp/sitemap.xml")
	if sm.status != 200 || !strings.Contains(sm.header.Get("Content-Type"), "application/xml") {
		t.Fatalf("sitemap: %d %v", sm.status, sm.header)
	}
	if !strings.Contains(sm.body, testOrigin+"/hulp/a/"+slug) || !strings.Contains(sm.body, testOrigin+"/hulp/c/sitemap") || !strings.Contains(sm.body, "<loc>"+testOrigin+"/hulp</loc>") {
		t.Errorf("sitemap lacks published pages: %s", sm.body)
	}
	if strings.Contains(sm.body, hidden) {
		t.Errorf("sitemap lists a draft: %s", sm.body)
	}
	for _, path := range []string{"/hulp/robots.txt", "/robots.txt"} {
		rb := e.get(path)
		if rb.status != 200 || !strings.Contains(rb.body, "Sitemap: "+testOrigin+"/hulp/sitemap.xml") || !strings.Contains(rb.body, "Allow: /hulp") {
			t.Errorf("%s: %d %s", path, rb.status, rb.body)
		}
	}

	page := e.get("/hulp/a/" + slug)
	for _, want := range []string{
		`<link rel="canonical" href="` + testOrigin + `/hulp/a/` + slug + `">`,
		`<meta property="og:title" content="Zichtbaar - Kennisbank">`,
		`<meta property="og:description" content="Een korte uitleg &amp; meer.">`,
		`<meta property="og:type" content="article">`,
		`<html lang="nl">`,
	} {
		if !strings.Contains(page.body, want) {
			t.Errorf("article page lacks %s", want)
		}
	}
	if strings.Contains(page.body, "<script") {
		t.Error("the public site must not contain script")
	}

	// A reverse proxy domain changes the absolute URLs only.
	expect(t, e.admin.do("PUT", "/api/v1/kb/portal", map[string]any{"name": "Hulp <Acme>", "title": "Hulp", "intro": "", "logo_text": "", "custom_domain": "Hulp.Example.NL"}), 200, "")
	page = e.get("/hulp/a/" + slug)
	if !strings.Contains(page.body, `href="https://hulp.example.nl/hulp/a/`+slug+`"`) || !strings.Contains(page.body, "Hulp &lt;Acme&gt;") {
		t.Errorf("custom domain / escaping: %s", page.body)
	}
	if sm = e.get("/hulp/sitemap.xml"); !strings.Contains(sm.body, "https://hulp.example.nl/hulp/a/"+slug) {
		t.Errorf("sitemap ignores custom domain: %s", sm.body)
	}
	for _, bad := range []string{"http://x.nl", "no_dots", "-a.nl", "a.nl/path", "a b.nl", "évil.nl"} {
		expect(t, e.admin.do("PUT", "/api/v1/kb/portal", map[string]any{"name": "N", "title": "T", "intro": "", "logo_text": "", "custom_domain": bad}), 422, "validation_failed")
	}
	expect(t, e.admin.do("PUT", "/api/v1/kb/portal", map[string]any{"name": "", "title": "T", "intro": "", "logo_text": "", "custom_domain": ""}), 422, "validation_failed")
}

func (e *kbEnv) uploadImage(c *client, filename string, data []byte) response {
	e.h.t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	hdr := textproto.MIMEHeader{}
	hdr.Set("Content-Disposition", fmt.Sprintf(`form-data; name="file"; filename=%q`, filename))
	hdr.Set("Content-Type", "image/png")
	part, err := mw.CreatePart(hdr)
	if err != nil {
		e.h.t.Fatal(err)
	}
	if _, err := part.Write(data); err != nil {
		e.h.t.Fatal(err)
	}
	if err := mw.Close(); err != nil {
		e.h.t.Fatal(err)
	}
	req, err := http.NewRequest("POST", e.h.ts.URL+"/api/v1/kb/images", &buf)
	if err != nil {
		e.h.t.Fatal(err)
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("Origin", c.origin)
	req.Header.Set("X-Forwarded-For", c.ip)
	req.Header.Set("X-CSRF-Token", c.csrf)
	resp, err := c.http.Do(req)
	if err != nil {
		e.h.t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		e.h.t.Fatal(err)
	}
	out := response{status: resp.StatusCode, header: resp.Header, raw: raw}
	if err := json.Unmarshal(raw, &out.body); err != nil {
		e.h.t.Fatalf("decode: %v: %s", err, raw)
	}
	return out
}

func TestKBImages(t *testing.T) {
	e := newKBEnv(t)
	cat := e.category("Beeld")
	expect(t, e.uploadImage(e.agent, "tekst.png", []byte("gewoon tekst, geen plaatje")), 422, "validation_failed")
	expect(t, e.uploadImage(e.agent, "x.png", []byte(`<svg xmlns="http://www.w3.org/2000/svg" onload="alert(1)"/>`)), 422, "validation_failed")
	expect(t, e.uploadImage(e.readonly, "x.png", pngBytes), 403, "forbidden")

	up := e.uploadImage(e.agent, "schermafdruk.png", pngBytes)
	expect(t, up, 201, "")
	imgURL := up.body["url"].(string)
	if !strings.HasPrefix(imgURL, "/hulp/i/") || up.body["content_type"] != "image/png" {
		t.Fatalf("upload = %s", up.raw)
	}
	// Before a published article uses it, only signed-in agents see the image.
	if got := e.get(imgURL); got.status != 404 {
		t.Fatalf("anonymous access to an unused image: %d", got.status)
	}
	if got := e.agent.do("GET", imgURL, nil); got.status != 200 || got.header.Get("Cache-Control") != "private, no-store" {
		t.Fatalf("agent access: %d %v", got.status, got.header)
	}
	r := e.article(e.agent, map[string]any{"title": "Met plaatje", "category_id": cat, "body_html": `<p>zie</p><img src="` + imgURL + `" alt="Schermafdruk">`})
	expect(t, r, 201, "")
	id, slug := r.body["id"].(string), r.body["slug"].(string)
	if got := e.get(imgURL); got.status != 404 {
		t.Fatalf("image of a draft: %d", got.status)
	}
	expect(t, e.agent.do("POST", "/api/v1/kb/articles/"+id+"/status", map[string]string{"status": "published"}), 200, "")
	got := e.get(imgURL)
	if got.status != 200 || got.header.Get("Content-Type") != "image/png" || !bytes.Equal([]byte(got.body), pngBytes) || !strings.Contains(got.header.Get("Cache-Control"), "public") {
		t.Fatalf("published image: %d %v", got.status, got.header)
	}
	if page := e.get("/hulp/a/" + slug); !strings.Contains(page.body, `src="`+imgURL+`"`) {
		t.Errorf("page lacks the image: %s", page.body)
	}
	// Removing the image from the article makes it private again.
	expect(t, e.agent.do("PATCH", "/api/v1/kb/articles/"+id, map[string]any{
		"title": "Met plaatje", "slug": slug, "category_id": cat, "body_html": "<p>zonder</p>", "version": 2,
	}), 200, "")
	if got := e.get(imgURL); got.status != 404 {
		t.Fatalf("image no longer used: %d", got.status)
	}
	if got := e.get("/hulp/i/niet-een-uuid"); got.status != 404 {
		t.Fatalf("bad image id: %d", got.status)
	}
	// Unused images are cleaned up by the next upload once they are a day old.
	if _, err := e.h.pool.Exec(context.Background(), `UPDATE kb_images SET created_at = now() - interval '2 days'`); err != nil {
		t.Fatal(err)
	}
	expect(t, e.uploadImage(e.agent, "nieuw.png", pngBytes), 201, "")
	var n int
	if err := e.h.pool.QueryRow(context.Background(), `SELECT count(*) FROM kb_images`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("images left = %d (%v)", n, err)
	}
}

func TestKBAuditsPublishingAndDeleting(t *testing.T) {
	e := newKBEnv(t)
	cat := e.category("Audit")
	id, _ := e.publishedArticle("Auditartikel", "<p>tekst</p>", cat)
	set := func(status string) {
		expect(t, e.agent.do("POST", "/api/v1/kb/articles/"+id+"/status", map[string]string{"status": status}), 200, "")
	}
	set("draft")
	set("published")
	set("archived")
	expect(t, e.agent.do("DELETE", "/api/v1/kb/articles/"+id, nil), 204, "")
	expect(t, e.agent.do("DELETE", "/api/v1/kb/articles/"+id, nil), 404, "not_found")
	expect(t, e.admin.do("PUT", "/api/v1/kb/portal", map[string]any{"name": "N", "title": "T", "intro": "", "logo_text": "", "custom_domain": ""}), 200, "")
	rows, err := e.h.pool.Query(context.Background(), `SELECT action FROM audit_log WHERE action LIKE 'kb.%' ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var got []string
	for rows.Next() {
		var a string
		if err := rows.Scan(&a); err != nil {
			t.Fatal(err)
		}
		got = append(got, a)
	}
	want := []string{"kb.category_created", "kb.article_published", "kb.article_unpublished", "kb.article_published", "kb.article_archived", "kb.article_deleted", "kb.portal_updated"}
	if !slices.Equal(got, want) {
		t.Fatalf("audit = %v\nwant %v", got, want)
	}
}

// The help center must stay fast with a large knowledge base: pages are rendered from a few
// indexed queries.
func TestKBPagesAreFastWithAThousandArticles(t *testing.T) {
	e := newKBEnv(t)
	cat := e.category("Veel")
	ctx := context.Background()
	if _, err := e.h.pool.Exec(ctx, `
		INSERT INTO kb_articles (category_id, title, slug, body_html, body_text, status, published_at, view_count)
		SELECT $1, 'Artikel over onderwerp ' || n, 'artikel-' || n,
		       '<p>Dit is de tekst van artikel ' || n || ' over facturen, betalingen en retouren.</p>',
		       'Dit is de tekst van artikel ' || n || ' over facturen, betalingen en retouren. ' || repeat('Lorem ipsum dolor sit amet. ', 60),
		       'published', now(), n
		FROM generate_series(1, 1000) n`, cat); err != nil {
		t.Fatal(err)
	}
	paths := []string{"/hulp", "/hulp/c/veel", "/hulp/a/artikel-500", "/hulp/zoeken?q=facturen+retouren", "/hulp/sitemap.xml"}
	for _, p := range paths {
		e.get(p) // warm the connection and the plan cache
		var durations []time.Duration
		for i := range 15 {
			start := time.Now()
			got := e.pub("GET", p, fmt.Sprintf("198.51.100.%d", 20+i), nil, nil)
			durations = append(durations, time.Since(start))
			if got.status != 200 {
				t.Fatalf("%s: %d", p, got.status)
			}
		}
		slices.Sort(durations)
		// The budget catches order-of-magnitude regressions (a query per article, an unindexed
		// scan), not milliseconds: shared CI runners are several times slower than a laptop.
		if median := durations[len(durations)/2]; median > 200*time.Millisecond {
			t.Errorf("%s: median %v, want under 200ms", p, median)
		}
	}
	if res := e.get("/hulp/zoeken?q=facturen"); strings.Count(res.body, "<mark>") == 0 || strings.Count(res.body, `<li><a href="/hulp/a/`) != 20 {
		t.Errorf("search returned %d results", strings.Count(res.body, `<li><a href="/hulp/a/`))
	}
}
