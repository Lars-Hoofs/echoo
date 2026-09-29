package api

import (
	"context"
	"encoding/base64"
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
	"echoo/internal/contacts"
	"echoo/internal/db"
	"echoo/internal/db/dbq"
	"echoo/internal/mail/ingest"
)

var domainPattern = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?(\.[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?)+$`)

type organizationItemJSON struct {
	ID           string    `json:"id"`
	Name         string    `json:"name"`
	Domains      []string  `json:"domains"`
	ContactCount int32     `json:"contact_count"`
	CreatedAt    time.Time `json:"created_at"`
}

type organizationContactJSON struct {
	ID             string    `json:"id"`
	Name           string    `json:"name"`
	Email          string    `json:"email"`
	LastActivityAt time.Time `json:"last_activity_at"`
}

type organizationDetailJSON struct {
	ID               string                    `json:"id"`
	Name             string                    `json:"name"`
	Domains          []string                  `json:"domains"`
	CustomAttributes map[string]any            `json:"custom_attributes"`
	ContactCount     int32                     `json:"contact_count"`
	CreatedAt        time.Time                 `json:"created_at"`
	UpdatedAt        time.Time                 `json:"updated_at"`
	Contacts         []organizationContactJSON `json:"contacts"`
}

func (s *Server) listOrganizations(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	_, v, err := s.contactViewer(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	limit, err := pageLimit(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	params := dbq.ListVisibleOrganizationsParams{UserID: v.UserID, Admin: v.Admin, MailboxIds: v.MailboxIDs, PageSize: limit + 1}
	if q := strings.TrimSpace(r.URL.Query().Get("q")); q != "" {
		if utf8.RuneCountInString(q) > 100 {
			writeError(w, r, errBadRequest("q is too long"))
			return
		}
		params.Search = pgtype.Text{String: contacts.EscapeLike(q), Valid: true}
	}
	if c := r.URL.Query().Get("cursor"); c != "" {
		name, id, ok := decodeNameCursor(c)
		if !ok {
			writeError(w, r, errBadRequest("invalid cursor"))
			return
		}
		params.CursorName = pgtype.Text{String: name, Valid: true}
		params.CursorID = id
	}
	rows, err := s.q.ListVisibleOrganizations(ctx, params)
	if err != nil {
		writeError(w, r, fmt.Errorf("list organizations: %w", err))
		return
	}
	out := struct {
		Organizations []organizationItemJSON `json:"organizations"`
		NextCursor    *string                `json:"next_cursor"`
	}{Organizations: []organizationItemJSON{}}
	if len(rows) > int(limit) {
		rows = rows[:limit]
		last := rows[len(rows)-1]
		next := encodeNameCursor(last.SortName, last.ID)
		out.NextCursor = &next
	}
	for _, row := range rows {
		out.Organizations = append(out.Organizations, organizationItemJSON{
			ID: uuidStr(row.ID), Name: row.Name, Domains: row.Domains, ContactCount: row.ContactCount, CreatedAt: row.CreatedAt.Time.UTC(),
		})
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) organizationDetail(r *http.Request, v contacts.Viewer, id pgtype.UUID) (*organizationDetailJSON, error) {
	ctx := r.Context()
	o, err := s.q.GetVisibleOrganization(ctx, dbq.GetVisibleOrganizationParams{ID: id, UserID: v.UserID, Admin: v.Admin, MailboxIds: v.MailboxIDs})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, errNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("load organization: %w", err)
	}
	attrs, err := attributesOut(o.CustomAttributes)
	if err != nil {
		return nil, err
	}
	members, err := s.q.ListOrganizationContacts(ctx, dbq.ListOrganizationContactsParams{
		OrganizationID: id, UserID: v.UserID, Admin: v.Admin, MailboxIds: v.MailboxIDs, RowLimit: 100,
	})
	if err != nil {
		return nil, fmt.Errorf("list organization contacts: %w", err)
	}
	out := &organizationDetailJSON{
		ID: uuidStr(o.ID), Name: o.Name, Domains: o.Domains, CustomAttributes: attrs, ContactCount: o.ContactCount,
		CreatedAt: o.CreatedAt.Time.UTC(), UpdatedAt: o.UpdatedAt.Time.UTC(), Contacts: []organizationContactJSON{},
	}
	for _, m := range members {
		out.Contacts = append(out.Contacts, organizationContactJSON{ID: uuidStr(m.ID), Name: m.Name, Email: m.Email, LastActivityAt: m.LastActivityAt.Time.UTC()})
	}
	return out, nil
}

func (s *Server) getOrganization(w http.ResponseWriter, r *http.Request) {
	id, ok := parseUUID(chi.URLParam(r, "id"))
	if !ok {
		writeError(w, r, errNotFound)
		return
	}
	_, v, err := s.contactViewer(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	out, err := s.organizationDetail(r, v, id)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"organization": out})
}

type organizationRequest struct {
	Name             *string                    `json:"name"`
	Domains          []string                   `json:"domains"`
	CustomAttributes map[string]json.RawMessage `json:"custom_attributes"`
}

// cleanDomains lower-cases and validates organization domains. Free-mail domains never
// identify an organization, matching what mail ingest does.
func cleanDomains(in []string) ([]string, string) {
	if len(in) > maxDomainsPerOrg {
		return nil, "too_many"
	}
	out := make([]string, 0, len(in))
	for _, d := range in {
		d = strings.ToLower(strings.TrimSpace(d))
		switch {
		case len(d) > 253 || !domainPattern.MatchString(d):
			return nil, "invalid"
		case ingest.IsFreeMailDomain(d):
			return nil, "free_mail"
		case slices.Contains(out, d):
			return nil, "duplicate"
		}
		out = append(out, d)
	}
	return out, ""
}

var errDomainTaken = &apiError{Status: http.StatusConflict, Code: "domain_in_use", Message: "a domain already belongs to another organization"}

// lockDomains takes the ingest advisory lock per domain, in stable order, so mail ingest cannot
// create an organization for a domain while it is being assigned here.
func lockDomains(ctx context.Context, q *dbq.Queries, domains []string) error {
	sorted := slices.Clone(domains)
	slices.Sort(sorted)
	for _, d := range sorted {
		if err := q.IngestAdvisoryLock(ctx, "organization:"+d); err != nil {
			return fmt.Errorf("lock organization domain: %w", err)
		}
	}
	return nil
}

func (s *Server) createOrganization(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	user, v, err := s.contactViewer(r)
	if err == nil {
		err = requireModifier(user)
	}
	if err != nil {
		writeError(w, r, err)
		return
	}
	var req organizationRequest
	if err := decode(r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	fields := map[string]string{}
	name := ""
	if req.Name != nil {
		name = strings.TrimSpace(*req.Name)
	}
	if name == "" || utf8.RuneCountInString(name) > 200 || strings.ContainsFunc(name, func(r rune) bool { return r < 0x20 || r == 0x7f }) {
		fields["name"] = "invalid"
	}
	domains, problem := cleanDomains(req.Domains)
	if problem != "" {
		fields["domains"] = problem
	}
	if len(fields) > 0 {
		writeError(w, r, errValidation(fields))
		return
	}
	defs, err := s.attributeDefs(ctx, contacts.EntityOrganization)
	if err != nil {
		writeError(w, r, err)
		return
	}
	patch, err := contacts.ParsePatch(req.CustomAttributes)
	if err != nil {
		writeError(w, r, errBadRequest(err.Error()))
		return
	}
	attrs, problems := contacts.ApplyPatch(defs, contacts.Attributes{}, patch, false)
	if len(problems) > 0 {
		writeError(w, r, attributeProblems(problems))
		return
	}
	encoded, err := attrs.Encode()
	if err != nil {
		writeError(w, r, err)
		return
	}
	var id pgtype.UUID
	err = db.InTx(ctx, s.pool, func(q *dbq.Queries) error {
		if err := s.checkDomainsFree(ctx, q, domains, pgtype.UUID{}); err != nil {
			return err
		}
		var err error
		id, err = q.InsertOrganization(ctx, dbq.InsertOrganizationParams{Name: name, Domains: domains, CustomAttributes: encoded, CreatedBy: user.ID})
		if err != nil {
			return fmt.Errorf("create organization: %w", err)
		}
		return audit.Write(ctx, q, audit.Entry{Actor: user.ID, IP: clientFrom(r).IP, Action: audit.OrganizationCreated, TargetType: "organization", TargetID: uuidStr(id)})
	})
	if err != nil {
		writeError(w, r, err)
		return
	}
	out, err := s.organizationDetail(r, v, id)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"organization": out})
}

func (s *Server) checkDomainsFree(ctx context.Context, q *dbq.Queries, domains []string, except pgtype.UUID) error {
	if len(domains) == 0 {
		return nil
	}
	if err := lockDomains(ctx, q, domains); err != nil {
		return err
	}
	owners, err := q.OrganizationsOwningDomains(ctx, dbq.OrganizationsOwningDomainsParams{Domains: domains, ExceptID: except})
	if err != nil {
		return fmt.Errorf("check domains: %w", err)
	}
	if len(owners) > 0 {
		return errDomainTaken
	}
	return nil
}

func (s *Server) patchOrganization(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id, ok := parseUUID(chi.URLParam(r, "id"))
	if !ok {
		writeError(w, r, errNotFound)
		return
	}
	user, v, err := s.contactViewer(r)
	if err == nil {
		err = requireModifier(user)
	}
	if err != nil {
		writeError(w, r, err)
		return
	}
	var req organizationRequest
	if err := decode(r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	fields := map[string]string{}
	if req.Name != nil {
		n := strings.TrimSpace(*req.Name)
		if n == "" || utf8.RuneCountInString(n) > 200 || strings.ContainsFunc(n, func(r rune) bool { return r < 0x20 || r == 0x7f }) {
			fields["name"] = "invalid"
		}
		req.Name = &n
	}
	var domains []string
	if req.Domains != nil {
		var problem string
		if domains, problem = cleanDomains(req.Domains); problem != "" {
			fields["domains"] = problem
		}
	}
	if len(fields) > 0 {
		writeError(w, r, errValidation(fields))
		return
	}
	patch, err := contacts.ParsePatch(req.CustomAttributes)
	if err != nil {
		writeError(w, r, errBadRequest(err.Error()))
		return
	}
	defs, err := s.attributeDefs(ctx, contacts.EntityOrganization)
	if err != nil {
		writeError(w, r, err)
		return
	}
	err = db.InTx(ctx, s.pool, func(q *dbq.Queries) error {
		if domains != nil {
			if err := lockDomains(ctx, q, domains); err != nil {
				return err
			}
		}
		cur, err := q.LockOrganization(ctx, id)
		if errors.Is(err, pgx.ErrNoRows) {
			return errNotFound
		}
		if err != nil {
			return fmt.Errorf("lock organization: %w", err)
		}
		visible, err := v.OrganizationVisible(ctx, q, id)
		if err != nil {
			return fmt.Errorf("check visibility: %w", err)
		}
		if !visible {
			return errNotFound
		}
		params := dbq.UpdateOrganizationParams{ID: id, Name: cur.Name, Domains: cur.Domains}
		changed := []string{}
		if req.Name != nil {
			params.Name = *req.Name
			changed = append(changed, "name")
		}
		if domains != nil {
			if err := s.checkDomainsFree(ctx, q, domains, id); err != nil {
				return err
			}
			params.Domains = domains
			changed = append(changed, "domains")
		}
		current, err := contacts.DecodeAttributes(cur.CustomAttributes)
		if err != nil {
			return err
		}
		merged, problems := contacts.ApplyPatch(defs, current, patch, false)
		if len(problems) > 0 {
			return attributeProblems(problems)
		}
		if params.CustomAttributes, err = merged.Encode(); err != nil {
			return err
		}
		if len(patch) > 0 {
			changed = append(changed, "custom_attributes")
		}
		if err := q.UpdateOrganization(ctx, params); err != nil {
			return fmt.Errorf("update organization: %w", err)
		}
		return audit.Write(ctx, q, audit.Entry{
			Actor: user.ID, IP: clientFrom(r).IP, Action: audit.OrganizationUpdated, TargetType: "organization", TargetID: uuidStr(id),
			Metadata: map[string]any{"fields": changed},
		})
	})
	if err != nil {
		writeError(w, r, err)
		return
	}
	out, err := s.organizationDetail(r, v, id)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"organization": out})
}

func (s *Server) organizationConversations(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id, ok := parseUUID(chi.URLParam(r, "id"))
	if !ok {
		writeError(w, r, errNotFound)
		return
	}
	_, v, err := s.contactViewer(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	limit, err := pageLimit(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	visible, err := v.OrganizationVisible(ctx, s.q, id)
	if err != nil {
		writeError(w, r, err)
		return
	}
	if !visible {
		writeError(w, r, errNotFound)
		return
	}
	params := dbq.ListOrganizationConversationPageParams{OrganizationID: id, Admin: v.Admin, MailboxIds: v.MailboxIDs, PageSize: limit + 1}
	if c := r.URL.Query().Get("cursor"); c != "" {
		cur, err := decodeRankCursor(c)
		if err != nil {
			writeError(w, r, err)
			return
		}
		params.CursorRank = cur.rank
		params.CursorAt = pgtype.Timestamptz{Time: cur.at, Valid: true}
		params.CursorID = cur.id
	}
	rows, err := s.q.ListOrganizationConversationPage(ctx, params)
	if err != nil {
		writeError(w, r, fmt.Errorf("list organization conversations: %w", err))
		return
	}
	ids, ranks, ats := make([]pgtype.UUID, len(rows)), make([]int32, len(rows)), make([]time.Time, len(rows))
	for i, row := range rows {
		ids[i], ranks[i], ats[i] = row.ID, row.Rank, row.LastMessageAt.Time
	}
	out, err := s.conversationPage(ctx, v, ids, ranks, ats, limit)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func encodeNameCursor(name string, id pgtype.UUID) string {
	return base64.RawURLEncoding.EncodeToString([]byte(id.String() + "|" + name))
}

func decodeNameCursor(s string) (string, pgtype.UUID, bool) {
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return "", pgtype.UUID{}, false
	}
	idText, name, ok := strings.Cut(string(raw), "|")
	id, idOK := parseUUID(idText)
	return name, id, ok && idOK
}
