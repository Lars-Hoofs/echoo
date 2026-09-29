package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"echoo/internal/audit"
	"echoo/internal/compose"
	"echoo/internal/db/dbq"
	"echoo/internal/policy"
)

var shortcodePattern = regexp.MustCompile(`^[a-z0-9_-]{0,32}$`)

var templateScopes = map[string]bool{"personal": true, "team": true, "mailbox": true, "global": true}

type templateJSON struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Shortcode string    `json:"shortcode"`
	Scope     string    `json:"scope"`
	TeamID    *string   `json:"team_id"`
	MailboxID *string   `json:"mailbox_id"`
	Subject   string    `json:"subject"`
	BodyHTML  string    `json:"body_html"`
	Editable  bool      `json:"editable"`
	UpdatedAt time.Time `json:"updated_at"`
}

func toTemplateJSON(t dbq.Template, user dbq.User) templateJSON {
	out := templateJSON{
		ID: uuidStr(t.ID), Name: t.Name, Shortcode: t.Shortcode, Scope: t.Scope, Subject: t.Subject,
		BodyHTML: t.BodyHtml, UpdatedAt: t.UpdatedAt.Time.UTC(), Editable: canEditTemplate(user, t),
	}
	if t.TeamID.Valid {
		id := uuidStr(t.TeamID)
		out.TeamID = &id
	}
	if t.MailboxID.Valid {
		id := uuidStr(t.MailboxID)
		out.MailboxID = &id
	}
	return out
}

// canEditTemplate: personal templates belong to their owner, every other scope to the admins.
// Readonly users never write.
func canEditTemplate(user dbq.User, t dbq.Template) bool {
	if !policy.Has(user, policy.ConversationsWrite) {
		return false
	}
	if t.Scope == "personal" {
		return t.OwnerUserID == user.ID
	}
	return policy.Has(user, policy.TemplatesManage)
}

func (s *Server) listTemplates(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	user := sessionFrom(ctx).User
	manage := false
	switch r.URL.Query().Get("manage") {
	case "":
	case "true":
		if !policy.Has(user, policy.TemplatesManage) {
			writeError(w, r, errForbidden)
			return
		}
		manage = true
	default:
		writeError(w, r, errBadRequest("manage must be true"))
		return
	}
	scope, err := policy.MailboxScope(ctx, s.q, user)
	if err != nil {
		writeError(w, r, err)
		return
	}
	rows, err := s.q.ComposeListTemplates(ctx, dbq.ComposeListTemplatesParams{UserID: user.ID, Manage: manage, MailboxIds: scope.Read})
	if err != nil {
		writeError(w, r, fmt.Errorf("list templates: %w", err))
		return
	}
	out := make([]templateJSON, len(rows))
	for i, t := range rows {
		out[i] = toTemplateJSON(t, user)
	}
	writeJSON(w, http.StatusOK, map[string]any{"templates": out})
}

type templateRequest struct {
	Name      string  `json:"name"`
	Shortcode string  `json:"shortcode"`
	Scope     string  `json:"scope"`
	TeamID    *string `json:"team_id"`
	MailboxID *string `json:"mailbox_id"`
	Subject   string  `json:"subject"`
	BodyHTML  string  `json:"body_html"`
}

// validateTemplateContent checks the fields shared by create and update.
func validateTemplateContent(req templateRequest) (name, subject, body string, fields map[string]string) {
	fields = map[string]string{}
	var ok bool
	if name, ok = cleanText(req.Name, 100); !ok {
		fields["name"] = "invalid"
	}
	if !shortcodePattern.MatchString(req.Shortcode) {
		fields["shortcode"] = "invalid"
	}
	subject = strings.TrimSpace(req.Subject)
	if strings.ContainsAny(subject, "\r\n\x00") || utf8.RuneCountInString(subject) > maxSubjectRunes {
		fields["subject"] = "invalid"
	}
	if len(req.BodyHTML) > maxBodyHTMLBytes {
		fields["body_html"] = "too_large"
	} else if body = compose.Sanitize(req.BodyHTML, compose.SanitizeOptions{}); compose.IsEmpty(body) {
		fields["body_html"] = "required"
	}
	return name, subject, body, fields
}

