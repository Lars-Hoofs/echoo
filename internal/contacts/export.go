package contacts

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"

	"echoo/internal/db/dbq"
	"echoo/internal/jobs"
	"echoo/internal/mail"
	"echoo/internal/policy"
	"echoo/internal/storage"
)

const (
	exportTimeout   = 30 * time.Minute
	exportRetention = 7 * 24 * time.Hour
	maxFilenameLen  = 100
)

type exportAttachment struct {
	Filename string `json:"filename"`
	Size     int64  `json:"size_bytes"`
	// File is the path inside the ZIP; empty when the stored file could not be found.
	File string `json:"file,omitempty"`
}

type exportMessage struct {
	Kind        string             `json:"kind"`
	Direction   string             `json:"direction,omitempty"`
	From        mail.Address       `json:"from"`
	To          []mail.Address     `json:"to"`
	Cc          []mail.Address     `json:"cc"`
	Subject     string             `json:"subject"`
	Text        string             `json:"text"`
	Author      string             `json:"author,omitempty"`
	SentAt      *time.Time         `json:"sent_at,omitempty"`
	ReceivedAt  time.Time          `json:"received_at"`
	Attachments []exportAttachment `json:"attachments"`
}

type exportSurvey struct {
	Rating    int16     `json:"rating"`
	Comment   string    `json:"comment"`
	CreatedAt time.Time `json:"created_at"`
}

type exportConversation struct {
	Number           int64           `json:"number"`
	Subject          string          `json:"subject"`
	Status           string          `json:"status"`
	Mailbox          string          `json:"mailbox"`
	CreatedAt        time.Time       `json:"created_at"`
	CustomAttributes json.RawMessage `json:"custom_attributes"`
	Survey           *exportSurvey   `json:"satisfaction_survey"`
	Messages         []exportMessage `json:"messages"`
}

type exportNote struct {
	Author    string    `json:"author"`
	CreatedAt time.Time `json:"created_at"`
	Text      string    `json:"text"`
}

type exportOrganization struct {
	Name    string   `json:"name"`
	Domains []string `json:"domains"`
}

type exportDocument struct {
	ExportedAt       time.Time            `json:"exported_at"`
	ID               string               `json:"id"`
	Name             string               `json:"name"`
	Phone            string               `json:"phone"`
	Addresses        []string             `json:"email_addresses"`
	Organization     *exportOrganization  `json:"organization"`
	CustomAttributes json.RawMessage      `json:"custom_attributes"`
	CreatedAt        time.Time            `json:"created_at"`
	Notes            []exportNote         `json:"notes"`
	Conversations    []exportConversation `json:"conversations"`
}

