package api

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"echoo/internal/db/dbq"
	"echoo/internal/policy"
	"echoo/internal/sanitize"
	"echoo/internal/scan"
	"echoo/internal/storage"
)

// servableInlineImage decides whether a sniffed type may be served to <img>. SVG is never
// served inline: it can carry script and is only ever a download.
func servableInlineImage(sniffed string) bool {
	return strings.HasPrefix(sniffed, "image/") && sniffed != "image/svg+xml"
}

// documentOptions builds the sanitizer options for the rendered iframe document: cid images
// point at signed attachment URLs, and remote images go through the signed proxy only when
// allowed.
func (s *Server) documentOptions(ctx context.Context, messageID pgtype.UUID, allowRemote bool) (sanitize.Options, func() error, error) {
	atts, err := s.q.ListRenderAttachments(ctx, messageID)
	if err != nil {
		return sanitize.Options{}, nil, fmt.Errorf("list inline attachments: %w", err)
	}
	cids := map[string]pgtype.UUID{}
	for _, a := range atts {
		if servableInlineImage(a.SniffedType) {
			cids[strings.ToLower(strings.Trim(a.ContentID, "<>"))] = a.ID
		}
	}
	var signErr error
	opts := sanitize.Options{
		ResolveCID: func(cid string) (string, bool) {
			id, ok := cids[cid]
			if !ok {
				return "", false
			}
			u, err := s.signedAttachmentURL(uuidStr(id))
			if err != nil {
				signErr = err
				return "", false
			}
			return u, true
		},
	}
	if allowRemote {
		opts.ProxyURL = func(remote string) string {
			u, err := s.signedProxyURL(remote)
			if err != nil {
				signErr = err
			}
			return u
		}
	}
	return opts, func() error { return signErr }, nil
}

func (s *Server) senderImagesAllowed(ctx context.Context, mailbox pgtype.UUID, from string) (bool, error) {
	patterns, err := s.q.ListSenderImagePatterns(ctx, mailbox)
	if err != nil {
		return false, fmt.Errorf("list image allowlist: %w", err)
	}
	return senderAllowed(patterns, from), nil
}

// renderMessage serves the sanitized message as a standalone document for the sandboxed iframe.
func (s *Server) renderMessage(w http.ResponseWriter, r *http.Request) {
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
	row, err := s.q.GetMessageForRender(ctx, dbq.GetMessageForRenderParams{ID: id, MailboxIds: scope.Read})
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, r, errNotFound)
		return
	}
	if err != nil {
		writeError(w, r, fmt.Errorf("load message for render: %w", err))
		return
	}

	fragment, plain, truncated := sanitize.Text(row.BodyText), true, false
	if row.BodyHtml != "" {
		allow := r.URL.Query().Get("images") == "once"
		if !allow {
			if allow, err = s.senderImagesAllowed(ctx, row.MailboxID, row.FromAddr); err != nil {
				writeError(w, r, err)
				return
			}
		}
		opts, signErr, err := s.documentOptions(ctx, id, allow)
		if err != nil {
			writeError(w, r, err)
			return
		}
		res, err := sanitize.HTML(row.BodyHtml, opts)
		switch {
		case errors.Is(err, sanitize.ErrUnparseable):
			// Markup the parser refuses (absurd nesting) is shown as the text alternative.
		case err != nil:
			writeError(w, r, fmt.Errorf("sanitize message: %w", err))
			return
		default:
			if err := signErr(); err != nil {
				writeError(w, r, err)
				return
			}
			fragment, plain, truncated = res.HTML, false, res.Truncated
		}
	}

	doc := sanitize.Document(fragment, plain, truncated)
	h := w.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Content-Security-Policy", sanitize.ContentSecurityPolicy)
	// Only this route may be framed, and only by the app itself.
	h.Set("X-Frame-Options", "SAMEORIGIN")
	h.Set("Cache-Control", "private, no-store")
	h.Set("Content-Length", strconv.Itoa(len(doc)))
	// The client may have gone away; the headers are already sent.
	_, _ = w.Write(doc)
}

// serveBlob streams a stored blob as a passive resource: no sniffing, no script, no framing.
func serveBlob(w http.ResponseWriter, contentType string, size int64, body io.Reader) {
	h := w.Header()
	h.Set("Content-Type", contentType)
	h.Set("Content-Security-Policy", "default-src 'none'; sandbox")
	h.Set("Content-Length", strconv.FormatInt(size, 10))
	// The signature already limits who can ask; an opaque-origin document must be able to embed it.
	h.Set("Cross-Origin-Resource-Policy", "cross-origin")
	h.Set("Cache-Control", "private, max-age=3600")
	// The client may have gone away mid-stream; the status line is already sent.
	_, _ = io.Copy(w, body)
}

func (s *Server) openBlob(ctx context.Context, key string) (io.ReadCloser, error) {
	if s.store == nil {
		return nil, errors.New("blob storage is not configured")
	}
	rc, err := s.store.Open(ctx, key)
	if errors.Is(err, storage.ErrNotFound) {
		return nil, errNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("open blob: %w", err)
	}
	return rc, nil
}

// renderAttachment serves an inline (cid) image to the sandboxed iframe.
func (s *Server) renderAttachment(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id, ok := parseUUID(chi.URLParam(r, "id"))
	if !ok || !s.verifyRenderSignature("attachment", uuidStr(id), r.URL.Query()) {
		writeError(w, r, errNotFound)
		return
	}
	att, err := s.q.GetAttachmentForServe(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && (!servableInlineImage(att.SniffedType) || att.ScanStatus == scan.StatusInfected)) {
		writeError(w, r, errNotFound)
		return
	}
	if err != nil {
		writeError(w, r, fmt.Errorf("load attachment: %w", err))
		return
	}
	rc, err := s.openBlob(ctx, att.BlobKey)
	if err != nil {
		writeError(w, r, err)
		return
	}
	defer func() { _ = rc.Close() }()
	serveBlob(w, att.SniffedType, att.SizeBytes, rc)
}
