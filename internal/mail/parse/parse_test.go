package parse

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"echoo/internal/mail"
)

const hdr = "From: a@example.org\r\nTo: b@example.org\r\nMessage-ID: <t@example.org>\r\n"

func msg(headers, body string) []byte {
	return []byte(hdr + headers + "\r\n" + strings.ReplaceAll(body, "\n", "\r\n"))
}

func TestBodies(t *testing.T) {
	tests := []struct {
		name         string
		raw          []byte
		text, html   string
		attachments  int
		textContains string
	}{
		{
			name: "alternative picks both renditions",
			raw: msg("Content-Type: multipart/alternative; boundary=x\n", `--x
Content-Type: text/plain

plain
--x
Content-Type: text/html

<p>html</p>
--x--
`),
			text: "plain", html: "<p>html</p>",
		},
		{
			name: "html only derives text",
			raw: msg("Content-Type: text/html; charset=utf-8\n", `<html><head><title>t</title><style>p{}</style></head>
<body><script>alert(1)</script><p>Hello <b>world</b></p><p>Second</p></body></html>`),
			html: "<html><head><title>t</title><style>p{}</style></head>\r\n<body><script>alert(1)</script><p>Hello <b>world</b></p><p>Second</p></body></html>",
			text: "Hello world\n\nSecond",
		},
		{
			name: "whitespace-only plain part falls back to html",
			raw: msg("Content-Type: multipart/alternative; boundary=x\n", `--x
Content-Type: text/plain

  
--x
Content-Type: text/html

<div>Visible</div>
--x--
`),
			html: "<div>Visible</div>", text: "Visible",
		},
		{
			name: "mixed concatenates text parts and keeps attachment",
			raw: msg("Content-Type: multipart/mixed; boundary=x\n", `--x
Content-Type: text/plain

first
--x
Content-Type: image/jpeg
Content-Disposition: attachment; filename=a.jpg
Content-Transfer-Encoding: base64

/9j/4A==
--x
Content-Type: text/plain

second
--x--
`),
			text: "first\n\nsecond", attachments: 1,
		},
		{
			name: "nested mixed alternative related",
			raw: msg("Content-Type: multipart/mixed; boundary=m\n", `--m
Content-Type: multipart/alternative; boundary=a

--a
Content-Type: text/plain

hi
--a
Content-Type: multipart/related; boundary=r

--r
Content-Type: text/html

<img src="cid:img1">
--r
Content-Type: image/gif
Content-ID: <IMG1>
Content-Transfer-Encoding: base64

R0lGODlhAQABAAAAACw=
--r--

--a--

--m
Content-Type: application/pdf; name=doc.pdf
Content-Transfer-Encoding: base64

JVBERi0xLjQ=
--m--
`),
			text: "hi", html: "<img src=\"cid:img1\">", attachments: 2,
		},
		{
			name: "text attachment with filename is not the body",
			raw: msg("Content-Type: multipart/mixed; boundary=x\n", `--x
Content-Type: text/plain

body
--x
Content-Type: text/plain; charset=utf-8
Content-Disposition: attachment; filename="notes.txt"

attached
--x--
`),
			text: "body", attachments: 1,
		},
		{
			name: "windows-1252 quoted-printable",
			raw: msg("Content-Type: text/plain; charset=windows-1252\nContent-Transfer-Encoding: quoted-printable\n",
				"prijs =80 5,- =96 =93goed=94\n"),
			text: "prijs € 5,- – “goed”\n",
		},
		{
			name: "utf-8 with BOM",
			raw: msg("Content-Type: text/plain; charset=utf-8\n",
				"\ufeffhallo wereld\n"),
			text: "hallo wereld\n",
		},
		{
			name: "unknown charset is best effort",
			raw: msg("Content-Type: text/plain; charset=x-klingon\nContent-Transfer-Encoding: 8bit\n",
				"caf\xe9\n"),
			text: "café\n",
		},
		{
			name: "undeclared 8-bit text",
			raw:  msg("Content-Transfer-Encoding: 8bit\n", "na\xefef\n"),
			text: "naïef\n",
		},
		{
			name: "mislabeled utf-8",
			raw:  msg("Content-Type: text/plain; charset=utf-8\n", "smart \x92quote\x92\n"),
			text: "smart ’quote’\n",
		},
		{
			name: "base64 body",
			raw: msg("Content-Type: text/plain; charset=utf-8\nContent-Transfer-Encoding: base64\n",
				"SGVsbG8gd8O2cmxkCg==\n"),
			text: "Hello wörld\n",
		},
		{
			name: "corrupt base64 keeps decoded prefix",
			raw: msg("Content-Type: text/plain; charset=utf-8\nContent-Transfer-Encoding: base64\n",
				"SGVsbG8gd29ybGQK\n!!!!\n"),
			textContains: "Hello world",
		},
		{
			name: "unknown transfer encoding",
			raw: msg("Content-Type: text/plain\nContent-Transfer-Encoding: x-uuencode\n",
				"raw body\n"),
			textContains: "raw body",
		},
		{
			name: "NUL bytes removed",
			raw:  msg("Content-Type: text/plain\n", "a\x00b\n"),
			text: "ab\n",
		},
		{
			name: "format flowed with delsp",
			raw: msg("Content-Type: text/plain; format=flowed; delsp=yes\n",
				"lange regel die doo \nrloopt\n> geciteer \n> d\nnormaal\n"),
			text: "lange regel die doorloopt\n> geciteerd\nnormaal\n",
		},
		{
			name: "format flowed keeps the space",
			raw:  msg("Content-Type: text/plain; format=flowed\n", "een \nregel\n"),
			text: "een regel\n",
		},
		{
			name: "multipart without boundary yields nothing but does not fail",
			raw:  msg("Content-Type: multipart/mixed\n", "body\n"),
		},
		{
			name: "truncated multipart keeps earlier parts",
			raw: msg("Content-Type: multipart/mixed; boundary=x\n", `--x
Content-Type: text/plain

kept
--x
Content-Ty`),
			textContains: "kept",
		},
		{
			name: "garbage line in header block is dropped",
			raw: []byte("From MAILER-DAEMON Mon Jan  5 10:11:12 2026\r\n" + hdr +
				"garbage without colon\r\n Subject: folded-looking garbage\r\nContent-Type: text/plain\r\n\r\nbody\r\n"),
			text: "body\r\n",
		},
		{
			name:        "unusable content type is an attachment, not a failure",
			raw:         msg("Content-Type: ???\n", "data\n"),
			attachments: 1,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := mustParse(t, tt.raw)
			if tt.textContains != "" {
				contains(t, "Text", p.Text, tt.textContains)
			} else {
				eq(t, "Text", strings.ReplaceAll(p.Text, "\r\n", "\n"), strings.ReplaceAll(tt.text, "\r\n", "\n"))
			}
			eq(t, "HTML", strings.ReplaceAll(p.HTML, "\r\n", "\n"), strings.ReplaceAll(tt.html, "\r\n", "\n"))
			eq(t, "attachments", len(p.Attachments), tt.attachments)
			eq(t, "MessageID", p.MessageID, "t@example.org")
		})
	}
}