func (s *Server) createTemplate(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	user := sessionFrom(ctx).User
	var req templateRequest
	if err := decode(r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	if !templateScopes[req.Scope] {
		writeError(w, r, errValidation(map[string]string{"template_scope": "invalid"}))
		return
	}
	if !policy.Has(user, policy.ConversationsWrite) || (req.Scope != "personal" && !policy.Has(user, policy.TemplatesManage)) {
		writeError(w, r, errForbidden)
		return
	}
	name, subject, body, fields := validateTemplateContent(req)
	arg := dbq.ComposeInsertTemplateParams{Name: name, Shortcode: req.Shortcode, Scope: req.Scope, Subject: subject, BodyHtml: body}
	switch req.Scope {
	case "personal":
		arg.OwnerUserID = user.ID
	case "team":
		if id, ok := optionalUUID(req.TeamID); ok {
			arg.TeamID = id
		} else {
			fields["team_id"] = "required"
		}
	case "mailbox":
		if id, ok := optionalUUID(req.MailboxID); ok {
			arg.MailboxID = id
		} else {
			fields["mailbox_id"] = "required"
		}
	}
	if len(fields) > 0 {
		writeError(w, r, errValidation(fields))
		return
	}
	var row dbq.Template
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		q := dbq.New(tx)
		var err error
		if row, err = q.ComposeInsertTemplate(ctx, arg); err != nil {
			return err
		}
		return s.auditTemplate(ctx, q, r, audit.TemplateCreated, row)
	})
	if isForeignKeyViolation(err) {
		writeError(w, r, errValidation(map[string]string{"template_scope": "unknown"}))
		return
	}
	if err != nil {
		writeError(w, r, fmt.Errorf("create template: %w", err))
		return
	}
	writeJSON(w, http.StatusCreated, toTemplateJSON(row, user))
}

func optionalUUID(s *string) (pgtype.UUID, bool) {
	if s == nil {
		return pgtype.UUID{}, false
	}
	return parseUUID(*s)
}

// auditTemplate records changes to shared templates; personal ones are the owner's own business.
func (s *Server) auditTemplate(ctx context.Context, q *dbq.Queries, r *http.Request, action string, t dbq.Template) error {
	if t.Scope == "personal" {
		return nil
	}
	return audit.Write(ctx, q, audit.Entry{
		Actor: sessionFrom(ctx).User.ID, IP: clientFrom(r).IP, Action: action, TargetType: "template", TargetID: uuidStr(t.ID),
		Metadata: map[string]any{"scope": t.Scope, "name": t.Name},
	})
}

// editableTemplate loads a template the user may change: someone else's personal template does
// not exist for them; a shared one they may not edit is forbidden.
func (s *Server) editableTemplate(r *http.Request) (dbq.Template, error) {
	ctx := r.Context()
	user := sessionFrom(ctx).User
	id, ok := parseUUID(chi.URLParam(r, "id"))
	if !ok {
		return dbq.Template{}, errNotFound
	}
	t, err := s.q.ComposeGetTemplate(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return t, errNotFound
	}
	if err != nil {
		return t, fmt.Errorf("load template: %w", err)
	}
	if t.Scope == "personal" && t.OwnerUserID != user.ID {
		return t, errNotFound
	}
	if !canEditTemplate(user, t) {
		return t, errForbidden
	}
	return t, nil
}

func (s *Server) updateTemplate(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	t, err := s.editableTemplate(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	var req templateRequest
	if err := decode(r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	name, subject, body, fields := validateTemplateContent(req)
	if len(fields) > 0 {
		writeError(w, r, errValidation(fields))
		return
	}
	var row dbq.Template
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		q := dbq.New(tx)
		var err error
		if row, err = q.ComposeUpdateTemplate(ctx, dbq.ComposeUpdateTemplateParams{ID: t.ID, Name: name, Shortcode: req.Shortcode, Subject: subject, BodyHtml: body}); err != nil {
			return err
		}
		return s.auditTemplate(ctx, q, r, audit.TemplateUpdated, row)
	})
	if err != nil {
		writeError(w, r, fmt.Errorf("update template: %w", err))
		return
	}
	writeJSON(w, http.StatusOK, toTemplateJSON(row, sessionFrom(ctx).User))
}

