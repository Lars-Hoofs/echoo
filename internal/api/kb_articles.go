package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"echoo/internal/audit"
	"echoo/internal/db/dbq"
	"echoo/internal/kb"
	"echoo/internal/policy"
)

const (
	kbRevisionsKept = 20
	kbMaxBodyBytes  = 512 << 10
	kbListLimit     = 100
	kbAutoSlugTries = 50
	kbExcerptRunes  = 300
	kbMaxQueryRunes = 200
)

func (s *Server) kbRoutes(r chi.Router) {
	r.Get("/kb/portal", s.getKBPortal)
	r.Get("/kb/categories", s.listKBCategories)
	r.Get("/kb/articles", s.listKBArticles)
	r.Get("/kb/articles/{id}", s.getKBArticle)
	r.Post("/kb/articles", s.createKBArticle)
	r.Patch("/kb/articles/{id}", s.updateKBArticle)
	r.Post("/kb/articles/{id}/status", s.setKBArticleStatus)
	r.Delete("/kb/articles/{id}", s.deleteKBArticle)
	r.Get("/kb/articles/{id}/revisions", s.listKBRevisions)
	r.Post("/kb/articles/{id}/revisions/{revisionId}/restore", s.restoreKBRevision)
	r.Post("/kb/images", s.createKBImage)
}

func (s *Server) kbAdminRoutes(r chi.Router) {
	r.Put("/kb/portal", s.putKBPortal)
	r.Post("/kb/categories", s.createKBCategory)
	r.Patch("/kb/categories/{id}", s.updateKBCategory)
	r.Delete("/kb/categories/{id}", s.deleteKBCategory)
}

type kbArticleJSON struct {
	ID           string     `json:"id"`
	Title        string     `json:"title"`
	Slug         string     `json:"slug"`
	Excerpt      string     `json:"excerpt"`
	Status       string     `json:"status"`
	CategoryID   *string    `json:"category_id"`
	CategoryName *string    `json:"category_name"`
	Version      int32      `json:"version"`
	ViewCount    int64      `json:"view_count"`
	HelpfulYes   int32      `json:"helpful_yes"`
	HelpfulNo    int32      `json:"helpful_no"`
	UpdatedAt    time.Time  `json:"updated_at"`
	UpdatedBy    *string    `json:"updated_by_name"`
	PublishedAt  *time.Time `json:"published_at"`
	// PublicURL is set while the article is published.
	PublicURL *string `json:"public_url"`
}

type kbArticleDetailJSON struct {
	kbArticleJSON
	BodyHTML  string    `json:"body_html"`
	CreatedAt time.Time `json:"created_at"`
}

func textPtr(t pgtype.Text) *string {
	if !t.Valid {
		return nil
	}
	return &t.String
}

func uuidPtr(id pgtype.UUID) *string {
	if !id.Valid {
		return nil
	}
	v := id.String()
	return &v
}

func kbPublicURL(base, slug, status string) *string {
	if status != "published" {
		return nil
	}
	u := base + "/hulp/a/" + slug
	return &u
}

func toKBArticleJSON(a dbq.KbListArticlesRow, base string) kbArticleJSON {
	return kbArticleJSON{
		ID: uuidStr(a.ID), Title: a.Title, Slug: a.Slug, Excerpt: kb.Excerpt(a.Excerpt, kbExcerptRunes), Status: a.Status,
		CategoryID: uuidPtr(a.CategoryID), CategoryName: textPtr(a.CategoryName), Version: a.Version,
		ViewCount: a.ViewCount, HelpfulYes: a.HelpfulYes, HelpfulNo: a.HelpfulNo, UpdatedAt: a.UpdatedAt.Time.UTC(),
		UpdatedBy: textPtr(a.UpdatedByName), PublishedAt: timeOrNil(a.PublishedAt), PublicURL: kbPublicURL(base, a.Slug, a.Status),
	}
}

