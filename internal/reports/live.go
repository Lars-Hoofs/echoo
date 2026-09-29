package reports

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"echoo/internal/db/dbq"
)

// LiveAgent is one person on the live tab.
type LiveAgent struct {
	ID           string
	Name         string
	Open         int64
	Waiting      int64
	SLARisk      int64
	Online       bool
	Availability string
}

// AttentionWindow is how far ahead a first-response due time counts as "due soon".
const AttentionWindow = 60 * time.Minute

// Holder is a person holding conversations that need attention.
type Holder struct {
	ID    string
	Name  string
	Count int64
}

// Attention is what needs a reply now: open conversations without a first reply from us. Breached
// ones are past their first-response due time, DueSoon ones reach it within AttentionWindow.
// Holders lists the people who hold breached or due soon ones (largest first), Unassigned counts
// those nobody holds.
type Attention struct {
	Unanswered int64
	Breached   int64
	DueSoon    int64
	NextDue    time.Duration
	Oldest     time.Duration
	Unassigned int64
	Holders    []Holder
}

// Live is the state of the mailboxes right now.
type Live struct {
	Open        int64
	Unassigned  int64
	Waiting     int64
	SLAAtRisk   int64
	SLABreached int64
	Agents      []LiveAgent
	Attention   Attention
}

func durationOf(v float64) time.Duration { return time.Duration(v * float64(time.Second)) }

func (s *Service) attention(ctx context.Context, f Filter, only func(id string) bool) (Attention, error) {
	window := int32(AttentionWindow / time.Minute)
	row, err := s.q.ReportAttention(ctx, dbq.ReportAttentionParams{
		WindowMinutes: window, MailboxIds: f.Mailboxes, TeamID: f.Team, AgentID: f.Agent, LabelID: f.Label,
	})
	if err != nil {
		return Attention{}, fmt.Errorf("report attention: %w", err)
	}
	out := Attention{
		Unanswered: row.Unanswered, Breached: row.Breached, DueSoon: row.DueSoon,
		NextDue: durationOf(row.NextDueSeconds), Oldest: durationOf(row.OldestSeconds), Holders: []Holder{},
	}
	if row.Breached+row.DueSoon == 0 {
		return out, nil
	}
	held, err := s.q.ReportAttentionAssignees(ctx, dbq.ReportAttentionAssigneesParams{
		WindowMinutes: window, MailboxIds: f.Mailboxes, TeamID: f.Team, AgentID: f.Agent, LabelID: f.Label,
	})
	if err != nil {
		return Attention{}, fmt.Errorf("report attention assignees: %w", err)
	}
	counts := map[pgtype.UUID]int64{}
	var ids []pgtype.UUID
	for _, h := range held {
		if !h.UserID.Valid {
			out.Unassigned += h.N
			continue
		}
		counts[h.UserID] = h.N
		ids = append(ids, h.UserID)
	}
	users, err := s.q.ReportAgents(ctx, ids)
	if err != nil {
		return Attention{}, fmt.Errorf("report agents: %w", err)
	}
	for _, u := range users {
		if id := keyString(u.ID); only == nil || only(id) {
			out.Holders = append(out.Holders, Holder{ID: id, Name: u.Name, Count: counts[u.ID]})
		}
	}
	slices.SortFunc(out.Holders, func(a, b Holder) int {
		return cmp.Or(cmp.Compare(b.Count, a.Count), cmp.Compare(a.Name, b.Name), cmp.Compare(a.ID, b.ID))
	})
	return out, nil
}

// Live reads the current counts within the filters (a filter on a label, team or agent narrows
// them like it does the period numbers). online holds the ids of people with an open event
// stream; only lists which agents appear in the per-agent table and the attention holders (nil
// lists all of them).
func (s *Service) Live(ctx context.Context, f Filter, online map[string]bool, only func(id string) bool) (Live, error) {
	out := Live{Agents: []LiveAgent{}, Attention: Attention{Holders: []Holder{}}}
	mailboxes := f.Mailboxes
	if len(mailboxes) == 0 {
		return out, nil
	}
	row, err := s.q.ReportLive(ctx, dbq.ReportLiveParams{MailboxIds: mailboxes, TeamID: f.Team, AgentID: f.Agent, LabelID: f.Label})
	if err != nil {
		return Live{}, fmt.Errorf("report live: %w", err)
	}
	out.Open, out.Unassigned, out.Waiting, out.SLAAtRisk, out.SLABreached = row.Open, row.Unassigned, row.Waiting, row.SlaAtRisk, row.SlaBreached

	out.Attention, err = s.attention(ctx, f, only)
	if err != nil {
		return Live{}, err
	}

	perAgent, err := s.q.ReportLiveByAgent(ctx, dbq.ReportLiveByAgentParams{MailboxIds: mailboxes, TeamID: f.Team, AgentID: f.Agent, LabelID: f.Label})
	if err != nil {
		return Live{}, fmt.Errorf("report live by agent: %w", err)
	}
	counts := map[string]dbq.ReportLiveByAgentRow{}
	var ids []pgtype.UUID
	for _, a := range perAgent {
		counts[keyString(a.UserID)] = a
		ids = append(ids, a.UserID)
	}
	for id := range online {
		var u pgtype.UUID
		if err := u.Scan(id); err == nil && !slices.Contains(ids, u) {
			ids = append(ids, u)
		}
	}
	users, err := s.q.ReportAgents(ctx, ids)
	if err != nil {
		return Live{}, fmt.Errorf("report agents: %w", err)
	}
	for _, u := range users {
		id := keyString(u.ID)
		if only != nil && !only(id) {
			continue
		}
		c := counts[id]
		out.Agents = append(out.Agents, LiveAgent{
			ID: id, Name: u.Name, Open: c.Open, Waiting: c.Waiting, SLARisk: c.SlaRisk,
			Online: online[id], Availability: u.Availability,
		})
	}
	slices.SortFunc(out.Agents, func(a, b LiveAgent) int {
		return cmp.Or(cmp.Compare(b.Open, a.Open), cmp.Compare(a.Name, b.Name), cmp.Compare(a.ID, b.ID))
	})
	return out, nil
}
