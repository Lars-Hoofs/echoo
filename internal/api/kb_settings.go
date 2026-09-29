package api

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"echoo/internal/audit"
	"echoo/internal/compose"
	"echoo/internal/db/dbq"
	"echoo/internal/kb"
	"echoo/internal/sniff"
)

const kbMaxImageBytes = 5 << 20

var kbImageTypes = map[string]bool{"image/png": true, "image/jpeg": true, "image/gif": true, "image/webp": true}

var kbDomain = regexp.MustCompile(`^([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?(\.[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?)+)?$`)

// kbPublicBase is the origin the public help center is reachable at: the custom domain of a
// reverse proxy when one is set, otherwise the base URL of Echoo. It has no trailing slash.
func (s *Server) kbPublicBase(ctx context.Context, q *dbq.Queries) (string, error) {
	p, err := q.KbGetPortal(ctx)
	if err != nil {
		return "", fmt.Errorf("load portal: %w", err)
	}
	return s.kbBase(p.CustomDomain), nil
}

func (s *Server) kbBase(customDomain string) string {
	if customDomain != "" {
		return "https://" + customDomain
	}
	return s.cfg.Origin()
}

type kbPortalJSON struct {
	Name         string    `json:"name"`
	Title        string    `json:"title"`
	Intro        string    `json:"intro"`
	LogoText     string    `json:"logo_text"`
	CustomDomain string    `json:"custom_domain"`
	PublicURL    string    `json:"public_url"`
	UpdatedAt    time.Time `json:"updated_at"`
}

func (s *Server) toKBPortalJSON(p dbq.KbGetPortalRow) kbPortalJSON {
	return kbPortalJSON{
		Name: p.Name, Title: p.Title, Intro: p.Intro, LogoText: p.LogoText, CustomDomain: p.CustomDomain,
		PublicURL: s.kbBase(p.CustomDomain) + "/hulp", UpdatedAt: p.UpdatedAt.Time.UTC(),
	}
}

func (s *Server) getKBPortal(w http.ResponseWriter, r *http.Request) {
	p, err := s.q.KbGetPortal(r.Context())
	if err != nil {
		writeError(w, r, fmt.Errorf("load portal: %w", err))
		return
	}
	writeJSON(w, http.StatusOK, s.toKBPortalJSON(p))
}

type kbPortalRequest struct {
	Name         string `json:"name"`
	Title        string `json:"title"`
	Intro        string `json:"intro"`
	LogoText     string `json:"logo_text"`
	CustomDomain string `json:"custom_domain"`
}

func (s *Server) putKBPortal(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var req kbPortalRequest
	if err := decode(r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	fields := map[string]string{}
	arg := dbq.KbUpdatePortalParams{}
	var ok bool
	if arg.Name, ok = cleanText(req.Name, 100); !ok {
		fields["name"] = "invalid"
	}
	if arg.Title, ok = cleanText(req.Title, 150); !ok {
		fields["title"] = "invalid"
	}
	if arg.Intro = strings.TrimSpace(req.Intro); utf8.RuneCountInString(arg.Intro) > 500 || strings.ContainsAny(arg.Intro, "\r\n\x00") {
		fields["intro"] = "invalid"
	}
	if arg.LogoText = strings.TrimSpace(req.LogoText); arg.LogoText != "" {
		if _, ok := cleanText(arg.LogoText, 40); !ok {
			fields["logo_text"] = "invalid"
		}
	}
	arg.CustomDomain = strings.ToLower(strings.TrimSpace(req.CustomDomain))
	if len(arg.CustomDomain) > 253 || !kbDomain.MatchString(arg.CustomDomain) {
		fields["custom_domain"] = "invalid"
	}
	if len(fields) > 0 {
		writeError(w, r, errValidation(fields))
		return
	}
	var row dbq.KbUpdatePortalRow
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		q := dbq.New(tx)
		var err error
		if row, err = q.KbUpdatePortal(ctx, arg); err != nil {
			return err
		}
		return audit.Write(ctx, q, audit.Entry{
			Actor: sessionFrom(ctx).User.ID, IP: clientFrom(r).IP, Action: audit.KBPortalUpdated, TargetType: "kb_portal", TargetID: "portal",
			Metadata: map[string]any{"custom_domain": row.CustomDomain},
		})
	})
	if err != nil {
		writeError(w, r, fmt.Errorf("update portal: %w", err))
		return
	}
	writeJSON(w, http.StatusOK, s.toKBPortalJSON(dbq.KbGetPortalRow(row)))
}

