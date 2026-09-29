package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"echoo/internal/compose"
	"echoo/internal/db/dbq"
	"echoo/internal/mail"
)

type draftRequest struct {
	To            []mail.Address `json:"to"`
	Cc            []mail.Address `json:"cc"`
	Bcc           []mail.Address `json:"bcc"`
	Subject       string         `json:"subject"`
	HTML          string         `json:"html"`
	AttachmentIDs []string       `json:"attachment_ids"`
}

type draftJSON struct {
	To          []mail.Address `json:"to"`
	Cc          []mail.Address `json:"cc"`
	Bcc         []mail.Address `json:"bcc"`
	Subject     string         `json:"subject"`
	HTML        string         `json:"html"`
	Attachments []uploadJSON   `json:"attachments"`
	UpdatedAt   time.Time      `json:"updated_at"`
}

func (s *Server) putDraft(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	user := sessionFrom(ctx).User
	conv, err := s.writableConversation(ctx, user, chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, r, err)
		return
	}
	var req draftRequest
	if err := decode(r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	fields := map[string]string{}
	var lists [3][]mail.Address
	for i, f := range []struct {
		name string
		src  []mail.Address
	}{{"to", req.To}, {"cc", req.Cc}, {"bcc", req.Bcc}} {
		if lists[i], err = compose.NormalizeAddresses(f.src); err != nil {
			fields[f.name] = "invalid"
		}
	}
	if len(req.HTML) > maxBodyHTMLBytes {
		fields["html"] = "too_large"
	}
	if len(req.AttachmentIDs) > maxAttachments {
		fields["attachment_ids"] = "too_many"
	}
	ids := make([]pgtype.UUID, 0, len(req.AttachmentIDs))
	for _, v := range req.AttachmentIDs {
		id, ok := parseUUID(v)
		if !ok {
			fields["attachment_ids"] = "invalid"
			break
		}
		ids = append(ids, id)
	}
	if len(fields) > 0 {
		writeError(w, r, errValidation(fields))
		return
	}
	// A draft only keeps attachments that still exist and belong to its author; uploads expire
	// after a day, so the rest are dropped rather than failing every autosave.
	live, err := s.q.ComposeListUploads(ctx, dbq.ComposeListUploadsParams{Ids: ids, UserID: user.ID})
	if err != nil {
		writeError(w, r, fmt.Errorf("list uploads: %w", err))
		return
	}
	cids := make([]string, len(live))
	liveIDs := make([]pgtype.UUID, len(live))
	for i, u := range live {
		cids[i], liveIDs[i] = u.ContentID, u.ID
	}
	var raw [3][]byte
	for i, l := range lists {
		if raw[i], err = json.Marshal(nonNilAddrs(l)); err != nil {
			writeError(w, r, fmt.Errorf("encode addresses: %w", err))
			return
		}
	}
	row, err := s.q.ComposeUpsertDraft(ctx, dbq.ComposeUpsertDraftParams{
		ConversationID: conv.ID, UserID: user.ID, ToAddrs: raw[0], CcAddrs: raw[1], BccAddrs: raw[2],
		Subject: req.Subject, BodyHtml: compose.Sanitize(req.HTML, compose.SanitizeOptions{ContentIDs: cids}), AttachmentIds: liveIDs,
	})
	if err != nil {
		writeError(w, r, fmt.Errorf("save draft: %w", err))
		return
	}
	out, err := toDraftJSON(row, live)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"draft": out})
}

func (s *Server) getDraft(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	user := sessionFrom(ctx).User
	conv, err := s.writableConversation(ctx, user, chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, r, err)
		return
	}
	row, err := s.q.ComposeGetDraft(ctx, dbq.ComposeGetDraftParams{ConversationID: conv.ID, UserID: user.ID})
	if errors.Is(err, pgx.ErrNoRows) {
		writeJSON(w, http.StatusOK, map[string]any{"draft": nil})
		return
	}
	if err != nil {
		writeError(w, r, fmt.Errorf("load draft: %w", err))
		return
	}
	live, err := s.q.ComposeListUploads(ctx, dbq.ComposeListUploadsParams{Ids: row.AttachmentIds, UserID: user.ID})
	if err != nil {
		writeError(w, r, fmt.Errorf("list uploads: %w", err))
		return
	}
	out, err := toDraftJSON(row, live)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"draft": out})
}

func (s *Server) deleteDraft(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	user := sessionFrom(ctx).User
	conv, err := s.writableConversation(ctx, user, chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, r, err)
		return
	}
	if err := s.q.ComposeDeleteDraft(ctx, dbq.ComposeDeleteDraftParams{ConversationID: conv.ID, UserID: user.ID}); err != nil {
		writeError(w, r, fmt.Errorf("delete draft: %w", err))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func toDraftJSON(d dbq.Draft, uploads []dbq.Upload) (draftJSON, error) {
	out := draftJSON{Subject: d.Subject, HTML: d.BodyHtml, UpdatedAt: d.UpdatedAt.Time.UTC(), Attachments: make([]uploadJSON, len(uploads))}
	for i, u := range uploads {
		out.Attachments[i] = toUploadJSON(u)
	}
	var err error
	for _, f := range []struct {
		dst *[]mail.Address
		raw []byte
	}{{&out.To, d.ToAddrs}, {&out.Cc, d.CcAddrs}, {&out.Bcc, d.BccAddrs}} {
		if *f.dst, err = decodeAddresses(f.raw); err != nil {
			return out, fmt.Errorf("decode draft addresses: %w", err)
		}
	}
	return out, nil
}