func (s *Server) deleteTemplate(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	t, err := s.editableTemplate(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		q := dbq.New(tx)
		if err := q.ComposeDeleteTemplate(ctx, t.ID); err != nil {
			return err
		}
		return s.auditTemplate(ctx, q, r, audit.TemplateDeleted, t)
	})
	if err != nil {
		writeError(w, r, fmt.Errorf("delete template: %w", err))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type renderedTemplateJSON struct {
	Subject    string   `json:"subject"`
	BodyHTML   string   `json:"body_html"`
	Unresolved []string `json:"unresolved"`
}

func (s *Server) renderTemplate(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	user := sessionFrom(ctx).User
	tid, ok := parseUUID(chi.URLParam(r, "id"))
	if !ok {
		writeError(w, r, errNotFound)
		return
	}
	convID := r.URL.Query().Get("conversation_id")
	scope, err := policy.MailboxScope(ctx, s.q, user)
	if err != nil {
		writeError(w, r, err)
		return
	}
	cid, ok := parseUUID(convID)
	if !ok {
		writeError(w, r, errBadRequest("conversation_id must be a UUID"))
		return
	}
	conv, err := s.conversationInScope(ctx, scope, cid, false)
	if err != nil {
		writeError(w, r, err)
		return
	}
	visible, err := s.q.ComposeTemplateVisible(ctx, dbq.ComposeTemplateVisibleParams{ID: tid, UserID: user.ID, MailboxIds: scope.Read})
	if err != nil {
		writeError(w, r, fmt.Errorf("check template: %w", err))
		return
	}
	if !visible {
		writeError(w, r, errNotFound)
		return
	}
	t, err := s.q.ComposeGetTemplate(ctx, tid)
	if err != nil {
		writeError(w, r, fmt.Errorf("load template: %w", err))
		return
	}
	mb, err := s.q.GetMailbox(ctx, conv.MailboxID)
	if err != nil {
		writeError(w, r, fmt.Errorf("load mailbox: %w", err))
		return
	}
	vars := compose.Variables{AgentName: user.Name, ConversationNumber: fmt.Sprint(conv.Number), MailboxName: mb.Name}
	if conv.ContactID.Valid {
		c, err := s.q.ComposeGetContact(ctx, conv.ContactID)
		if err != nil {
			writeError(w, r, fmt.Errorf("load contact: %w", err))
			return
		}
		vars.ContactName, vars.ContactEmail = c.Name, c.Email
	}
	body, unresolvedBody := vars.Render(t.BodyHtml)
	subject, unresolvedSubject := vars.RenderText(t.Subject)
	unresolved := []string{}
	for _, name := range append(unresolvedBody, unresolvedSubject...) {
		if !slices.Contains(unresolved, name) {
			unresolved = append(unresolved, name)
		}
	}
	writeJSON(w, http.StatusOK, renderedTemplateJSON{Subject: subject, BodyHTML: body, Unresolved: unresolved})
}

type signatureJSON struct {
	MailboxID *string `json:"mailbox_id"`
	BodyHTML  string  `json:"body_html"`
}

func (s *Server) listMySignatures(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	rows, err := s.q.ComposeListUserSignatures(ctx, sessionFrom(ctx).User.ID)
	if err != nil {
		writeError(w, r, fmt.Errorf("list signatures: %w", err))
		return
	}
	out := make([]signatureJSON, len(rows))
	for i, row := range rows {
		out[i].BodyHTML = row.BodyHtml
		if row.MailboxID.Valid {
			id := uuidStr(row.MailboxID)
			out[i].MailboxID = &id
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"signatures": out})
}

func (s *Server) putMySignature(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	user := sessionFrom(ctx).User
	if !policy.Has(user, policy.ConversationsWrite) {
		writeError(w, r, errForbidden)
		return
	}
	var req signatureJSON
	if err := decode(r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	var mailbox pgtype.UUID
	if req.MailboxID != nil {
		id, ok := parseUUID(*req.MailboxID)
		if !ok {
			writeError(w, r, errValidation(map[string]string{"mailbox_id": "invalid"}))
			return
		}
		scope, err := policy.MailboxScope(ctx, s.q, user)
		if err != nil {
			writeError(w, r, err)
			return
		}
		if !slices.Contains(scope.Read, id) {
			writeError(w, r, errNotFound)
			return
		}
		mailbox = id
	}
	body, err := sanitizeSignature(req.BodyHTML)
	if err != nil {
		writeError(w, r, err)
		return
	}
	switch {
	case body == "":
		err = s.q.ComposeDeleteUserSignature(ctx, dbq.ComposeDeleteUserSignatureParams{UserID: user.ID, MailboxID: mailbox})
	case mailbox.Valid:
		err = s.q.ComposeUpsertUserSignature(ctx, dbq.ComposeUpsertUserSignatureParams{UserID: user.ID, MailboxID: mailbox, BodyHtml: body})
	default:
		err = s.q.ComposeUpsertUserDefaultSignature(ctx, dbq.ComposeUpsertUserDefaultSignatureParams{UserID: user.ID, BodyHtml: body})
	}
	if err != nil {
		writeError(w, r, fmt.Errorf("save signature: %w", err))
		return
	}
	writeJSON(w, http.StatusOK, signatureJSON{MailboxID: req.MailboxID, BodyHTML: body})
}

const maxSignatureBytes = 20 << 10

// sanitizeSignature returns "" for an empty signature, which means "remove it".
func sanitizeSignature(raw string) (string, error) {
	if len(raw) > maxSignatureBytes {
		return "", errValidation(map[string]string{"body_html": "too_large"})
	}
	body := compose.Sanitize(raw, compose.SanitizeOptions{})
	if compose.IsEmpty(body) {
		return "", nil
	}
	return body, nil
}

func (s *Server) effectiveSignature(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	user := sessionFrom(ctx).User
	id, ok := parseUUID(r.URL.Query().Get("mailbox_id"))
	if !ok {
		writeError(w, r, errBadRequest("mailbox_id must be a UUID"))
		return
	}
	scope, err := policy.MailboxScope(ctx, s.q, user)
	if err != nil {
		writeError(w, r, err)
		return
	}
	if !slices.Contains(scope.Read, id) {
		writeError(w, r, errNotFound)
		return
	}
	row, err := s.q.ComposeResolveSignature(ctx, dbq.ComposeResolveSignatureParams{UserID: user.ID, MailboxID: id})
	if errors.Is(err, pgx.ErrNoRows) {
		writeJSON(w, http.StatusOK, map[string]string{"body_html": "", "source": ""})
		return
	}
	if err != nil {
		writeError(w, r, fmt.Errorf("resolve signature: %w", err))
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"body_html": row.BodyHtml, "source": row.Source})
}

// composerAdminRoutes are the workspace-level settings of the composer.
func (s *Server) composerAdminRoutes(r chi.Router) {
	r.Get("/mailboxes/{id}/signature", s.getMailboxSignature)
	r.Put("/mailboxes/{id}/signature", s.putMailboxSignature)
	r.Get("/settings/email", s.getEmailSettings)
	r.Put("/settings/email", s.putEmailSettings)
}

func (s *Server) getMailboxSignature(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id, ok := parseUUID(chi.URLParam(r, "id"))
	if !ok {
		writeError(w, r, errNotFound)
		return
	}
	body, err := s.q.ComposeGetMailboxSignature(ctx, id)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		writeError(w, r, fmt.Errorf("load signature: %w", err))
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"body_html": body})
}

