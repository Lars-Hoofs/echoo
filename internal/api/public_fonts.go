package api

import (
	"embed"
	"net/http"

	"github.com/go-chi/chi/v5"
)

// The server-rendered public pages (help center, survey, unsubscribe) have a content security
// policy that allows nothing but their own origin, so they cannot load a font from a CDN. The
// Urbanist weights they use are served from here; the file names are fixed, hence the day-long cache.
//
//go:embed fonts/urbanist-latin-300-normal.woff2 fonts/urbanist-latin-400-normal.woff2 fonts/urbanist-latin-500-normal.woff2
var publicFonts embed.FS

// fontFiles is the complete set of servable names; a request can only ever select one of these.
var fontFiles = map[string][]byte{}

func init() {
	for _, name := range []string{"urbanist-latin-300-normal.woff2", "urbanist-latin-400-normal.woff2", "urbanist-latin-500-normal.woff2"} {
		body, err := publicFonts.ReadFile("fonts/" + name)
		if err != nil {
			panic(err) // the names are embedded above, so a failure is a build mistake
		}
		fontFiles[name] = body
	}
}

func (s *Server) publicFontRoutes(r chi.Router) {
	r.Get("/fonts/{name}", func(w http.ResponseWriter, r *http.Request) {
		body, ok := fontFiles[chi.URLParam(r, "name")]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "font/woff2")
		w.Header().Set("Cache-Control", "public, max-age=86400")
		_, _ = w.Write(body)
	})
}
