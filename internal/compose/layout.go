package compose

import (
	"html"
	"strings"
)

// Layout is the content of one outgoing message before it is wrapped for delivery.
type Layout struct {
	// BrandName is shown in the header; Echoo uses the mailbox display name.
	BrandName     string
	BodyHTML      string // sanitized editor HTML
	BodyText      string // plain text alternative; derived from BodyHTML when empty
	SignatureHTML string // sanitized, may be empty
	FooterText    string // plain text, may be empty
}

const (
	fontStack = "-apple-system,BlinkMacSystemFont,'Segoe UI',Helvetica,Arial,sans-serif"
	inkColor  = "#111827"
	faintInk  = "#6b7280"
	ruleColor = "#e5e7eb"
)

// Render returns the HTML and plain text parts. The HTML is an email-client-safe table layout
// with inline styles only: many clients drop <style> blocks and ignore CSS layout.
func (l Layout) Render() (htmlPart, textPart string) {
	var h strings.Builder
	h.WriteString(`<!doctype html><html><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"></head>`)
	h.WriteString(`<body style="margin:0;padding:0;background:#f3f4f6;">`)
	h.WriteString(`<table role="presentation" width="100%" cellpadding="0" cellspacing="0" border="0" style="background:#f3f4f6;"><tr><td align="center" style="padding:24px 12px;">`)
	h.WriteString(`<table role="presentation" width="600" cellpadding="0" cellspacing="0" border="0" style="width:100%;max-width:600px;background:#ffffff;border:1px solid ` + ruleColor + `;">`)
	if name := strings.TrimSpace(l.BrandName); name != "" {
		h.WriteString(`<tr><td style="padding:16px 24px;border-bottom:1px solid ` + ruleColor + `;font-family:` + fontStack + `;font-size:16px;font-weight:600;color:` + inkColor + `;">` + html.EscapeString(name) + `</td></tr>`)
	}
	h.WriteString(`<tr><td style="padding:24px;font-family:` + fontStack + `;font-size:15px;line-height:1.5;color:` + inkColor + `;">` + l.BodyHTML)
	if l.SignatureHTML != "" {
		h.WriteString(`<div style="margin-top:24px;padding-top:12px;border-top:1px solid ` + ruleColor + `;color:` + inkColor + `;">` + l.SignatureHTML + `</div>`)
	}
	h.WriteString(`</td></tr>`)
	if footer := strings.TrimSpace(l.FooterText); footer != "" {
		h.WriteString(`<tr><td style="padding:12px 24px;border-top:1px solid ` + ruleColor + `;font-family:` + fontStack + `;font-size:12px;line-height:1.4;color:` + faintInk + `;">` + html.EscapeString(footer) + `</td></tr>`)
	}
	h.WriteString(`</table></td></tr></table></body></html>`)

	var t strings.Builder
	bodyText := strings.TrimSpace(l.BodyText)
	if bodyText == "" {
		bodyText = TextFromHTML(l.BodyHTML)
	}
	t.WriteString(bodyText)
	if l.SignatureHTML != "" {
		// "-- " is the conventional signature separator, including the trailing space.
		t.WriteString("\n\n-- \n" + TextFromHTML(l.SignatureHTML))
	}
	if footer := strings.TrimSpace(l.FooterText); footer != "" {
		t.WriteString("\n\n" + footer)
	}
	return h.String(), t.String()
}