func TestInlineDetection(t *testing.T) {
	p := mustParse(t, msg("Content-Type: multipart/related; boundary=r\n", `--r
Content-Type: text/html

<img src="CID:Logo@X"><img src="cid:other">
--r
Content-Type: image/png
Content-ID: <logo@x>
Content-Transfer-Encoding: base64

iVBORw0KGgo=
--r
Content-Type: image/png
Content-ID: <unused@x>
Content-Disposition: attachment; filename=u.png
Content-Transfer-Encoding: base64

iVBORw0KGgo=
--r
Content-Type: image/png
Content-Disposition: inline; filename=i.png
Content-Transfer-Encoding: base64

iVBORw0KGgo=
--r--
`))
	var got []string
	for _, a := range p.Attachments {
		got = append(got, fmt.Sprintf("%s cid=%q inline=%v", a.Filename, a.ContentID, a.Inline))
	}
	eq(t, "attachments", got, []string{
		`bijlage.png cid="logo@x" inline=true`,
		`u.png cid="unused@x" inline=false`,
		`i.png cid="" inline=true`,
	})
}

func TestDSNVariants(t *testing.T) {
	report := func(status string) []byte {
		return msg("Content-Type: multipart/report; report-type=delivery-status; boundary=r\n", `--r
Content-Type: text/plain

failed
--r
Content-Type: message/delivery-status

Reporting-MTA: dns; mx.example.org
Original-Envelope-Id: <env-1@example.org>

`+status+`
--r--
`)
	}
	tests := []struct {
		name string
		raw  []byte
		want *mail.DSN
	}{
		{
			name: "envelope id used when no returned message",
			raw:  report("Final-Recipient: rfc822; A@X.example\nAction: Delayed\nStatus: 4.4.1\n\nOriginal-Recipient: rfc822; b@x.example\nAction: failed\nStatus: 5.0.0\nDiagnostic-Code: X-Custom; nope"),
			want: &mail.DSN{OriginalMessageID: "env-1@example.org", Recipients: []mail.DSNRecipient{
				{Address: "a@x.example", Action: "delayed", Status: "4.4.1"},
				{Address: "b@x.example", Action: "failed", Status: "5.0.0", DiagnosticCode: "X-Custom; nope"},
			}},
		},
		{
			name: "no recipient groups",
			raw:  report(""),
			want: &mail.DSN{OriginalMessageID: "env-1@example.org"},
		},
		{
			name: "other report type is not a DSN",
			raw: msg("Content-Type: multipart/report; report-type=disposition-notification; boundary=r\n", `--r
Content-Type: text/plain

read
--r--
`),
			want: nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			eq(t, "DSN", mustParse(t, tt.raw).DSN, tt.want)
		})
	}
}

