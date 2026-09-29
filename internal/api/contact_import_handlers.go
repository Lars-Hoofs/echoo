package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"echoo/internal/audit"
	"echoo/internal/contacts"
	"echoo/internal/db/dbq"
	"echoo/internal/jobs"
)

const maxOptionsBytes = 64 << 10

type contactImportJSON struct {
	ID            string     `json:"id"`
	Status        string     `json:"status"`
	Filename      string     `json:"filename"`
	TotalRows     int32      `json:"total_rows"`
	ProcessedRows int32      `json:"processed_rows"`
	CreatedCount  int32      `json:"created_count"`
	UpdatedCount  int32      `json:"updated_count"`
	SkippedCount  int32      `json:"skipped_count"`
	FailedCount   int32      `json:"failed_count"`
	HasErrors     bool       `json:"has_errors"`
	Error         string     `json:"error"`
	CreatedAt     time.Time  `json:"created_at"`
	FinishedAt    *time.Time `json:"finished_at"`
}

func toContactImportJSON(i dbq.ContactImport) contactImportJSON {
	return contactImportJSON{
		ID: uuidStr(i.ID), Status: i.Status, Filename: i.Filename, TotalRows: i.TotalRows, ProcessedRows: i.ProcessedRows,
		CreatedCount: i.CreatedCount, UpdatedCount: i.UpdatedCount, SkippedCount: i.SkippedCount, FailedCount: i.FailedCount,
		HasErrors: i.ErrorKey != "", Error: i.Error, CreatedAt: i.CreatedAt.Time.UTC(), FinishedAt: timeOrNil(i.FinishedAt),
	}
}

// readImportUpload reads the multipart body: a "file" part (the CSV) and an "options" part
// (JSON, see contacts.ImportOptions).
func readImportUpload(w http.ResponseWriter, r *http.Request) (filename string, data []byte, options contacts.ImportOptions, err error) {
	r.Body = http.MaxBytesReader(w, r.Body, contacts.MaxImportBytes+maxOptionsBytes+(1<<20))
	mr, err := r.MultipartReader()
	if err != nil {
		return "", nil, options, errBadRequest("expected multipart/form-data")
	}
	var haveFile, haveOptions bool
	for {
		part, err := mr.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return "", nil, options, importUploadError(err)
		}
		switch part.FormName() {
		case "file":
			if data, err = readPart(part, contacts.MaxImportBytes); err != nil {
				return "", nil, options, err
			}
			filename, haveFile = part.FileName(), true
		case "options":
			raw, err := readPart(part, maxOptionsBytes)
			if err != nil {
				return "", nil, options, err
			}
			if err := json.Unmarshal(raw, &options); err != nil {
				return "", nil, options, errValidation(map[string]string{"options": "invalid"})
			}
			haveOptions = true
		}
	}
	if !haveFile {
		return "", nil, options, errValidation(map[string]string{"file": "required"})
	}
	if !haveOptions {
		return "", nil, options, errValidation(map[string]string{"options": "required"})
	}
	return filename, data, options, nil
}

func readPart(p *multipart.Part, limit int64) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(p, limit+1))
	if err != nil {
		return nil, importUploadError(err)
	}
	if int64(len(data)) > limit {
		return nil, errValidation(map[string]string{p.FormName(): "too_large"})
	}
	return data, nil
}

func importUploadError(err error) error {
	var tooBig *http.MaxBytesError
	if errors.As(err, &tooBig) {
		return errValidation(map[string]string{"file": "too_large"})
	}
	return errBadRequest("invalid upload")
}

func importFileProblem(err error) error {
	code := map[error]string{
		contacts.ErrNotUTF8: "not_utf8", contacts.ErrTooManyRows: "too_many_rows",
		contacts.ErrEmptyCSV: "empty", contacts.ErrBadCSV: "invalid_csv",
	}
	for target, c := range code {
		if errors.Is(err, target) {
			return errValidation(map[string]string{"file": c})
		}
	}
	return errValidation(map[string]string{"options": "invalid"})
}

