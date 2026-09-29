package api

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"

	"echoo/internal/audit"
	"echoo/internal/db"
	"echoo/internal/db/dbq"
	"echoo/internal/policy"
	"echoo/internal/scan"
	"echoo/internal/sniff"
)

var errAttachmentInfected = &apiError{Status: http.StatusForbidden, Code: "attachment_infected", Message: "the virus scanner flagged this attachment"}

// contentDisposition builds an RFC 6266 attachment header: an ASCII filename for old clients
// and the UTF-8 filename* form that current browsers prefer.
func contentDisposition(name string) string {
	fallback := strings.Map(func(r rune) rune {
		if r < 0x20 || r > 0x7e || r == '"' || r == '\\' || r == '%' {
			return '_'
		}
		return r
	}, name)
	if strings.Trim(fallback, "_ ") == "" {
		fallback = "attachment"
	}
	var enc strings.Builder
	for _, b := range []byte(name) {
		switch {
		case b >= 'a' && b <= 'z', b >= 'A' && b <= 'Z', b >= '0' && b <= '9', strings.IndexByte("!#$&+-.^_`|~", b) >= 0:
			enc.WriteByte(b)
		default:
			fmt.Fprintf(&enc, "%%%02X", b)
		}
	}
	return `attachment; filename="` + fallback + `"; filename*=UTF-8''` + enc.String()
}

// downloadAttachment serves the stored bytes as a download only. Dangerous types are still
// served, but as opaque bytes; the UI warns before requesting them.
func (s *Server) downloadAttachment(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id, ok := parseUUID(chi.URLParam(r, "id"))
	if !ok {
		writeError(w, r, errNotFound)
		return
	}
	scope, err := policy.MailboxScope(ctx, s.q, sessionFrom(ctx).User)
	if err != nil {
		writeError(w, r, err)
		return
	}
	att, err := s.q.GetAttachmentForDownload(ctx, dbq.GetAttachmentForDownloadParams{ID: id, MailboxIds: scope.Read})
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, r, errNotFound)
		return
	}
	if err != nil {
		writeError(w, r, fmt.Errorf("load attachment: %w", err))
		return
	}
	if att.ScanStatus == scan.StatusInfected {
		writeError(w, r, errAttachmentInfected)
		return
	}
	rc, err := s.openBlob(ctx, att.BlobKey)
	if err != nil {
		writeError(w, r, err)
		return
	}
	defer func() { _ = rc.Close() }()

	contentType := att.SniffedType
	if contentType == "" || sniff.Dangerous(att.Filename, att.SniffedType) {
		contentType = "application/octet-stream"
	}
	h := w.Header()
	h.Set("Content-Type", contentType)
	h.Set("Content-Disposition", contentDisposition(att.Filename))
	h.Set("Content-Security-Policy", "sandbox")
	h.Set("Content-Length", strconv.FormatInt(att.SizeBytes, 10))
	// The client may have gone away mid-stream; the status line is already sent.
	_, _ = io.Copy(w, rc)
}

type allowImagesResponse struct {
	RenderURL string `json:"render_url"`
}

// allowImages lets a message show its remote images: once (this view only, any reader), or for
// the sender or the sender's domain in this mailbox (persisted, needs write access, audited).
func (s *Server) allowImages(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	convID, ok1 := parseUUID(chi.URLParam(r, "id"))
	msgID, ok2 := parseUUID(chi.URLParam(r, "mid"))
	if !ok1 || !ok2 {
		writeError(w, r, errNotFound)
		return
	}
	mode := r.URL.Query().Get("scope")
	if mode != "once" && mode != "sender" && mode != "domain" {
		writeError(w, r, errBadRequest("scope must be once, sender or domain"))
		return
	}
	user := sessionFrom(ctx).User
	scope, err := policy.MailboxScope(ctx, s.q, user)
	if err != nil {
		writeError(w, r, err)
		return
	}
	msg, err := s.q.GetMessageForRender(ctx, dbq.GetMessageForRenderParams{ID: msgID, MailboxIds: scope.Read})
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && msg.ConversationID != convID) {
		writeError(w, r, errNotFound)
		return
	}
	if err != nil {
		writeError(w, r, fmt.Errorf("load message: %w", err))
		return
	}
	base := "/render/messages/" + uuidStr(msgID)
	if mode == "once" {
		writeJSON(w, http.StatusOK, allowImagesResponse{RenderURL: base + "?images=once"})
		return
	}
	if !slices.Contains(scope.Write, msg.MailboxID) {
		writeError(w, r, errForbidden)
		return
	}
	addr := strings.ToLower(strings.TrimSpace(msg.FromAddr))
	at := strings.LastIndexByte(addr, '@')
	if at < 1 || at == len(addr)-1 {
		writeError(w, r, errValidation(map[string]string{"scope": "no_sender_address"}))
		return
	}
	pattern := addr
	if mode == "domain" {
		pattern = addr[at:]
	}
	ip := clientFrom(r).IP
	err = db.InTx(ctx, s.pool, func(q *dbq.Queries) error {
		if err := q.AddSenderImagePattern(ctx, dbq.AddSenderImagePatternParams{
			MailboxID: msg.MailboxID, Pattern: pattern, CreatedBy: user.ID,
		}); err != nil {
			return fmt.Errorf("add image allowlist entry: %w", err)
		}
		return audit.Write(ctx, q, audit.Entry{
			Actor: user.ID, IP: ip, Action: audit.SenderImagesAllowed,
			TargetType: "mailbox", TargetID: uuidStr(msg.MailboxID),
			Metadata: map[string]any{"pattern": pattern},
		})
	})
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, allowImagesResponse{RenderURL: base})
}
