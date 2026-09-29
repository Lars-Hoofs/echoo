// Package docs embeds the parts of the documentation that the server itself serves.
package docs

import _ "embed"

// OpenAPI is the description of the HTTP API, served at GET /api/v1/openapi.yaml.
//
//go:embed openapi.yaml
var OpenAPI []byte
