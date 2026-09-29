// Package ingest turns stored raw messages into conversation messages: it parses them,
// threads them, links the sender to a contact and records delivery failures reported by DSNs.
package ingest

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"

	"echoo/internal/compose"
	"echoo/internal/db/dbq"
	"echoo/internal/jobs"
	"echoo/internal/mail"
	"echoo/internal/mail/parse"
	"echoo/internal/realtime"
	"echoo/internal/scan"
	"echoo/internal/sniff"
	"echoo/internal/storage"
	"echoo/internal/threading"
	"echoo/internal/webhooks"
)

// Parsing and storing attachments of a large message can exceed River's default one minute.
const workTimeout = 5 * time.Minute

// scanBudget is the most time one message may spend in virus scanning, well inside workTimeout
// so a hanging clamd cannot make the job time out; attachments left over when it runs out are
// recorded as scan errors.
const scanBudget = 90 * time.Second

const (
	statusParsed  = "parsed"
	statusFailed  = "failed"
	statusSkipped = "skipped"
)

// errDuplicate aborts the transaction when a concurrent job stored the identical message
// first, so a conversation created for it is rolled back too.
var errDuplicate = errors.New("identical message already stored")

type Deps struct {
	Pool   *pgxpool.Pool
	Store  storage.Store
	Logger *slog.Logger
	Limits parse.Limits
	// Scanner virus-scans attachments; nil leaves them not_scanned.
	Scanner scan.Scanner
	// Jobs is only set by tests; a running worker uses the client River puts in its context.
	Jobs *river.Client[pgx.Tx]
}

// Worker runs mail.parse jobs.
type Worker struct {
	river.WorkerDefaults[jobs.ParseRaw]
	d Deps
	q *dbq.Queries
	// parse is a field so tests can reach the failure branch; the real parser only fails on
	// structural limits.
	parse func([]byte, parse.Limits) (*mail.Parsed, error)
	// scanBudget overrides the package default in tests.
	scanBudget time.Duration
}

func NewWorker(d Deps) *Worker {
	if d.Logger == nil {
		d.Logger = slog.Default()
	}
	return &Worker{d: d, q: dbq.New(d.Pool), parse: parse.Parse}
}

func (w *Worker) Timeout(*river.Job[jobs.ParseRaw]) time.Duration { return workTimeout }

// Work is idempotent: the raw row's parse_status decides whether anything is left to do.
// Parse failures are deterministic, so they are recorded on the row and not retried; only
// database and storage errors are returned for River to retry.
func (w *Worker) Work(ctx context.Context, job *river.Job[jobs.ParseRaw]) error {
	var id pgtype.UUID
	if err := id.Scan(job.Args.RawMessageID); err != nil || !id.Valid {
		return river.JobCancel(fmt.Errorf("invalid raw message id %q", job.Args.RawMessageID))
	}
	raw, err := w.q.IngestGetRaw(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return river.JobCancel(errors.New("raw message does not exist"))
	}
	if err != nil {
		return fmt.Errorf("load raw message: %w", err)
	}
	if raw.ParseStatus == statusParsed || raw.ParseStatus == statusSkipped {
		return nil
	}
	mbox, err := w.q.IngestGetMailbox(ctx, raw.MailboxID)
	if err != nil {
		return fmt.Errorf("load mailbox: %w", err)
	}
	data, err := w.readBlob(ctx, raw.BlobKey)
	if err != nil {
		return err
	}

	parsed, err := w.parse(data, w.d.Limits)
	switch {
	case errors.Is(err, parse.ErrTooLarge), errors.Is(err, parse.ErrLimits):
		w.d.Logger.WarnContext(ctx, "raw message skipped", "raw_message_id", id.String(), "error", err)
		return w.setStatus(ctx, w.q, id, statusSkipped, err.Error())
	case err != nil:
		w.d.Logger.WarnContext(ctx, "raw message failed to parse", "raw_message_id", id.String(), "error", err)
		return w.setStatus(ctx, w.q, id, statusFailed, err.Error())
	}

	atts, err := w.storeAttachments(ctx, parsed)
	if err != nil {
		return err
	}
	err = pgx.BeginFunc(ctx, w.d.Pool, func(tx pgx.Tx) error {
		return w.ingest(ctx, tx, w.q.WithTx(tx), raw, mbox, parsed, atts)
	})
	if errors.Is(err, errDuplicate) {
		return w.setStatus(ctx, w.q, id, statusParsed, "")
	}
	if err != nil {
		return fmt.Errorf("ingest raw message: %w", err)
	}
	return nil
}

