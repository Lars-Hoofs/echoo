package retention

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"net/netip"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"

	"echoo/internal/audit"
	"echoo/internal/contacts"
	"echoo/internal/db"
	"echoo/internal/db/dbq"
	"echoo/internal/jobs"
)

// BatchSize is how many rows one purge transaction touches; a batch is the unit of locking,
// auditing and blob queueing.
const BatchSize = 1000

// Service runs the purges. Blobs may be nil for callers that only preview.
type Service struct {
	pool  *pgxpool.Pool
	q     *dbq.Queries
	blobs contacts.Blobs
	now   func() time.Time
}

func NewService(pool *pgxpool.Pool, blobs contacts.Blobs) *Service {
	return &Service{pool: pool, q: dbq.New(pool), blobs: blobs, now: time.Now}
}

// Result counts what one run deleted.
type Result struct {
	ClosedConversations int64
	SpamConversations   int64
	TrashConversations  int64
	Attachments         int64
	AuditEntries        int64
}

func (s *Service) months(n int) time.Time { return s.now().AddDate(0, -n, 0) }
func (s *Service) days(n int) time.Time   { return s.now().AddDate(0, 0, -n) }

func ts(t time.Time) pgtype.Timestamptz { return pgtype.Timestamptz{Time: t, Valid: true} }

// Run applies the stored settings. A failure in one mailbox or kind of data does not stop the
// others; the errors are joined, and the next daily run picks up where this one stopped.
func (s *Service) Run(ctx context.Context) (Result, error) {
	var res Result
	set, err := Load(ctx, s.q)
	if err != nil {
		return res, err
	}
	mailboxes, err := s.q.RetentionListMailboxIDs(ctx)
	if err != nil {
		return res, fmt.Errorf("list mailboxes: %w", err)
	}
	var errs []error
	step := func(n int64, err error, into *int64) {
		*into += n
		if err != nil {
			errs = append(errs, err)
		}
	}
	for _, mb := range mailboxes {
		p := set.effective(mb)
		if p.ClosedConversationMonths != nil {
			n, err := s.purgeConversations(ctx, mb, "closed", s.months(*p.ClosedConversationMonths))
			step(n, err, &res.ClosedConversations)
		}
		if p.SpamDays != nil {
			n, err := s.purgeConversations(ctx, mb, "spam", s.days(*p.SpamDays))
			step(n, err, &res.SpamConversations)
		}
		if p.TrashDays != nil {
			entry := audit.Entry{Action: audit.RetentionPurged, Metadata: map[string]any{"kind": "trash_conversations"}}
			n, err := s.purgeTrash(ctx, mb, s.days(*p.TrashDays), entry, true)
			step(n, err, &res.TrashConversations)
		}
		if p.AttachmentMonths != nil {
			n, err := s.purgeAttachments(ctx, mb, s.months(*p.AttachmentMonths))
			step(n, err, &res.Attachments)
		}
	}
	if set.AuditMonths != nil {
		n, err := s.purgeAudit(ctx, *set.AuditMonths)
		step(n, err, &res.AuditEntries)
	}
	return res, errors.Join(errs...)
}

// purgeConversations deletes conversations in the given status whose last activity is before
// the cutoff, batch by batch.
func (s *Service) purgeConversations(ctx context.Context, mailbox pgtype.UUID, status string, before time.Time) (int64, error) {
	pick := func(q *dbq.Queries) ([]pgtype.UUID, error) {
		return q.RetentionPickConversations(ctx, dbq.RetentionPickConversationsParams{
			MailboxID: mailbox, Status: status, Before: ts(before), BatchSize: BatchSize,
		})
	}
	entry := audit.Entry{Action: audit.RetentionPurged, TargetType: "mailbox", TargetID: mailbox.String(),
		Metadata: map[string]any{"kind": status + "_conversations"}}
	total, err := s.purgeBatches(ctx, pick, entry, true)
	if err != nil {
		return total, fmt.Errorf("purge %s conversations: %w", status, err)
	}
	return total, nil
}

