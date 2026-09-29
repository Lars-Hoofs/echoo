package automation

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"

	"echoo/internal/compose"
	"echoo/internal/db/dbq"
	"echoo/internal/mail"
	"echoo/internal/mail/send"
)

// noReplyLocal matches local parts of addresses that never read mail.
var noReplyLocal = regexp.MustCompile(`^(no[-_.]?reply|do[-_.]?not[-_.]?reply|mailer-daemon|postmaster|bounces?)$`)

// autoReply sends the canned response or inline text to the sender of the latest inbound
// message. It returns a reason instead of sending when a suppression rule applies: automatic
// mail is never answered automatically, and one rule answers one address once per 24 hours.
func (e *Engine) autoReply(ctx context.Context, rc *runContext, a Action) (skipped string, err error) {
	if !rc.ruleID.Valid {
		return "", errors.New("auto reply needs a rule")
	}
	// Earlier actions of the same rule may have changed the conversation, for instance to spam.
	conv, err := e.q.AutoGetConversation(ctx, rc.conv.ID)
	if err != nil {
		return "", fmt.Errorf("load conversation: %w", err)
	}
	rc.conv = conv
	msg, err := e.q.AutoLatestInbound(ctx, dbq.AutoLatestInboundParams{ConversationID: rc.conv.ID, MessageID: rc.messageID})
	if errors.Is(err, pgx.ErrNoRows) {
		return "no inbound message", nil
	}
	if err != nil {
		return "", fmt.Errorf("load message: %w", err)
	}
	to := strings.ToLower(strings.TrimSpace(msg.FromAddr))
	local, _, _ := strings.Cut(to, "@")
	switch {
	case rc.conv.Status == "spam":
		return "conversation is spam", nil
	case msg.AutoSubmitted:
		return "message is auto-submitted", nil
	case msg.IsBulk:
		return "message is bulk or list mail", nil
	case to == "" || !strings.Contains(to, "@") || noReplyLocal.MatchString(local):
		return "sender does not accept replies", nil
	}
	own, err := e.q.ComposeMailboxAddresses(ctx)
	if err != nil {
		return "", fmt.Errorf("list mailbox addresses: %w", err)
	}
	if slices.Contains(own, to) {
		return "sender is one of our mailboxes", nil
	}
	subject, text, htmlBody, err := e.replyContent(ctx, rc.conv, msg, a)
	if err != nil {
		return "", err
	}
	client, err := e.client(ctx)
	if err != nil {
		return "", err
	}
	params := send.EnqueueParams{
		ConversationID: rc.conv.ID, MailboxID: rc.conv.MailboxID, AutoReplied: true,
		To:      []mail.Address{{Name: msg.FromName, Address: to}},
		Subject: subject, Text: text, HTML: htmlBody,
		InReplyTo: msg.MessageIDHeader, References: append(slices.Clone(msg.ReferencesHdr), msg.MessageIDHeader),
	}
	sent := false
	err = e.withTx(ctx, func(tx pgx.Tx) error {
		q := dbq.New(tx)
		n, err := q.AutoClaimReply(ctx, dbq.AutoClaimReplyParams{RuleID: rc.ruleID, Address: to})
		if err != nil {
			return fmt.Errorf("claim reply: %w", err)
		}
		if n == 0 {
			return nil
		}
		if params.IdempotencyKey, err = q.NewUUID(ctx); err != nil {
			return fmt.Errorf("new idempotency key: %w", err)
		}
		if _, err := send.Enqueue(ctx, tx, client, params); err != nil {
			return fmt.Errorf("enqueue auto reply: %w", err)
		}
		sent = true
		return nil
	})
	if err != nil {
		return "", err
	}
	if !sent {
		return "already replied to this address in the last 24 hours", nil
	}
	return "", nil
}

func (e *Engine) withTx(ctx context.Context, fn func(tx pgx.Tx) error) error {
	return pgx.BeginFunc(ctx, e.d.Pool, fn)
}

func dropUnresolved(s string, names []string) string {
	for _, n := range names {
		s = regexp.MustCompile(`\{\{\s*`+regexp.QuoteMeta(n)+`\s*\}\}`).ReplaceAllString(s, "")
	}
	return s
}

// replyContent renders the subject, plain text and HTML of the reply. Variables that cannot
// be resolved are removed rather than sent as {{placeholders}}.
func (e *Engine) replyContent(ctx context.Context, conv dbq.AutoGetConversationRow, msg dbq.AutoLatestInboundRow, a Action) (subject, text, htmlBody string, err error) {
	vars, err := e.variables(ctx, conv, msg)
	if err != nil {
		return "", "", "", err
	}
	subject = a.Subject
	body := a.Text
	fromTemplate := false
	if a.TemplateID != "" {
		id, _ := parseID(a.TemplateID)
		tpl, err := e.q.ComposeGetTemplate(ctx, id)
		if errors.Is(err, pgx.ErrNoRows) {
			return "", "", "", errors.New("canned response no longer exists")
		}
		if err != nil {
			return "", "", "", fmt.Errorf("load canned response: %w", err)
		}
		// Whoever wrote the rule was checked against the template then; it may have changed scope
		// since, and a personal or foreign one must never be sent from this mailbox.
		if tpl.Scope != "global" && (tpl.Scope != "mailbox" || tpl.MailboxID != conv.MailboxID) {
			return "", "", "", errors.New("canned response is not available for this mailbox")
		}
		subject, body, fromTemplate = tpl.Subject, tpl.BodyHtml, true
	}
	var missing []string
	subject, missing = vars.RenderText(subject)
	subject = strings.TrimSpace(dropUnresolved(subject, missing))
	if subject == "" {
		subject = replySubject(conv.Subject)
	}
	if fromTemplate {
		rendered, missing := vars.Render(body)
		htmlBody = compose.Sanitize(dropUnresolved(rendered, missing), compose.SanitizeOptions{})
	} else {
		rendered, missing := vars.RenderText(body)
		htmlBody = compose.Sanitize(textToHTML(dropUnresolved(rendered, missing)), compose.SanitizeOptions{})
	}
	if compose.IsEmpty(htmlBody) {
		return "", "", "", errors.New("reply is empty")
	}
	return subject, compose.TextFromHTML(htmlBody), htmlBody, nil
}

func replySubject(subject string) string {
	if len(subject) >= 3 && strings.EqualFold(subject[:3], "re:") {
		return subject
	}
	return "Re: " + subject
}

func (e *Engine) variables(ctx context.Context, conv dbq.AutoGetConversationRow, msg dbq.AutoLatestInboundRow) (compose.Variables, error) {
	v := compose.Variables{
		ContactName: msg.FromName, ContactEmail: msg.FromAddr, MailboxName: conv.MailboxName,
		ConversationNumber: fmt.Sprint(conv.Number), AgentName: conv.MailboxDisplayName,
	}
	if v.AgentName == "" {
		v.AgentName = conv.MailboxName
	}
	if conv.ContactID.Valid {
		name, err := e.q.AutoContactName(ctx, conv.ContactID)
		if err != nil {
			return v, fmt.Errorf("load contact: %w", err)
		}
		if name != "" {
			v.ContactName = name
		}
	}
	if conv.AssigneeUserID.Valid {
		name, err := e.q.AutoUserName(ctx, conv.AssigneeUserID)
		if err != nil {
			return v, fmt.Errorf("load assignee: %w", err)
		}
		v.AgentName = name
	}
	return v, nil
}
