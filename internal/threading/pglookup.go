package threading

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"echoo/internal/db/dbq"
)

// PGLookup implements Lookup on Postgres. Build it from the caller's transaction
// (queries.WithTx(tx)) so threading sees the same snapshot as the insert that follows.
type PGLookup struct {
	q *dbq.Queries
}

func NewPGLookup(q *dbq.Queries) *PGLookup { return &PGLookup{q: q} }

func (l *PGLookup) ConversationsByRefs(ctx context.Context, mailbox ID, hashes [][]byte) (map[string]ID, error) {
	rows, err := l.q.ListThreadRefConversations(ctx, dbq.ListThreadRefConversationsParams{
		MailboxID: pgUUID(mailbox),
		Hashes:    hashes,
	})
	if err != nil {
		return nil, err
	}
	out := make(map[string]ID, len(rows))
	for _, r := range rows {
		out[string(r.MessageIDHash)] = r.ConversationID.Bytes
	}
	return out, nil
}

func (l *PGLookup) ConversationInMailbox(ctx context.Context, mailbox, conversation ID) (bool, error) {
	return l.q.ConversationInMailbox(ctx, dbq.ConversationInMailboxParams{
		ID:        pgUUID(conversation),
		MailboxID: pgUUID(mailbox),
	})
}

func (l *PGLookup) SubjectCandidates(ctx context.Context, mailbox ID, normalizedSubject string, notBefore time.Time) ([]Candidate, error) {
	rows, err := l.q.ListSubjectCandidates(ctx, dbq.ListSubjectCandidatesParams{
		MailboxID:         pgUUID(mailbox),
		SubjectNormalized: normalizedSubject,
		LastMessageAt:     pgtype.Timestamptz{Time: notBefore, Valid: true},
	})
	if err != nil {
		return nil, err
	}
	out := make([]Candidate, 0, len(rows))
	for _, r := range rows {
		if !r.LastMessageAt.Valid {
			return nil, fmt.Errorf("conversation %x has no last_message_at", r.ID.Bytes)
		}
		c := Candidate{
			ID:            r.ID.Bytes,
			Status:        r.Status,
			LastMessageAt: r.LastMessageAt.Time,
			Participants:  r.Participants,
		}
		if r.ResolvedAt.Valid {
			c.ResolvedAt = r.ResolvedAt.Time
		}
		out = append(out, c)
	}
	return out, nil
}

func pgUUID(id ID) pgtype.UUID { return pgtype.UUID{Bytes: id, Valid: true} }