func (s *Server) kbArticleDetail(ctx context.Context, q *dbq.Queries, a dbq.KbArticle) (kbArticleDetailJSON, error) {
	base, err := s.kbPublicBase(ctx, q)
	if err != nil {
		return kbArticleDetailJSON{}, err
	}
	out := kbArticleDetailJSON{BodyHTML: a.BodyHtml, CreatedAt: a.CreatedAt.Time.UTC()}
	out.kbArticleJSON = kbArticleJSON{
		ID: uuidStr(a.ID), Title: a.Title, Slug: a.Slug, Excerpt: a.Excerpt, Status: a.Status, CategoryID: uuidPtr(a.CategoryID),
		Version: a.Version, ViewCount: a.ViewCount, HelpfulYes: a.HelpfulYes, HelpfulNo: a.HelpfulNo, UpdatedAt: a.UpdatedAt.Time.UTC(),
		PublishedAt: timeOrNil(a.PublishedAt), PublicURL: kbPublicURL(base, a.Slug, a.Status),
	}
	if a.CategoryID.Valid {
		c, err := q.KbGetCategory(ctx, a.CategoryID)
		if err != nil {
			return out, fmt.Errorf("load category: %w", err)
		}
		out.CategoryName = &c.Name
	}
	if a.UpdatedBy.Valid {
		u, err := q.GetUser(ctx, a.UpdatedBy)
		if err != nil {
			return out, fmt.Errorf("load user: %w", err)
		}
		out.UpdatedBy = &u.Name
	}
	return out, nil
}

// kbEditor is the role-level gate for everything that writes articles.
func kbEditor(r *http.Request) (dbq.User, error) {
	user := sessionFrom(r.Context()).User
	if !policy.Has(user, policy.KBWrite) {
		return user, errForbidden
	}
	return user, nil
}

func escapeLike(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}

func (s *Server) listKBArticles(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	user := sessionFrom(ctx).User
	qs := r.URL.Query()
	arg := dbq.KbListArticlesParams{PublishedOnly: !policy.Has(user, policy.KBWrite), PageSize: kbListLimit}
	if st := qs.Get("status"); st != "" {
		if st != "draft" && st != "published" && st != "archived" {
			writeError(w, r, errBadRequest("status must be draft, published or archived"))
			return
		}
		arg.Status = pgtype.Text{String: st, Valid: true}
	}
	if c := qs.Get("category_id"); c != "" {
		id, ok := parseUUID(c)
		if !ok {
			writeError(w, r, errBadRequest("category_id is not a valid id"))
			return
		}
		arg.CategoryID = id
	}
	if text := strings.TrimSpace(qs.Get("q")); text != "" {
		if len([]rune(text)) > kbMaxQueryRunes {
			writeError(w, r, errBadRequest("q is too long"))
			return
		}
		arg.Text = pgtype.Text{String: text, Valid: true}
		arg.TitlePattern = pgtype.Text{String: "%" + escapeLike(text) + "%", Valid: true}
	}
	rows, err := s.q.KbListArticles(ctx, arg)
	if err != nil {
		writeError(w, r, fmt.Errorf("list articles: %w", err))
		return
	}
	base, err := s.kbPublicBase(ctx, s.q)
	if err != nil {
		writeError(w, r, err)
		return
	}
	out := make([]kbArticleJSON, len(rows))
	for i, a := range rows {
		out[i] = toKBArticleJSON(a, base)
	}
	writeJSON(w, http.StatusOK, map[string]any{"articles": out})
}

// loadKBArticle finds the article of the request. Readonly users only get published ones; for
// them a draft does not exist.
func (s *Server) loadKBArticle(r *http.Request, q *dbq.Queries) (dbq.KbArticle, error) {
	var a dbq.KbArticle
	id, ok := parseUUID(chi.URLParam(r, "id"))
	if !ok {
		return a, errNotFound
	}
	a, err := q.KbGetArticle(r.Context(), id)
	if errors.Is(err, pgx.ErrNoRows) {
		return a, errNotFound
	}
	if err != nil {
		return a, fmt.Errorf("load article: %w", err)
	}
	if a.Status != "published" && !policy.Has(sessionFrom(r.Context()).User, policy.KBWrite) {
		return a, errNotFound
	}
	return a, nil
}

