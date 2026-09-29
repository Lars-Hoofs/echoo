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

// CSATSummary counts the surveys sent in a period and the answers to them.
type CSATSummary struct {
	Sent      int
	Responses int
	// Average is the mean rating; meaningless when Responses is zero.
	Average float64
	// Distribution[i] is the number of answers with rating i+1.
	Distribution [5]int
}

// ResponseRate is Responses/Sent as a percentage, or false when nothing was sent.
func (c CSATSummary) ResponseRate() (float64, bool) {
	return Rate{Met: c.Responses, Total: c.Sent}.Percent()
}

type csatAcc struct {
	sent, responses, sum int
	dist                 [5]int
}

func (a *csatAcc) add(rating pgtype.Int2) {
	a.sent++
	if !rating.Valid {
		return
	}
	a.responses++
	a.sum += int(rating.Int16)
	a.dist[rating.Int16-1]++
}

func (a *csatAcc) summary() CSATSummary {
	out := CSATSummary{Sent: a.sent, Responses: a.responses, Distribution: a.dist}
	if a.responses > 0 {
		out.Average = float64(a.sum) / float64(a.responses)
	}
	return out
}

// CSATPoint is one chart bucket of the satisfaction report.
type CSATPoint struct {
	Day string
	CSATSummary
}

// CSATRow is one agent, team, mailbox or label in the satisfaction table.
type CSATRow struct {
	ID   string
	Name string
	CSATSummary
}

// Comment is a customer's written remark with the rating that came with it.
type Comment struct {
	ConversationID string
	Number         int64
	Subject        string
	Rating         int
	Text           string
	At             time.Time
	Assignee       string
}

// CSATReport is the satisfaction tab. Surveys are counted in the period they were sent, so the
// response rate cannot exceed 100 percent.
type CSATReport struct {
	Summary  CSATSummary
	Series   []CSATPoint
	Rows     []CSATRow
	Comments []Comment
}

func (s *Service) csatRows(ctx context.Context, r Range, f Filter, dim Dim) ([]dbq.ReportCsatRowsRow, error) {
	if len(f.Mailboxes) == 0 {
		return nil, nil
	}
	defer s.slot()()
	rows, err := s.q.ReportCsatRows(ctx, dbq.ReportCsatRowsParams{
		Tz: r.Loc.String(), Dim: string(dim), RangeFrom: stamp(r.From), RangeTo: stamp(r.To),
		MailboxIds: f.Mailboxes, TeamID: f.Team, AgentID: f.Agent, LabelID: f.Label,
	})
	if err != nil {
		return nil, fmt.Errorf("report csat: %w", err)
	}
	return rows, nil
}

func (s *Service) csatSummary(ctx context.Context, r Range, f Filter) (CSATSummary, error) {
	rows, err := s.csatRows(ctx, r, f, DimNone)
	if err != nil {
		return CSATSummary{}, err
	}
	var a csatAcc
	for _, row := range rows {
		a.add(row.Rating)
	}
	return a.summary(), nil
}

// CSAT builds the satisfaction report. rowDim is the dimension of the table; keep filters its keys.
func (s *Service) CSAT(ctx context.Context, r Range, f Filter, rowDim Dim, keep func(key string) bool) (CSATReport, error) {
	rows, err := s.csatRows(ctx, r, f, DimNone)
	if err != nil {
		return CSATReport{}, err
	}
	var total csatAcc
	byDay := map[string]*csatAcc{}
	for _, row := range rows {
		total.add(row.Rating)
		day := r.Bucket(row.Day.Time)
		if byDay[day] == nil {
			byDay[day] = &csatAcc{}
		}
		byDay[day].add(row.Rating)
	}
	out := CSATReport{Summary: total.summary()}
	for _, day := range r.Buckets() {
		p := CSATPoint{Day: day}
		if a := byDay[day]; a != nil {
			p.CSATSummary = a.summary()
		}
		out.Series = append(out.Series, p)
	}

	grouped, err := s.csatRows(ctx, r, f, rowDim)
	if err != nil {
		return CSATReport{}, err
	}
	byKey := map[string]*csatAcc{}
	var ids []pgtype.UUID
	for _, row := range grouped {
		key := keyString(row.Key)
		if keep != nil && !keep(key) {
			continue
		}
		if byKey[key] == nil {
			byKey[key] = &csatAcc{}
			if row.Key.Valid {
				ids = append(ids, row.Key)
			}
		}
		byKey[key].add(row.Rating)
	}
	names, err := s.names(ctx, rowDim, ids)
	if err != nil {
		return CSATReport{}, err
	}
	for key, a := range byKey {
		out.Rows = append(out.Rows, CSATRow{ID: key, Name: names[key], CSATSummary: a.summary()})
	}
	slices.SortFunc(out.Rows, func(a, b CSATRow) int {
		return cmp.Or(cmp.Compare(b.Sent, a.Sent), cmp.Compare(a.Name, b.Name), cmp.Compare(a.ID, b.ID))
	})

	if len(f.Mailboxes) > 0 {
		comments, err := s.q.ReportCsatComments(ctx, dbq.ReportCsatCommentsParams{
			RangeFrom: stamp(r.From), RangeTo: stamp(r.To), MailboxIds: f.Mailboxes,
			TeamID: f.Team, AgentID: f.Agent, LabelID: f.Label,
		})
		if err != nil {
			return CSATReport{}, fmt.Errorf("report csat comments: %w", err)
		}
		for _, c := range comments {
			out.Comments = append(out.Comments, Comment{
				ConversationID: keyString(c.ConversationID), Number: c.Number, Subject: c.Subject,
				Rating: int(c.Rating), Text: c.Comment, At: c.UpdatedAt.Time, Assignee: c.AssigneeName.String,
			})
		}
	}
	return out, nil
}
