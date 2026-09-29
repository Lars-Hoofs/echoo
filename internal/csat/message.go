package csat

import (
	"fmt"
	"html"
	"strings"

	"echoo/internal/compose"
)

type message struct{ subject, text, html string }

var ratingLabels = [5]string{"zeer ontevreden", "ontevreden", "gemiddeld", "tevreden", "zeer tevreden"}

// SurveyURL is the public page for a token, optionally with a rating preselected.
func SurveyURL(baseURL, token string, rating int) string {
	u := baseURL + "/tevredenheid/" + token
	if rating > 0 {
		u += fmt.Sprintf("?r=%d", rating)
	}
	return u
}

// buildMessage renders the survey email in the branded layout. The links only open the page:
// nothing is recorded until the customer confirms there, so mail scanners that follow links
// cannot rate a conversation.
func buildMessage(baseURL, token, brand string) message {
	var h strings.Builder
	h.WriteString(`<p style="margin:0 0 12px;">Hoe tevreden bent u over de hulp die u van ` + html.EscapeString(brand) + ` kreeg?</p>`)
	h.WriteString(`<p style="margin:0 0 16px;">Kies een cijfer van 1 (zeer ontevreden) tot 5 (zeer tevreden). Het duurt een paar seconden.</p>`)
	h.WriteString(`<table role="presentation" cellpadding="0" cellspacing="0" border="0"><tr>`)
	var t strings.Builder
	t.WriteString("Hoe tevreden bent u over de hulp die u van " + brand + " kreeg?\n\nKies een cijfer van 1 (zeer ontevreden) tot 5 (zeer tevreden):\n\n")
	for i := 1; i <= 5; i++ {
		link := SurveyURL(baseURL, token, i)
		h.WriteString(`<td style="padding-right:8px;"><a href="` + html.EscapeString(link) + `" style="display:inline-block;min-width:20px;padding:10px 16px;border:1px solid #d1d5db;border-radius:6px;background:#ffffff;color:#111827;font-weight:600;text-align:center;text-decoration:none;" title="` + ratingLabels[i-1] + `">` + fmt.Sprint(i) + `</a></td>`)
		fmt.Fprintf(&t, "%d (%s): %s\n", i, ratingLabels[i-1], link)
	}
	h.WriteString(`</tr></table>`)
	htmlPart, textPart := compose.Layout{BrandName: brand, BodyHTML: h.String(), BodyText: t.String()}.Render()
	return message{subject: "Hoe was uw contact met " + brand + "?", text: textPart, html: htmlPart}
}
