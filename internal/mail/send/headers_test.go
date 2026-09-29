package send

import (
	"errors"
	"strings"
	"testing"
)

func TestBuildAddsAllowedExtraHeadersInOrder(t *testing.T) {
	m := baseMessage()
	m.Headers = []Header{
		{Name: "List-Unsubscribe", Value: "<https://echoo.test/afmelden/abc>"},
		{Name: "List-Unsubscribe-Post", Value: "List-Unsubscribe=One-Click"},
		{Name: "Precedence", Value: "bulk"},
	}
	raw, err := build(m, counterBoundary())
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	for _, want := range []string{
		"List-Unsubscribe: <https://echoo.test/afmelden/abc>\r\n",
		"List-Unsubscribe-Post: List-Unsubscribe=One-Click\r\n",
		"Precedence: bulk\r\n",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("message lacks %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "Auto-Submitted") {
		t.Error("bulk mail is not an auto-reply and must not carry Auto-Submitted")
	}
}

func TestBuildRefusesOtherOrForgedHeaders(t *testing.T) {
	for name, h := range map[string]Header{
		"routing field":  {Name: "Received", Value: "from evil"},
		"auth results":   {Name: "Authentication-Results", Value: "spf=pass"},
		"injected value": {Name: "Precedence", Value: "bulk\r\nBcc: victim@example.org"},
	} {
		t.Run(name, func(t *testing.T) {
			m := baseMessage()
			m.Headers = []Header{h}
			_, err := build(m, counterBoundary())
			var ve *ValidationError
			if !errors.As(err, &ve) {
				t.Fatalf("got %v, want a validation error", err)
			}
		})
	}
}
