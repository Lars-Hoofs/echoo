package parse

import (
	"strings"
	"testing"
	"time"

	"github.com/emersion/go-message"
	"github.com/emersion/go-message/textproto"

	"echoo/internal/mail"
)

func TestExtractIDs(t *testing.T) {
	tests := []struct {
		name, in string
		want     []string
	}{
		{"empty", "", nil},
		{"single", "<a@b.example>", []string{"a@b.example"}},
		{"domain lower-cased, local part kept", "<Ab.CD@Example.ORG>", []string{"Ab.CD@example.org"}},
		{"folded", "<a@x.example>\r\n\t<b@x.example>\r\n <c@x.example>", []string{"a@x.example", "b@x.example", "c@x.example"}},
		{"commas", "<a@x.example>, <b@x.example>,<c@x.example>", []string{"a@x.example", "b@x.example", "c@x.example"}},
		{"missing brackets", "a@x.example b@x.example", []string{"a@x.example", "b@x.example"}},
		{"garbage between ids", "<a@x.example> see also (comment@ignored) blah <b@x.example> ###", []string{"a@x.example", "b@x.example"}},
		{"unterminated bracket", "<a@x.example> <b@x.example", []string{"a@x.example", "b@x.example"}},
		{"spaces inside brackets", "< a@x.example >", []string{"a@x.example"}},
		{"double bracket", "<<a@x.example>", []string{"a@x.example"}},
		{"duplicates dropped", "<a@x.example> <b@x.example> <a@x.example>", []string{"a@x.example", "b@x.example"}},
		{"no at sign", "<nonsense>", nil},
		{"empty brackets", "<>", nil},
		{"only garbage", "no reference here", nil},
		{"unterminated comment", "(oops <a@x.example>", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			eq(t, "ids", extractIDs(tt.in), tt.want)
		})
	}
}

func TestParseAddresses(t *testing.T) {
	tests := []struct {
		name string
		in   []string
		want []mail.Address
	}{
		{"none", nil, nil},
		{"plain", []string{"Jan@Example.ORG"}, []mail.Address{{Address: "jan@example.org"}}},
		{"quoted name with comma", []string{`"Jansen, Jan" <jan@example.org>`}, []mail.Address{{Name: "Jansen, Jan", Address: "jan@example.org"}}},
		{"encoded name", []string{"=?ISO-8859-1?Q?Andr=E9?= <a@example.org>"}, []mail.Address{{Name: "André", Address: "a@example.org"}}},
		{"unknown charset in name", []string{"=?x-unknown?Q?caf=E9?= <a@example.org>"}, []mail.Address{{Name: "café", Address: "a@example.org"}}},
		{"multiple headers", []string{"a@example.org", "b@example.org"}, []mail.Address{{Address: "a@example.org"}, {Address: "b@example.org"}}},
		{"bad entry is dropped", []string{"a@example.org, not an address, b@example.org"}, []mail.Address{{Address: "a@example.org"}, {Address: "b@example.org"}}},
		{"outlook semicolons", []string{`"A" <a@example.org>; "B" <b@example.org>`}, []mail.Address{{Name: "A", Address: "a@example.org"}, {Name: "B", Address: "b@example.org"}}},
		{"comma inside quotes survives salvage", []string{`"X, Y" <x@example.org>, broken@, z@example.org`}, []mail.Address{{Name: "X, Y", Address: "x@example.org"}, {Address: "z@example.org"}}},
		{"comment", []string{"jan@example.org (Jan)"}, []mail.Address{{Name: "Jan", Address: "jan@example.org"}}},
		{"group", []string{"Team: a@example.org, b@example.org;"}, []mail.Address{{Address: "a@example.org"}, {Address: "b@example.org"}}},
		{"empty group", []string{"undisclosed-recipients:;"}, []mail.Address{}},
		{"all garbage", []string{"<<>> ,, @@"}, []mail.Address{}},
		{"control characters from encoded word", []string{"=?utf-8?Q?a=00b?= <a@example.org>"}, []mail.Address{{Name: "a b", Address: "a@example.org"}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseAddresses(tt.in)
			if len(got) == 0 && len(tt.want) == 0 {
				return
			}
			eq(t, "addresses", got, tt.want)
		})
	}
}

func TestParseDate(t *testing.T) {
	tests := []struct {
		name, in string
		want     time.Time
	}{
		{"rfc5322", "Tue, 3 Mar 2026 18:13:41 +0100", date(2026, 3, 3, 17, 13, 41)},
		{"with comment", "Tue, 3 Mar 2026 18:13:41 +0100 (CET)", date(2026, 3, 3, 17, 13, 41)},
		{"no weekday", "3 Mar 2026 18:13:41 +0100", date(2026, 3, 3, 17, 13, 41)},
		{"two-digit year", "Tue, 3 Mar 26 18:13:41 +0000", date(2026, 3, 3, 18, 13, 41)},
		{"gmt offset", "Tue, 3 Mar 2026 18:13:41 GMT+0100", date(2026, 3, 3, 17, 13, 41)},
		{"utc name", "Tue, 3 Mar 2026 18:13:41 UTC", date(2026, 3, 3, 18, 13, 41)},
		{"extra whitespace", "Tue,  3   Mar 2026  18:13:41  +0100", date(2026, 3, 3, 17, 13, 41)},
		{"no seconds", "Tue, 3 Mar 2026 18:13 +0100", date(2026, 3, 3, 17, 13, 0)},
		{"iso 8601", "2026-03-03T18:13:41+01:00", date(2026, 3, 3, 17, 13, 41)},
		{"iso with space", "2026-03-03 18:13:41 +0100", date(2026, 3, 3, 17, 13, 41)},
		{"ctime", "Tue Mar 3 18:13:41 2026", date(2026, 3, 3, 18, 13, 41)},
		{"empty", "", time.Time{}},
		{"garbage", "gisteren rond lunchtijd", time.Time{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseDate(tt.in)
			if !got.Equal(tt.want) {
				t.Errorf("parseDate(%q) = %v, want %v", tt.in, got, tt.want)
			}
			if !got.IsZero() && got.Location() != time.UTC {
				t.Errorf("location = %v, want UTC", got.Location())
			}
		})
	}
}

