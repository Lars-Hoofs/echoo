package automation

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"echoo/internal/inbox"
	"echoo/internal/jobs"
)

// MacroResult is the outcome of a macro on one conversation. Err is inbox.ErrNotFound or
// inbox.ErrForbidden when the actor may not touch it, and nil otherwise; failed actions are
// listed in Actions.
type MacroResult struct {
	ConversationID pgtype.UUID
	Err            error
	Actions        []ActionResult
}

// Failed reports whether the conversation was not (fully) updated.
func (r MacroResult) Failed() bool {
	return r.Err != nil || slices.ContainsFunc(r.Actions, func(a ActionResult) bool { return a.Result == resultFailed })
}

// RunMacro runs the actions on each conversation as the person who started it. The actions go
// through the same executor as rules, but each conversation queues at most one evaluation of
// the update rules afterwards instead of one per action.
func (e *Engine) RunMacro(ctx context.Context, actor inbox.Actor, name string, actions []Action, ids []pgtype.UUID) ([]MacroResult, error) {
	if len(ids) > inbox.MaxBulk {
		return nil, inbox.ErrTooManyIDs
	}
	actor.Source = inbox.SourceMacro
	client, err := e.client(ctx)
	if err != nil {
		return nil, err
	}
	results := make([]MacroResult, 0, len(ids))
	seen := make(map[pgtype.UUID]bool, len(ids))
	for _, id := range ids {
		if seen[id] {
			continue
		}
		seen[id] = true
		res := MacroResult{ConversationID: id}
		conv, err := e.q.AutoGetConversation(ctx, id)
		switch {
		case errors.Is(err, pgx.ErrNoRows) || (err == nil && !slices.Contains(actor.Read, conv.MailboxID)):
			res.Err = inbox.ErrNotFound
		case err != nil:
			return results, fmt.Errorf("load conversation: %w", err)
		case !slices.Contains(actor.Write, conv.MailboxID):
			res.Err = inbox.ErrForbidden
		default:
			res.Actions = e.runActions(ctx, &runContext{conv: conv, actor: actor, label: "Macro: " + name}, actions)
			if slices.ContainsFunc(res.Actions, func(a ActionResult) bool { return a.Result == resultApplied }) {
				_, err := client.Insert(ctx, jobs.EvaluateRules{ConversationID: id.String(), Trigger: jobs.TriggerConversationUpdated}, nil)
				if err != nil {
					return results, fmt.Errorf("queue rules: %w", err)
				}
			}
		}
		results = append(results, res)
		if ctx.Err() != nil {
			return results, ctx.Err()
		}
	}
	return results, nil
}
