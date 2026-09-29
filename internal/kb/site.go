package kb

import (
	"bytes"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"fmt"
	"html/template"
)

//go:embed site/templates/*.html site/kb.css
var siteFS embed.FS

// Site renders the public pages. The stylesheet is served under a name that contains its
// content hash, so it can be cached forever.
type Site struct {
	pages   map[string]*template.Template
	css     []byte
	cssPath string
}

var pageNames = []string{"home", "category", "article", "search", "message"}

func NewSite() (*Site, error) {
	css, err := siteFS.ReadFile("site/kb.css")
	if err != nil {
		return nil, fmt.Errorf("read kb.css: %w", err)
	}
	sum := sha256.Sum256(css)
	s := &Site{pages: map[string]*template.Template{}, css: css, cssPath: "/hulp/static/kb." + hex.EncodeToString(sum[:6]) + ".css"}
	for _, name := range pageNames {
		t, err := template.ParseFS(siteFS, "site/templates/layout.html", "site/templates/"+name+".html")
		if err != nil {
			return nil, fmt.Errorf("parse %s template: %w", name, err)
		}
		s.pages[name] = t
	}
	return s, nil
}

// CSS returns the stylesheet and the path it is served under.
func (s *Site) CSS() (body []byte, path string) { return s.css, s.cssPath }

// Render executes a page into memory, so an error never leaves a half-written response.
func (s *Site) Render(page string, data any) ([]byte, error) {
	t, ok := s.pages[page]
	if !ok {
		return nil, fmt.Errorf("unknown page %q", page)
	}
	var buf bytes.Buffer
	if err := t.ExecuteTemplate(&buf, "layout", data); err != nil {
		return nil, fmt.Errorf("render %s: %w", page, err)
	}
	return buf.Bytes(), nil
}

// Page is what every template needs on top of its own data. Templates read the fields of the
// embedded struct through it.
type Page struct {
	Title       string
	Description string
	Canonical   string
	OGType      string
	NoIndex     bool
	CSS         string
	Portal      Portal
}

type Portal struct {
	Name, Title, Intro, LogoText string
}

type CategoryCard struct {
	Slug, Name, Description string
	Count                   int
}

type ArticleLink struct {
	Slug, Title, Excerpt string
}

type Result struct {
	Slug, Title string
	Snippet     []SnippetPart
}

type HomeData struct {
	Page
	Query      string
	Categories []CategoryCard
	Popular    []ArticleLink
}

type CategoryData struct {
	Page
	Category CategoryCard
	Parent   *CategoryCard
	Children []CategoryCard
	Articles []ArticleLink
}

type ArticleData struct {
	Page
	Article       ArticleLink
	Body          template.HTML
	Updated       string
	CategoryName  string
	CategorySlug  string
	ParentName    string
	ParentSlug    string
	Related       []ArticleLink
	FeedbackToken string
	Thanks        bool
}

type SearchData struct {
	Page
	Query   string
	Results []Result
}

// MessageData is the page for errors and notices (not found, expired form, rate limited).
type MessageData struct {
	Page
	Heading, Text string
}
