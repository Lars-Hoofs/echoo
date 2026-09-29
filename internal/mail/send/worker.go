package send

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"

	"echoo/internal/db/dbq"
	"echoo/internal/jobs"
	"echoo/internal/keyring"
	"echoo/internal/mail"
	"echoo/internal/realtime"
	"echoo/internal/storage"
	"echoo/internal/webhooks"
)

const (
	// A row in 'sending' this long after its claim belongs to a crashed job.
	staleAfter = 10 * time.Minute
	// maxAttempts counts the first delivery attempt, so len(backoff)+1.
	maxAttempts = 6
	// claimLostDelay is how long a job waits after losing the claim race before it looks at
	// the row again.
	claimLostDelay = 5 * time.Second
	// maxSentChecks is the number of job attempts spent on checking the Sent folder of a
	// stale row before it is declared uncertain.
	maxSentChecks = 3
	recordTimeout = 30 * time.Second
	workTimeout   = 10 * time.Minute
)

var backoff = [maxAttempts - 1]time.Duration{30 * time.Second, 2 * time.Minute, 10 * time.Minute, 30 * time.Minute, 2 * time.Hour}

// errUnsendable marks messages that can never be sent as they are, as opposed to failures
// worth retrying: a validation error, a disabled mailbox, missing SMTP settings.
var errUnsendable = errors.New("message cannot be sent")

// Deps are the collaborators of Worker. Clock defaults to time.Now.
type Deps struct {
	Pool    *pgxpool.Pool
	Storage storage.Store
	Keyring *keyring.Keyring
	// Tokens supplies access tokens for OAuth mailboxes.
	Tokens TokenSource
	Clock  func() time.Time
	// TLS supplies extra trust roots for SMTP and IMAP; certificate verification stays on.
	TLS *tls.Config
}

// Worker runs mail.send jobs.
type Worker struct {
	river.WorkerDefaults[jobs.SendOutbound]
	d Deps
	q *dbq.Queries
}

func NewWorker(d Deps) *Worker {
	if d.Clock == nil {
		d.Clock = time.Now
	}
	return &Worker{d: d, q: dbq.New(d.Pool)}
}

func (w *Worker) Timeout(*river.Job[jobs.SendOutbound]) time.Duration { return workTimeout }

func (w *Worker) now() time.Time { return w.d.Clock().UTC() }

func ts(t time.Time) pgtype.Timestamptz { return pgtype.Timestamptz{Time: t, Valid: true} }

// Work is idempotent: the outbound row, not the job, decides what happens. Business retries
// snooze the job until the row's next_attempt_at instead of using River's error retries, which
// are reserved for failures that left the row untouched. If the process dies between writing
// 'retry' and snoozing, River's rescuer re-runs the job and the row still says when it is due.
func (w *Worker) Work(ctx context.Context, job *river.Job[jobs.SendOutbound]) error {
	var id pgtype.UUID
	if err := id.Scan(job.Args.MessageID); err != nil || !id.Valid {
		return river.JobCancel(fmt.Errorf("invalid message id %q", job.Args.MessageID))
	}
	ob, err := w.q.SendGetOutbound(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return river.JobCancel(errors.New("outbound row does not exist"))
	}
	if err != nil {
		return fmt.Errorf("load outbound: %w", err)
	}
	now := w.now()
	switch ob.Status {
	case "queued", "retry":
	case "sending":
		return w.reconcile(ctx, id, ob, now, job.Attempt)
	default:
		return nil
	}
	if due := dueAt(ob); due.After(now) {
		return river.JobSnooze(due.Sub(now))
	}

	p, err := w.prepare(ctx, id, ob, now)
	var validation *ValidationError
	if errors.Is(err, errUnsendable) || errors.As(err, &validation) {
		slog.WarnContext(ctx, "outbound message is unsendable", "message_id", id.String(), "error", err)
		return w.setResult(ctx, id, "failed", "", err.Error(), pgtype.Timestamptz{}, pgtype.Timestamptz{}, now)
	}
	if err != nil {
		return err
	}

	claimed, err := w.q.SendClaimOutbound(ctx, dbq.SendClaimOutboundParams{MessageID: id, Now: ts(now)})
	if errors.Is(err, pgx.ErrNoRows) {
		return river.JobSnooze(claimLostDelay)
	}
	if err != nil {
		return fmt.Errorf("claim outbound: %w", err)
	}

	res := Deliver(ctx, p.smtp, p.from, p.rcpt, p.raw)
	return w.recordOutcome(ctx, id, claimed.Attempt, res, p)
}