func (s *Server) putMailboxSignature(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id, ok := parseUUID(chi.URLParam(r, "id"))
	if !ok {
		writeError(w, r, errNotFound)
		return
	}
	var req struct {
		BodyHTML string `json:"body_html"`
	}
	if err := decode(r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	body, err := sanitizeSignature(req.BodyHTML)
	if err != nil {
		writeError(w, r, err)
		return
	}
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		q := dbq.New(tx)
		exists, err := q.ComposeMailboxExists(ctx, id)
		if err != nil {
			return err
		}
		if !exists {
			return errNotFound
		}
		if body == "" {
			err = q.ComposeDeleteMailboxSignature(ctx, id)
		} else {
			err = q.ComposeUpsertMailboxSignature(ctx, dbq.ComposeUpsertMailboxSignatureParams{MailboxID: id, BodyHtml: body})
		}
		if err != nil {
			return err
		}
		return audit.Write(ctx, q, audit.Entry{
			Actor: sessionFrom(ctx).User.ID, IP: clientFrom(r).IP, Action: audit.MailboxSignatureChanged,
			TargetType: "mailbox", TargetID: id.String(),
		})
	})
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"body_html": body})
}

const maxFooterRunes = 300

type emailSettings struct {
	FooterText string `json:"footer_text"`
}

func loadEmailSettings(ctx context.Context, q *dbq.Queries) (emailSettings, error) {
	raw, err := q.GetSetting(ctx, settingEmailKey)
	if errors.Is(err, pgx.ErrNoRows) {
		return emailSettings{}, nil
	}
	if err != nil {
		return emailSettings{}, fmt.Errorf("load email settings: %w", err)
	}
	var out emailSettings
	if err := json.Unmarshal(raw, &out); err != nil {
		return emailSettings{}, fmt.Errorf("decode email settings: %w", err)
	}
	return out, nil
}

func (s *Server) getEmailSettings(w http.ResponseWriter, r *http.Request) {
	set, err := loadEmailSettings(r.Context(), s.q)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, set)
}

func (s *Server) putEmailSettings(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var req emailSettings
	if err := decode(r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	req.FooterText = strings.TrimSpace(req.FooterText)
	if utf8.RuneCountInString(req.FooterText) > maxFooterRunes || strings.ContainsAny(req.FooterText, "\r\n\x00") {
		writeError(w, r, errValidation(map[string]string{"footer_text": "invalid"}))
		return
	}
	raw, err := json.Marshal(req)
	if err != nil {
		writeError(w, r, err)
		return
	}
	actor := sessionFrom(ctx).User
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		q := dbq.New(tx)
		if err := q.UpsertSetting(ctx, dbq.UpsertSettingParams{Key: settingEmailKey, Value: raw, UpdatedBy: actor.ID}); err != nil {
			return err
		}
		return audit.Write(ctx, q, audit.Entry{Actor: actor.ID, IP: clientFrom(r).IP, Action: audit.EmailSettingsChanged, TargetType: "settings", TargetID: settingEmailKey})
	})
	if err != nil {
		writeError(w, r, fmt.Errorf("save email settings: %w", err))
		return
	}
	writeJSON(w, http.StatusOK, req)
}