func (s *Server) createContactImport(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	user := sessionFrom(ctx).User
	if s.jobs == nil {
		writeError(w, r, errors.New("job queue is not configured"))
		return
	}
	blobs, err := s.blobs()
	if err != nil {
		writeError(w, r, err)
		return
	}
	if err := http.NewResponseController(w).SetReadDeadline(time.Now().Add(5 * time.Minute)); err != nil && !errors.Is(err, http.ErrNotSupported) {
		writeError(w, r, err)
		return
	}
	filename, data, options, err := readImportUpload(w, r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	defs, err := s.attributeDefs(ctx, contacts.EntityContact)
	if err != nil {
		writeError(w, r, err)
		return
	}
	prepared, err := contacts.PrepareImport(data, options, defs)
	if err != nil {
		writeError(w, r, importFileProblem(err))
		return
	}
	mapping, err := json.Marshal(prepared.Options)
	if err != nil {
		writeError(w, r, fmt.Errorf("encode mapping: %w", err))
		return
	}
	key, _, err := blobs.Put(ctx, data)
	if err != nil {
		writeError(w, r, fmt.Errorf("store import file: %w", err))
		return
	}
	if len(filename) > 200 {
		filename = filename[:200]
	}
	var id pgtype.UUID
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		q := dbq.New(tx)
		var err error
		id, err = q.InsertContactImport(ctx, dbq.InsertContactImportParams{
			CreatedBy: user.ID, Filename: filename, Dedupe: prepared.Options.Dedupe, Delimiter: prepared.Options.Delimiter,
			Mapping: mapping, SourceKey: key, TotalRows: int32(len(prepared.Table.Rows)), //nolint:gosec // bounded by MaxImportRows
		})
		if err != nil {
			return fmt.Errorf("create import: %w", err)
		}
		if _, err := s.jobs.InsertTx(ctx, tx, jobs.ContactImport{ImportID: uuidStr(id)}, nil); err != nil {
			return fmt.Errorf("enqueue import: %w", err)
		}
		return audit.Write(ctx, q, audit.Entry{
			Actor: user.ID, IP: clientFrom(r).IP, Action: audit.ContactImportStarted, TargetType: "contact_import", TargetID: uuidStr(id),
			Metadata: map[string]any{"rows": len(prepared.Table.Rows), "dedupe": prepared.Options.Dedupe},
		})
	})
	if err != nil {
		contacts.DeleteUnreferenced(ctx, s.q, blobs, []string{key})
		writeError(w, r, err)
		return
	}
	imp, err := s.q.GetContactImport(ctx, id)
	if err != nil {
		writeError(w, r, fmt.Errorf("load import: %w", err))
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"import": toContactImportJSON(imp)})
}

func (s *Server) loadContactImport(r *http.Request) (dbq.ContactImport, error) {
	id, ok := parseUUID(chi.URLParam(r, "id"))
	if !ok {
		return dbq.ContactImport{}, errNotFound
	}
	imp, err := s.q.GetContactImport(r.Context(), id)
	if errors.Is(err, pgx.ErrNoRows) {
		return dbq.ContactImport{}, errNotFound
	}
	if err != nil {
		return dbq.ContactImport{}, fmt.Errorf("load import: %w", err)
	}
	return imp, nil
}

func (s *Server) getContactImport(w http.ResponseWriter, r *http.Request) {
	imp, err := s.loadContactImport(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"import": toContactImportJSON(imp)})
}

func (s *Server) downloadContactImportErrors(w http.ResponseWriter, r *http.Request) {
	imp, err := s.loadContactImport(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	if imp.ErrorKey == "" {
		writeError(w, r, errNotFound)
		return
	}
	blobs, err := s.blobs()
	if err != nil {
		writeError(w, r, err)
		return
	}
	rc, err := blobs.Open(r.Context(), imp.ErrorKey)
	if err != nil {
		writeError(w, r, fmt.Errorf("open error report: %w", err))
		return
	}
	defer func() { _ = rc.Close() }()
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="import-fouten-`+strconv.Itoa(int(imp.FailedCount))+`-rijen.csv"`)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if _, err := io.Copy(w, rc); err != nil {
		writeStreamError(r, "import error report", err)
	}
}
