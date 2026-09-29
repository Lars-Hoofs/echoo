package automation

import (
	"context"
	"errors"
	"fmt"
	"html"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"echoo/internal/db/dbq"
	"echoo/internal/inbox"
	"echoo/internal/realtime"
	"echoo/internal/webhooks"
)

// runContext is one conversation an action list runs on. ruleID is invalid for macros.
type runContext struct {
	conv      dbq.AutoGetConversationRow
	actor     inbox.Actor
	ruleID    pgtype.UUID
	label     string
	messageID pgtype.UUID
}

// runActions runs every action in order. One failing action does not stop the others; each
// outcome is returned so it can be stored and shown.
func (e *Engine) runActions(ctx context.Context, rc *runContext, actions []Action) actionResults {
	out := make(actionResults, 0, len(actions))
	for _, a := range actions {
		res := ActionResult{Type: a.Type, Result: resultApplied}
		skipped, err := e.runAction(ctx, rc, a)
		switch {
		case err != nil:
			res.Result, res.Detail = resultFailed, err.Error()
		case skipped != "":
			res.Result, res.Detail = resultSkipped, skipped
		}
		out = append(out, res)
		if ctx.Err() != nil {
			break
		}
	}
	return out
}

// runAction returns a reason when the action was skipped on purpose.
func (e *Engine) runAction(ctx context.Context, rc *runContext, a Action) (skipped string, err error) {
	id := rc.conv.ID
	switch a.Type {
	case ActionAssignAgent:
		user, _ := parseID(a.UserID)
		_, err = e.inbox.Assign(ctx, rc.actor, id, nil, user)
	case ActionAssignTeam:
		team, _ := parseID(a.TeamID)
		_, err = e.inbox.AssignTeam(ctx, rc.actor, id, nil, team)
	case ActionAssignRoundRobin:
		team, _ := parseID(a.TeamID)
		var ok bool
		ok, err = e.assignBest(ctx, rc.actor, id, rc.conv.MailboxID, team, false)
		if err == nil && !ok {
			skipped = "no agent available"
		}
	case ActionSetPriority:
		_, err = e.inbox.SetPriority(ctx, rc.actor, id, nil, a.Priority)
	case ActionAddLabel:
		label, _ := parseID(a.LabelID)
		_, err = e.inbox.AddLabels(ctx, rc.actor, id, nil, []pgtype.UUID{label})
	case ActionRemoveLabel:
		label, _ := parseID(a.LabelID)
		_, err = e.inbox.RemoveLabels(ctx, rc.actor, id, nil, []pgtype.UUID{label})
	case ActionSetStatus:
		_, err = e.inbox.SetStatus(ctx, rc.actor, id, nil, a.Status)
	case ActionMarkSpam:
		_, err = e.inbox.SetStatus(ctx, rc.actor, id, nil, inbox.StatusSpam)
	case ActionSnooze:
		_, err = e.inbox.Snooze(ctx, rc.actor, id, nil, e.d.Now().Add(time.Duration(a.Hours)*time.Hour))
	case ActionApplySLA:
		skipped, err = e.applyPolicyByID(ctx, id, a.PolicyID)
	case ActionAddNote:
		err = e.addNote(ctx, rc, a.Text)
	case ActionSendWebhook:
		err = e.inTx(ctx, func(q *dbq.Queries) error {
			return webhooks.Record(ctx, q, webhooks.Event{Type: webhooks.ConversationAutomation, MailboxID: rc.conv.MailboxID, ConversationID: id})
		})
	case ActionAutoReply:
		skipped, err = e.autoReply(ctx, rc, a)
	default:
		err = fmt.Errorf("unknown action %q", a.Type)
	}
	return skipped, err
}

// applyPolicyByID starts the clocks of a policy now. A conversation that already runs under the
// same policy keeps its deadlines: a rule that applies the policy on every customer message
// must not keep pushing the deadline back.
func (e *Engine) applyPolicyByID(ctx context.Context, conv pgtype.UUID, policyID string) (skipped string, err error) {
	pid, _ := parseID(policyID)
	policy, err := e.q.AutoGetSLAPolicy(ctx, pid)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", errors.New("sla policy no longer exists")
	}
	if err != nil {
		return "", fmt.Errorf("load sla policy: %w", err)
	}
	// The conversation may have changed since the rule started.
	fresh, err := e.q.AutoGetConversation(ctx, conv)
	if err != nil {
		return "", fmt.Errorf("load conversation: %w", err)
	}
	if fresh.SlaPolicyID == policy.ID && !fresh.SlaFinalizedAt.Valid {
		return "policy already applied", nil
	}
	return "", e.applyPolicy(ctx, fresh, policy, e.d.Now())
}

func (e *Engine) addNote(ctx context.Context, rc *runContext, text string) error {
	return e.inTx(ctx, func(q *dbq.Queries) error {
		_, err := q.AutoInsertNote(ctx, dbq.AutoInsertNoteParams{
			ConversationID: rc.conv.ID, MailboxID: rc.conv.MailboxID, FromName: rc.label,
			BodyText: text, BodyHtml: textToHTML(text),
		})
		if err != nil {
			return fmt.Errorf("insert note: %w", err)
		}
		version, err := q.AutoTouchConversation(ctx, rc.conv.ID)
		if err != nil {
			return fmt.Errorf("touch conversation: %w", err)
		}
		for _, typ := range []string{realtime.TypeMessageCreated, realtime.TypeConversationUpdated} {
			ev := realtime.Event{Type: typ, ConversationID: rc.conv.ID.String(), MailboxID: rc.conv.MailboxID.String(), Version: int64(version)}
			if err := realtime.Notify(ctx, q, ev); err != nil {
				return err
			}
		}
		return nil
	})
}

// textToHTML turns plain text into escaped paragraphs; blank lines separate them.
func textToHTML(text string) string {
	var b strings.Builder
	for _, para := range strings.Split(strings.ReplaceAll(strings.TrimSpace(text), "\r\n", "\n"), "\n\n") {
		para = strings.TrimSpace(para)
		if para == "" {
			continue
		}
		b.WriteString("<p>")
		b.WriteString(strings.ReplaceAll(html.EscapeString(para), "\n", "<br>"))
		b.WriteString("</p>")
	}
	return b.String()
}
