package campaigns

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
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
	"echoo/internal/keyring"
	"echoo/internal/mail"
	"echoo/internal/mail/send"
)

const (
	// DefaultRate is the sending rate of a new campaign, in messages per minute.
	DefaultRate = 60
	// DefaultMaxRate is the highest rate a campaign may ask for unless configured otherwise.
	DefaultMaxRate = 120

	maxNameRunes    = 100
	maxSubjectRunes = 300
	maxBodyBytes    = 500 << 10
)

var (
	// ErrConflict means the campaign is in a state that does not allow the action.
	ErrConflict = errors.New("campaign state does not allow this")
	// ErrNotFound means there is no such campaign.
	ErrNotFound = errors.New("campaign not found")
)

// NotReadyError lists what is still missing before a campaign can start, by field.
type NotReadyError struct{ Fields map[string]string }

func (e *NotReadyError) Error() string { return "campaign is not complete" }

// Deps are the collaborators of Service. Now defaults to time.Now.
type Deps struct {
	Pool *pgxpool.Pool
	// BaseURL is ECHOO_BASE_URL without a trailing slash; unsubscribe links point at it.
	BaseURL string
	// Jobs queues the send jobs. Without it the dispatcher uses the client of the running job.
	Jobs *river.Client[pgx.Tx]
	// Keyring, Tokens and TLS let a test mail go out over the mailbox's SMTP server directly.
	Keyring *keyring.Keyring
	Tokens  send.TokenSource
	TLS     *tls.Config
	// MaxRate caps the rate of every campaign, in messages per minute per mailbox.
	MaxRate int
	Now     func() time.Time
}

// Service runs campaigns.
type Service struct {
	d Deps
	q *dbq.Queries
}

// NewService returns a Service.
func NewService(d Deps) *Service {
	if d.Now == nil {
		d.Now = time.Now
	}
	if d.MaxRate <= 0 {
		d.MaxRate = DefaultMaxRate
	}
	return &Service{d: d, q: dbq.New(d.Pool)}
}

// WithJobs returns a Service that queues jobs through client.
func (s *Service) WithJobs(client *river.Client[pgx.Tx]) *Service {
	d := s.d
	d.Jobs = client
	return &Service{d: d, q: s.q}
}

func (s *Service) now() time.Time { return s.d.Now().UTC() }

func ts(t time.Time) pgtype.Timestamptz { return pgtype.Timestamptz{Time: t, Valid: true} }

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

// Draft is the editable part of a campaign.
type Draft struct {
	Name      string
	MailboxID pgtype.UUID
	SegmentID pgtype.UUID
	Subject   string
	BodyHTML  string
	Rate      int
}

// Validate reports what is wrong with a draft, by field. A draft may be saved incomplete;
// complete additionally demands what a campaign needs to be sent.
func (d Draft) Validate(maxRate int, complete bool) map[string]string {
	fields := map[string]string{}
	if name := strings.TrimSpace(d.Name); name == "" {
		fields["name"] = "required"
	} else if utf8.RuneCountInString(name) > maxNameRunes || strings.ContainsFunc(name, isControl) {
		fields["name"] = "invalid"
	}
	if !d.MailboxID.Valid {
		fields["mailbox_id"] = "required"
	}
	if d.Rate < 1 || d.Rate > maxRate {
		fields["rate_per_minute"] = "invalid"
	}
	if strings.ContainsAny(d.Subject, "\r\n\x00") || utf8.RuneCountInString(d.Subject) > maxSubjectRunes {
		fields["subject"] = "invalid"
	}
	if len(d.BodyHTML) > maxBodyBytes {
		fields["body_html"] = "too_large"
	}
	if fields["subject"] == "" && len(UnknownVariables(d.Subject)) > 0 {
		fields["subject"] = "unknown_variable"
	}
	if fields["body_html"] == "" && len(UnknownVariables(d.BodyHTML)) > 0 {
		fields["body_html"] = "unknown_variable"
	}
	if complete {
		if strings.TrimSpace(d.Subject) == "" {
			fields["subject"] = "required"
		}
		if compose.IsEmpty(d.BodyHTML) {
			fields["body_html"] = "required"
		}
		if !d.SegmentID.Valid {
			fields["segment_id"] = "required"
		}
	}
	return fields
}