func header(kv ...string) message.Header {
	var h textproto.Header
	for i := 0; i < len(kv); i += 2 {
		h.Add(kv[i], kv[i+1])
	}
	return message.Header{Header: h}
}

func TestAutoSubmittedAndBulk(t *testing.T) {
	tests := []struct {
		name string
		h    message.Header
		auto bool
		bulk bool
	}{
		{"none", header("Subject", "x"), false, false},
		{"auto-submitted no", header("Auto-Submitted", "no"), false, false},
		{"auto-submitted No with comment", header("Auto-Submitted", "No; foo"), false, false},
		{"auto-replied", header("Auto-Submitted", "auto-replied"), true, false},
		{"auto-generated", header("Auto-Submitted", "auto-generated; x"), true, false},
		{"x-autoreply", header("X-Autoreply", "yes"), true, false},
		{"x-autorespond", header("X-Autorespond", "1"), true, false},
		{"precedence auto_reply", header("Precedence", "auto_reply"), true, false},
		{"precedence bulk", header("Precedence", "Bulk"), false, true},
		{"precedence list", header("Precedence", "list"), false, true},
		{"precedence junk", header("Precedence", "junk"), false, true},
		{"precedence first-class", header("Precedence", "first-class"), false, false},
		{"list-unsubscribe", header("List-Unsubscribe", "<mailto:u@example.org>"), false, true},
		{"list-id alone is not bulk", header("List-Id", "<l.example.org>"), false, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			eq(t, "AutoSubmitted", isAutoSubmitted(tt.h), tt.auto)
			eq(t, "Bulk", isBulk(tt.h), tt.bulk)
		})
	}
}

func TestListID(t *testing.T) {
	tests := []struct{ in, want string }{
		{"", ""},
		{"<dev.lists.example.org>", "dev.lists.example.org"},
		{"Dev list <dev.lists.example.org>", "dev.lists.example.org"},
		{`"Dev <x> list" <dev.lists.example.org>`, "dev.lists.example.org"},
		{"dev.lists.example.org", "dev.lists.example.org"},
		{"<unterminated.example.org", "<unterminated.example.org"},
	}
	for _, tt := range tests {
		eq(t, "listID("+tt.in+")", listID(tt.in), tt.want)
	}
}

func TestDecodeHeaderText(t *testing.T) {
	tests := []struct{ name, in, want string }{
		{"plain", "Hello", "Hello"},
		{"q-encoded utf-8", "=?UTF-8?Q?caf=C3=A9?=", "café"},
		{"b-encoded", "=?utf-8?B?Y2Fmw6k=?=", "café"},
		{"mixed charsets", "=?ISO-8859-1?Q?caf=E9?= =?UTF-8?B?4oCU?= =?windows-1252?Q?=93x=94?=", "café—“x”"},
		{"unknown charset", "=?x-unknown?Q?caf=E9?=", "café"},
		{"raw 8-bit latin-1", "caf\xe9", "café"},
		{"control characters", "a\x00b\rc", "a b c"},
		{"invalid encoded word stays", "=?utf-8?B?!!!?= rest", "=?utf-8?B?!!!?= rest"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			eq(t, "decoded", decodeHeaderText(tt.in), tt.want)
		})
	}
}

func TestMultipleAuthenticationResultsKeepOrder(t *testing.T) {
	raw := "Authentication-Results: first.example; spf=pass\r\nAuthentication-Results: second.example; dkim=fail\r\n" + hdr + "\r\nx"
	p := mustParse(t, []byte(raw))
	eq(t, "AuthResults", p.AuthResults, []string{"first.example; spf=pass", "second.example; dkim=fail"})
}

func TestMessageIDVariants(t *testing.T) {
	tests := []struct{ name, header, want string }{
		{"brackets", "<a@X.example>", "a@x.example"},
		{"no brackets", "a@X.example", "a@x.example"},
		{"trailing comment", "<a@x.example> (added by relay)", "a@x.example"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := mustParse(t, []byte("From: a@example.org\r\nMessage-ID: "+tt.header+"\r\n\r\nx"))
			eq(t, "MessageID", p.MessageID, tt.want)
		})
	}
	t.Run("second header used when first is invalid", func(t *testing.T) {
		p := mustParse(t, []byte("Message-ID: garbage\r\nMessage-ID: <ok@x.example>\r\n\r\nx"))
		eq(t, "MessageID", p.MessageID, "ok@x.example")
	})
	t.Run("synthetic is deterministic and depends on content", func(t *testing.T) {
		a := mustParse(t, []byte("Subject: a\r\n\r\nx"))
		b := mustParse(t, []byte("Subject: a\r\n\r\nx"))
		c := mustParse(t, []byte("Subject: b\r\n\r\nx"))
		eq(t, "same input", a.MessageID, b.MessageID)
		if a.MessageID == c.MessageID {
			t.Error("different messages share a synthetic ID")
		}
		if !strings.HasSuffix(a.MessageID, "@echoo.invalid") {
			t.Errorf("MessageID = %q", a.MessageID)
		}
	})
}
