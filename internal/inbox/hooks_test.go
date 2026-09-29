package inbox

import (
	"context"
	"reflect"
	"slices"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"

	"echoo/internal/jobs"
	"echoo/internal/webhooks"
)

func (f *fixture) webhookEventTypes() []string {
	f.t.Helper()
	rows, err := f.pool.Query(context.Background(), `SELECT type FROM webhook_events ORDER BY id`)
	if err != nil {
		f.t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var typ string
		if err := rows.Scan(&typ); err != nil {
			f.t.Fatal(err)
		}
		out = append(out, typ)
	}
	if err := rows.Err(); err != nil {
		f.t.Fatal(err)
	}
	return out
}

func TestChangesAreRecordedForWebhooks(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()
	f.exec(`INSERT INTO webhooks (url, secret_enc, events) VALUES ('https://example.com/hook', '\x00', $1)`,
		[]string{webhooks.ConversationUpdated, webhooks.ConversationStatusChanged, webhooks.ConversationAssigned})

	if _, err := f.svc.SetStatus(ctx, f.actor, f.conv, nil, StatusClosed); err != nil {
		t.Fatal(err)
	}
	if got, want := f.webhookEventTypes(), []string{webhooks.ConversationUpdated, webhooks.ConversationStatusChanged}; !slices.Equal(got, want) {
		t.Fatalf("after status change: %v, want %v", got, want)
	}

	if _, err := f.svc.Assign(ctx, f.actor, f.conv, nil, f.writer); err != nil {
		t.Fatal(err)
	}
	if got, want := f.webhookEventTypes()[2:], []string{webhooks.ConversationUpdated, webhooks.ConversationAssigned}; !slices.Equal(got, want) {
		t.Fatalf("after assignment: %v, want %v", got, want)
	}

	if _, err := f.svc.SetPriority(ctx, f.actor, f.conv, nil, "high"); err != nil {
		t.Fatal(err)
	}
	if got := f.webhookEventTypes()[4:]; !slices.Equal(got, []string{webhooks.ConversationUpdated}) {
		t.Fatalf("after priority change: %v", got)
	}

	before := len(f.webhookEventTypes())
	if _, err := f.svc.SetPriority(ctx, f.actor, f.conv, nil, "high"); err != nil {
		t.Fatal(err)
	}
	if got := len(f.webhookEventTypes()); got != before {
		t.Fatalf("a no-op change recorded %d event(s)", got-before)
	}
}

func TestNoWebhookEventWithoutSubscriber(t *testing.T) {
	f := newFixture(t)
	if _, err := f.svc.SetStatus(t.Context(), f.actor, f.conv, nil, StatusClosed); err != nil {
		t.Fatal(err)
	}
	if got := f.webhookEventTypes(); len(got) != 0 {
		t.Fatalf("events without subscriber: %v", got)
	}
}

type recordingEnqueuer struct{ args []river.JobArgs }

func (r *recordingEnqueuer) InsertTx(_ context.Context, _ pgx.Tx, args river.JobArgs, _ *river.InsertOpts) (*rivertype.JobInsertResult, error) {
	r.args = append(r.args, args)
	return &rivertype.JobInsertResult{}, nil
}

func TestHumanChangesQueueRules(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()
	rec := &recordingEnqueuer{}
	svc := f.svc.WithJobs(rec)
	want := jobs.EvaluateRules{ConversationID: f.conv.String(), Trigger: jobs.TriggerConversationUpdated}

	if _, err := svc.SetStatus(ctx, f.actor, f.conv, nil, StatusWaiting); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(rec.args, []river.JobArgs{want}) {
		t.Fatalf("queued %v, want one %v", rec.args, want)
	}

	rec.args = nil
	if _, err := svc.Assign(ctx, f.actor, f.conv, nil, f.writer); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.AddLabels(ctx, f.actor, f.conv, nil, []pgtype.UUID{f.label("Billing")}); err != nil {
		t.Fatal(err)
	}
	if len(rec.args) != 2 {
		t.Fatalf("assignment and label change queued %d jobs, want 2", len(rec.args))
	}

	rec.args = nil
	if _, err := svc.SetPriority(ctx, f.actor, f.conv, nil, "high"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.SetStatus(ctx, f.actor, f.conv, nil, StatusWaiting); err != nil {
		t.Fatal(err)
	}
	if len(rec.args) != 0 {
		t.Fatalf("priority change and no-op status change queued %v", rec.args)
	}
}

func TestRuleAndSystemChangesDoNotQueueRules(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()
	rec := &recordingEnqueuer{}
	svc := f.svc.WithJobs(rec)
	rule := f.id(`INSERT INTO rules (name, trigger, conditions, actions, position) VALUES ('r', 'message_received', '{}', '[]', 1) RETURNING id`)

	actor := f.actor
	actor.UserID = pgtype.UUID{}
	actor.Source, actor.RuleID = SourceRule, rule
	if _, err := svc.SetStatus(ctx, actor, f.conv, nil, StatusWaiting); err != nil {
		t.Fatal(err)
	}
	actor.Source, actor.RuleID = SourceSystem, pgtype.UUID{}
	if _, err := svc.SetStatus(ctx, actor, f.conv, nil, StatusClosed); err != nil {
		t.Fatal(err)
	}
	if len(rec.args) != 0 {
		t.Fatalf("queued %v", rec.args)
	}

	rows, err := f.pool.Query(ctx, `SELECT type, actor_user_id IS NULL, data->>'source', COALESCE(data->>'rule_id', '') FROM conversation_events WHERE conversation_id = $1 ORDER BY created_at, id`, f.conv)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	type row struct {
		typ         string
		noActor     bool
		source, rid string
	}
	var got []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.typ, &r.noActor, &r.source, &r.rid); err != nil {
			t.Fatal(err)
		}
		got = append(got, r)
	}
	want := []row{{"status_changed", true, "rule", rule.String()}, {"resolved", true, "system", ""}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("events %v, want %v", got, want)
	}
}
