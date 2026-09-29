package parse

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"echoo/internal/mail"
)

// loadFixture reads testdata/name with CRLF line endings, as IMAP delivers messages. The
// files are stored with LF so they stay readable and diff cleanly.
func loadFixture(t testing.TB, name string) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return []byte(strings.ReplaceAll(strings.ReplaceAll(string(raw), "\r\n", "\n"), "\n", "\r\n"))
}

func mustParse(t testing.TB, raw []byte) *mail.Parsed {
	t.Helper()
	p, err := Parse(raw, DefaultLimits)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	return p
}

func date(y int, m time.Month, d, hh, mm, ss int) time.Time {
	return time.Date(y, m, d, hh, mm, ss, 0, time.UTC)
}

func TestFixtures(t *testing.T) {
	const ourID = "echoo.0123456789abcdef0123456789abcdef.aabbccddeeff0011@acme.example"
	tests := []struct {
		file  string
		check func(t *testing.T, p *mail.Parsed)
	}{
		{"gmail_reply.eml", func(t *testing.T, p *mail.Parsed) {
			eq(t, "MessageID", p.MessageID, "CAF3xk2Ns7yQw8dT4uVbLx9kE5mHq6oPzRt1nYc3aWjXs0Dg@mail.gmail.com")
			eq(t, "InReplyTo", p.InReplyTo, []string{ourID})
			eq(t, "References", p.References, []string{"CAF3xk2Pq9v7gZ0r1@mail.gmail.com", ourID})
			eq(t, "From", p.From, mail.Address{Name: "Jan Jansen", Address: "jan.jansen@gmail.com"})
			eq(t, "Cc", p.Cc, []mail.Address{{Name: "Jansen, Marieke", Address: "marieke@example.org"}})
			eq(t, "Date", p.Date, date(2026, 3, 3, 17, 13, 41))
			eq(t, "Subject", p.Subject, "Re: Vraag over mijn bestelling #4821")
			contains(t, "Text", p.Text, "de doos was beschadigd", "> Hallo Jan,")
			contains(t, "HTML", p.HTML, `<div dir="ltr">`, "gmail_quote")
			eq(t, "AuthResults", len(p.AuthResults), 1)
			contains(t, "AuthResults", p.AuthResults[0], "dkim=pass", "spf=pass", "dmarc=pass")
			eq(t, "attachments", len(p.Attachments), 0)
		}},
		{"outlook_reply.eml", func(t *testing.T, p *mail.Parsed) {
			eq(t, "ThreadTopic", p.ThreadTopic, "Offerte kozijnen")
			eq(t, "ThreadIndex", p.ThreadIndex, "AQHb6cIFxk9dZ0mQTQ6F1dQyE0+sVw==")
			eq(t, "References", len(p.References), 0)
			eq(t, "MessageID", p.MessageID, "AM0PR01MB1234ABCD5678EF9012@am0pr01mb1234.eurprd01.prod.exchangelabs.com")
			eq(t, "From", p.From.Address, "p.devries@contoso.example")
			contains(t, "Text", p.Text, "De prijs van € 4.250,- is akkoord", "“hoogwaardige”", "leverancier’s", "\nVan: Acme Support <support@acme.example>\nVerzonden:")
			contains(t, "HTML", p.HTML, "<b>Van:</b>", "€ 4.250,-")
			eq(t, "attachments", len(p.Attachments), 0)
		}},
		{"apple_mail_inline.eml", func(t *testing.T, p *mail.Parsed) {
			eq(t, "From", p.From, mail.Address{Name: "Fleur van den Berg", Address: "fleur@icloud.com"})
			eq(t, "attachments", len(p.Attachments), 1)
			a := p.Attachments[0]
			eq(t, "Filename", a.Filename, "schade.png")
			eq(t, "DeclaredType", a.DeclaredType, "image/png")
			eq(t, "ContentID", a.ContentID, "logo.png@01D9A1B2.C3D4E5F6")
			eq(t, "Inline", a.Inline, true)
			eq(t, "PNG magic", string(a.Data[:4]), "\x89PNG")
			contains(t, "HTML", p.HTML, `src="cid:logo.png@01D9A1B2.C3D4E5F6"`)
			contains(t, "Text", p.Text, "Hierbij een foto van de schade:")
			notContains(t, "Text", p.Text, "<div>")
		}},
		{"mailing_list.eml", func(t *testing.T, p *mail.Parsed) {
			eq(t, "ListID", p.ListID, "dev-nl.lists.example.org")
			eq(t, "Bulk", p.Bulk, true)
			eq(t, "AutoSubmitted", p.AutoSubmitted, false)
			eq(t, "Subject", p.Subject, "[dev-nl] Re: Aanbeveling voor een e-mailbibliotheek?")
			eq(t, "Sender", p.Sender, &mail.Address{Address: "dev-nl-bounces@lists.example.org"})
			eq(t, "ReplyTo", p.ReplyTo, []mail.Address{{Address: "dev-nl@lists.example.org"}})
			eq(t, "References", p.References, []string{"20260304150000.GA9001@example.com", "20260304201500.GB1203@example.org"})
			contains(t, "Text (flowed lines joined)", p.Text, "erg tevreden over. Het is een  kleine bibliotheek")
			contains(t, "Text (signature kept)", p.Text, "\n-- \nSanne\n")
		}},
		{"out_of_office.eml", func(t *testing.T, p *mail.Parsed) {
			eq(t, "AutoSubmitted", p.AutoSubmitted, true)
			eq(t, "Bulk", p.Bulk, false)
			eq(t, "From", p.From, mail.Address{Name: "Marijn Vóorthijsen", Address: "marijn@example.org"})
			eq(t, "Subject", p.Subject, "Automatische reactie: Re: Factuur 2026-0312")
			eq(t, "InReplyTo", p.InReplyTo, []string{"echoo.0123456789abcdef0123456789abcdef.1122334455667788@acme.example"})
			contains(t, "Text (soft break removed)", p.Text, "e-mail. Voor dringende zaken")
		}},
		{"forward_attachment.eml", func(t *testing.T, p *mail.Parsed) {
			eq(t, "Text", strings.TrimSpace(p.Text), "Kunnen jullie hier even naar kijken? Zie bijlage.\n\nLotte")
			eq(t, "attachments", len(p.Attachments), 1)
			a := p.Attachments[0]
			eq(t, "Filename", a.Filename, "Doorgestuurd bericht.eml")
			eq(t, "DeclaredType", a.DeclaredType, "message/rfc822")
			eq(t, "Inline", a.Inline, false)
			contains(t, "Data", string(a.Data), "Message-ID: <original-complaint-42@example.net>", "Mijn pakket is niet aangekomen.")
			eq(t, "DSN", p.DSN == nil, true)
		}},
		{"forward_inline.eml", func(t *testing.T, p *mail.Parsed) {
			eq(t, "attachments", len(p.Attachments), 0)
			contains(t, "Text", p.Text, "---------- Forwarded message ---------", "Mijn pakket is niet aangekomen.")
		}},
		{"iso8859_plain.eml", func(t *testing.T, p *mail.Parsed) {
			eq(t, "From", p.From.Name, "René Müller")
			eq(t, "Subject (mixed charsets)", p.Subject, "Prijsinformatie café— vraag")
			contains(t, "Text", p.Text, "café-inrichting", "René Müller")
		}},
		{"rfc2231_filename.eml", func(t *testing.T, p *mail.Parsed) {
			var names []string
			for _, a := range p.Attachments {
				names = append(names, a.Filename)
			}
			eq(t, "filenames", names, []string{
				"Overeenkomst € café.pdf",
				"Kwartaalrapport Übersicht Q1 2026.xlsx",
				"foto kærken☺.jpg",
			})
			eq(t, "first attachment data", string(p.Attachments[0].Data), "%PDF-1.4\n% minimal\n")
			eq(t, "DeclaredType", p.Attachments[2].DeclaredType, "image/jpeg")
		}},
		{"path_traversal.eml", func(t *testing.T, p *mail.Parsed) {
			var names, types []string
			for _, a := range p.Attachments {
				names = append(names, a.Filename)
				types = append(types, a.DeclaredType)
			}
			eq(t, "filenames", names, []string{"passwd", "evil.exe", "htaccess", "bijlage.png"})
			eq(t, "types", types, []string{"application/octet-stream", "application/x-msdownload", "text/plain", "image/png"})
			eq(t, "Text", strings.TrimSpace(p.Text), "See attached.")
		}},
		{"missing_message_id.eml", func(t *testing.T, p *mail.Parsed) {
			if !strings.HasPrefix(p.MessageID, "synthetic-") || !strings.HasSuffix(p.MessageID, "@echoo.invalid") {
				t.Fatalf("MessageID = %q", p.MessageID)
			}
			eq(t, "length", len(p.MessageID), len("synthetic-")+64+len("@echoo.invalid"))
			eq(t, "InReplyTo", len(p.InReplyTo), 0)
			eq(t, "normalizes", mail.NormalizeMessageID(p.MessageID), p.MessageID)
		}},
		{"broken_date.eml", func(t *testing.T, p *mail.Parsed) {
			eq(t, "Date", p.Date.IsZero(), true)
			eq(t, "MessageID", p.MessageID, "broken-date-001@example.org")
		}},
		{"postfix_dsn.eml", func(t *testing.T, p *mail.Parsed) {
			eq(t, "AutoSubmitted", p.AutoSubmitted, true)
			eq(t, "attachments", len(p.Attachments), 0)
			eq(t, "DSN", p.DSN, &mail.DSN{
				OriginalMessageID: "echoo.0123456789abcdef0123456789abcdef.99aabbccddeeff00@acme.example",
				Recipients: []mail.DSNRecipient{{
					Address:        "nobody@example.net",
					Action:         "failed",
					Status:         "5.1.1",
					DiagnosticCode: "smtp; 550 5.1.1 <Nobody@Example.NET>: Recipient address rejected: User unknown in virtual mailbox table",
				}},
			})
			contains(t, "Text", p.Text, "Recipient address rejected")
		}},
		{"exchange_ndr.eml", func(t *testing.T, p *mail.Parsed) {
			eq(t, "AutoSubmitted", p.AutoSubmitted, true)
			eq(t, "DSN", p.DSN, &mail.DSN{
				OriginalMessageID: "echoo.0123456789abcdef0123456789abcdef.5566778899aabbcc@acme.example",
				Recipients: []mail.DSNRecipient{{
					Address:        "mark.bos@contoso.example",
					Action:         "failed",
					Status:         "5.4.1",
					DiagnosticCode: "smtp;550 5.4.1 Recipient address rejected: Access denied. AS(201806281) [AM0PR02FT099.eop-EUR02.prod.protection.outlook.com 2026-03-14T10:14:59.812Z]",
				}},
			})
			contains(t, "Text", p.Text, "Delivery has failed to these recipients")
			contains(t, "HTML", p.HTML, "<h1>Your message wasn't delivered</h1>")
			eq(t, "returned message kept as attachment", len(p.Attachments), 1)
			eq(t, "Filename", p.Attachments[0].Filename, "Doorgestuurd bericht.eml")
		}},
	}
	for _, tt := range tests {
		t.Run(strings.TrimSuffix(tt.file, ".eml"), func(t *testing.T) {
			tt.check(t, mustParse(t, loadFixture(t, tt.file)))
		})
	}
}

func TestFixturesWithBareLF(t *testing.T) {
	for _, name := range []string{"gmail_reply.eml", "apple_mail_inline.eml", "postfix_dsn.eml"} {
		t.Run(name, func(t *testing.T) {
			raw, err := os.ReadFile(filepath.Join("testdata", name))
			if err != nil {
				t.Fatal(err)
			}
			crlf := mustParse(t, loadFixture(t, name))
			lf := mustParse(t, raw)
			eq(t, "MessageID", lf.MessageID, crlf.MessageID)
			eq(t, "Subject", lf.Subject, crlf.Subject)
			eq(t, "Text", lf.Text, crlf.Text)
			eq(t, "attachments", len(lf.Attachments), len(crlf.Attachments))
			eq(t, "DSN", lf.DSN, crlf.DSN)
		})
	}
}
