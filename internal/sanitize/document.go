package sanitize

import (
	"crypto/sha256"
	"encoding/base64"
)

// resizeScript reports the document height to the parent, which validates event.source.
// The target origin is "*" because a sandboxed document has an opaque origin; the message
// carries nothing but a number.
const resizeScript = `(function(){` +
	`function post(){parent.postMessage({type:"echoo:render-height",height:Math.ceil(document.body.getBoundingClientRect().height)},"*")}` +
	`new ResizeObserver(post).observe(document.body);` +
	`window.addEventListener("load",post);post()` +
	`})()`

const documentStyle = `html{background:#fff}` +
	`body{margin:0;padding:16px;background:#fff;color:#1a1a1a;font:14px/1.5 system-ui,sans-serif;overflow-wrap:anywhere}` +
	`img{max-width:100%;height:auto}table{max-width:100%}pre{white-space:pre-wrap}` +
	`a{color:#1d4ed8}blockquote{margin:8px 0 8px 4px;padding-left:12px;border-left:2px solid #d1d5db;color:#4b5563}` +
	`.plain{white-space:pre-wrap}.truncated{margin-top:16px;color:#4b5563;font-size:13px}`

var scriptHash = func() string {
	sum := sha256.Sum256([]byte(resizeScript))
	return "'sha256-" + base64.StdEncoding.EncodeToString(sum[:]) + "'"
}()

// ContentSecurityPolicy is served with every rendered document. Only the hashed resize script
// may run; images may come from this origin (attachments, proxy) or be data: URIs.
var ContentSecurityPolicy = "default-src 'none'; img-src 'self' data:; style-src 'unsafe-inline'; script-src " +
	scriptHash + "; base-uri 'none'; form-action 'none'; frame-ancestors 'self'"

// Document wraps an already sanitized fragment (from HTML or Text) into a full page. plain
// selects pre-wrapped text layout.
func Document(fragment string, plain, truncated bool) []byte {
	class := ""
	if plain {
		class = ` class="plain"`
	}
	note := ""
	if truncated {
		note = `<p class="truncated">Dit bericht is ingekort omdat het erg groot is.</p>`
	}
	return []byte(`<!doctype html><html><head><meta charset="utf-8">` +
		`<meta name="viewport" content="width=device-width,initial-scale=1">` +
		`<style>` + documentStyle + `</style></head><body><div` + class + `>` + fragment + `</div>` + note +
		`<script>` + resizeScript + `</script></body></html>`)
}
