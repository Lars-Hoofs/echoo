package campaigns

import (
	"crypto/sha256"
	"fmt"
	"html"
	"regexp"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5/pgtype"

	"echoo/internal/compose"
	"echoo/internal/mail"
	"echoo/internal/mail/send"
)

var variableRef = regexp.MustCompile(`\{\{\s*([a-z_]+\.[a-z_]+)\s*\}\}`)

// Variables lists the placeholders a campaign may use. Unlike a reply it belongs to no
// conversation yet, so conversation.number is not available.
var Variables = []string{"contact.name", "contact.first_name", "contact.email", "agent.name", "agent.first_name", "mailbox.name"}

// UnknownVariables returns the placeholders in the texts that a campaign cannot fill.
func UnknownVariables(texts ...string) []string {
	var out []string
	for _, t := range texts {
		for _, m := range variableRef.FindAllStringSubmatch(t, -1) {
			if !slices.Contains(Variables, m[1]) && !slices.Contains(out, m[1]) {
				out = append(out, m[1])
			}
		}
	}
	return out
}

// Content is what a recipient's message is made of.
type Content struct {
	Subject string
	HTML    string
	Text    string
	Headers []send.Header
}

// Sender is who the mail appears to come from and who wrote it.
type Sender struct {
	Brand string // the mailbox display name
	Agent string // the campaign's creator
}

// Render fills in the placeholders for one recipient and adds the unsubscribe footer and the
// bulk-mail headers. A placeholder the recipient has no value for is removed, not sent as
// written: a customer must never see template syntax.
func Render(subject, bodyHTML string, from Sender, to mail.Address, unsubscribeURL string) Content {
	vars := compose.Variables{ContactName: to.Name, ContactEmail: to.Address, AgentName: from.Agent, MailboxName: from.Brand}
	subject, unresolved := vars.RenderText(subject)
	subject = strings.Join(strings.Fields(blank(subject, unresolved)), " ")
	body, unresolved := vars.Render(bodyHTML)
	body = blank(body, unresolved)

	notice := fmt.Sprintf("Je ontvangt deze e-mail als contactpersoon van %s.", from.Brand)
	footerHTML := `<p style="margin:24px 0 0;font-size:12px;line-height:1.4;color:#6b7280;">` + html.EscapeString(notice) +
		` <a href="` + html.EscapeString(unsubscribeURL) + `">Afmelden</a></p>`
	text := compose.TextFromHTML(body) + "\n\n-- \n" + notice + " Afmelden: " + unsubscribeURL
	htmlPart, textPart := compose.Layout{BrandName: from.Brand, BodyHTML: body + footerHTML, BodyText: text}.Render()

	return Content{
		Subject: subject, HTML: htmlPart, Text: textPart,
		Headers: []send.Header{
			{Name: "List-Unsubscribe", Value: "<" + unsubscribeURL + ">"},
			{Name: "List-Unsubscribe-Post", Value: "List-Unsubscribe=One-Click"},
			{Name: "Precedence", Value: "bulk"},
		},
	}
}

func blank(text string, unresolved []string) string {
	for _, name := range unresolved {
		text = regexp.MustCompile(`\{\{\s*`+regexp.QuoteMeta(name)+`\s*\}\}`).ReplaceAllString(text, "")
	}
	return text
}

// idempotencyKey is the send-queue key of one recipient's message. It derives from the
// campaign and the recipient alone, so a retry after a crash finds the earlier message instead
// of queueing a second one.
func idempotencyKey(campaign, recipient pgtype.UUID) pgtype.UUID {
	h := sha256.New()
	h.Write([]byte("echoo.campaign.v1"))
	h.Write(campaign.Bytes[:])
	h.Write(recipient.Bytes[:])
	id := pgtype.UUID{Valid: true}
	copy(id.Bytes[:], h.Sum(nil))
	id.Bytes[6] = id.Bytes[6]&0x0f | 0x50
	id.Bytes[8] = id.Bytes[8]&0x3f | 0x80
	return id
}