func (s *Server) getKBArticle(w http.ResponseWriter, r *http.Request) {
	a, err := s.loadKBArticle(r, s.q)
	if err != nil {
		writeError(w, r, err)
		return
	}
	out, err := s.kbArticleDetail(r.Context(), s.q, a)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

type kbArticleRequest struct {
	Title      string  `json:"title"`
	Slug       string  `json:"slug"`
	CategoryID *string `json:"category_id"`
	BodyHTML   string  `json:"body_html"`
	Excerpt    string  `json:"excerpt"`
	// Version is required on update: the version the editor loaded.
	Version int32 `json:"version"`
}

// kbContent is a validated, sanitized article ready to be stored.
type kbContent struct {
	title, slug, bodyHTML, bodyText, excerpt string
	categoryID                               pgtype.UUID
	imageIDs                                 []pgtype.UUID
}

func (s *Server) validateKBContent(ctx context.Context, q *dbq.Queries, req kbArticleRequest) (kbContent, map[string]string, error) {
	fields := map[string]string{}
	var c kbContent
	var ok bool
	if c.title, ok = cleanText(req.Title, 200); !ok {
		fields["title"] = "invalid"
	}
	excerpt := strings.TrimSpace(req.Excerpt)
	if excerpt != "" {
		if c.excerpt, ok = cleanText(excerpt, kbExcerptRunes); !ok {
			fields["excerpt"] = "invalid"
		}
	}
	if len(req.BodyHTML) > kbMaxBodyBytes {
		fields["body_html"] = "too_large"
	} else {
		c.bodyHTML = kb.Sanitize(req.BodyHTML)
		c.bodyText = kb.Text(c.bodyHTML)
		for _, raw := range kb.ImageIDs(c.bodyHTML) {
			id, _ := parseUUID(raw)
			c.imageIDs = append(c.imageIDs, id)
		}
		if len(c.imageIDs) > 0 {
			found, err := q.KbExistingImages(ctx, c.imageIDs)
			if err != nil {
				return c, nil, fmt.Errorf("check images: %w", err)
			}
			if len(found) != len(c.imageIDs) {
				fields["body_html"] = "unknown_image"
			}
		}
	}
	if req.CategoryID != nil {
		id, ok := parseUUID(*req.CategoryID)
		if !ok {
			fields["category_id"] = "invalid"
		}
		c.categoryID = id
	}
	return c, fields, nil
}

// resolveKBSlug returns the slug to store. A requested slug must be well-formed and not used by
// another article, now or in the past; without one it is derived from the title.
func (s *Server) resolveKBSlug(ctx context.Context, q *dbq.Queries, requested, title string, self pgtype.UUID) (string, string, error) {
	free := func(slug string) (bool, error) {
		owner, err := q.KbSlugOwner(ctx, slug)
		if errors.Is(err, pgx.ErrNoRows) {
			return true, nil
		}
		if err != nil {
			return false, fmt.Errorf("check slug: %w", err)
		}
		return self.Valid && owner == self, nil
	}
	if requested != "" {
		if !kb.ValidSlug(requested) {
			return "", "invalid", nil
		}
		ok, err := free(requested)
		if err != nil {
			return "", "", err
		}
		if !ok {
			return "", "taken", nil
		}
		return requested, "", nil
	}
	base := kb.Slugify(title)
	if base == "" {
		base = "artikel"
	}
	for n := 1; n <= kbAutoSlugTries; n++ {
		cand := base
		if n > 1 {
			suffix := "-" + strconv.Itoa(n)
			cand = strings.TrimRight(base[:min(len(base), kb.MaxSlugLen-len(suffix))], "-") + suffix
		}
		ok, err := free(cand)
		if err != nil {
			return "", "", err
		}
		if ok {
			return cand, "", nil
		}
	}
	return "", "taken", nil
}

func (s *Server) createKBArticle(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	user, err := kbEditor(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	var req kbArticleRequest
	if err := decode(r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	var row dbq.KbArticle
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		q := dbq.New(tx)
		c, fields, err := s.validateKBContent(ctx, q, req)
		if err != nil {
			return err
		}
		slug, problem, err := s.resolveKBSlug(ctx, q, strings.TrimSpace(req.Slug), c.title, pgtype.UUID{})
		if err != nil {
			return err
		}
		if problem != "" {
			fields["slug"] = problem
		}
		if len(fields) > 0 {
			return errValidation(fields)
		}
		row, err = q.KbInsertArticle(ctx, dbq.KbInsertArticleParams{
			CategoryID: c.categoryID, Title: c.title, Slug: slug, BodyHtml: c.bodyHTML, BodyText: c.bodyText,
			Excerpt: c.excerpt, UserID: user.ID,
		})
		if err != nil {
			return err
		}
		return q.KbSetArticleImages(ctx, dbq.KbSetArticleImagesParams{ArticleID: row.ID, ImageIds: c.imageIDs})
	})
	if err := kbWriteError(err, "create article"); err != nil {
		writeError(w, r, err)
		return
	}
	out, err := s.kbArticleDetail(ctx, s.q, row)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, out)
}