func isControl(r rune) bool { return r < 0x20 || r == 0x7f }

// Start moves a draft to sending, or to scheduled when at lies in the future. Recipients are
// resolved by the dispatcher, not here, so starting a campaign on a large segment stays fast.
//
// The recipients are those starter may see, and the segment must be one they may use, which is
// checked again here because the draft may have been written by someone else.
func (s *Service) Start(ctx context.Context, tx pgx.Tx, id pgtype.UUID, starter dbq.User, at *time.Time) (scheduled bool, err error) {
	q := dbq.New(tx)
	c, err := q.CampaignGet(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, ErrNotFound
	}
	if err != nil {
		return false, fmt.Errorf("load campaign: %w", err)
	}
	if c.Status != "draft" {
		return false, ErrConflict
	}
	fields := Draft{
		Name: c.Name, MailboxID: c.MailboxID, SegmentID: c.SegmentID, Subject: c.Subject, BodyHTML: c.BodyHtml, Rate: int(c.RatePerMinute),
	}.Validate(s.d.MaxRate, true)
	if len(fields) > 0 {
		return false, &NotReadyError{Fields: fields}
	}
	seg, err := q.GetSegment(ctx, c.SegmentID)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && seg.OwnerUserID != starter.ID && !seg.Shared) {
		return false, &NotReadyError{Fields: map[string]string{"segment_id": "unknown"}}
	}
	if err != nil {
		return false, fmt.Errorf("load segment: %w", err)
	}
	mb, err := q.GetMailbox(ctx, c.MailboxID)
	if err != nil {
		return false, fmt.Errorf("load mailbox: %w", err)
	}
	if mb.DisabledAt.Valid {
		return false, &NotReadyError{Fields: map[string]string{"mailbox_id": "disabled"}}
	}

	now := s.now()
	params := dbq.CampaignStartParams{ID: id, Status: "sending", StartedAt: ts(now), StartedBy: starter.ID}
	if at != nil && at.After(now) {
		params.Status, params.ScheduledAt, params.StartedAt = "scheduled", ts(*at), pgtype.Timestamptz{}
	}
	n, err := q.CampaignStart(ctx, params)
	if err != nil {
		return false, fmt.Errorf("start campaign: %w", err)
	}
	if n == 0 {
		return false, ErrConflict
	}
	if params.Status == "sending" {
		return false, s.wake(ctx, tx)
	}
	return true, nil
}

// wake queues a dispatcher run right away instead of waiting for the next periodic one.
func (s *Service) wake(ctx context.Context, tx pgx.Tx) error {
	client, err := s.client(ctx)
	if err != nil {
		return err
	}
	if _, err := client.InsertTx(ctx, tx, jobs.CampaignTick{}, nil); err != nil {
		return fmt.Errorf("queue dispatcher: %w", err)
	}
	return nil
}

// Pause stops handing out new messages. Messages already in the send queue still go.
func (s *Service) Pause(ctx context.Context, tx pgx.Tx, id pgtype.UUID) error {
	n, err := dbq.New(tx).CampaignPause(ctx, id)
	if err != nil {
		return fmt.Errorf("pause campaign: %w", err)
	}
	if n == 0 {
		return ErrConflict
	}
	return nil
}

// Resume continues a paused campaign.
func (s *Service) Resume(ctx context.Context, tx pgx.Tx, id pgtype.UUID) error {
	n, err := dbq.New(tx).CampaignResume(ctx, id)
	if err != nil {
		return fmt.Errorf("resume campaign: %w", err)
	}
	if n == 0 {
		return ErrConflict
	}
	return s.wake(ctx, tx)
}