func dueAt(ob dbq.Outbound) time.Time {
	due := ob.ScheduledAt.Time
	if ob.NextAttemptAt.Valid && ob.NextAttemptAt.Time.After(due) {
		due = ob.NextAttemptAt.Time
	}
	return due
}

func (w *Worker) setResult(ctx context.Context, id pgtype.UUID, status, response, errText string, next, sent pgtype.Timestamptz, now time.Time) error {
	return pgx.BeginFunc(ctx, w.d.Pool, func(tx pgx.Tx) error {
		return w.setResultTx(ctx, tx, id, status, response, errText, next, sent, now)
	})
}

func (w *Worker) setResultTx(ctx context.Context, tx pgx.Tx, id pgtype.UUID, status, response, errText string, next, sent pgtype.Timestamptz, now time.Time) error {
	q := w.q.WithTx(tx)
	n, err := q.SendSetOutboundResult(ctx, dbq.SendSetOutboundResultParams{
		MessageID: id, Status: status, SmtpResponse: response, Error: errText,
		NextAttemptAt: next, SentAt: sent, Now: ts(now),
	})
	if err != nil {
		return fmt.Errorf("set outbound %s: %w", status, err)
	}
	if n == 0 {
		return fmt.Errorf("set outbound %s: row is no longer pending", status)
	}
	if status == "sent" {
		if err := q.SendSetMessageSentAt(ctx, dbq.SendSetMessageSentAtParams{ID: id, SentAt: sent}); err != nil {
			return fmt.Errorf("set message sent_at: %w", err)
		}
		if err := q.SendRecordConversationReply(ctx, dbq.SendRecordConversationReplyParams{MessageID: id, SentAt: sent}); err != nil {
			return fmt.Errorf("update conversation: %w", err)
		}
	}
	ref, err := q.RealtimeMessageRef(ctx, id)
	if err != nil {
		return fmt.Errorf("load message for event: %w", err)
	}
	events := []string{realtime.TypeMessageUpdated}
	if status == "sent" {
		events = append(events, realtime.TypeConversationUpdated)
	}
	for _, typ := range events {
		ev := realtime.Event{Type: typ, ConversationID: ref.ConversationID.String(), MailboxID: ref.MailboxID.String()}
		if err := realtime.Notify(ctx, q, ev); err != nil {
			return err
		}
	}
	return recordWebhookOutcome(ctx, q, status, id, ref)
}

// recordWebhookOutcome announces the final outcomes; retries and bounces are not events.
func recordWebhookOutcome(ctx context.Context, q *dbq.Queries, status string, id pgtype.UUID, ref dbq.RealtimeMessageRefRow) error {
	typ := map[string]string{"sent": webhooks.MessageSent, "failed": webhooks.MessageFailed}[status]
	if typ == "" {
		return nil
	}
	return webhooks.Record(ctx, q, webhooks.Event{Type: typ, MailboxID: ref.MailboxID, ConversationID: ref.ConversationID, MessageID: id})
}