// kbWriteError maps constraint violations of the article and category tables to validation
// errors; anything else is an internal error.
func kbWriteError(err error, what string) error {
	switch {
	case err == nil:
		return nil
	case isUniqueViolation(err):
		return errValidation(map[string]string{"slug": "taken"})
	case isForeignKeyViolation(err):
		return errValidation(map[string]string{"category_id": "unknown"})
	}
	var ae *apiError
	if errors.As(err, &ae) {
		return err
	}
	return fmt.Errorf("%s: %w", what, err)
}

func errKBVersionConflict(current int32) *apiError {
	return &apiError{Status: http.StatusConflict, Code: "version_conflict", Message: "the article changed since it was loaded", CurrentVersion: &current}
}

// applyKBContent stores c over the locked article a. The previous title, excerpt and body are
// kept as a revision when they change. An old slug of a published article keeps redirecting.
func (s *Server) applyKBContent(ctx context.Context, q *dbq.Queries, user dbq.User, a dbq.KbArticle, c kbContent) (dbq.KbArticle, error) {
	if a.Status == "published" && !c.categoryID.Valid {
		return a, errValidation(map[string]string{"category_id": "required_when_published"})
	}
	if a.Title != c.title || a.BodyHtml != c.bodyHTML || a.Excerpt != c.excerpt {
		if err := q.KbInsertRevision(ctx, dbq.KbInsertRevisionParams{ID: a.ID, UserID: user.ID}); err != nil {
			return a, fmt.Errorf("save revision: %w", err)
		}
	}
	if c.slug != a.Slug {
		if a.PublishedAt.Valid {
			if err := q.KbInsertRedirect(ctx, dbq.KbInsertRedirectParams{Slug: a.Slug, ArticleID: a.ID}); err != nil {
				return a, fmt.Errorf("keep old slug: %w", err)
			}
		}
		if err := q.KbDeleteRedirect(ctx, c.slug); err != nil {
			return a, fmt.Errorf("release slug: %w", err)
		}
	}
	row, err := q.KbUpdateArticle(ctx, dbq.KbUpdateArticleParams{
		ID: a.ID, CategoryID: c.categoryID, Title: c.title, Slug: c.slug, BodyHtml: c.bodyHTML, BodyText: c.bodyText,
		Excerpt: c.excerpt, UserID: user.ID,
	})
	if err != nil {
		return a, err
	}
	if err := q.KbClearArticleImages(ctx, a.ID); err != nil {
		return a, fmt.Errorf("clear article images: %w", err)
	}
	if err := q.KbSetArticleImages(ctx, dbq.KbSetArticleImagesParams{ArticleID: a.ID, ImageIds: c.imageIDs}); err != nil {
		return a, fmt.Errorf("link article images: %w", err)
	}
	if err := q.KbTrimRevisions(ctx, dbq.KbTrimRevisionsParams{ArticleID: a.ID, Keep: kbRevisionsKept}); err != nil {
		return a, fmt.Errorf("trim revisions: %w", err)
	}
	return row, nil
}

