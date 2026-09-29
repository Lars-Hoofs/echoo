package api

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"echoo/internal/audit"
	"echoo/internal/contacts"
	"echoo/internal/db"
	"echoo/internal/db/dbq"
	"echoo/internal/jobs"
	"echoo/internal/policy"
)

var (
	errStorageUnavailable = &apiError{Status: http.StatusInternalServerError, Code: "internal", Message: "storage is not configured"}
	errGone               = &apiError{Status: http.StatusGone, Code: "gone", Message: "this export was already downloaded or has expired"}
)

func (s *Server) blobs() (contacts.Blobs, error) {
	if s.store == nil {
		return nil, errStorageUnavailable
	}
	return contacts.AsBlobs(s.store)
}

type eraseRequest struct {
	Confirmation string `json:"confirmation"`
}

// eraseContact deletes a contact for good (GDPR). The caller types the contact's primary
// address, which guards against erasing the wrong record.
func (s *Server) eraseContact(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if err := requireSeesAll(sessionFrom(ctx).User); err != nil {
		writeError(w, r, err)
		return
	}
	id, ok := parseUUID(chi.URLParam(r, "id"))
	if !ok {
		writeError(w, r, errNotFound)
		return
	}
	var req eraseRequest
	if err := decode(r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	blobs, err := s.blobs()
	if err != nil {
		writeError(w, r, err)
		return
	}
	emails, err := s.q.ContactAddressEmails(ctx, id)
	if err != nil {
		writeError(w, r, fmt.Errorf("load addresses: %w", err))
		return
	}
	if len(emails) == 0 {
		writeError(w, r, errNotFound)
		return
	}
	if !strings.EqualFold(strings.TrimSpace(req.Confirmation), emails[0]) {
		writeError(w, r, errValidation(map[string]string{"confirmation": "mismatch"}))
		return
	}
	user := sessionFrom(ctx).User
	res, err := contacts.Erase(ctx, s.pool, blobs, actorOf(r, user), id)
	if errors.Is(err, contacts.ErrNotFound) {
		writeError(w, r, errNotFound)
		return
	}
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"erased": map[string]any{
		"conversations_erased": res.ConversationsErased, "conversations_kept": res.ConversationsKept,
		"messages_blanked": res.MessagesBlanked, "messages_tombstoned": res.MessagesTombstoned,
		"attachments_deleted": res.AttachmentsDeleted, "files_pending_deletion": res.BlobDeletionsPending,
	}})
}

type dataExportJSON struct {
	ID        string    `json:"id"`
	Status    string    `json:"status"`
	Error     string    `json:"error"`
	SizeBytes int64     `json:"size_bytes"`
	CreatedAt time.Time `json:"created_at"`
	ExpiresAt time.Time `json:"expires_at"`
}

func toDataExportJSON(e dbq.DataExport) dataExportJSON {
	return dataExportJSON{
		ID: uuidStr(e.ID), Status: e.Status, Error: e.Error, SizeBytes: e.SizeBytes,
		CreatedAt: e.CreatedAt.Time.UTC(), ExpiresAt: e.ExpiresAt.Time.UTC(),
	}
}

func (s *Server) requestContactDataExport(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if err := requireSeesAll(sessionFrom(ctx).User); err != nil {
		writeError(w, r, err)
		return
	}
	id, ok := parseUUID(chi.URLParam(r, "id"))
	if !ok {
		writeError(w, r, errNotFound)
		return
	}
	if s.jobs == nil {
		writeError(w, r, errors.New("job queue is not configured"))
		return
	}
	if _, err := s.blobs(); err != nil {
		writeError(w, r, err)
		return
	}
	user := sessionFrom(ctx).User
	var exportID pgtype.UUID
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		q := dbq.New(tx)
		if _, err := q.LockContact(ctx, id); errors.Is(err, pgx.ErrNoRows) {
			return errNotFound
		} else if err != nil {
			return fmt.Errorf("lock contact: %w", err)
		}
		var err error
		if exportID, err = q.InsertDataExport(ctx, dbq.InsertDataExportParams{ContactID: id, RequestedBy: user.ID}); err != nil {
			return fmt.Errorf("create export: %w", err)
		}
		if _, err := s.jobs.InsertTx(ctx, tx, jobs.ContactExport{ExportID: uuidStr(exportID)}, nil); err != nil {
			return fmt.Errorf("enqueue export: %w", err)
		}
		return audit.Write(ctx, q, audit.Entry{Actor: user.ID, IP: clientFrom(r).IP, Action: audit.ContactDataExportMade, TargetType: "contact", TargetID: uuidStr(id)})
	})
	if err != nil {
		writeError(w, r, err)
		return
	}
	exp, err := s.q.GetDataExport(ctx, exportID)
	if err != nil {
		writeError(w, r, fmt.Errorf("load export: %w", err))
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"export": toDataExportJSON(exp)})
}