// recordOutcome persists what happened to a claimed row. It runs on a context that survives
// shutdown: losing the outcome of a message the server already accepted would be worse than
// delaying the shutdown by a few seconds.
func (w *Worker) recordOutcome(ctx context.Context, id pgtype.UUID, attempt int32, res Result, p *prepared) error {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), recordTimeout)
	defer cancel()
	now := w.now()
	errText := ""
	if res.Err != nil {
		errText = res.Err.Error()
	}
	slog.InfoContext(ctx, "outbound delivery attempt finished", "message_id", id.String(), "attempt", attempt, "outcome", res.Outcome.String())

	switch res.Outcome {
	case Delivered:
		if err := w.setResult(ctx, id, "sent", res.Response, "", pgtype.Timestamptz{}, ts(now), now); err != nil {
			return err
		}
		w.appendToSent(ctx, id, p, now)
		return nil
	case PermanentFailure:
		return w.setResult(ctx, id, "failed", res.Response, errText, pgtype.Timestamptz{}, pgtype.Timestamptz{}, now)
	case UnknownOutcome:
		return w.setResult(ctx, id, "uncertain", res.Response, errText, pgtype.Timestamptz{}, pgtype.Timestamptz{}, now)
	case TemporaryFailure:
		if int(attempt) >= maxAttempts {
			return w.setResult(ctx, id, "failed", res.Response, errText, pgtype.Timestamptz{}, pgtype.Timestamptz{}, now)
		}
		delay := backoff[attempt-1]
		next := now.Add(delay)
		if err := w.setResult(ctx, id, "retry", res.Response, errText, ts(next), pgtype.Timestamptz{}, now); err != nil {
			return err
		}
		return river.JobSnooze(delay)
	}
	return fmt.Errorf("unexpected delivery outcome %d", res.Outcome)
}

func (w *Worker) appendToSent(ctx context.Context, id pgtype.UUID, p *prepared, now time.Time) {
	if p.sent == nil {
		return
	}
	if err := AppendToSent(ctx, *p.sent, p.raw, now); err != nil {
		slog.WarnContext(ctx, "could not append message to Sent folder", "message_id", id.String(), "error", err)
	}
}

// reconcile handles a row found in 'sending'. It never resends: it only looks in the Sent
// folder for proof that the message went out.
func (w *Worker) reconcile(ctx context.Context, id pgtype.UUID, ob dbq.Outbound, now time.Time, attempt int) error {
	if ob.LastAttemptAt.Valid {
		if age := now.Sub(ob.LastAttemptAt.Time); age < staleAfter {
			return river.JobSnooze(staleAfter - age)
		}
	}
	msg, err := w.q.SendGetMessage(ctx, id)
	if err != nil {
		return fmt.Errorf("load message: %w", err)
	}
	mb, err := w.q.SendGetMailbox(ctx, msg.MailboxID)
	if err != nil {
		return fmt.Errorf("load mailbox: %w", err)
	}
	none := pgtype.Timestamptz{}
	cfg, err := w.imapConfig(ctx, mb)
	if err != nil {
		return w.setResult(ctx, id, "uncertain", "", "Sent folder could not be checked: "+err.Error(), none, none, now)
	}
	if cfg == nil {
		return w.setResult(ctx, id, "uncertain", "", "interrupted while sending and no Sent folder is configured to check", none, none, now)
	}
	found, err := InSentFolder(ctx, *cfg, msg.MessageIDHeader)
	if err != nil {
		// River's error retries space the checks out; a row must not stay 'sending' forever.
		if attempt < maxSentChecks {
			return fmt.Errorf("check Sent folder: %w", err)
		}
		slog.WarnContext(ctx, "Sent folder check failed, giving up", "message_id", id.String(), "error", err)
		return w.setResult(ctx, id, "uncertain", "", "Sent folder could not be checked: "+err.Error(), none, none, now)
	}
	if found {
		slog.InfoContext(ctx, "interrupted send found in Sent folder", "message_id", id.String())
		return w.setResult(ctx, id, "sent", "", "", none, ts(now), now)
	}
	slog.WarnContext(ctx, "interrupted send not found in Sent folder", "message_id", id.String())
	return w.setResult(ctx, id, "uncertain", "", "interrupted while sending and not found in the Sent folder", none, none, now)
}

type prepared struct {
	raw  []byte
	from string
	rcpt []string
	smtp SMTPConfig
	sent *IMAPConfig // nil when copies are not stored
}