func (w *Worker) readBlob(ctx context.Context, key string) ([]byte, error) {
	rc, err := w.d.Store.Open(ctx, key)
	if err != nil {
		return nil, fmt.Errorf("open raw blob: %w", err)
	}
	defer func() { _ = rc.Close() }()
	data, err := io.ReadAll(rc)
	if err != nil {
		return nil, fmt.Errorf("read raw blob: %w", err)
	}
	return data, nil
}

func (w *Worker) setStatus(ctx context.Context, q *dbq.Queries, id pgtype.UUID, status, detail string) error {
	err := q.IngestSetRawStatus(ctx, dbq.IngestSetRawStatusParams{ID: id, ParseStatus: status, ParseError: detail})
	if err != nil {
		return fmt.Errorf("set raw message status %s: %w", status, err)
	}
	return nil
}

type storedAttachment struct {
	mail.Attachment
	key     string
	sha256  []byte
	sniffed string
	scan    string
	detail  string
}

// storeAttachments runs before the transaction to keep it short. Blobs are content-addressed
// and write-once, so a blob orphaned by a rollback or retry is harmless.
func (w *Worker) storeAttachments(ctx context.Context, p *mail.Parsed) ([]storedAttachment, error) {
	out := make([]storedAttachment, 0, len(p.Attachments))
	scanCtx, cancel := context.WithTimeout(ctx, cmp.Or(w.scanBudget, scanBudget))
	defer cancel()
	for _, a := range p.Attachments {
		key, sum, err := w.d.Store.Put(ctx, a.Data)
		if err != nil {
			return nil, fmt.Errorf("store attachment: %w", err)
		}
		sa := storedAttachment{Attachment: a, key: key, sha256: sum, sniffed: sniff.Type(a.Data), scan: "not_scanned"}
		if w.d.Scanner != nil {
			res := scan.Result{Status: scan.StatusError, Detail: "scan budget for this message used up"}
			if scanCtx.Err() == nil {
				res = w.d.Scanner.Scan(scanCtx, a.Data)
			}
			sa.scan, sa.detail = res.Status, res.Detail
			if res.Status != scan.StatusClean {
				w.d.Logger.WarnContext(ctx, "attachment scan", "status", res.Status, "detail", res.Detail)
			}
		}
		out = append(out, sa)
	}
	return out, nil
}

