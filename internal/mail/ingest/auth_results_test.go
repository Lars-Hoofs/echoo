package ingest

import (
	"testing"
	"time"

	"echoo/internal/mail/parse"
)

func TestOnlyTheTopMostAuthenticationResultsIsStored(t *testing.T) {
	e := newEnv(t, parse.Limits{})
	e.ingest(inbound("ar1@x", "Jan <jan@klant.example>", "Hallo", "tekst",
		"Authentication-Results: mx.shop.example; spf=fail smtp.mailfrom=klant.example",
		"Authentication-Results: forged.example; spf=pass"), 0)
	e.ingest(inbound("ar2@x", "Jan <jan@klant.example>", "Zonder", "tekst"), time.Minute)

	var got string
	e.scan(&got, `SELECT auth_results FROM messages WHERE message_id_header = 'ar1@x'`)
	if got != "mx.shop.example; spf=fail smtp.mailfrom=klant.example" {
		t.Errorf("auth_results = %q", got)
	}
	e.scan(&got, `SELECT auth_results FROM messages WHERE message_id_header = 'ar2@x'`)
	if got != "" {
		t.Errorf("message without header stored %q", got)
	}
}
