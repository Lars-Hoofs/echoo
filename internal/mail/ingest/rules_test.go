package ingest

import (
	"echoo/internal/mail/parse"
	"testing"
	"time"
)

func TestIngestQueuesRulesInTheSameTransaction(t *testing.T) {
	e := newEnv(t, parse.Limits{})
	rulesJobs := func() (triggers []string) {
		rows, err := e.pool.Query(t.Context(), `SELECT args->>'trigger' FROM river_job WHERE kind = 'rules.evaluate' ORDER BY id`)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		for rows.Next() {
			var trigger string
			if err := rows.Scan(&trigger); err != nil {
				t.Fatal(err)
			}
			triggers = append(triggers, trigger)
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		return triggers
	}

	e.ingest(inbound("r1@acme.example", "Jane <jane@acme.example>", "Order", "Hello"), 0)
	e.ingest(inbound("r2@acme.example", "Jane <jane@acme.example>", "Re: Order", "More", "In-Reply-To: <r1@acme.example>"), time.Minute)
	got := rulesJobs()
	if len(got) != 2 || got[0] != "conversation_created" || got[1] != "message_received" {
		t.Fatalf("queued triggers = %v, want conversation_created then message_received", got)
	}

	// A duplicate stores nothing, so it must not queue anything either.
	e.ingest(inbound("r2@acme.example", "Jane <jane@acme.example>", "Re: Order", "More", "In-Reply-To: <r1@acme.example>"), time.Minute)
	if got := rulesJobs(); len(got) != 2 {
		t.Fatalf("a duplicate queued another job: %v", got)
	}
}
