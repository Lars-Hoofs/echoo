package mailflow

import (
	"bytes"
	"context"
	"net/mail"
	"regexp"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"

	"echoo/internal/campaigns"
)

// sendCampaign runs a one-contact campaign through the real dispatcher, send queue and SMTP
// sink, and returns the campaign, the contact and the message as the customer received it.
func (e *env) sendCampaign(svc *campaigns.Service) (campaign, contact pgtype.UUID, msg *mail.Message) {
	e.t.Helper()
	ctx := context.Background()
	var admin, segment pgtype.UUID
	if err := e.pool.QueryRow(ctx, `INSERT INTO users (email, name, role, password_hash) VALUES ('boss@example.com', 'Boss', 'admin', 'x') RETURNING id`).Scan(&admin); err != nil {
		e.t.Fatal(err)
	}
	if err := e.pool.QueryRow(ctx, `INSERT INTO contacts (name, created_by) VALUES ('Jane Customer', $1) RETURNING id`, admin).Scan(&contact); err != nil {
		e.t.Fatal(err)
	}
	e.exec(`INSERT INTO contact_addresses (contact_id, email, is_primary) VALUES ($1, $2, true)`, contact, customerAddr)
	if err := e.pool.QueryRow(ctx, `INSERT INTO contact_segments (name, owner_user_id, shared, filter) VALUES ('All', $1, true, '{"match":"all","conditions":[]}') RETURNING id`, admin).Scan(&segment); err != nil {
		e.t.Fatal(err)
	}
	err := e.pool.QueryRow(ctx, `
		INSERT INTO campaigns (name, mailbox_id, segment_id, segment_name, subject, body_html, status, started_at, created_by, started_by)
		VALUES ('Herfst', $1, $2, 'All', 'Aanbieding voor {{contact.first_name}}', '<p>Hallo {{contact.first_name}}, kijk mee.</p>', 'sending', now(), $3, $3)
		RETURNING id`, e.mailID, segment, admin).Scan(&campaign)
	if err != nil {
		e.t.Fatal(err)
	}
	if err := svc.Tick(ctx); err != nil {
		e.t.Fatal(err)
	}
	waitFor(e.t, "campaign message sent", func() bool {
		return e.count(`SELECT count(*) FROM outbound WHERE status = 'sent'`) == 1
	})
	raws := e.smtp.messages()
	if len(raws) != 1 {
		e.t.Fatalf("SMTP server accepted %d messages, want 1", len(raws))
	}
	msg, err = mail.ReadMessage(bytes.NewReader(raws[0]))
	if err != nil {
		e.t.Fatal(err)
	}
	return campaign, contact, msg
}

func (e *env) campaignService() *campaigns.Service {
	return campaigns.NewService(campaigns.Deps{Pool: e.pool, BaseURL: "https://echoo.test", Jobs: e.river})
}

