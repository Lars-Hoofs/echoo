package automation

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"

	"echoo/internal/jobs"
	"echoo/internal/mail/ingest"
	"echoo/internal/storage"
)

// TestIngestedMailFlowsThroughRulesIntoAssignment runs the whole path with the real workers:
// a raw message is parsed and stored by ingest, which queues rules.evaluate in its transaction;
// that job runs the rules, applies the mailbox default SLA policy and assigns an agent.
func TestIngestedMailFlowsThroughRulesIntoAssignment(t *testing.T) {
	f := newFixture(t)
	store, err := storage.NewFS(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	policy := f.id(`INSERT INTO sla_policies (name, first_response_minutes) VALUES ('Snel', 60) RETURNING id`)
	f.exec(`UPDATE mailboxes SET auto_assign_mode = 'round_robin', default_sla_policy_id = $2 WHERE id = $1`, f.mailbox, policy)
	f.rule("Facturen", jobs.TriggerConversationCreated, group("all", cond("subject", "contains", `"factuur"`)),
		actions(addLabel(f.labelBilling), `{"type":"auto_reply","text":"Bedankt, we kijken ernaar."}`), false)
	f.rule("Reactie", jobs.TriggerMessageReceived, allMatch, actions(`{"type":"set_priority","priority":"high"}`), false)
	f.availability(f.agentB, "offline")

	jobsClient := f.e.d.Jobs
	ingestWorker := ingest.NewWorker(ingest.Deps{Pool: f.pool, Store: store, Jobs: jobsClient})
	evaluate := &EvaluateWorker{e: f.e}

	receive := func(id, subject, inReplyTo string) {
		t.Helper()
		headers := []string{
			"Message-ID: <" + id + ">", "From: Jane <jane@acme.example>", "To: support@shop.example",
			"Subject: " + subject, "Date: Mon, 02 Mar 2026 10:00:00 +0000", "Content-Type: text/plain; charset=utf-8",
		}
		if inReplyTo != "" {
			headers = append(headers, "In-Reply-To: <"+inReplyTo+">")
		}
		data := strings.Join(headers, "\r\n") + "\r\n\r\nHallo, mijn factuur klopt niet.\r\n"
		key, sum, err := store.Put(context.Background(), []byte(data))
		if err != nil {
			t.Fatal(err)
		}
		raw := f.id(`INSERT INTO raw_messages (mailbox_id, source, sha256, size_bytes, blob_key, received_at)
			VALUES ($1, 'webhook', $2, $3, $4, now()) RETURNING id`, f.mailbox, sum, len(data), key)
		if err := ingestWorker.Work(context.Background(), &river.Job[jobs.ParseRaw]{Args: jobs.ParseRaw{RawMessageID: raw.String()}}); err != nil {
			t.Fatal(err)
		}
	}
	runQueued := func() int {
		t.Helper()
		rows, err := f.pool.Query(context.Background(), `SELECT id, args FROM river_job WHERE kind = 'rules.evaluate' AND state = 'available' ORDER BY id`)
		if err != nil {
			t.Fatal(err)
		}
		type queued struct {
			id   int64
			args jobs.EvaluateRules
		}
		var list []queued
		for rows.Next() {
			var q queued
			var raw []byte
			if err := rows.Scan(&q.id, &raw); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(raw, &q.args); err != nil {
				t.Fatal(err)
			}
			list = append(list, q)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		for _, q := range list {
			job := &river.Job[jobs.EvaluateRules]{JobRow: &rivertype.JobRow{ID: q.id}, Args: q.args}
			if err := evaluate.Work(context.Background(), job); err != nil {
				t.Fatal(err)
			}
			f.exec(`UPDATE river_job SET state = 'completed', finalized_at = now() WHERE id = $1`, q.id)
		}
		return len(list)
	}

	receive("first@acme.example", "Factuur 2026-113", "")
	if n := runQueued(); n != 1 {
		t.Fatalf("%d rules job(s) after the first message, want 1", n)
	}
	var conv pgtype.UUID
	if err := f.pool.QueryRow(context.Background(), `SELECT id FROM conversations`).Scan(&conv); err != nil {
		t.Fatal(err)
	}
	s := f.state(conv)
	if len(s.Labels) != 1 || s.Labels[0] != "Facturen" || s.Assignee != f.agentA || s.Priority != "none" {
		t.Fatalf("after the first message: %+v (only conversation_created rules run, agent B is offline)", s)
	}
	if r := f.sla(conv); r.Policy != policy || !r.FirstDue.Valid || r.State != "ok" {
		t.Fatalf("sla = %+v", r)
	}
	if f.replies() != 1 {
		t.Fatalf("replies = %d, want the auto reply", f.replies())
	}

	// The auto reply is not a customer message; a real reply triggers message_received.
	receive("second@acme.example", "Re: Factuur 2026-113", "first@acme.example")
	if n := runQueued(); n != 1 {
		t.Fatalf("%d rules job(s) after the reply, want 1", n)
	}
	if got := f.state(conv); got.Priority != "high" || got.Assignee != f.agentA || len(got.Labels) != 1 {
		t.Fatalf("after the reply: %+v", got)
	}
	if f.replies() != 1 {
		t.Fatalf("replies = %d: the rule on new conversations must not answer a reply", f.replies())
	}
	if n := f.count(`SELECT count(*) FROM conversations`); n != 1 {
		t.Fatalf("%d conversations", n)
	}
}