// Cancel ends the campaign for good. Recipients that were not reached, and messages still
// waiting in the send queue, are skipped; a message that is being delivered right now still
// goes out and is recorded as sent.
func (s *Service) Cancel(ctx context.Context, tx pgx.Tx, id pgtype.UUID) error {
	q := dbq.New(tx)
	now := ts(s.now())
	n, err := q.CampaignCancel(ctx, dbq.CampaignCancelParams{ID: id, Now: now})
	if err != nil {
		return fmt.Errorf("cancel campaign: %w", err)
	}
	if n == 0 {
		return ErrConflict
	}
	if _, err := q.CampaignCancelOutbound(ctx, dbq.CampaignCancelOutboundParams{CampaignID: id, Now: now}); err != nil {
		return fmt.Errorf("stop queued messages: %w", err)
	}
	if _, err := q.CampaignSkipPending(ctx, dbq.CampaignSkipPendingParams{CampaignID: id, Now: now}); err != nil {
		return fmt.Errorf("skip pending recipients: %w", err)
	}
	return nil
}

func (s *Service) unsubscribeURL(recipient, secret pgtype.UUID, now time.Time) string {
	return s.d.BaseURL + "/afmelden/" + issueToken(recipient, secret, now.Add(TokenTTL))
}

// TestSendError explains why a test mail did not go out.
type TestSendError struct{ Reason string }

func (e *TestSendError) Error() string { return "test mail not sent: " + e.Reason }

// SendTest delivers the campaign to one address right away, over the mailbox's own SMTP
// server, without creating a conversation. The unsubscribe link in it is a placeholder.
func (s *Service) SendTest(ctx context.Context, id pgtype.UUID, to mail.Address) error {
	c, err := s.q.CampaignGet(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("load campaign: %w", err)
	}
	mb, err := s.q.GetMailbox(ctx, c.MailboxID)
	if err != nil {
		return fmt.Errorf("load mailbox: %w", err)
	}
	if mb.DisabledAt.Valid {
		return &TestSendError{Reason: "mailbox is disabled"}
	}
	_, domain, _ := strings.Cut(mb.EmailAddress, "@")
	var random pgtype.UUID
	if err := s.d.Pool.QueryRow(ctx, `SELECT uuidv7()`).Scan(&random); err != nil {
		return fmt.Errorf("generate id: %w", err)
	}
	messageID, err := mail.NewOutboundMessageID(random.Bytes, domain)
	if err != nil {
		return fmt.Errorf("generate message id: %w", err)
	}
	brand := brandOf(mb)
	content := Render(c.Subject, c.BodyHtml, Sender{Brand: brand, Agent: c.CreatorName.String}, to, s.d.BaseURL+"/afmelden/voorbeeld")
	raw, err := send.Build(send.Outgoing{
		From: mail.Address{Name: mb.DisplayName, Address: mb.EmailAddress}, To: []mail.Address{to},
		Subject: "[Test] " + content.Subject, MessageID: messageID, Date: s.now(),
		Text: content.Text, HTML: content.HTML, Headers: content.Headers,
	})
	if err != nil {
		var ve *send.ValidationError
		if errors.As(err, &ve) {
			return &TestSendError{Reason: ve.Error()}
		}
		return fmt.Errorf("build test message: %w", err)
	}
	cfg, err := send.MailboxSMTP(ctx, s.d.Keyring, s.d.Tokens, s.d.TLS, mb)
	if send.IsUnsendable(err) {
		return &TestSendError{Reason: err.Error()}
	}
	if err != nil {
		return fmt.Errorf("mailbox smtp settings: %w", err)
	}
	res := send.Deliver(ctx, cfg, mb.EmailAddress, []string{to.Address}, raw)
	if res.Outcome != send.Delivered {
		reason := res.Response
		if res.Err != nil {
			reason = res.Err.Error()
		}
		return &TestSendError{Reason: reason}
	}
	return nil
}

func brandOf(mb dbq.Mailbox) string {
	if mb.DisplayName != "" {
		return mb.DisplayName
	}
	return mb.Name
}