type kbCategoryJSON struct {
	ID             string  `json:"id"`
	ParentID       *string `json:"parent_id"`
	Name           string  `json:"name"`
	Slug           string  `json:"slug"`
	Description    string  `json:"description"`
	Position       int32   `json:"position"`
	ArticleCount   int32   `json:"article_count"`
	PublishedCount int32   `json:"published_count"`
}

func (s *Server) listKBCategories(w http.ResponseWriter, r *http.Request) {
	rows, err := s.q.KbListCategories(r.Context())
	if err != nil {
		writeError(w, r, fmt.Errorf("list categories: %w", err))
		return
	}
	out := make([]kbCategoryJSON, len(rows))
	for i, c := range rows {
		out[i] = kbCategoryJSON{
			ID: uuidStr(c.ID), ParentID: uuidPtr(c.ParentID), Name: c.Name, Slug: c.Slug, Description: c.Description,
			Position: c.Position, ArticleCount: c.ArticleCount, PublishedCount: c.PublishedCount,
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"categories": out})
}

type kbCategoryRequest struct {
	Name        string  `json:"name"`
	Slug        string  `json:"slug"`
	Description string  `json:"description"`
	ParentID    *string `json:"parent_id"`
	Position    int32   `json:"position"`
}

var errKBCategoryInUse = &apiError{Status: http.StatusConflict, Code: "category_not_empty", Message: "the category still has articles or subcategories"}

// validateKBCategory checks a category request. The tree has one level: a parent must be a
// top-level category, and a category that has children cannot get a parent itself.
func (s *Server) validateKBCategory(ctx context.Context, q *dbq.Queries, req kbCategoryRequest, self pgtype.UUID) (dbq.KbInsertCategoryParams, map[string]string, error) {
	fields := map[string]string{}
	arg := dbq.KbInsertCategoryParams{Position: req.Position}
	var ok bool
	if arg.Name, ok = cleanText(req.Name, 100); !ok {
		fields["name"] = "invalid"
	}
	if arg.Description = strings.TrimSpace(req.Description); utf8.RuneCountInString(arg.Description) > 300 || strings.ContainsAny(arg.Description, "\r\n\x00") {
		fields["description"] = "invalid"
	}
	if req.Position < 0 || req.Position > 10000 {
		fields["position"] = "invalid"
	}
	arg.Slug = strings.TrimSpace(req.Slug)
	if arg.Slug == "" {
		if arg.Slug = kb.Slugify(arg.Name); arg.Slug == "" {
			fields["slug"] = "invalid"
		}
	} else if !kb.ValidSlug(arg.Slug) {
		fields["slug"] = "invalid"
	}
	if req.ParentID != nil {
		id, valid := parseUUID(*req.ParentID)
		if !valid || id == self {
			fields["parent_id"] = "invalid"
			return arg, fields, nil
		}
		parent, err := q.KbGetCategory(ctx, id)
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			fields["parent_id"] = "unknown"
		case err != nil:
			return arg, nil, fmt.Errorf("load parent category: %w", err)
		case parent.ParentID.Valid:
			fields["parent_id"] = "too_deep"
		default:
			arg.ParentID = id
		}
		if self.Valid && arg.ParentID.Valid {
			use, err := q.KbCategoryUsage(ctx, self)
			if err != nil {
				return arg, nil, fmt.Errorf("check category usage: %w", err)
			}
			if use.Children > 0 {
				fields["parent_id"] = "too_deep"
			}
		}
	}
	return arg, fields, nil
}

func (s *Server) auditKBCategory(ctx context.Context, q *dbq.Queries, r *http.Request, action string, c dbq.KbCategory) error {
	return audit.Write(ctx, q, audit.Entry{
		Actor: sessionFrom(ctx).User.ID, IP: clientFrom(r).IP, Action: action, TargetType: "kb_category", TargetID: uuidStr(c.ID),
		Metadata: map[string]any{"name": c.Name, "slug": c.Slug},
	})
}

func toKBCategoryJSON(c dbq.KbCategory, articles, published int32) kbCategoryJSON {
	return kbCategoryJSON{
		ID: uuidStr(c.ID), ParentID: uuidPtr(c.ParentID), Name: c.Name, Slug: c.Slug, Description: c.Description,
		Position: c.Position, ArticleCount: articles, PublishedCount: published,
	}
}

func (s *Server) createKBCategory(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var req kbCategoryRequest
	if err := decode(r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	var row dbq.KbCategory
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		q := dbq.New(tx)
		arg, fields, err := s.validateKBCategory(ctx, q, req, pgtype.UUID{})
		if err != nil {
			return err
		}
		if len(fields) > 0 {
			return errValidation(fields)
		}
		if row, err = q.KbInsertCategory(ctx, arg); err != nil {
			return err
		}
		return s.auditKBCategory(ctx, q, r, audit.KBCategoryCreated, row)
	})
	if err := kbWriteError(err, "create category"); err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, toKBCategoryJSON(row, 0, 0))
}