func (w *Worker) ingest(ctx context.Context, tx pgx.Tx, q *dbq.Queries, raw dbq.RawMessage, mbox dbq.IngestGetMailboxRow, p *mail.Parsed, atts []storedAttachment) error {
	locked, err := q.IngestLockRaw(ctx, raw.ID)
	if err != nil {
		return fmt.Errorf("lock raw message: %w", err)
	}
	if locked.ParseStatus == statusParsed || locked.ParseStatus == statusSkipped {
		return nil
	}

	if p.DSN != nil {
		matched, err := w.recordBounce(ctx, q, raw, p.DSN)
		if err != nil {
			return err
		}
		if matched {
			return w.setStatus(ctx, q, raw.ID, statusParsed, "")
		}
	}

	hash := messageHash(p.MessageID)
	if hash != nil {
		exists, err := q.IngestMessageExists(ctx, dbq.IngestMessageExistsParams{
			MailboxID: raw.MailboxID, MessageIDHash: hash, RawSha256: raw.Sha256,
		})
		if err != nil {
			return fmt.Errorf("check duplicate message: %w", err)
		}
		if exists {
			return w.setStatus(ctx, q, raw.ID, statusParsed, "")
		}
	}

	conv, err := w.resolveConversation(ctx, q, raw, mbox, p)
	if err != nil {
		return err
	}
	msgID, err := w.insertMessage(ctx, q, raw, p, conv.id, hash)
	if err != nil {
		return err
	}
	if err := recordWebhookEvents(ctx, q, raw.MailboxID, conv, msgID); err != nil {
		return err
	}
	for _, a := range atts {
		disposition := "attachment"
		if a.Inline {
			disposition = "inline"
		}
		err := q.IngestInsertAttachment(ctx, dbq.IngestInsertAttachmentParams{
			MessageID: msgID, Filename: a.Filename, DeclaredType: a.DeclaredType, SniffedType: a.sniffed,
			SizeBytes: int64(len(a.Data)), Sha256: a.sha256, BlobKey: a.key, ContentID: a.ContentID,
			Disposition: disposition, ScanStatus: a.scan, ScanDetail: a.detail,
		})
		if err != nil {
			return fmt.Errorf("insert attachment: %w", err)
		}
	}
	err = q.IngestInsertThreadRefs(ctx, dbq.IngestInsertThreadRefsParams{
		MailboxID: raw.MailboxID, ConversationID: conv.id, Hashes: conv.refHashes,
	})
	if err != nil {
		return fmt.Errorf("insert thread refs: %w", err)
	}

	reopen := !p.AutoSubmitted && (conv.status == "closed" || conv.status == "waiting")
	wake := !p.AutoSubmitted && conv.status != "spam" && conv.snoozed
	err = q.IngestUpdateConversation(ctx, dbq.IngestUpdateConversationParams{
		ReceivedAt:     raw.ReceivedAt,
		Preview:        preview(p.Text),
		HasAttachments: len(atts) > 0,
		Reopen:         reopen,
		Wake:           wake,
		ContactID:      conv.contact,
		ID:             conv.id,
	})
	if err != nil {
		return fmt.Errorf("update conversation: %w", err)
	}
	if err := w.recordEvents(ctx, q, raw.MailboxID, conv, reopen, wake); err != nil {
		return err
	}
	if err := w.queueRules(ctx, tx, conv, msgID); err != nil {
		return err
	}
	if !p.AutoSubmitted {
		if err := notifyAssignee(ctx, q, conv.id, msgID); err != nil {
			return err
		}
	}
	events := []string{realtime.TypeMessageCreated}
	if conv.isNew || reopen || wake {
		events = append(events, realtime.TypeConversationUpdated)
	}
	for _, typ := range events {
		ev := realtime.Event{Type: typ, ConversationID: conv.id.String(), MailboxID: raw.MailboxID.String()}
		if err := realtime.Notify(ctx, q, ev); err != nil {
			return err
		}
	}
	w.d.Logger.InfoContext(ctx, "message ingested",
		"raw_message_id", raw.ID.String(), "conversation_id", conv.id.String(), "reason", string(conv.reason))
	return w.setStatus(ctx, q, raw.ID, statusParsed, "")
}

// notifyAssignee tells the agent a conversation is assigned to that the customer wrote again.
func notifyAssignee(ctx context.Context, q *dbq.Queries, conv, msg pgtype.UUID) error {
	assignee, err := q.GetConversationAssignee(ctx, conv)
	if err != nil {
		return fmt.Errorf("load assignee: %w", err)
	}
	if !assignee.Valid {
		return nil
	}
	return compose.Notify(ctx, q, compose.Notification{UserID: assignee, Kind: compose.KindReply, ConversationID: conv, MessageID: msg})
}

type conversation struct {
	id        pgtype.UUID
	contact   pgtype.UUID
	status    string
	snoozed   bool
	isNew     bool
	reason    threading.Reason
	refHashes [][]byte
}

