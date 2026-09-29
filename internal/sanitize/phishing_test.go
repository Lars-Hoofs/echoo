package sanitize

import (
	"reflect"
	"testing"
)

func kinds(ws []Warning) []string {
	out := []string{}
	for _, w := range ws {
		out = append(out, w.Kind)
	}
	return out
}

func TestAnalyze(t *testing.T) {
	mailbox := Sender{MailboxAddr: "support@acme.example", MailboxName: "Acme Support"}
	with := func(f func(*Sender)) Sender {
		s := mailbox
		s.FromAddr = "klant@klant.example"
		s.FromName = "Klant"
		f(&s)
		return s
	}
	cases := []struct {
		name string
		in   Sender
		want []string
		det  string
	}{
		{"clean", with(func(*Sender) {}), []string{}, ""},
		{"address in name at other domain", with(func(s *Sender) {
			s.FromName = "billing@bank.example"
			s.FromAddr = "x@evil.test"
		}), []string{KindDisplayNameSpoof}, "billing@bank.example"},
		{"address in name at same domain", with(func(s *Sender) {
			s.FromName = "Jan (jan@klant.example)"
		}), []string{}, ""},
		{"own domain in name", with(func(s *Sender) {
			s.FromName = "IT acme.example"
			s.FromAddr = "x@evil.test"
		}), []string{KindDisplayNameSpoof}, "acme.example"},
		{"own domain in name, lookalike suffix is not own domain", with(func(s *Sender) {
			s.FromName = "IT notacme.example"
			s.FromAddr = "x@evil.test"
		}), []string{}, ""},
		{"own display name from outside", with(func(s *Sender) {
			s.FromName = "acme support"
			s.FromAddr = "x@evil.test"
		}), []string{KindDisplayNameSpoof}, "acme.example"},
		{"own display name from own domain", with(func(s *Sender) {
			s.FromName = "Acme Support"
			s.FromAddr = "collega@acme.example"
		}), []string{}, ""},
		{"reply-to other domain", with(func(s *Sender) {
			s.ReplyTo = []string{"reply@other.test"}
		}), []string{KindReplyToMismatch}, "other.test"},
		{"reply-to subdomain is fine", with(func(s *Sender) {
			s.ReplyTo = []string{"reply@mail.klant.example"}
		}), []string{}, ""},
		{"reply-to same domain", with(func(s *Sender) {
			s.ReplyTo = []string{"other@klant.example"}
		}), []string{}, ""},
		{"punycode", with(func(s *Sender) {
			s.FromAddr = "x@xn--pypal-4ve.example"
		}), []string{KindPunycodeDomain, KindMixedScript}, "p\u0430ypal.example"},
		{"mixed script raw unicode", with(func(s *Sender) {
			s.FromAddr = "x@pаypal.example"
		}), []string{KindMixedScript}, "pаypal.example"},
		{"single non-latin script is not mixed", with(func(s *Sender) {
			s.FromAddr = "x@пример.example"
		}), []string{}, ""},
		{"cjk scripts count as one", with(func(s *Sender) {
			s.FromAddr = "x@日本語のドメイン.example"
		}), []string{}, ""},
		{"spf fail", with(func(s *Sender) {
			s.AuthResults = "mx.acme.example; spf=fail smtp.mailfrom=klant.example; dkim=pass header.d=klant.example"
		}), []string{KindAuthFailed}, "spf"},
		{"dkim and dmarc fail", with(func(s *Sender) {
			s.AuthResults = "mx.acme.example; dkim=fail; dmarc=FAIL (p=reject)"
		}), []string{KindAuthFailed}, "dkim, dmarc"},
		{"pass everywhere", with(func(s *Sender) {
			s.AuthResults = "mx.acme.example; spf=pass; dkim=pass; dmarc=pass"
		}), []string{}, ""},
		{"fail inside a comment is ignored", with(func(s *Sender) {
			s.AuthResults = "mx.acme.example; spf=pass (was: spf=fail before forwarding)"
		}), []string{}, ""},
		{"no header", with(func(s *Sender) {}), []string{}, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := Analyze(c.in)
			if !reflect.DeepEqual(kinds(got), c.want) {
				t.Fatalf("kinds = %v, want %v (%+v)", kinds(got), c.want, got)
			}
			if c.det != "" && got[0].Detail != c.det {
				t.Fatalf("detail = %q, want %q", got[0].Detail, c.det)
			}
		})
	}
}

func TestLinkWarningsCapped(t *testing.T) {
	var in []LinkMismatch
	for i := 0; i < 10; i++ {
		in = append(in, LinkMismatch{Shown: "a.example", Actual: "b.test"})
	}
	if got := LinkWarnings(in); len(got) != 3 || got[0].Kind != KindLinkMismatch {
		t.Fatalf("got %v", got)
	}
}