// safeFilename keeps only a plain base name so an attachment can never escape its directory
// inside the ZIP.
func safeFilename(name string) string {
	name = path.Base(strings.ReplaceAll(name, `\`, "/"))
	var b strings.Builder
	for _, r := range name {
		if r < 0x20 || r == 0x7f || r == '/' || r == '\\' || r == ':' {
			r = '_'
		}
		b.WriteRune(r)
	}
	out := strings.Trim(b.String(), ". ")
	if out == "" {
		return "bijlage"
	}
	if utf8.RuneCountInString(out) > maxFilenameLen {
		out = string([]rune(out)[:maxFilenameLen])
	}
	return out
}

// BuildExport returns the ZIP with everything held about a contact: contact.json with the
// profile, notes and every conversation in the given mailboxes with its message text, plus
// the attachment files.
func BuildExport(ctx context.Context, q *dbq.Queries, store storage.Store, contactID pgtype.UUID, mailboxIDs []pgtype.UUID) ([]byte, error) {
	contact, err := q.ExportContactRow(ctx, contactID)
	if err != nil {
		return nil, fmt.Errorf("load contact: %w", err)
	}
	addresses, err := q.ContactAddressEmails(ctx, contactID)
	if err != nil {
		return nil, fmt.Errorf("load addresses: %w", err)
	}
	notes, err := q.ExportContactNotes(ctx, contactID)
	if err != nil {
		return nil, fmt.Errorf("load notes: %w", err)
	}
	convs, err := q.ExportContactConversations(ctx, dbq.ExportContactConversationsParams{ContactID: contactID, MailboxIds: mailboxIDs})
	if err != nil {
		return nil, fmt.Errorf("load conversations: %w", err)
	}

	doc := exportDocument{
		ExportedAt: time.Now().UTC(), ID: contactID.String(), Name: contact.Name, Phone: contact.Phone,
		Addresses: addresses, CustomAttributes: contact.CustomAttributes, CreatedAt: contact.CreatedAt.Time.UTC(),
		Notes: make([]exportNote, 0, len(notes)), Conversations: make([]exportConversation, 0, len(convs)),
	}
	if contact.OrganizationName.Valid {
		doc.Organization = &exportOrganization{Name: contact.OrganizationName.String, Domains: contact.OrganizationDomains}
	}
	for _, n := range notes {
		doc.Notes = append(doc.Notes, exportNote{Author: n.AuthorName.String, CreatedAt: n.CreatedAt.Time.UTC(), Text: n.Body})
	}

	convIDs := make([]pgtype.UUID, len(convs))
	byID := make(map[pgtype.UUID]*exportConversation, len(convs))
	doc.Conversations = doc.Conversations[:0]
	for i, c := range convs {
		convIDs[i] = c.ID
		doc.Conversations = append(doc.Conversations, exportConversation{
			Number: c.Number, Subject: c.Subject, Status: c.Status, Mailbox: c.MailboxName,
			CreatedAt: c.CreatedAt.Time.UTC(), CustomAttributes: c.CustomAttributes, Messages: []exportMessage{},
		})
	}
	for i, c := range convs {
		byID[c.ID] = &doc.Conversations[i]
	}
	surveys, err := q.ExportCSATResponses(ctx, convIDs)
	if err != nil {
		return nil, fmt.Errorf("load surveys: %w", err)
	}
	for _, sv := range surveys {
		byID[sv.ConversationID].Survey = &exportSurvey{Rating: sv.Rating, Comment: sv.Comment, CreatedAt: sv.CreatedAt.Time.UTC()}
	}
	msgs, err := q.ExportConversationMessages(ctx, convIDs)
	if err != nil {
		return nil, fmt.Errorf("load messages: %w", err)
	}
	msgIDs := make([]pgtype.UUID, len(msgs))
	for i, m := range msgs {
		msgIDs[i] = m.ID
	}
	atts, err := q.ExportMessageAttachments(ctx, msgIDs)
	if err != nil {
		return nil, fmt.Errorf("load attachments: %w", err)
	}

	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	attachmentsOf := map[pgtype.UUID][]exportAttachment{}
	for i, a := range atts {
		entry := exportAttachment{Filename: a.Filename, Size: a.SizeBytes}
		name := fmt.Sprintf("bijlagen/%d-%s", i+1, safeFilename(a.Filename))
		found, err := copyBlob(ctx, zw, store, name, a.BlobKey)
		if err != nil {
			return nil, err
		}
		if found {
			entry.File = name
		}
		attachmentsOf[a.MessageID] = append(attachmentsOf[a.MessageID], entry)
	}
	for _, m := range msgs {
		to, err := decodeAddresses(m.ToAddrs)
		if err != nil {
			return nil, err
		}
		cc, err := decodeAddresses(m.CcAddrs)
		if err != nil {
			return nil, err
		}
		em := exportMessage{
			Kind: m.Kind, From: mail.Address{Name: m.FromName, Address: m.FromAddr}, To: to, Cc: cc,
			Subject: m.Subject, Text: m.BodyText, Author: m.AuthorName.String,
			ReceivedAt: m.ReceivedAt.Time.UTC(), Attachments: attachmentsOf[m.ID],
		}
		if em.Attachments == nil {
			em.Attachments = []exportAttachment{}
		}
		if m.Direction.Valid {
			em.Direction = m.Direction.String
		}
		if m.SentAt.Valid {
			t := m.SentAt.Time.UTC()
			em.SentAt = &t
		}
		conv := byID[m.ConversationID]
		conv.Messages = append(conv.Messages, em)
	}

	w, err := createEntry(zw, "contact.json")
	if err != nil {
		return nil, fmt.Errorf("write zip: %w", err)
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	if err := enc.Encode(doc); err != nil {
		return nil, fmt.Errorf("encode export: %w", err)
	}
	if err := zw.Close(); err != nil {
		return nil, fmt.Errorf("finish zip: %w", err)
	}
	return buf.Bytes(), nil
}

// createEntry starts a compressed ZIP entry stamped with the current time (zip.Create would
// leave the 1980 default).
func createEntry(zw *zip.Writer, name string) (io.Writer, error) {
	return zw.CreateHeader(&zip.FileHeader{Name: name, Method: zip.Deflate, Modified: time.Now().UTC()})
}

func decodeAddresses(raw []byte) ([]mail.Address, error) {
	out := []mail.Address{}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("decode addresses: %w", err)
	}
	return out, nil
}

// copyBlob adds a stored file to the ZIP. A blob that is gone (deleted by retention or a
// previous erasure of a shared file) is reported as not found instead of failing the export.
func copyBlob(ctx context.Context, zw *zip.Writer, store storage.Store, name, key string) (bool, error) {
	rc, err := store.Open(ctx, key)
	if errors.Is(err, storage.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("open attachment: %w", err)
	}
	defer func() { _ = rc.Close() }()
	w, err := createEntry(zw, name)
	if err != nil {
		return false, fmt.Errorf("write zip: %w", err)
	}
	if _, err := io.Copy(w, rc); err != nil {
		return false, fmt.Errorf("copy attachment: %w", err)
	}
	return true, nil
}

// ExportWorker runs contacts.export jobs.
type ExportWorker struct {
	river.WorkerDefaults[jobs.ContactExport]
	q     *dbq.Queries
	blobs Blobs
}

func NewExportWorker(pool *pgxpool.Pool, blobs Blobs) *ExportWorker {
	return &ExportWorker{q: dbq.New(pool), blobs: blobs}
}

func (w *ExportWorker) Timeout(*river.Job[jobs.ContactExport]) time.Duration { return exportTimeout }

func (w *ExportWorker) Work(ctx context.Context, job *river.Job[jobs.ContactExport]) error {
	id, ok := parseID(job.Args.ExportID)
	if !ok {
		return river.JobCancel(errors.New("invalid export id"))
	}
	exp, err := w.q.GetDataExport(ctx, id)
	if err != nil {
		return river.JobCancel(fmt.Errorf("load export: %w", err))
	}
	if exp.Status != "queued" {
		return nil
	}
	if !exp.ContactID.Valid {
		return w.fail(ctx, id, "contact_gone", errors.New("contact no longer exists"))
	}
	requester, err := w.q.GetUser(ctx, exp.RequestedBy)
	if err != nil {
		return fmt.Errorf("load requester: %w", err)
	}
	// Exports cover everything held about a person, so only someone who sees every mailbox may
	// have one; the requester may have lost that since asking.
	if !policy.SeesAll(requester) || requester.DeactivatedAt.Valid {
		return w.fail(ctx, id, "forbidden", errors.New("requester may no longer export contacts"))
	}
	scope, err := policy.MailboxScope(ctx, w.q, requester)
	if err != nil {
		return err
	}
	data, err := BuildExport(ctx, w.q, w.blobs, exp.ContactID, scope.Read)
	if err != nil {
		if ctx.Err() != nil {
			return err
		}
		return w.fail(ctx, id, "export_failed", err)
	}
	key, _, err := w.blobs.Put(ctx, data)
	if err != nil {
		return fmt.Errorf("store export: %w", err)
	}
	if err := w.q.MarkDataExportReady(ctx, dbq.MarkDataExportReadyParams{ID: id, BlobKey: key, SizeBytes: int64(len(data))}); err != nil {
		return fmt.Errorf("mark export ready: %w", err)
	}
	return nil
}

func (w *ExportWorker) fail(ctx context.Context, id pgtype.UUID, code string, cause error) error {
	if err := w.q.MarkDataExportFailed(ctx, dbq.MarkDataExportFailedParams{ID: id, Error: code}); err != nil {
		return fmt.Errorf("mark export failed: %w", err)
	}
	return river.JobCancel(cause)
}

// PurgeWorker runs contacts.purge jobs.
type PurgeWorker struct {
	river.WorkerDefaults[jobs.ContactsPurge]
	q     *dbq.Queries
	blobs Blobs
}

func NewPurgeWorker(pool *pgxpool.Pool, blobs Blobs) *PurgeWorker {
	return &PurgeWorker{q: dbq.New(pool), blobs: blobs}
}

// PurgeInterval is how often expired exports and import files are removed.
const PurgeInterval = time.Hour

func PeriodicJobs() []*river.PeriodicJob {
	return []*river.PeriodicJob{
		river.NewPeriodicJob(river.PeriodicInterval(PurgeInterval),
			func() (river.JobArgs, *river.InsertOpts) {
				return jobs.ContactsPurge{}, &river.InsertOpts{MaxAttempts: 1}
			},
			&river.PeriodicJobOpts{RunOnStart: true}),
	}
}

func (w *PurgeWorker) Work(ctx context.Context, _ *river.Job[jobs.ContactsPurge]) error {
	return w.Run(ctx, time.Now())
}

// Run deletes GDPR exports that were downloaded or expired, and import files and error
// reports older than a week.
func (w *PurgeWorker) Run(ctx context.Context, now time.Time) error {
	var keys []string
	exports, err := w.q.ExpiredDataExports(ctx)
	if err != nil {
		return fmt.Errorf("list expired exports: %w", err)
	}
	for _, e := range exports {
		if err := w.q.MarkDataExportExpired(ctx, e.ID); err != nil {
			return fmt.Errorf("expire export: %w", err)
		}
		keys = append(keys, e.BlobKey)
	}
	imports, err := w.q.ExpiredContactImports(ctx, pgtype.Timestamptz{Time: now.Add(-exportRetention), Valid: true})
	if err != nil {
		return fmt.Errorf("list expired imports: %w", err)
	}
	for _, i := range imports {
		if err := w.q.ClearContactImportBlobs(ctx, i.ID); err != nil {
			return fmt.Errorf("expire import: %w", err)
		}
		keys = append(keys, i.SourceKey, i.ErrorKey)
	}
	if err := QueueBlobDeletions(ctx, w.q, keys); err != nil {
		return err
	}
	failed, err := DrainBlobDeletions(ctx, w.q, w.blobs)
	if err != nil {
		return err
	}
	if failed > 0 {
		return fmt.Errorf("%d stored files could not be deleted", failed)
	}
	return nil
}