// ownDataExport loads an export that the caller requested; anyone else gets a 404.
func (s *Server) ownDataExport(r *http.Request) (dbq.DataExport, error) {
	id, ok := parseUUID(chi.URLParam(r, "id"))
	if !ok {
		return dbq.DataExport{}, errNotFound
	}
	exp, err := s.q.GetDataExport(r.Context(), id)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && exp.RequestedBy != sessionFrom(r.Context()).User.ID) {
		return dbq.DataExport{}, errNotFound
	}
	if err != nil {
		return dbq.DataExport{}, fmt.Errorf("load export: %w", err)
	}
	return exp, nil
}

func (s *Server) getDataExport(w http.ResponseWriter, r *http.Request) {
	exp, err := s.ownDataExport(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"export": toDataExportJSON(exp)})
}

// downloadDataExport hands the ZIP to the requester exactly once: claiming it flips the
// status atomically, and the file is deleted as soon as it was sent.
func (s *Server) downloadDataExport(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	exp, err := s.ownDataExport(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	blobs, err := s.blobs()
	if err != nil {
		writeError(w, r, err)
		return
	}
	switch exp.Status {
	case "queued":
		writeError(w, r, &apiError{Status: http.StatusConflict, Code: "not_ready", Message: "the export is still being prepared"})
		return
	case "failed":
		writeError(w, r, &apiError{Status: http.StatusConflict, Code: "export_failed", Message: "the export failed"})
		return
	case "downloaded", "expired":
		writeError(w, r, errGone)
		return
	}
	user := sessionFrom(ctx).User
	claimed, err := s.q.ClaimDataExportDownload(ctx, dbq.ClaimDataExportDownloadParams{ID: exp.ID, RequestedBy: user.ID})
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, r, errGone)
		return
	}
	if err != nil {
		writeError(w, r, fmt.Errorf("claim export: %w", err))
		return
	}
	err = db.InTx(ctx, s.pool, func(q *dbq.Queries) error {
		return audit.Write(ctx, q, audit.Entry{Actor: user.ID, IP: clientFrom(r).IP, Action: audit.ContactDataExportLoaded, TargetType: "contact", TargetID: uuidStr(exp.ContactID)})
	})
	if err != nil {
		writeError(w, r, err)
		return
	}
	rc, err := blobs.Open(ctx, claimed.BlobKey)
	if err != nil {
		writeError(w, r, fmt.Errorf("open export: %w", err))
		return
	}
	defer func() { _ = rc.Close() }()
	if err := http.NewResponseController(w).SetWriteDeadline(time.Now().Add(10 * time.Minute)); err != nil && !errors.Is(err, http.ErrNotSupported) {
		writeError(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", `attachment; filename="contact-export.zip"`)
	w.Header().Set("Content-Length", strconv.FormatInt(claimed.SizeBytes, 10))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if _, err := io.Copy(w, rc); err != nil {
		slog.WarnContext(ctx, "data export transfer interrupted", "export_id", uuidStr(exp.ID), "err", err)
		return // stays claimed; the purge job removes the file
	}
	if err := s.q.ClearDataExportBlob(ctx, exp.ID); err != nil {
		slog.ErrorContext(ctx, "clear data export blob", "export_id", uuidStr(exp.ID), "err", err)
		return
	}
	contacts.DeleteUnreferenced(ctx, s.q, blobs, []string{claimed.BlobKey})
}

// Notes

type noteJSON struct {
	ID        string    `json:"id"`
	Body      string    `json:"body"`
	Author    *refJSON  `json:"author"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	CanEdit   bool      `json:"can_edit"`
}

type noteBodyRequest struct {
	Body string `json:"body"`
}

func cleanNoteBody(s string) (string, bool) {
	s = strings.TrimSpace(s)
	if s == "" || utf8.RuneCountInString(s) > maxNoteRunes {
		return "", false
	}
	for _, r := range s {
		if (r < 0x20 && r != '\n' && r != '\r' && r != '\t') || r == 0x7f {
			return "", false
		}
	}
	return s, true
}

func canEditNote(u dbq.User, authorID pgtype.UUID) bool {
	return requireModifier(u) == nil && (authorID == u.ID || policy.Has(u, policy.ContactsModerate))
}

// noteOwner identifies the contact or organization a set of notes hangs off.
type noteOwner struct {
	entity string
	id     pgtype.UUID
}

func (s *Server) noteOwnerFrom(r *http.Request, entity string, v contacts.Viewer) (noteOwner, error) {
	id, ok := parseUUID(chi.URLParam(r, "id"))
	if !ok {
		return noteOwner{}, errNotFound
	}
	var visible bool
	var err error
	if entity == contacts.EntityContact {
		visible, err = v.ContactVisible(r.Context(), s.q, id)
	} else {
		visible, err = v.OrganizationVisible(r.Context(), s.q, id)
	}
	if err != nil {
		return noteOwner{}, fmt.Errorf("check visibility: %w", err)
	}
	if !visible {
		return noteOwner{}, errNotFound
	}
	return noteOwner{entity: entity, id: id}, nil
}

func (s *Server) listNotes(entity string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		user, v, err := s.contactViewer(r)
		if err != nil {
			writeError(w, r, err)
			return
		}
		owner, err := s.noteOwnerFrom(r, entity, v)
		if err != nil {
			writeError(w, r, err)
			return
		}
		limit := int32(noteListLimit)
		if n, err := strconv.ParseInt(r.URL.Query().Get("limit"), 10, 32); err == nil && n >= 1 && n < int64(limit) {
			limit = int32(n)
		}
		out := []noteJSON{}
		if entity == contacts.EntityContact {
			rows, err := s.q.ListContactNotes(ctx, dbq.ListContactNotesParams{ContactID: owner.id, RowLimit: limit})
			if err != nil {
				writeError(w, r, fmt.Errorf("list notes: %w", err))
				return
			}
			for _, n := range rows {
				out = append(out, noteOf(user, n.ID, n.Body, n.AuthorID, n.AuthorName, n.CreatedAt, n.UpdatedAt))
			}
		} else {
			rows, err := s.q.ListOrganizationNotes(ctx, dbq.ListOrganizationNotesParams{OrganizationID: owner.id, RowLimit: limit})
			if err != nil {
				writeError(w, r, fmt.Errorf("list notes: %w", err))
				return
			}
			for _, n := range rows {
				out = append(out, noteOf(user, n.ID, n.Body, n.AuthorID, n.AuthorName, n.CreatedAt, n.UpdatedAt))
			}
		}
		writeJSON(w, http.StatusOK, map[string]any{"notes": out})
	}
}

func noteOf(user dbq.User, id pgtype.UUID, body string, authorID pgtype.UUID, authorName pgtype.Text, created, updated pgtype.Timestamptz) noteJSON {
	n := noteJSON{
		ID: uuidStr(id), Body: body, CreatedAt: created.Time.UTC(), UpdatedAt: updated.Time.UTC(),
		CanEdit: canEditNote(user, authorID),
	}
	if authorID.Valid {
		n.Author = &refJSON{ID: uuidStr(authorID), Name: authorName.String}
	}
	return n
}

func (s *Server) createNote(entity string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		user, v, err := s.contactViewer(r)
		if err == nil {
			err = requireModifier(user)
		}
		if err != nil {
			writeError(w, r, err)
			return
		}
		owner, err := s.noteOwnerFrom(r, entity, v)
		if err != nil {
			writeError(w, r, err)
			return
		}
		var req noteBodyRequest
		if err := decode(r, &req); err != nil {
			writeError(w, r, err)
			return
		}
		body, ok := cleanNoteBody(req.Body)
		if !ok {
			writeError(w, r, errValidation(map[string]string{"body": "invalid"}))
			return
		}
		params := dbq.InsertCRMNoteParams{AuthorUserID: user.ID, Body: body}
		if entity == contacts.EntityContact {
			params.ContactID = owner.id
		} else {
			params.OrganizationID = owner.id
		}
		id, err := s.q.InsertCRMNote(ctx, params)
		if err != nil {
			writeError(w, r, fmt.Errorf("create note: %w", err))
			return
		}
		n, err := s.q.GetCRMNoteView(ctx, id)
		if err != nil {
			writeError(w, r, fmt.Errorf("load note: %w", err))
			return
		}
		writeJSON(w, http.StatusCreated, map[string]any{"note": noteOf(user, n.ID, n.Body, n.AuthorID, n.AuthorName, n.CreatedAt, n.UpdatedAt)})
	}
}

// ownedNote loads a note that belongs to the entity in the URL and that the caller may edit.
func (s *Server) ownedNote(r *http.Request, entity string) (dbq.GetCRMNoteRow, dbq.User, error) {
	user, v, err := s.contactViewer(r)
	if err != nil {
		return dbq.GetCRMNoteRow{}, user, err
	}
	owner, err := s.noteOwnerFrom(r, entity, v)
	if err != nil {
		return dbq.GetCRMNoteRow{}, user, err
	}
	noteID, ok := parseUUID(chi.URLParam(r, "noteId"))
	if !ok {
		return dbq.GetCRMNoteRow{}, user, errNotFound
	}
	note, err := s.q.GetCRMNote(r.Context(), noteID)
	if errors.Is(err, pgx.ErrNoRows) {
		return dbq.GetCRMNoteRow{}, user, errNotFound
	}
	if err != nil {
		return dbq.GetCRMNoteRow{}, user, fmt.Errorf("load note: %w", err)
	}
	if (entity == contacts.EntityContact && note.ContactID != owner.id) || (entity == contacts.EntityOrganization && note.OrganizationID != owner.id) {
		return dbq.GetCRMNoteRow{}, user, errNotFound
	}
	if !canEditNote(user, note.AuthorUserID) {
		return dbq.GetCRMNoteRow{}, user, errForbidden
	}
	return note, user, nil
}

func (s *Server) updateNote(entity string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		note, user, err := s.ownedNote(r, entity)
		if err != nil {
			writeError(w, r, err)
			return
		}
		var req noteBodyRequest
		if err := decode(r, &req); err != nil {
			writeError(w, r, err)
			return
		}
		body, ok := cleanNoteBody(req.Body)
		if !ok {
			writeError(w, r, errValidation(map[string]string{"body": "invalid"}))
			return
		}
		if err := s.q.UpdateCRMNote(r.Context(), dbq.UpdateCRMNoteParams{ID: note.ID, Body: body}); err != nil {
			writeError(w, r, fmt.Errorf("update note: %w", err))
			return
		}
		n, err := s.q.GetCRMNoteView(r.Context(), note.ID)
		if err != nil {
			writeError(w, r, fmt.Errorf("load note: %w", err))
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"note": noteOf(user, n.ID, n.Body, n.AuthorID, n.AuthorName, n.CreatedAt, n.UpdatedAt)})
	}
}

func (s *Server) deleteNote(entity string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		note, _, err := s.ownedNote(r, entity)
		if err != nil {
			writeError(w, r, err)
			return
		}
		if err := s.q.DeleteCRMNote(r.Context(), note.ID); err != nil {
			writeError(w, r, fmt.Errorf("delete note: %w", err))
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}