// purgeTrash deletes conversations of the mailbox that went into the trash before the cutoff.
func (s *Service) purgeTrash(ctx context.Context, mailbox pgtype.UUID, before time.Time, entry audit.Entry, drain bool) (int64, error) {
	pick := func(q *dbq.Queries) ([]pgtype.UUID, error) {
		return q.TrashPickExpired(ctx, dbq.TrashPickExpiredParams{MailboxID: mailbox, Before: ts(before), BatchSize: BatchSize})
	}
	entry.TargetType, entry.TargetID = "mailbox", mailbox.String()
	total, err := s.purgeBatches(ctx, pick, entry, drain)
	if err != nil {
		return total, fmt.Errorf("purge trash: %w", err)
	}
	return total, nil
}

// By is who asks for a manual purge, for the audit log.
type By struct {
	UserID pgtype.UUID
	IP     *netip.Addr
}

// EmptyTrash permanently deletes everything in the trash of the given mailboxes, except
// conversations with mail still being sent. Files are only queued for deletion; call Drain.
func (s *Service) EmptyTrash(ctx context.Context, mailboxes []pgtype.UUID, by By) (int64, error) {
	var total int64
	// The cutoff is taken once, so a conversation trashed while this runs is left alone.
	before := s.now()
	entry := audit.Entry{Actor: by.UserID, IP: by.IP, Action: audit.ConversationsPurged, Metadata: map[string]any{"emptied_trash": true}}
	for _, mb := range mailboxes {
		n, err := s.purgeTrash(ctx, mb, before, entry, false)
		total += n
		if err != nil {
			return total, err
		}
	}
	return total, nil
}

// PurgeOutcome says what happened to one conversation of PurgeTrashed.
type PurgeOutcome int

const (
	Purged PurgeOutcome = iota
	// NotInTrash covers conversations that do not exist, are outside the mailboxes or are not
	// in the trash.
	NotInTrash
	// StillSending conversations have mail on its way out and are kept.
	StillSending
)

