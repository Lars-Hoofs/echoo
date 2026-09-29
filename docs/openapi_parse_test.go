package docs

import (
	"testing"

	"go.yaml.in/yaml/v3"
)

// The route coverage test in internal/api scans lines; this one makes sure the document is
// valid YAML (yaml.v3 also rejects duplicate mapping keys) so clients can actually load it.
func TestOpenAPIIsValidYAML(t *testing.T) {
	var doc map[string]any
	if err := yaml.Unmarshal(OpenAPI, &doc); err != nil {
		t.Fatalf("docs/openapi.yaml is not valid YAML: %v", err)
	}
	for _, key := range []string{"openapi", "info", "paths"} {
		if _, ok := doc[key]; !ok {
			t.Errorf("missing top-level key %q", key)
		}
	}
}