func (w *Worker) resolveConversation(ctx context.Context, q *dbq.Queries, raw dbq.RawMessage, mbox dbq.IngestGetMailboxRow, p *mail.Parsed) (conversation, error) {
	dec, err := threading.Resolve(ctx, threading.NewPGLookup(q), threading.Input{
		MailboxID:    mbox.ID.Bytes,
		OwnAddresses: []string{mbox.EmailAddress},
		Message:      p,
		ReceivedAt:   raw.ReceivedAt.Time,
	})
	if err != nil {
		return conversation{}, fmt.Errorf("resolve thread: %w", err)
	}
	contact, err := w.upsertSender(ctx, q, senderOf(p), mbox.EmailAddress)
	if err != nil {
		return conversation{}, err
	}
	conv := conversation{contact: contact, reason: dec.Reason, refHashes: dec.RefHashes}

	if !dec.IsNew() {
		row, err := q.IngestLockConversation(ctx, dbq.IngestLockConversationParams{
			ID: pgtype.UUID{Bytes: dec.ConversationID, Valid: true}, MailboxID: raw.MailboxID,
		})
		if err == nil {
			conv.id, conv.status, conv.snoozed = row.ID, row.Status, row.SnoozedUntil.Valid
			return conv, nil
		}
		// A soft-deleted conversation is still referenced by thread_refs.
		if !errors.Is(err, pgx.ErrNoRows) {
			return conversation{}, fmt.Errorf("lock conversation: %w", err)
		}
		conv.reason = threading.ReasonNewConversation
	}

	id, err := q.IngestCreateConversation(ctx, dbq.IngestCreateConversationParams{
		MailboxID: raw.MailboxID, Subject: p.Subject, SubjectNormalized: dec.SubjectNormalized,
		ContactID: contact, LastMessageAt: raw.ReceivedAt,
	})
	if err != nil {
		return conversation{}, fmt.Errorf("create conversation: %w", err)
	}
	conv.id, conv.status, conv.isNew = id, "open", true
	return conv, nil
}

func (w *Worker) insertMessage(ctx context.Context, q *dbq.Queries, raw dbq.RawMessage, p *mail.Parsed, conv pgtype.UUID, hash []byte) (pgtype.UUID, error) {
	from := senderOf(p)
	arg := dbq.IngestInsertMessageParams{
		ConversationID:  conv,
		MailboxID:       raw.MailboxID,
		RawMessageID:    raw.ID,
		RawSha256:       raw.Sha256,
		MessageIDHeader: p.MessageID,
		MessageIDHash:   hash,
		ReferencesHdr:   nonNil(p.References),
		FromAddr:        from.Address,
		FromName:        from.Name,
		Subject:         p.Subject,
		ReceivedAt:      raw.ReceivedAt,
		BodyText:        p.Text,
		BodyHtml:        p.HTML,
		AutoSubmitted:   p.AutoSubmitted,
		IsBulk:          p.Bulk || p.ListID != "",
	}
	// Only the top-most header was added by our own receiving server; lower ones can be forged.
	if len(p.AuthResults) > 0 {
		arg.AuthResults = p.AuthResults[0]
	}
	if len(p.InReplyTo) > 0 {
		arg.InReplyTo = p.InReplyTo[0]
	}
	if !p.Date.IsZero() {
		arg.SentAt = pgtype.Timestamptz{Time: p.Date, Valid: true}
	}
	for _, f := range []struct {
		dst  *[]byte
		addr []mail.Address
	}{{&arg.ToAddrs, p.To}, {&arg.CcAddrs, p.Cc}, {&arg.BccAddrs, p.Bcc}, {&arg.ReplyTo, p.ReplyTo}} {
		b, err := json.Marshal(nonNil(f.addr))
		if err != nil {
			return pgtype.UUID{}, fmt.Errorf("encode addresses: %w", err)
		}
		*f.dst = b
	}
	id, err := q.IngestInsertMessage(ctx, arg)
	if errors.Is(err, pgx.ErrNoRows) {
		return pgtype.UUID{}, errDuplicate
	}
	if err != nil {
		return pgtype.UUID{}, fmt.Errorf("insert message: %w", err)
	}
	return id, nil
}

func (w *Worker) recordEvents(ctx context.Context, q *dbq.Queries, mailbox pgtype.UUID, conv conversation, reopen, wake bool) error {
	add := func(typ string, data any) error {
		b, err := json.Marshal(data)
		if err != nil {
			return fmt.Errorf("encode %s event: %w", typ, err)
		}
		err = q.IngestInsertEvent(ctx, dbq.IngestInsertEventParams{ConversationID: conv.id, MailboxID: mailbox, Type: typ, Data: b})
		if err != nil {
			return fmt.Errorf("insert %s event: %w", typ, err)
		}
		return nil
	}
	if conv.isNew {
		if err := add("created", map[string]string{"reason": string(conv.reason)}); err != nil {
			return err
		}
	}
	if reopen {
		if err := add("reopened", struct{}{}); err != nil {
			return err
		}
	}
	if wake {
		return add("woke", struct{}{})
	}
	return nil
}

