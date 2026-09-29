package csat

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"log/slog"
	"net/mail"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"

	"echoo/internal/compose"
	"echoo/internal/db/dbq"
	"echoo/internal/jobs"
	echoomail "echoo/internal/mail"
	"echoo/internal/mail/send"
)

const (
	sweepBatch = 50
	// sweepRounds bounds one run; a backlog carries over to the next minute.
	sweepRounds = 20
	// LowRating is the highest rating that alerts the assignee.
	LowRating = 2
	// MaxComment is the longest comment a customer can leave, in characters.
	MaxComment = 1000
)

var (
	// ErrInvalidInput means the rating or comment cannot be stored.
	ErrInvalidInput = errors.New("invalid rating or comment")
	// ErrLocked means the answer can no longer be changed.
	ErrLocked = errors.New("survey answer can no longer be changed")
)

// noReplyLocal matches local parts of addresses that never read mail.
var noReplyLocal = regexp.MustCompile(`^(no[-_.]?reply|do[-_.]?not[-_.]?reply|mailer-daemon|postmaster|bounces?)$`)

// Deps are the collaborators of Service.
type Deps struct {
	Pool   *pgxpool.Pool
	Signer *Signer
	// BaseURL is ECHOO_BASE_URL without a trailing slash; the survey links point at it.
	BaseURL string
	// Jobs queues the survey mail. Without it the sweep uses the client of the running job.
	Jobs *river.Client[pgx.Tx]
	Now  func() time.Time
}

// Service sends surveys and stores answers.
type Service struct {
	d Deps
	q *dbq.Queries
}

// NewService returns a Service.
func NewService(d Deps) *Service {
	if d.Now == nil {
		d.Now = time.Now
	}
	return &Service{d: d, q: dbq.New(d.Pool)}
}

func (s *Service) client(ctx context.Context) (*river.Client[pgx.Tx], error) {
	if s.d.Jobs != nil {
		return s.d.Jobs, nil
	}
	c, err := river.ClientFromContextSafely[pgx.Tx](ctx)
	if err != nil {
		return nil, fmt.Errorf("job client: %w", err)
	}
	return c, nil
}

// Sweep queues the survey for every conversation that is due and returns how many mails it
// queued. Conversations that must not be surveyed get a row saying why, so they are not
// looked at again.
func (s *Service) Sweep(ctx context.Context) (int, error) {
	sent := 0
	for range sweepRounds {
		ids, err := s.q.CsatDueConversations(ctx, sweepBatch)
		if err != nil {
			return sent, fmt.Errorf("list due surveys: %w", err)
		}
		for _, id := range ids {
			queued, err := s.survey(ctx, id)
			if err != nil {
				return sent, fmt.Errorf("survey conversation %s: %w", id.String(), err)
			}
			if queued {
				sent++
			}
		}
		if len(ids) < sweepBatch {
			break
		}
	}
	return sent, nil
}

// skipReason says why the customer of a conversation must not get a survey, or is empty.
func skipReason(inbound dbq.CsatLatestInboundRow, found bool, own []string) string {
	if !found {
		return "no_customer_message"
	}
	addr, err := mail.ParseAddress(inbound.FromAddr)
	switch {
	case inbound.AutoSubmitted:
		return "auto_submitted"
	case inbound.IsBounce:
		return "bounce"
	case inbound.IsBulk:
		return "bulk"
	case err != nil:
		return "no_reply_address"
	}
	local, _, _ := strings.Cut(strings.ToLower(addr.Address), "@")
	switch {
	case noReplyLocal.MatchString(local):
		return "no_reply_address"
	case slices.Contains(own, strings.ToLower(addr.Address)):
		return "own_mailbox"
	}
	return ""
}