func TestCampaignMailIsBulkMailAndRepliesThreadIntoItsConversation(t *testing.T) {
	e := newEnv(t)
	e.startSync()
	svc := e.campaignService()
	campaign, contact, msg := e.sendCampaign(svc)

	unsub := msg.Header.Get("List-Unsubscribe")
	link := regexp.MustCompile(`^<(https://echoo\.test/afmelden/[A-Za-z0-9_-]+)>$`).FindStringSubmatch(unsub)
	if link == nil {
		t.Fatalf("List-Unsubscribe = %q, want one https link", unsub)
	}
	if got := msg.Header.Get("List-Unsubscribe-Post"); got != "List-Unsubscribe=One-Click" {
		t.Errorf("List-Unsubscribe-Post = %q", got)
	}
	if got := msg.Header.Get("Precedence"); got != "bulk" {
		t.Errorf("Precedence = %q, want bulk", got)
	}
	if got := msg.Header.Get("Auto-Submitted"); got != "" {
		t.Errorf("Auto-Submitted = %q: a campaign is not an auto-reply", got)
	}
	if got := msg.Header.Get("Subject"); got != "Aanbieding voor Jane" {
		t.Errorf("Subject = %q", got)
	}
	var body bytes.Buffer
	if _, err := body.ReadFrom(msg.Body); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(body.String(), "afmelden") {
		t.Error("the message body has no unsubscribe link")
	}

	var conv pgtype.UUID
	if err := e.pool.QueryRow(context.Background(), `SELECT conversation_id FROM campaign_recipients WHERE campaign_id = $1`, campaign).Scan(&conv); err != nil {
		t.Fatal(err)
	}
	e.deliver(customerMail("cust-reply@customer.example", "Re: Aanbieding voor Jane", "Interessant, stuur meer.\r\n",
		"In-Reply-To: "+msg.Header.Get("Message-Id"), "References: "+msg.Header.Get("Message-Id")))
	e.waitIngested(1)

	if got := e.conversationOf("cust-reply@customer.example"); got != conv {
		t.Fatalf("reply landed in %s, want the campaign conversation %s", got.String(), conv.String())
	}
	var status string
	if err := e.pool.QueryRow(context.Background(), `SELECT status FROM conversations WHERE id = $1`, conv).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "open" {
		t.Errorf("conversation status = %s, want open: a reply reopens the closed campaign conversation", status)
	}
	if got := e.count(`SELECT count(*) FROM conversations`); got != 1 {
		t.Errorf("conversations = %d, want 1", got)
	}

	if err := svc.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	var campaignStatus, state string
	err := e.pool.QueryRow(context.Background(), `SELECT c.status, r.state FROM campaigns c JOIN campaign_recipients r ON r.campaign_id = c.id WHERE c.id = $1`, campaign).Scan(&campaignStatus, &state)
	if err != nil {
		t.Fatal(err)
	}
	if campaignStatus != "done" || state != "sent" {
		t.Errorf("campaign = %s recipient = %s, want done and sent", campaignStatus, state)
	}

	token := link[1][strings.LastIndex(link[1], "/")+1:]
	if _, err := svc.Unsubscribe(context.Background(), token); err != nil {
		t.Fatalf("the link in the delivered mail does not unsubscribe: %v", err)
	}
	if got := e.count(`SELECT count(*) FROM contacts WHERE id = $1 AND unsubscribed_at IS NOT NULL`, contact); got != 1 {
		t.Error("contact is not unsubscribed")
	}
}

func TestHardBounceOfCampaignMailMarksTheAddress(t *testing.T) {
	e := newEnv(t)
	e.startSync()
	svc := e.campaignService()
	campaign, _, msg := e.sendCampaign(svc)

	e.deliver(dsnFor(msg.Header.Get("Message-Id")))
	e.waitIngested(1)
	waitFor(t, "address marked as bouncing", func() bool {
		return e.count(`SELECT count(*) FROM contact_addresses WHERE email = $1 AND bounced_at IS NOT NULL`, customerAddr) == 1
	})
	var state, errText string
	if err := e.pool.QueryRow(context.Background(), `SELECT state, error FROM campaign_recipients WHERE campaign_id = $1`, campaign).Scan(&state, &errText); err != nil {
		t.Fatal(err)
	}
	if state != "failed" || !strings.HasPrefix(errText, "bounced:") {
		t.Errorf("recipient = %s %q, want failed with the bounce", state, errText)
	}

	e.exec(`INSERT INTO campaigns (name, mailbox_id, segment_id, segment_name, subject, body_html, status, started_at, created_by, started_by)
		SELECT 'Winter', mailbox_id, segment_id, segment_name, subject, body_html, 'sending', now(), created_by, started_by FROM campaigns WHERE id = $1`, campaign)
	if err := svc.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := e.count(`SELECT count(*) FROM campaign_recipients r JOIN campaigns c ON c.id = r.campaign_id WHERE c.name = 'Winter' AND r.state = 'skipped' AND r.skip_reason = 'bounced'`); got != 1 {
		t.Errorf("the next campaign skipped %d bounced recipients, want 1", got)
	}
	if got := len(e.smtp.messages()); got != 1 {
		t.Errorf("SMTP accepted %d messages, want no second mail to a bouncing address", got)
	}
}