func (s *Server) updateKBArticle(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	user, err := kbEditor(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	id, ok := parseUUID(chi.URLParam(r, "id"))
	if !ok {
		writeError(w, r, errNotFound)
		return
	}
	var req kbArticleRequest
	if err := decode(r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	var row dbq.KbArticle
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		q := dbq.New(tx)
		a, err := q.KbLockArticle(ctx, id)
		if errors.Is(err, pgx.ErrNoRows) {
			return errNotFound
		}
		if err != nil {
			return fmt.Errorf("lock article: %w", err)
		}
		if req.Version != a.Version {
			return errKBVersionConflict(a.Version)
		}
		c, fields, err := s.validateKBContent(ctx, q, req)
		if err != nil {
			return err
		}
		requested := strings.TrimSpace(req.Slug)
		if requested == "" {
			fields["slug"] = "invalid"
		}
		var problem string
		if c.slug, problem, err = s.resolveKBSlug(ctx, q, requested, c.title, a.ID); err != nil {
			return err
		}
		if problem != "" {
			fields["slug"] = problem
		}
		if len(fields) > 0 {
			return errValidation(fields)
		}
		row, err = s.applyKBContent(ctx, q, user, a, c)
		return err
	})
	if err := kbWriteError(err, "update article"); err != nil {
		writeError(w, r, err)
		return
	}
	out, err := s.kbArticleDetail(ctx, s.q, row)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) auditKBArticle(ctx context.Context, q *dbq.Queries, r *http.Request, action string, a dbq.KbArticle) error {
	return audit.Write(ctx, q, audit.Entry{
		Actor: sessionFrom(ctx).User.ID, IP: clientFrom(r).IP, Action: action, TargetType: "kb_article", TargetID: uuidStr(a.ID),
		Metadata: map[string]any{"title": a.Title, "slug": a.Slug},
	})
}

