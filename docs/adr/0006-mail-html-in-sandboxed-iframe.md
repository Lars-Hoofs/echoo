# 6. Render mail HTML in a sandboxed, script-less iframe

Status: proposed

## Context
Inbound HTML is attacker-controlled. Sanitizers have had bypasses.

## Decision
Sanitize with a bluemonday allowlist at render time, then serve from `/render/messages/{id}`
into `<iframe sandbox="allow-scripts allow-popups allow-popups-to-escape-sandbox">` without
`allow-same-origin`, so the frame has an opaque origin and cannot reach the app, its cookies or
its API. The render response carries its own CSP:
`default-src 'none'; script-src 'sha256-<hash of our resize script>'; style-src 'unsafe-inline';
img-src 'self' data:; frame-ancestors 'self'`.
The only script that can run is our ~10-line resize script, which posts the document height to
the parent; the parent accepts messages only from that iframe's `contentWindow` and only a
numeric height. Remote images are blocked by default and, when allowed, fetched through an
SSRF-safe proxy.

## Alternatives considered
- No `allow-scripts` at all: strongest, but the parent cannot learn the content height from an
  opaque-origin frame, so every message would need a fixed-height scroll box. Poor for a mail
  client where most messages are short.
- `allow-same-origin` to measure from the parent: rejected; combined with any script it would
  let mail content act as the app.

## Consequences
Three layers: sanitizer, hash-only CSP (injected scripts cannot run), opaque origin (even a
running script cannot touch the app). One extra request per rendered message.