func (w *Worker) prepare(ctx context.Context, id pgtype.UUID, ob dbq.Outbound, now time.Time) (*prepared, error) {
	msg, err := w.q.SendGetMessage(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("load message: %w", err)
	}
	mb, err := w.q.SendGetMailbox(ctx, msg.MailboxID)
	if err != nil {
		return nil, fmt.Errorf("load mailbox: %w", err)
	}
	if mb.DisabledAt.Valid {
		return nil, fmt.Errorf("%w: mailbox is disabled", errUnsendable)
	}
	smtpCfg, err := MailboxSMTP(ctx, w.d.Keyring, w.d.Tokens, w.d.TLS, mb)
	if err != nil {
		return nil, err
	}

	out := Outgoing{
		From:        mail.Address{Name: msg.FromName, Address: msg.FromAddr},
		Subject:     msg.Subject,
		MessageID:   msg.MessageIDHeader,
		InReplyTo:   msg.InReplyTo,
		References:  msg.ReferencesHdr,
		Date:        now,
		Text:        msg.BodyText,
		HTML:        msg.BodyHtml,
		AutoReplied: msg.AutoSubmitted,
	}
	if err := json.Unmarshal(ob.ExtraHeaders, &out.Headers); err != nil {
		return nil, fmt.Errorf("%w: stored headers are corrupt", errUnsendable)
	}
	for _, f := range []struct {
		raw []byte
		dst *[]mail.Address
	}{{msg.ToAddrs, &out.To}, {msg.CcAddrs, &out.Cc}, {msg.BccAddrs, &out.Bcc}} {
		if err := json.Unmarshal(f.raw, f.dst); err != nil {
			return nil, fmt.Errorf("%w: stored recipients are corrupt", errUnsendable)
		}
	}
	atts, err := w.q.SendListAttachments(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("load attachments: %w", err)
	}
	for _, a := range atts {
		data, err := w.readBlob(ctx, a.BlobKey)
		if err != nil {
			return nil, fmt.Errorf("read attachment blob: %w", err)
		}
		out.Attachments = append(out.Attachments, mail.Attachment{
			Filename: a.Filename, DeclaredType: a.DeclaredType, ContentID: a.ContentID,
			Inline: a.Disposition == "inline", Data: data,
		})
	}
	raw, err := Build(out)
	if err != nil {
		return nil, err
	}
	// A broken Sent-folder setup must not stop the message itself from going out.
	sent, err := w.imapConfig(ctx, mb)
	if err != nil {
		slog.WarnContext(ctx, "Sent folder disabled for this message", "message_id", id.String(), "error", err)
		sent = nil
	}
	return &prepared{
		raw:  raw,
		from: mb.EmailAddress,
		rcpt: out.Recipients(),
		smtp: smtpCfg,
		sent: sent,
	}, nil
}

func (w *Worker) readBlob(ctx context.Context, key string) ([]byte, error) {
	r, err := w.d.Storage.Open(ctx, key)
	if err != nil {
		return nil, err
	}
	data, err := io.ReadAll(r)
	return data, errors.Join(err, r.Close())
}

func (w *Worker) secret(enc []byte, column string, mailboxID pgtype.UUID) (string, error) {
	if len(enc) == 0 {
		return "", nil
	}
	plain, err := w.d.Keyring.Decrypt(enc, keyring.AAD("mailboxes", column, mailboxID.String()))
	if err != nil {
		return "", fmt.Errorf("%w: cannot decrypt %s", errUnsendable, strings.TrimSuffix(column, "_enc"))
	}
	return string(plain), nil
}

// imapConfig returns nil when the mailbox does not keep copies of sent mail.
func (w *Worker) imapConfig(ctx context.Context, mb dbq.Mailbox) (*IMAPConfig, error) {
	if mb.SentFolder == "" || mb.ImapHost == "" {
		return nil, nil
	}
	cfg := &IMAPConfig{
		Host: mb.ImapHost, Port: int(mb.ImapPort), TLS: TLSMode(mb.ImapTls),
		Username: mb.ImapUsername, Folder: mb.SentFolder, AllowInternal: mb.AllowInternalHost, TLSConfig: w.d.TLS,
	}
	if mb.AuthType != "password" {
		if w.d.Tokens == nil {
			return nil, errors.New("OAuth mailboxes are not configured")
		}
		token, err := w.d.Tokens.AccessToken(ctx, mb.ID)
		if err != nil {
			return nil, err
		}
		cfg.AccessToken = token
		return cfg, nil
	}
	password, err := w.secret(mb.ImapSecretEnc, "imap_secret_enc", mb.ID)
	if err != nil {
		return nil, err
	}
	cfg.Password = password
	return cfg, nil
}