// recordBounce reports whether the DSN belongs to one of our outbound messages. Only a
// permanent failure moves the message to bounced, and only from a state where delivery was
// believed to have happened, so a late DSN cannot overwrite a newer state.
func (w *Worker) recordBounce(ctx context.Context, q *dbq.Queries, raw dbq.RawMessage, dsn *mail.DSN) (bool, error) {
	hash := messageHash(dsn.OriginalMessageID)
	if hash == nil {
		return false, nil
	}
	ob, err := q.IngestFindOutbound(ctx, dbq.IngestFindOutboundParams{MailboxID: raw.MailboxID, MessageIDHash: hash})
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("find bounced message: %w", err)
	}
	for _, r := range dsn.Recipients {
		if r.Action != "failed" {
			continue
		}
		n, err := q.IngestMarkOutboundBounced(ctx, dbq.IngestMarkOutboundBouncedParams{
			DsnStatus: r.Status, Diagnostic: r.DiagnosticCode, MessageID: ob.MessageID,
		})
		if err != nil {
			return false, fmt.Errorf("mark outbound bounced: %w", err)
		}
		if n == 0 {
			break
		}
		// A permanent failure means the address does not work: future campaigns skip it.
		err = q.CampaignMarkBounced(ctx, dbq.CampaignMarkBouncedParams{
			MessageID: ob.MessageID, Address: r.Address, Diagnostic: r.DiagnosticCode, Now: pgtype.Timestamptz{Time: time.Now().UTC(), Valid: true},
		})
		if err != nil {
			return false, fmt.Errorf("mark address as bouncing: %w", err)
		}
		data, err := json.Marshal(map[string]string{
			"message_id": ob.MessageID.String(), "recipient": r.Address, "dsn_status": r.Status,
		})
		if err != nil {
			return false, fmt.Errorf("encode bounced event: %w", err)
		}
		err = q.IngestInsertEvent(ctx, dbq.IngestInsertEventParams{
			ConversationID: ob.ConversationID, MailboxID: raw.MailboxID, Type: "bounced", Data: data,
		})
		if err != nil {
			return false, fmt.Errorf("insert bounced event: %w", err)
		}
		break
	}
	return true, nil
}

func messageHash(id string) []byte {
	n := mail.NormalizeMessageID(id)
	if n == "" {
		return nil
	}
	return mail.HashMessageID(n)
}

func nonNil[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}

func recordWebhookEvents(ctx context.Context, q *dbq.Queries, mailbox pgtype.UUID, conv conversation, msg pgtype.UUID) error {
	if conv.isNew {
		err := webhooks.Record(ctx, q, webhooks.Event{Type: webhooks.ConversationCreated, MailboxID: mailbox, ConversationID: conv.id})
		if err != nil {
			return err
		}
	}
	return webhooks.Record(ctx, q, webhooks.Event{Type: webhooks.MessageCreated, MailboxID: mailbox, ConversationID: conv.id, MessageID: msg})
}

// queueRules asks for the automation rules to run on the stored message, in the same
// transaction: the message and its evaluation exist together or not at all. A message that
// starts a conversation triggers conversation_created, any other one message_received.
func (w *Worker) queueRules(ctx context.Context, tx pgx.Tx, conv conversation, msg pgtype.UUID) error {
	client := w.d.Jobs
	if client == nil {
		var err error
		if client, err = river.ClientFromContextSafely[pgx.Tx](ctx); err != nil {
			return fmt.Errorf("queue rules: %w", err)
		}
	}
	trigger := jobs.TriggerMessageReceived
	if conv.isNew {
		trigger = jobs.TriggerConversationCreated
	}
	args := jobs.EvaluateRules{ConversationID: conv.id.String(), Trigger: trigger, MessageID: msg.String()}
	if _, err := client.InsertTx(ctx, tx, args, nil); err != nil {
		return fmt.Errorf("queue rules: %w", err)
	}
	return nil
}
