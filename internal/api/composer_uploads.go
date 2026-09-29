package api

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"echoo/internal/compose"
	"echoo/internal/contacts"
	"echoo/internal/db/dbq"
	"echoo/internal/policy"
	"echoo/internal/scan"
	"echoo/internal/sniff"
)

// multipartOverhead is what a request may exceed the file limit by, for boundaries and the
// other form fields.
const multipartOverhead = 1 << 20

type uploadJSON struct {
	ID          string    `json:"id"`
	Filename    string    `json:"filename"`
	Size        int64     `json:"size"`
	ContentType string    `json:"content_type"`
	ContentID   string    `json:"content_id"`
	ExpiresAt   time.Time `json:"expires_at"`
}

func toUploadJSON(u dbq.Upload) uploadJSON {
	return uploadJSON{ID: uuidStr(u.ID), Filename: u.Filename, Size: u.SizeBytes, ContentType: u.ContentType, ContentID: u.ContentID, ExpiresAt: u.ExpiresAt.Time.UTC()}
}

var errUploadInfected = &apiError{Status: http.StatusUnprocessableEntity, Code: "attachment_infected", Message: "the virus scanner flagged this file"}

var errFileTooLarge = &apiError{Status: http.StatusRequestEntityTooLarge, Code: "file_too_large", Message: "the file exceeds the attachment limit"}

// createUpload stores one file from a multipart request. The file is read through a limit, so
// an oversized upload is cut off instead of buffered. Its type comes from its content.
func (s *Server) createUpload(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	user := sessionFrom(ctx).User
	if !policy.Has(user, policy.ConversationsWrite) {
		writeError(w, r, errForbidden)
		return
	}
	if s.store == nil {
		writeError(w, r, errors.New("blob storage is not configured"))
		return
	}
	limit := s.cfg.MaxAttachmentBytes()
	// The server-wide read timeout is short; a large file on a slow connection needs longer.
	if err := http.NewResponseController(w).SetReadDeadline(time.Now().Add(5 * time.Minute)); err != nil && !errors.Is(err, http.ErrNotSupported) {
		writeError(w, r, fmt.Errorf("extend read deadline: %w", err))
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, limit+multipartOverhead)
	mr, err := r.MultipartReader()
	if err != nil {
		writeError(w, r, errBadRequest("expected a multipart/form-data body"))
		return
	}
	for {
		part, err := mr.NextPart()
		if errors.Is(err, io.EOF) {
			writeError(w, r, errValidation(map[string]string{"file": "required"}))
			return
		}
		if err != nil {
			writeError(w, r, uploadReadError(err))
			return
		}
		if part.FormName() != "file" {
			continue
		}
		data, err := io.ReadAll(io.LimitReader(part, limit+1))
		if err != nil {
			writeError(w, r, uploadReadError(err))
			return
		}
		if int64(len(data)) > limit {
			writeError(w, r, errFileTooLarge)
			return
		}
		if len(data) == 0 {
			writeError(w, r, errValidation(map[string]string{"file": "empty"}))
			return
		}
		if s.scanner != nil {
			res := s.scanner.Scan(ctx, data)
			if res.Status == scan.StatusInfected {
				slog.WarnContext(ctx, "upload rejected by virus scanner", "signature", res.Detail)
				writeError(w, r, errUploadInfected)
				return
			}
			if res.Status == scan.StatusError {
				slog.WarnContext(ctx, "upload not scanned", "reason", res.Detail)
			}
		}
		key, sum, err := s.store.Put(ctx, data)
		if err != nil {
			writeError(w, r, fmt.Errorf("store upload: %w", err))
			return
		}
		cid, err := compose.NewContentID()
		if err != nil {
			writeError(w, r, err)
			return
		}
		row, err := s.q.ComposeInsertUpload(ctx, dbq.ComposeInsertUploadParams{
			UserID: user.ID, Filename: compose.SafeFilename(part.FileName()), ContentType: sniff.Type(data),
			SizeBytes: int64(len(data)), Sha256: sum, BlobKey: key, ContentID: cid,
		})
		if err != nil {
			writeError(w, r, fmt.Errorf("insert upload: %w", err))
			return
		}
		writeJSON(w, http.StatusCreated, toUploadJSON(row))
		return
	}
}

func uploadReadError(err error) error {
	var tooBig *http.MaxBytesError
	if errors.As(err, &tooBig) {
		return errFileTooLarge
	}
	return errBadRequest("could not read the upload")
}

func (s *Server) deleteUpload(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id, ok := parseUUID(chi.URLParam(r, "id"))
	if !ok {
		writeError(w, r, errNotFound)
		return
	}
	blobs, err := s.blobs()
	if err != nil {
		writeError(w, r, err)
		return
	}
	keys, err := s.q.ComposeDeleteUpload(ctx, dbq.ComposeDeleteUploadParams{ID: id, UserID: sessionFrom(ctx).User.ID})
	if err != nil {
		writeError(w, r, fmt.Errorf("delete upload: %w", err))
		return
	}
	if len(keys) == 0 {
		writeError(w, r, errNotFound)
		return
	}
	// Expired uploads are removed by the uploads.purge job; this one is removed by its owner.
	contacts.DeleteUnreferenced(ctx, s.q, blobs, keys)
	w.WriteHeader(http.StatusNoContent)
}