func (s *Server) updateKBCategory(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id, ok := parseUUID(chi.URLParam(r, "id"))
	if !ok {
		writeError(w, r, errNotFound)
		return
	}
	var req kbCategoryRequest
	if err := decode(r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	var row dbq.KbCategory
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		q := dbq.New(tx)
		if _, err := q.KbLockCategory(ctx, id); errors.Is(err, pgx.ErrNoRows) {
			return errNotFound
		} else if err != nil {
			return fmt.Errorf("lock category: %w", err)
		}
		arg, fields, err := s.validateKBCategory(ctx, q, req, id)
		if err != nil {
			return err
		}
		if len(fields) > 0 {
			return errValidation(fields)
		}
		if row, err = q.KbUpdateCategory(ctx, dbq.KbUpdateCategoryParams{
			ParentID: arg.ParentID, Name: arg.Name, Slug: arg.Slug, Description: arg.Description, Position: arg.Position, ID: id,
		}); err != nil {
			return err
		}
		return s.auditKBCategory(ctx, q, r, audit.KBCategoryUpdated, row)
	})
	if err := kbWriteError(err, "update category"); err != nil {
		writeError(w, r, err)
		return
	}
	use, err := s.q.KbCategoryUsage(ctx, id)
	if err != nil {
		writeError(w, r, fmt.Errorf("count category articles: %w", err))
		return
	}
	writeJSON(w, http.StatusOK, toKBCategoryJSON(row, use.Articles, 0))
}

func (s *Server) deleteKBCategory(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id, ok := parseUUID(chi.URLParam(r, "id"))
	if !ok {
		writeError(w, r, errNotFound)
		return
	}
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		q := dbq.New(tx)
		c, err := q.KbLockCategory(ctx, id)
		if errors.Is(err, pgx.ErrNoRows) {
			return errNotFound
		}
		if err != nil {
			return fmt.Errorf("lock category: %w", err)
		}
		use, err := q.KbCategoryUsage(ctx, id)
		if err != nil {
			return fmt.Errorf("check category usage: %w", err)
		}
		if use.Children > 0 || use.Articles > 0 {
			return errKBCategoryInUse
		}
		if _, err := q.KbDeleteCategory(ctx, id); err != nil {
			return fmt.Errorf("delete category: %w", err)
		}
		return s.auditKBCategory(ctx, q, r, audit.KBCategoryDeleted, c)
	})
	if err != nil {
		writeError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type kbImageJSON struct {
	ID          string `json:"id"`
	URL         string `json:"url"`
	Filename    string `json:"filename"`
	ContentType string `json:"content_type"`
	Size        int64  `json:"size"`
}

// createKBImage stores one image of an article. Only raster formats are accepted, judged by
// content: an SVG can carry script, so it never gets in.
func (s *Server) createKBImage(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	user, err := kbEditor(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	if s.store == nil {
		writeError(w, r, errors.New("blob storage is not configured"))
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, kbMaxImageBytes+multipartOverhead)
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
		data, err := io.ReadAll(io.LimitReader(part, kbMaxImageBytes+1))
		if err != nil {
			writeError(w, r, uploadReadError(err))
			return
		}
		if len(data) > kbMaxImageBytes {
			writeError(w, r, errFileTooLarge)
			return
		}
		if len(data) == 0 {
			writeError(w, r, errValidation(map[string]string{"file": "empty"}))
			return
		}
		contentType := sniff.Type(data)
		if !kbImageTypes[contentType] {
			writeError(w, r, errValidation(map[string]string{"file": "not_an_image"}))
			return
		}
		key, _, err := s.store.Put(ctx, data)
		if err != nil {
			writeError(w, r, fmt.Errorf("store image: %w", err))
			return
		}
		if err := s.q.KbDeleteUnusedImages(ctx); err != nil {
			writeError(w, r, fmt.Errorf("remove unused images: %w", err))
			return
		}
		img, err := s.q.KbInsertImage(ctx, dbq.KbInsertImageParams{
			UploaderID: user.ID, Filename: compose.SafeFilename(part.FileName()), ContentType: contentType, SizeBytes: int64(len(data)), BlobKey: key,
		})
		if err != nil {
			writeError(w, r, fmt.Errorf("insert image: %w", err))
			return
		}
		id := uuidStr(img.ID)
		writeJSON(w, http.StatusCreated, kbImageJSON{ID: id, URL: "/hulp/i/" + id, Filename: img.Filename, ContentType: contentType, Size: img.SizeBytes})
		return
	}
}