func (s *Service) survey(ctx context.Context, id pgtype.UUID) (queued bool, err error) {
	client, err := s.client(ctx)
	if err != nil {
		return false, err
	}
	err = pgx.BeginFunc(ctx, s.d.Pool, func(tx pgx.Tx) error {
		q := dbq.New(tx)
		conv, err := q.CsatLockConversation(ctx, id)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("lock conversation: %w", err)
		}
		if conv.Status != "closed" {
			return nil
		}
		known, err := q.ComposeKnownAddresses(ctx, id)
		if err != nil {
			return fmt.Errorf("load participants: %w", err)
		}
		// Only a known participant gets the survey; a stranger who wrote into the thread does not.
		inbound, err := q.CsatLatestInbound(ctx, dbq.CsatLatestInboundParams{ConversationID: id, KnownCustomers: known})
		found := !errors.Is(err, pgx.ErrNoRows)
		if err != nil && found {
			return fmt.Errorf("load latest customer message: %w", err)
		}
		own, err := q.ComposeMailboxAddresses(ctx)
		if err != nil {
			return fmt.Errorf("list mailbox addresses: %w", err)
		}
		claim := dbq.CsatClaimRequestParams{ConversationID: id, MailboxID: conv.MailboxID, SkippedReason: skipReason(inbound, found, own)}
		var token string
		if claim.SkippedReason == "" {
			now := s.d.Now().UTC().Truncate(time.Second)
			expires := now.Add(TokenTTL)
			token = s.d.Signer.Issue(id, expires)
			claim.TokenHash = Hash(token)
			claim.SentAt = pgtype.Timestamptz{Time: now, Valid: true}
			claim.ExpiresAt = pgtype.Timestamptz{Time: expires, Valid: true}
		}
		if _, err := q.CsatClaimRequest(ctx, claim); errors.Is(err, pgx.ErrNoRows) {
			return nil
		} else if err != nil {
			return fmt.Errorf("claim survey: %w", err)
		}
		if token == "" {
			return nil
		}
		key, err := q.NewUUID(ctx)
		if err != nil {
			return fmt.Errorf("new idempotency key: %w", err)
		}
		brand := conv.MailboxDisplayName
		if brand == "" {
			brand = conv.MailboxName
		}
		addr, err := mail.ParseAddress(inbound.FromAddr)
		if err != nil {
			return fmt.Errorf("parse recipient: %w", err)
		}
		content := buildMessage(s.d.BaseURL, token, brand)
		res, err := send.Enqueue(ctx, tx, client, send.EnqueueParams{
			ConversationID: id, MailboxID: conv.MailboxID, IdempotencyKey: key, AutoReplied: true,
			To:      []echoomail.Address{{Name: inbound.FromName, Address: strings.ToLower(addr.Address)}},
			Subject: content.subject, Text: content.text, HTML: content.html,
			InReplyTo: inbound.MessageIDHeader, References: append(slices.Clone(inbound.ReferencesHdr), inbound.MessageIDHeader),
		})
		if err != nil {
			return fmt.Errorf("enqueue survey: %w", err)
		}
		if err := q.CsatSetRequestMessage(ctx, dbq.CsatSetRequestMessageParams{ConversationID: id, MessageID: res.MessageID}); err != nil {
			return fmt.Errorf("record survey message: %w", err)
		}
		queued = true
		return nil
	})
	return queued, err
}

// CSRF returns the value the survey form for token must echo back on submit.
func (s *Service) CSRF(token string) string { return s.d.Signer.CSRF(token) }

// CheckCSRF reports whether value is the CSRF value of token.
func (s *Service) CheckCSRF(token, value string) bool { return s.d.Signer.CheckCSRF(token, value) }

// Survey is what the public page shows for one token.
type Survey struct {
	ConversationID pgtype.UUID
	Brand          string
	// Rating is zero until the customer answered.
	Rating  int
	Comment string
	// Locked is set once the change window has passed; ChangeUntil is when it does or did.
	Locked      bool
	ChangeUntil time.Time
}

func (s *Service) lookup(ctx context.Context, q *dbq.Queries, token string) (Survey, dbq.CsatGetRequestRow, error) {
	conv, _, err := s.d.Signer.Verify(token, s.d.Now())
	if err != nil {
		return Survey{}, dbq.CsatGetRequestRow{}, err
	}
	req, err := q.CsatGetRequest(ctx, conv)
	if errors.Is(err, pgx.ErrNoRows) {
		return Survey{}, req, ErrInvalid
	}
	if err != nil {
		return Survey{}, req, fmt.Errorf("load survey: %w", err)
	}
	if subtle.ConstantTimeCompare(req.TokenHash, Hash(token)) != 1 {
		return Survey{}, req, ErrInvalid
	}
	out := Survey{ConversationID: conv, Brand: req.MailboxDisplayName}
	if out.Brand == "" {
		out.Brand = req.MailboxName
	}
	resp, err := q.CsatGetResponse(ctx, conv)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
	case err != nil:
		return Survey{}, req, fmt.Errorf("load answer: %w", err)
	default:
		out.Rating, out.Comment = int(resp.Rating), resp.Comment
		out.ChangeUntil = resp.CreatedAt.Time.Add(ChangeWindow)
		out.Locked = !s.d.Now().Before(out.ChangeUntil)
	}
	return out, req, nil
}