// PurgeTrashed permanently deletes the given conversations, which must be in the trash of one
// of the mailboxes, in one transaction with one audit entry. Files are only queued for
// deletion; call Drain.
func (s *Service) PurgeTrashed(ctx context.Context, ids, mailboxes []pgtype.UUID, by By) (map[pgtype.UUID]PurgeOutcome, error) {
	out := make(map[pgtype.UUID]PurgeOutcome, len(ids))
	for _, id := range ids {
		out[id] = NotInTrash
	}
	err := db.InTx(ctx, s.pool, func(q *dbq.Queries) error {
		rows, err := q.TrashLockForPurge(ctx, dbq.TrashLockForPurgeParams{Ids: ids, MailboxIds: mailboxes})
		if err != nil {
			return err
		}
		var purge []pgtype.UUID
		for _, r := range rows {
			if r.Sending {
				out[r.ID] = StillSending
				continue
			}
			out[r.ID] = Purged
			purge = append(purge, r.ID)
		}
		if len(purge) == 0 {
			return nil
		}
		_, err = deleteConversations(ctx, q, purge, audit.Entry{Actor: by.UserID, IP: by.IP, Action: audit.ConversationsPurged, TargetType: "conversations"})
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("purge trashed conversations: %w", err)
	}
	return out, nil
}

// Drain deletes queued files nothing refers to any more, for callers that purge without
// draining. What fails stays queued for the hourly contacts.purge job.
func (s *Service) Drain(ctx context.Context) error { return s.drain(ctx) }

// purgeBatches deletes what pick returns, one transaction and one audit entry per batch,
// until pick finds nothing. With drain, queued files are deleted after every batch.
func (s *Service) purgeBatches(ctx context.Context, pick func(*dbq.Queries) ([]pgtype.UUID, error), entry audit.Entry, drain bool) (int64, error) {
	var total int64
	for {
		var n int64
		err := db.InTx(ctx, s.pool, func(q *dbq.Queries) error {
			ids, err := pick(q)
			if err != nil || len(ids) == 0 {
				return err
			}
			n, err = deleteConversations(ctx, q, ids, entry)
			return err
		})
		if err != nil {
			return total, err
		}
		if n == 0 {
			return total, nil
		}
		total += n
		if !drain {
			continue
		}
		if err := s.drain(ctx); err != nil {
			return total, err
		}
	}
}

// deleteConversations removes conversations for good and writes entry with the counts added
// to its metadata. Attachment files and raw messages leave through the deletion queue; the raw
// message rows stay (blanked) so IMAP sync does not import the mail again.
func deleteConversations(ctx context.Context, q *dbq.Queries, ids []pgtype.UUID, entry audit.Entry) (int64, error) {
	totals, err := q.RetentionTotalsOfConversations(ctx, ids)
	if err != nil {
		return 0, err
	}
	keys, err := q.DeleteAttachmentsOfConversations(ctx, ids)
	if err != nil {
		return 0, err
	}
	rawKeys, err := dropRaw(ctx, q, func() ([]pgtype.UUID, error) { return q.RetentionRawIDsOfConversations(ctx, ids) })
	if err != nil {
		return 0, err
	}
	n, err := q.RetentionDeleteConversations(ctx, ids)
	if err != nil {
		return 0, err
	}
	if err := contacts.QueueBlobDeletions(ctx, q, append(keys, rawKeys...)); err != nil {
		return 0, err
	}
	meta := map[string]any{"conversations": n, "messages": totals.Messages, "attachments": totals.Attachments}
	maps.Copy(meta, entry.Metadata)
	entry.Metadata = meta
	return n, audit.Write(ctx, q, entry)
}

// purgeAttachments deletes attachments of messages received before the cutoff, keeps the
// message text, and drops the raw copy of those messages since it still contains the files.
func (s *Service) purgeAttachments(ctx context.Context, mailbox pgtype.UUID, before time.Time) (int64, error) {
	var total int64
	for {
		var n int64
		var size int64
		err := db.InTx(ctx, s.pool, func(q *dbq.Queries) error {
			rows, err := q.RetentionPickAttachments(ctx, dbq.RetentionPickAttachmentsParams{
				MailboxID: mailbox, Before: ts(before), BatchSize: BatchSize,
			})
			if err != nil || len(rows) == 0 {
				return err
			}
			ids := make([]pgtype.UUID, len(rows))
			keys := make([]string, len(rows))
			var messageIDs []pgtype.UUID
			for i, r := range rows {
				ids[i], keys[i] = r.ID, r.BlobKey
				size += r.SizeBytes
				if len(messageIDs) == 0 || !containsUUID(messageIDs, r.MessageID) {
					messageIDs = append(messageIDs, r.MessageID)
				}
			}
			if err := q.RetentionDeleteAttachments(ctx, ids); err != nil {
				return err
			}
			rawKeys, err := dropRaw(ctx, q, func() ([]pgtype.UUID, error) { return q.RetentionRawIDsOfMessages(ctx, messageIDs) })
			if err != nil {
				return err
			}
			if err := q.RetentionRefreshHasAttachments(ctx, messageIDs); err != nil {
				return err
			}
			n = int64(len(rows))
			if err := contacts.QueueBlobDeletions(ctx, q, append(keys, rawKeys...)); err != nil {
				return err
			}
			return audit.Write(ctx, q, audit.Entry{Action: audit.RetentionPurged, TargetType: "mailbox", TargetID: mailbox.String(),
				Metadata: map[string]any{"kind": "attachments", "attachments": n, "bytes": size, "messages": len(messageIDs)}})
		})
		if err != nil {
			return total, fmt.Errorf("purge attachments: %w", err)
		}
		if n == 0 {
			return total, nil
		}
		total += n
		if err := s.drain(ctx); err != nil {
			return total, err
		}
	}
}

func containsUUID(list []pgtype.UUID, id pgtype.UUID) bool {
	for _, x := range list {
		if x == id {
			return true
		}
	}
	return false
}

// dropRaw blanks the raw message rows behind ids and returns the blob keys they held.
func dropRaw(ctx context.Context, q *dbq.Queries, rawIDs func() ([]pgtype.UUID, error)) ([]string, error) {
	ids, err := rawIDs()
	if err != nil || len(ids) == 0 {
		return nil, err
	}
	keys, err := q.RawBlobKeys(ctx, ids)
	if err != nil {
		return nil, err
	}
	return keys, q.DropRawMessages(ctx, ids)
}

// purgeAudit deletes audit entries older than months through audit_log_purge, which writes its
// own audit entry with the count of each batch before deleting it.
func (s *Service) purgeAudit(ctx context.Context, months int) (int64, error) {
	var total int64
	for {
		n, err := s.q.RetentionPurgeAuditBatch(ctx, dbq.RetentionPurgeAuditBatchParams{Months: int32(months), BatchSize: BatchSize}) //nolint:gosec // months is validated to 3..120
		if err != nil {
			return total, fmt.Errorf("purge audit log: %w", err)
		}
		total += int64(n)
		if n < BatchSize {
			return total, nil
		}
	}
}

// PurgeUploads removes composer uploads that expired without being attached, and their files.
func (s *Service) PurgeUploads(ctx context.Context) (int64, error) {
	var total int64
	for {
		var n int64
		err := db.InTx(ctx, s.pool, func(q *dbq.Queries) error {
			keys, err := q.RetentionExpireUploads(ctx, BatchSize)
			if err != nil || len(keys) == 0 {
				return err
			}
			n = int64(len(keys))
			if err := contacts.QueueBlobDeletions(ctx, q, keys); err != nil {
				return err
			}
			return audit.Write(ctx, q, audit.Entry{Action: audit.UploadsPurged, TargetType: "uploads", Metadata: map[string]any{"count": n}})
		})
		if err != nil {
			return total, fmt.Errorf("purge uploads: %w", err)
		}
		total += n
		if n < BatchSize {
			break
		}
	}
	return total, s.drain(ctx)
}

// drain deletes queued files nothing refers to any more. Failures stay queued for the hourly
// contacts.purge job, so they are logged there and reported to River here as a failed run.
func (s *Service) drain(ctx context.Context) error {
	if s.blobs == nil {
		return errors.New("retention: no blob store to delete from")
	}
	failed, err := contacts.DrainAllBlobDeletions(ctx, s.q, s.blobs)
	if err != nil {
		return err
	}
	if failed > 0 {
		return fmt.Errorf("%d stored files could not be deleted", failed)
	}
	return nil
}

func (s *Service) recordRun(ctx context.Context, res Result) error {
	raw, err := json.Marshal(LastRun{At: s.now().UTC(), ClosedConversation: res.ClosedConversations,
		SpamConversations: res.SpamConversations, TrashConversations: res.TrashConversations,
		Attachments: res.Attachments, AuditEntries: res.AuditEntries})
	if err != nil {
		return err
	}
	return s.q.UpsertSetting(ctx, dbq.UpsertSettingParams{Key: lastRunKey, Value: raw})
}

// PurgeWorker runs retention.purge, daily.
type PurgeWorker struct {
	river.WorkerDefaults[jobs.RetentionPurge]
	svc *Service
}

func NewPurgeWorker(svc *Service) *PurgeWorker { return &PurgeWorker{svc: svc} }

func (w *PurgeWorker) Work(ctx context.Context, _ *river.Job[jobs.RetentionPurge]) error {
	res, err := w.svc.Run(ctx)
	if res != (Result{}) {
		slog.InfoContext(ctx, "retention purge", "closed_conversations", res.ClosedConversations,
			"spam_conversations", res.SpamConversations, "trash_conversations", res.TrashConversations,
			"attachments", res.Attachments, "audit_entries", res.AuditEntries)
	}
	if recErr := w.svc.recordRun(ctx, res); recErr != nil {
		err = errors.Join(err, fmt.Errorf("record retention run: %w", recErr))
	}
	return err
}

// UploadsWorker runs uploads.purge, hourly.
type UploadsWorker struct {
	river.WorkerDefaults[jobs.UploadsPurge]
	svc *Service
}

func NewUploadsWorker(svc *Service) *UploadsWorker { return &UploadsWorker{svc: svc} }

func (w *UploadsWorker) Work(ctx context.Context, _ *river.Job[jobs.UploadsPurge]) error {
	n, err := w.svc.PurgeUploads(ctx)
	if n > 0 {
		slog.InfoContext(ctx, "expired uploads purged", "count", n)
	}
	return err
}

// PeriodicJobs schedules the daily retention purge and the hourly upload sweep.
func PeriodicJobs() []*river.PeriodicJob {
	return []*river.PeriodicJob{
		river.NewPeriodicJob(river.PeriodicInterval(24*time.Hour),
			func() (river.JobArgs, *river.InsertOpts) {
				return jobs.RetentionPurge{}, &river.InsertOpts{MaxAttempts: 3}
			},
			&river.PeriodicJobOpts{RunOnStart: true}),
		river.NewPeriodicJob(river.PeriodicInterval(time.Hour),
			func() (river.JobArgs, *river.InsertOpts) {
				return jobs.UploadsPurge{}, &river.InsertOpts{MaxAttempts: 1}
			},
			&river.PeriodicJobOpts{RunOnStart: true}),
	}
}