func (s *Server) setKBArticleStatus(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	user, err := kbEditor(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	id, ok := parseUUID(chi.URLParam(r, "id"))
	if !ok {
		writeError(w, r, errNotFound)
		return
	}
	var req struct {
		Status string `json:"status"`
	}
	if err := decode(r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	if req.Status != "draft" && req.Status != "published" && req.Status != "archived" {
		writeError(w, r, errValidation(map[string]string{"status": "invalid"}))
		return
	}
	var row dbq.KbArticle
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		q := dbq.New(tx)
		a, err := q.KbLockArticle(ctx, id)
		if errors.Is(err, pgx.ErrNoRows) {
			return errNotFound
		}
		if err != nil {
			return fmt.Errorf("lock article: %w", err)
		}
		row = a
		if a.Status == req.Status {
			return nil
		}
		if req.Status == "published" {
			if !a.CategoryID.Valid {
				return errValidation(map[string]string{"category_id": "required_when_published"})
			}
			if a.BodyText == "" && len(kb.ImageIDs(a.BodyHtml)) == 0 {
				return errValidation(map[string]string{"body_html": "required_when_published"})
			}
		}
		if row, err = q.KbSetArticleStatus(ctx, dbq.KbSetArticleStatusParams{ID: id, Status: req.Status, UserID: user.ID}); err != nil {
			return err
		}
		switch {
		case req.Status == "published":
			return s.auditKBArticle(ctx, q, r, audit.KBArticlePublished, row)
		case req.Status == "archived":
			return s.auditKBArticle(ctx, q, r, audit.KBArticleArchived, row)
		case a.Status == "published":
			return s.auditKBArticle(ctx, q, r, audit.KBArticleUnpublished, row)
		}
		return nil
	})
	if err := kbWriteError(err, "set article status"); err != nil {
		writeError(w, r, err)
		return
	}
	out, err := s.kbArticleDetail(ctx, s.q, row)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) deleteKBArticle(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if _, err := kbEditor(r); err != nil {
		writeError(w, r, err)
		return
	}
	id, ok := parseUUID(chi.URLParam(r, "id"))
	if !ok {
		writeError(w, r, errNotFound)
		return
	}
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		q := dbq.New(tx)
		a, err := q.KbLockArticle(ctx, id)
		if errors.Is(err, pgx.ErrNoRows) {
			return errNotFound
		}
		if err != nil {
			return fmt.Errorf("lock article: %w", err)
		}
		if _, err := q.KbDeleteArticle(ctx, id); err != nil {
			return fmt.Errorf("delete article: %w", err)
		}
		return s.auditKBArticle(ctx, q, r, audit.KBArticleDeleted, a)
	})
	if err != nil {
		writeError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type kbRevisionJSON struct {
	ID           string    `json:"id"`
	Title        string    `json:"title"`
	CreatedAt    time.Time `json:"created_at"`
	EditedByName *string   `json:"edited_by_name"`
}

func (s *Server) listKBRevisions(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if _, err := kbEditor(r); err != nil {
		writeError(w, r, err)
		return
	}
	a, err := s.loadKBArticle(r, s.q)
	if err != nil {
		writeError(w, r, err)
		return
	}
	rows, err := s.q.KbListRevisions(ctx, a.ID)
	if err != nil {
		writeError(w, r, fmt.Errorf("list revisions: %w", err))
		return
	}
	out := make([]kbRevisionJSON, len(rows))
	for i, v := range rows {
		out[i] = kbRevisionJSON{ID: uuidStr(v.ID), Title: v.Title, CreatedAt: v.CreatedAt.Time.UTC(), EditedByName: textPtr(v.EditedByName)}
	}
	writeJSON(w, http.StatusOK, map[string]any{"revisions": out})
}

// restoreKBRevision puts the title, excerpt and body of a revision back. The current state is
// kept as a revision first, so a restore can be undone.
func (s *Server) restoreKBRevision(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	user, err := kbEditor(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	id, ok := parseUUID(chi.URLParam(r, "id"))
	revID, ok2 := parseUUID(chi.URLParam(r, "revisionId"))
	if !ok || !ok2 {
		writeError(w, r, errNotFound)
		return
	}
	var row dbq.KbArticle
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		q := dbq.New(tx)
		a, err := q.KbLockArticle(ctx, id)
		if errors.Is(err, pgx.ErrNoRows) {
			return errNotFound
		}
		if err != nil {
			return fmt.Errorf("lock article: %w", err)
		}
		rev, err := q.KbGetRevision(ctx, dbq.KbGetRevisionParams{ID: revID, ArticleID: id})
		if errors.Is(err, pgx.ErrNoRows) {
			return errNotFound
		}
		if err != nil {
			return fmt.Errorf("load revision: %w", err)
		}
		c, fields, err := s.validateKBContent(ctx, q, kbArticleRequest{Title: rev.Title, BodyHTML: rev.BodyHtml, Excerpt: rev.Excerpt})
		if err != nil {
			return err
		}
		if len(fields) > 0 {
			return errValidation(fields)
		}
		c.slug, c.categoryID = a.Slug, a.CategoryID
		row, err = s.applyKBContent(ctx, q, user, a, c)
		return err
	})
	if err := kbWriteError(err, "restore revision"); err != nil {
		writeError(w, r, err)
		return
	}
	out, err := s.kbArticleDetail(ctx, s.q, row)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}