// Lookup verifies token and returns the survey it belongs to.
func (s *Service) Lookup(ctx context.Context, token string) (Survey, error) {
	out, _, err := s.lookup(ctx, s.q, token)
	return out, err
}

// CleanComment trims a comment and rejects one that is too long or holds control characters.
func CleanComment(c string) (string, error) {
	c = strings.TrimSpace(strings.ReplaceAll(c, "\r\n", "\n"))
	if utf8.RuneCountInString(c) > MaxComment || !utf8.ValidString(c) {
		return "", ErrInvalidInput
	}
	for _, r := range c {
		if r < 0x20 && r != '\n' && r != '\t' || r == 0x7f {
			return "", ErrInvalidInput
		}
	}
	return c, nil
}

// Submit stores the answer, or replaces it within the change window. A low rating alerts the
// assignee once: on the first low answer, or when a better answer becomes a low one.
func (s *Service) Submit(ctx context.Context, token string, rating int, comment string) (Survey, error) {
	if rating < 1 || rating > 5 {
		return Survey{}, ErrInvalidInput
	}
	comment, err := CleanComment(comment)
	if err != nil {
		return Survey{}, err
	}
	err = pgx.BeginFunc(ctx, s.d.Pool, func(tx pgx.Tx) error {
		q := dbq.New(tx)
		survey, req, err := s.lookup(ctx, q, token)
		if err != nil {
			return err
		}
		if survey.Locked {
			return ErrLocked
		}
		previous := survey.Rating
		_, err = q.CsatUpsertResponse(ctx, dbq.CsatUpsertResponseParams{
			ConversationID: survey.ConversationID, MailboxID: req.MailboxID, Rating: int16(rating), //nolint:gosec // 1 to 5, checked above
			Comment: comment, TokenHash: req.TokenHash,
		})
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrLocked
		}
		if err != nil {
			return fmt.Errorf("store answer: %w", err)
		}
		if rating <= LowRating && (previous == 0 || previous > LowRating) {
			return s.alertAssignee(ctx, q, survey.ConversationID)
		}
		return nil
	})
	if err != nil {
		return Survey{}, err
	}
	return s.Lookup(ctx, token)
}

func (s *Service) alertAssignee(ctx context.Context, q *dbq.Queries, conversation pgtype.UUID) error {
	assignee, err := q.CsatAssignee(ctx, conversation)
	if err != nil {
		return fmt.Errorf("load assignee: %w", err)
	}
	if !assignee.Valid {
		return nil
	}
	return compose.Notify(ctx, q, compose.Notification{UserID: assignee, Kind: compose.KindCSAT, ConversationID: conversation})
}

// Worker runs the periodic csat.sweep job.
type Worker struct {
	river.WorkerDefaults[jobs.CSATSweep]
	svc *Service
}

// NewWorker returns the worker for the sweep job.
func NewWorker(svc *Service) *Worker { return &Worker{svc: svc} }

func (w *Worker) Work(ctx context.Context, _ *river.Job[jobs.CSATSweep]) error {
	n, err := w.svc.Sweep(ctx)
	if n > 0 {
		slog.InfoContext(ctx, "satisfaction surveys queued", "count", n)
	}
	return err
}

// PeriodicJobs runs the sweep every minute.
func PeriodicJobs() []*river.PeriodicJob {
	return []*river.PeriodicJob{
		river.NewPeriodicJob(river.PeriodicInterval(time.Minute),
			func() (river.JobArgs, *river.InsertOpts) { return jobs.CSATSweep{}, nil },
			&river.PeriodicJobOpts{RunOnStart: true}),
	}
}