func nested(depth int) []byte {
	var b strings.Builder
	for i := 0; i < depth; i++ {
		fmt.Fprintf(&b, "Content-Type: multipart/mixed; boundary=b%d\r\n\r\n--b%d\r\n", i, i)
	}
	b.WriteString("Content-Type: text/plain\r\n\r\nleaf\r\n")
	for i := depth - 1; i >= 0; i-- {
		fmt.Fprintf(&b, "--b%d--\r\n", i)
	}
	return []byte(hdr + b.String())
}

func flat(parts int) []byte {
	var b strings.Builder
	b.WriteString("Content-Type: multipart/mixed; boundary=x\r\n\r\n")
	for i := 0; i < parts; i++ {
		b.WriteString("--x\r\nContent-Type: text/plain\r\n\r\np\r\n")
	}
	b.WriteString("--x--\r\n")
	return []byte(hdr + b.String())
}

func TestLimits(t *testing.T) {
	tests := []struct {
		name string
		raw  []byte
		lim  Limits
		want error
	}{
		{"depth at limit", nested(19), DefaultLimits, nil},
		{"depth exceeded", nested(25), DefaultLimits, ErrLimits},
		{"depth custom", nested(5), Limits{MaxDepth: 4}, ErrLimits},
		{"parts at limit", flat(499), DefaultLimits, nil},
		{"parts exceeded", flat(600), DefaultLimits, ErrLimits},
		{"header block too large", []byte(hdr + "X-Big: " + strings.Repeat("a", 300<<10) + "\r\n\r\nbody"), DefaultLimits, ErrLimits},
		{"header without terminator too large", []byte(strings.Repeat("X-A: b\r\n", 50000)), DefaultLimits, ErrLimits},
		{"message too large", msg("", strings.Repeat("a", 2000)), Limits{MaxMessageBytes: 1000}, ErrTooLarge},
		{"message at limit", msg("", "abc"), Limits{MaxMessageBytes: len(msg("", "abc"))}, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p, err := Parse(tt.raw, tt.lim)
			if !errors.Is(err, tt.want) {
				t.Fatalf("err = %v, want %v", err, tt.want)
			}
			if tt.want != nil && p != nil {
				t.Errorf("Parsed must be nil on error, got %+v", p)
			}
			if tt.want == nil && p.MessageID == "" {
				t.Error("empty MessageID")
			}
		})
	}
}

func TestLimitErrorsAreDistinct(t *testing.T) {
	if errors.Is(ErrTooLarge, ErrLimits) || errors.Is(ErrLimits, ErrTooLarge) {
		t.Fatal("sentinels must be distinguishable")
	}
}

func TestZeroLimitsUseDefaults(t *testing.T) {
	p, err := Parse(nested(3), Limits{})
	if err != nil || p.Text != "leaf\r\n" && strings.TrimSpace(p.Text) != "leaf" {
		t.Fatalf("Parse with zero Limits: %v %+v", err, p)
	}
}

func TestEmptyAndGarbageInput(t *testing.T) {
	for _, raw := range []string{"", "\r", "A: b\r", " leading space\r\nA: b\r\n\r\n", "\r\n", "no headers at all", "\x00\x01\x02", ":\r\n\r\n", strings.Repeat("(", 5000)} {
		p, err := Parse([]byte(raw), DefaultLimits)
		if err != nil {
			t.Errorf("Parse(%q): %v", raw, err)
			continue
		}
		if !strings.HasPrefix(p.MessageID, "synthetic-") {
			t.Errorf("Parse(%q): MessageID = %q", raw, p.MessageID)
		}
	}
}
