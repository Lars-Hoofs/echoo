package api

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"io/fs"
	"net/http"
	"path"
	"strings"
)

// noncePlaceholder is replaced in index.html with a per-request nonce. The frontend reads it
// so Radix can tag the few <style> elements it injects; no inline scripts are ever allowed.
const noncePlaceholder = "__CSP_NONCE__"

func (s *Server) spa() http.Handler {
	index, err := fs.ReadFile(s.web, "index.html")
	if err != nil {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "frontend not built: run `npm run build` in web/", http.StatusNotFound)
		})
	}
	files := http.FileServerFS(s.web)

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		name := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
		if name != "" && name != "index.html" && !strings.HasPrefix(path.Base(name), ".") {
			if st, err := fs.Stat(s.web, name); err == nil && !st.IsDir() {
				if strings.HasPrefix(name, "assets/") {
					// Vite puts a content hash in every asset file name.
					w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
				} else {
					w.Header().Set("Cache-Control", "no-cache")
				}
				files.ServeHTTP(w, r)
				return
			} else if err != nil && !errors.Is(err, fs.ErrNotExist) {
				writeError(w, r, err)
				return
			}
		}
		s.serveIndex(w, r, index)
	})
}

func (s *Server) serveIndex(w http.ResponseWriter, r *http.Request, index []byte) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		writeError(w, r, err)
		return
	}
	nonce := base64.StdEncoding.EncodeToString(b)
	h := w.Header()
	h.Set("Content-Security-Policy", strings.Join([]string{
		"default-src 'self'",
		"script-src 'self'",
		"style-src 'self' 'nonce-" + nonce + "'",
		"img-src 'self' data: blob:",
		"font-src 'self'",
		"connect-src 'self'",
		"frame-src 'self'",
		"frame-ancestors 'none'",
		"base-uri 'none'",
		"form-action 'self'",
		"object-src 'none'",
	}, "; "))
	h.Set("Cache-Control", "no-store")
	h.Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	if r.Method == http.MethodHead {
		return
	}
	_, _ = w.Write(bytes.ReplaceAll(index, []byte(noncePlaceholder), []byte(nonce)))
}
