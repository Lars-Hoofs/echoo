package api

import (
	"net/http"
	"regexp"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"echoo/docs"
)

var (
	specPath   = regexp.MustCompile(`^  (/\S*):\s*$`)
	specMethod = regexp.MustCompile(`^    (get|post|put|patch|delete):\s*$`)
)

// documentedOperations reads "METHOD /path" pairs from the paths section of the spec. The file
// has a fixed layout (two spaces per level), so a line scan is enough and needs no YAML library.
func documentedOperations(spec string) map[string]bool {
	ops := map[string]bool{}
	inPaths := false
	path := ""
	for _, line := range strings.Split(spec, "\n") {
		if line != "" && line[0] != ' ' && line[0] != '#' {
			inPaths = line == "paths:"
			continue
		}
		if !inPaths {
			continue
		}
		if m := specPath.FindStringSubmatch(line); m != nil {
			path = m[1]
		} else if m := specMethod.FindStringSubmatch(line); m != nil && path != "" {
			ops[strings.ToUpper(m[1])+" "+path] = true
		}
	}
	return ops
}

// TestEveryRouteIsDocumented keeps docs/openapi.yaml in step with the router: a new route
// fails this test until it is described in the spec.
func TestEveryRouteIsDocumented(t *testing.T) {
	h := newHarness(t)
	documented := documentedOperations(string(docs.OpenAPI))
	if len(documented) == 0 {
		t.Fatal("no operations found in openapi.yaml")
	}
	const prefix = "/api/v1"
	registered := map[string]bool{}
	err := chi.Walk(h.srv.Handler(), func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		route = strings.TrimSuffix(strings.ReplaceAll(route, "/*/", "/"), "/*")
		path, ok := strings.CutPrefix(route, prefix)
		if !ok {
			return nil
		}
		key := method + " " + path
		registered[key] = true
		if !documented[key] {
			t.Errorf("route %s %s is not described in docs/openapi.yaml", method, route)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for key := range documented {
		if !registered[key] {
			t.Errorf("docs/openapi.yaml describes %s, which is not a route", key)
		}
	}
}

func TestOpenAPIIsServedToSignedInUsersOnly(t *testing.T) {
	h := newHarness(t)
	expect(t, h.client().do("GET", "/api/v1/openapi.yaml", nil), 401, "unauthenticated")

	c, _ := h.loggedIn("agent")
	r := c.do("GET", "/api/v1/openapi.yaml", nil)
	if r.status != 200 || !strings.HasPrefix(r.header.Get("Content-Type"), "application/yaml") || !strings.HasPrefix(string(r.raw), "openapi: 3.0.3") {
		t.Fatalf("status %d, type %q", r.status, r.header.Get("Content-Type"))
	}
	token, _ := h.newToken(c, "read")
	if r := h.bearer(token, "GET", "/api/v1/openapi.yaml", nil); r.status != 200 {
		t.Errorf("with a token: %d", r.status)
	}
}
