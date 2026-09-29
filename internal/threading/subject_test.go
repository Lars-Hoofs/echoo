package threading

import "testing"

func TestNormalizeSubject(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"plain", "Offerte kantoorstoelen", "offerte kantoorstoelen"},
		{"empty", "", ""},
		{"whitespace only", " \t ", ""},
		{"Re", "Re: Offerte", "offerte"},
		{"RE upper", "RE: Offerte", "offerte"},
		{"re without space", "re:Offerte", "offerte"},
		{"Fw", "Fw: Offerte", "offerte"},
		{"Fwd", "Fwd: Offerte", "offerte"},
		{"FW upper", "FW: Offerte", "offerte"},
		{"German AW", "AW: Angebot", "angebot"},
		{"German WG", "WG: Angebot", "angebot"},
		{"Dutch Antw", "Antw: Offerte", "offerte"},
		{"Dutch Antwoord", "Antwoord: Offerte", "offerte"},
		{"Dutch Doorst", "Doorst: Offerte", "offerte"},
		{"Dutch Doorgestuurd", "Doorgestuurd: Offerte", "offerte"},
		{"Scandinavian SV", "SV: Tilbud", "tilbud"},
		{"Norwegian VS", "VS: Tilbud", "tilbud"},
		{"French TR", "TR: Devis", "devis"},
		{"French Réf", "Réf: Devis", "devis"},
		{"French Réf upper", "RÉF: Devis", "devis"},
		{"French space before colon", "Re : Devis", "devis"},
		{"Spanish R", "R: Presupuesto", "presupuesto"},
		{"Italian Rif", "Rif: Offerta", "offerta"},
		{"Portuguese Enc", "ENC: Orçamento", "orçamento"},
		{"counted brackets", "Re[2]: Offerte", "offerte"},
		{"counted parens", "Re(3): Offerte", "offerte"},
		{"counted caret", "Re^2: Offerte", "offerte"},
		{"counted forward", "Fwd[2]: Offerte", "offerte"},
		{"full-width colon", "Re： Offerte", "offerte"},
		{"repeated same", "Re: Re: Re: Offerte", "offerte"},
		{"repeated mixed", "AW: Re: SV: Fwd: Offerte", "offerte"},
		{"Outlook mix", "RE: FW: RE: Offerte", "offerte"},
		{"list tag", "[dev-list] Build failure", "build failure"},
		{"list tag then prefix", "[dev-list] Re: Build failure", "build failure"},
		{"prefix then list tag", "Re: [dev-list] Build failure", "build failure"},
		{"nested tags and prefixes", "Re: [a] Re: [b] Build failure", "build failure"},
		{"ticket tag prefix", "[#1234] Printer stuk", "printer stuk"},
		{"Echoo ticket tag prefix", "[Echoo #1234] Re: Printer stuk", "printer stuk"},
		{"ticket tag suffix", "Re: Printer stuk [#1234]", "printer stuk"},
		{"ticket tag middle", "Printer [Echoo #1234] stuk", "printer stuk"},
		{"prefix ticket suffix", "Re: Re: Printer stuk [Echoo #98765]", "printer stuk"},
		{"whitespace collapsed", "  Re:   Printer \t  stuk \n", "printer stuk"},
		{"case folded", "PRINTER Stuk", "printer stuk"},
		{"unicode case folded", "Ärger MIT Größe", "ärger mit größe"},
		{"word Resultaten", "Resultaten Q3", "resultaten q3"},
		{"word Retour", "Retour zending", "retour zending"},
		{"word Rifle", "Rifle: specs", "rifle: specs"},
		{"word Fwd inside", "Offerte Fwd: iets", "offerte fwd: iets"},
		{"Ref without accent kept", "Ref: 12345 offerte", "ref: 12345 offerte"},
		{"Re inside after word", "Care: Offerte", "care: offerte"},
		{"bracket in middle kept", "Offerte [concept] v2", "offerte [concept] v2"},
		{"only prefix", "Re:", ""},
		{"only tag", "[dev-list]", ""},
		{"parenthesised generic", "Re: (geen onderwerp)", "(geen onderwerp)"},
		{"non-numeric count not a prefix", "Re[x]: Offerte", "re[x]: offerte"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := NormalizeSubject(tt.in); got != tt.want {
				t.Errorf("NormalizeSubject(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestNormalizeSubjectIsIdempotent(t *testing.T) {
	for _, s := range []string{"Re: [x] Fwd: Offerte [#12]", "AW: WG: Angebot", "Re[2]: a b c d", "  "} {
		once := NormalizeSubject(s)
		if twice := NormalizeSubject(once); twice != once {
			t.Errorf("NormalizeSubject not idempotent for %q: %q then %q", s, once, twice)
		}
	}
}

func TestFirstPrefix(t *testing.T) {
	tests := []struct {
		in      string
		want    string
		forward bool
	}{
		{"Offerte", "", false},
		{"Re: Offerte", "re", false},
		{"Fwd: Offerte", "fwd", true},
		{"FW: Offerte", "fw", true},
		{"WG: Angebot", "wg", true},
		{"Doorst: Offerte", "doorst", true},
		{"TR: Devis", "tr", true},
		{"VS: Tilbud", "vs", true},
		{"ENC: Orçamento", "enc", true},
		{"[list] Fwd: Offerte", "fwd", true},
		{"[#12] FW: Offerte", "fw", true},
		{"Fwd: Fwd: Offerte", "fwd", true},
		{"Re: Fwd: Offerte", "re", false},
		{"Fwd: Re: Offerte", "fwd", true},
		{"AW: Angebot", "aw", false},
		{"SV: Tilbud", "sv", false},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			_, got := normalizeSubject(tt.in)
			if got != tt.want || forwardPrefixes[got] != tt.forward {
				t.Errorf("normalizeSubject(%q) prefix = %q (forward %v), want %q (forward %v)",
					tt.in, got, forwardPrefixes[got], tt.want, tt.forward)
			}
		})
	}
}

func TestIsGenericSubject(t *testing.T) {
	tests := []struct {
		in   string
		want bool
	}{
		{"", true},
		{"vraag", true},
		{"question", true},
		{"factuur", true},
		{"invoice", true},
		{"contact", true},
		{"info", true},
		{"hallo", true},
		{"hello", true},
		{"hi", true},
		{"test", true},
		{"no subject", true},
		{"(geen onderwerp)", true},
		{"(no subject)", true},
		{"abc", true},
		{"日本", true},
		{"abcd", false},
		{"日本語の件名", false},
		{"factuur 2024-113", false},
		{"printer stuk", false},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			if got := IsGenericSubject(tt.in); got != tt.want {
				t.Errorf("IsGenericSubject(%q) = %v, want %v", tt.in, got, tt.want)
			}
		})
	}
}
