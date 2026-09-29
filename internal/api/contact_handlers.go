package api

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
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
	"echoo/internal/policy"
	"echoo/internal/webhooks"
)

const (
	maxEmailsPerContact = 10
	maxNoteRunes        = 10000
	noteListLimit       = 200
	defaultContactLimit = 50
	maxFilterBytes      = 8 << 10
	exportBatch         = 500
	maxDomainsPerOrg    = 20
)

// contactViewer resolves the signed-in user and what they may see of the CRM.
func (s *Server) contactViewer(r *http.Request) (dbq.User, contacts.Viewer, error) {
	user := sessionFrom(r.Context()).User
	if !policy.Has(user, policy.ContactsRead) {
		return user, contacts.Viewer{}, errForbidden
	}
	v, err := contacts.ViewerFor(r.Context(), s.q, user)
	return user, v, err
}

func requireModifier(u dbq.User) error {
	if !policy.Has(u, policy.ContactsWrite) {
		return errForbidden
	}
	return nil
}

func actorOf(r *http.Request, u dbq.User) contacts.Actor {
	return contacts.Actor{UserID: u.ID, IP: clientFrom(r).IP}
}

type contactAddressJSON struct {
	Email   string `json:"email"`
	Primary bool   `json:"primary"`
	// BouncedAt is set after a permanent delivery failure; campaigns skip the address.
	BouncedAt *time.Time `json:"bounced_at,omitempty"`
}

type contactItemJSON struct {
	ID                string         `json:"id"`
	Name              string         `json:"name"`
	Phone             string         `json:"phone"`
	Email             string         `json:"email"`
	Organization      *refJSON       `json:"organization"`
	CustomAttributes  map[string]any `json:"custom_attributes"`
	ConversationCount int32          `json:"conversation_count"`
	CreatedAt         time.Time      `json:"created_at"`
	LastActivityAt    time.Time      `json:"last_activity_at"`
}

type contactDetailFullJSON struct {
	ID                string               `json:"id"`
	Name              string               `json:"name"`
	Phone             string               `json:"phone"`
	Emails            []contactAddressJSON `json:"emails"`
	Organization      *refJSON             `json:"organization"`
	CustomAttributes  map[string]any       `json:"custom_attributes"`
	ConversationCount int32                `json:"conversation_count"`
	CreatedAt         time.Time            `json:"created_at"`
	UpdatedAt         time.Time            `json:"updated_at"`
	LastActivityAt    time.Time            `json:"last_activity_at"`
	// UnsubscribedAt is set when the contact opted out of campaigns.
	UnsubscribedAt *time.Time `json:"unsubscribed_at"`
}

func attributesOut(raw []byte) (map[string]any, error) {
	a, err := contacts.DecodeAttributes(raw)
	if err != nil {
		return nil, err
	}
	return a, nil
}

func (s *Server) attributeDefs(ctx context.Context, entity string) ([]dbq.CustomAttributeDef, error) {
	arg := pgtype.Text{}
	if entity != "" {
		arg = pgtype.Text{String: entity, Valid: true}
	}
	defs, err := s.q.ListAttributeDefs(ctx, arg)
	if err != nil {
		return nil, fmt.Errorf("load attribute definitions: %w", err)
	}
	return defs, nil
}

type contactListParams struct {
	contacts.ListParams
	defs []dbq.CustomAttributeDef
}

// parseContactList reads the shared query parameters of the list and the CSV export.
// paged is false for the export, which walks every page itself.
func (s *Server) parseContactList(r *http.Request, user dbq.User, paged bool) (contactListParams, error) {
	ctx := r.Context()
	q := r.URL.Query()
	p := contactListParams{ListParams: contacts.ListParams{Sort: contacts.SortActivity, Desc: true, Limit: defaultContactLimit}}
	defs, err := s.attributeDefs(ctx, "")
	if err != nil {
		return p, err
	}
	p.defs = defs
	if v := q.Get("sort"); v != "" {
		if !slices.Contains([]string{contacts.SortName, contacts.SortActivity, contacts.SortCreated}, v) {
			return p, errBadRequest("sort must be name, last_activity or created")
		}
		p.Sort = v
		p.Desc = v != contacts.SortName
	}
	switch q.Get("dir") {
	case "":
	case "asc":
		p.Desc = false
	case "desc":
		p.Desc = true
	default:
		return p, errBadRequest("dir must be asc or desc")
	}
	p.Search = strings.TrimSpace(q.Get("q"))
	if utf8.RuneCountInString(p.Search) > 100 {
		return p, errBadRequest("q is too long")
	}
	if v := q.Get("organization_id"); v != "" {
		id, ok := parseUUID(v)
		if !ok {
			return p, errBadRequest("organization_id must be a UUID")
		}
		p.OrganizationID = id
	}
	if paged {
		if v := q.Get("limit"); v != "" {
			n, err := strconv.Atoi(v)
			if err != nil || n < 1 || n > contacts.MaxPageSize {
				return p, errBadRequest("limit must be between 1 and " + strconv.Itoa(contacts.MaxPageSize))
			}
			p.Limit = n
		}
		p.Cursor = q.Get("cursor")
	}
	segmentID, filterRaw := q.Get("segment_id"), q.Get("filter")
	switch {
	case segmentID != "" && filterRaw != "":
		return p, errBadRequest("use either segment_id or filter")
	case segmentID != "":
		id, ok := parseUUID(segmentID)
		if !ok {
			return p, errBadRequest("segment_id must be a UUID")
		}
		seg, err := s.q.GetSegment(ctx, id)
		if errors.Is(err, pgx.ErrNoRows) || (err == nil && seg.OwnerUserID != user.ID && !seg.Shared) {
			return p, errNotFound
		}
		if err != nil {
			return p, fmt.Errorf("load segment: %w", err)
		}
		filterRaw = string(seg.Filter)
	}
	if filterRaw != "" {
		if len(filterRaw) > maxFilterBytes {
			return p, errValidation(map[string]string{"filter": "too_large"})
		}
		f, err := contacts.ParseFilter([]byte(filterRaw), defs)
		if err != nil {
			return p, errValidation(map[string]string{"filter": "invalid"})
		}
		p.Filter = &f
	}
	return p, nil
}

type contactListJSON struct {
	Contacts   []contactItemJSON `json:"contacts"`
	NextCursor *string           `json:"next_cursor"`
}

func toContactItem(row contacts.Row) (contactItemJSON, error) {
	attrs, err := attributesOut(row.CustomAttributes)
	if err != nil {
		return contactItemJSON{}, err
	}
	item := contactItemJSON{
		ID: uuidStr(row.ID), Name: row.Name, Phone: row.Phone, Email: row.PrimaryEmail, CustomAttributes: attrs,
		ConversationCount: row.ConversationCount, CreatedAt: row.CreatedAt.UTC(), LastActivityAt: row.LastActivityAt.UTC(),
	}
	if row.OrganizationID.Valid {
		item.Organization = &refJSON{ID: uuidStr(row.OrganizationID), Name: row.OrganizationName}
	}
	return item, nil
}

func (s *Server) listContacts(w http.ResponseWriter, r *http.Request) {
	user, v, err := s.contactViewer(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	p, err := s.parseContactList(r, user, true)
	if err != nil {
		writeError(w, r, err)
		return
	}
	rows, err := contacts.List(r.Context(), s.pool, v, p.ListParams, p.defs)
	if errors.Is(err, contacts.ErrInvalidCursor) {
		writeError(w, r, errBadRequest("invalid cursor"))
		return
	}
	if err != nil {
		writeError(w, r, err)
		return
	}
	out := contactListJSON{Contacts: []contactItemJSON{}}
	if len(rows) > p.Limit {
		rows = rows[:p.Limit]
		next, err := contacts.NextCursor(rows, p.ListParams)
		if err != nil {
			writeError(w, r, err)
			return
		}
		out.NextCursor = &next
	}
	for _, row := range rows {
		item, err := toContactItem(row)
		if err != nil {
			writeError(w, r, err)
			return
		}
		out.Contacts = append(out.Contacts, item)
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) contactDetail(ctx context.Context, v contacts.Viewer, id pgtype.UUID) (*contactDetailFullJSON, error) {
	c, err := s.q.GetVisibleContact(ctx, dbq.GetVisibleContactParams{ID: id, UserID: v.UserID, Admin: v.Admin, MailboxIds: v.MailboxIDs})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, errNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("load contact: %w", err)
	}
	addrs, err := s.q.ListAddressesOfContacts(ctx, []pgtype.UUID{id})
	if err != nil {
		return nil, fmt.Errorf("load addresses: %w", err)
	}
	attrs, err := attributesOut(c.CustomAttributes)
	if err != nil {
		return nil, err
	}
	out := &contactDetailFullJSON{
		ID: uuidStr(c.ID), Name: c.Name, Phone: c.Phone, Emails: make([]contactAddressJSON, 0, len(addrs)),
		CustomAttributes: attrs, ConversationCount: c.ConversationCount, CreatedAt: c.CreatedAt.Time.UTC(),
		UpdatedAt: c.UpdatedAt.Time.UTC(), LastActivityAt: c.LastActivityAt.Time.UTC(), UnsubscribedAt: timeOrNil(c.UnsubscribedAt),
	}
	for _, a := range addrs {
		out.Emails = append(out.Emails, contactAddressJSON{Email: a.Email, Primary: a.IsPrimary, BouncedAt: timeOrNil(a.BouncedAt)})
	}
	if c.OrganizationID.Valid {
		out.Organization = &refJSON{ID: uuidStr(c.OrganizationID), Name: c.OrganizationName.String}
	}
	return out, nil
}

func (s *Server) getContact(w http.ResponseWriter, r *http.Request) {
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
	out, err := s.contactDetail(r.Context(), v, id)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"contact": out})
}

type emailInput struct {
	Email   string `json:"email"`
	Primary bool   `json:"primary"`
}

// cleanEmails validates a submitted address list: valid, unique, one primary.
func cleanEmails(in []emailInput) ([]contactAddressJSON, map[string]string) {
	fields := map[string]string{}
	if len(in) == 0 || len(in) > maxEmailsPerContact {
		fields["emails"] = "invalid_count"
		return nil, fields
	}
	out := make([]contactAddressJSON, 0, len(in))
	primaries := 0
	for _, e := range in {
		addr, err := contacts.NormalizeEmail(e.Email)
		if err != nil {
			fields["emails"] = "invalid"
			return nil, fields
		}
		if slices.ContainsFunc(out, func(o contactAddressJSON) bool { return o.Email == addr }) {
			fields["emails"] = "duplicate"
			return nil, fields
		}
		if e.Primary {
			primaries++
		}
		out = append(out, contactAddressJSON{Email: addr, Primary: e.Primary})
	}
	switch {
	case primaries > 1:
		fields["emails"] = "multiple_primary"
		return nil, fields
	case primaries == 0:
		out[0].Primary = true
	}
	return out, nil
}

func cleanPersonName(s string, fields map[string]string, field string, max int) string {
	s = strings.TrimSpace(s)
	if utf8.RuneCountInString(s) > max || strings.ContainsFunc(s, func(r rune) bool { return r < 0x20 || r == 0x7f }) {
		fields[field] = "invalid"
	}
	return s
}

type contactRequest struct {
	Name             *string                    `json:"name"`
	Phone            *string                    `json:"phone"`
	Emails           []emailInput               `json:"emails"`
	OrganizationID   nullable[string]           `json:"organization_id"`
	CustomAttributes map[string]json.RawMessage `json:"custom_attributes"`
}

var errEmailInUse = &apiError{Status: http.StatusConflict, Code: "email_in_use", Message: "an email address already belongs to another contact"}

// resolveOrganization checks that the caller may see the organization they attach.
func (s *Server) resolveOrganization(ctx context.Context, q *dbq.Queries, v contacts.Viewer, n nullable[string]) (pgtype.UUID, error) {
	if !n.set || n.value == nil {
		return pgtype.UUID{}, nil
	}
	id, ok := parseUUID(*n.value)
	if !ok {
		return pgtype.UUID{}, errValidation(map[string]string{"organization_id": "invalid"})
	}
	_, err := q.GetVisibleOrganization(ctx, dbq.GetVisibleOrganizationParams{ID: id, UserID: v.UserID, Admin: v.Admin, MailboxIds: v.MailboxIDs})
	if errors.Is(err, pgx.ErrNoRows) {
		return pgtype.UUID{}, errValidation(map[string]string{"organization_id": "invalid"})
	}
	if err != nil {
		return pgtype.UUID{}, fmt.Errorf("check organization: %w", err)
	}
	return id, nil
}

func attributeProblems(problems map[string]string) error {
	fields := make(map[string]string, len(problems))
	for k, v := range problems {
		fields["custom_attributes."+k] = v
	}
	return errValidation(fields)
}

func (s *Server) createContact(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	user, v, err := s.contactViewer(r)
	if err == nil {
		err = requireModifier(user)
	}
	if err != nil {
		writeError(w, r, err)
		return
	}
	var req contactRequest
	if err := decode(r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	fields := map[string]string{}
	emails, emailFields := cleanEmails(req.Emails)
	for k, val := range emailFields {
		fields[k] = val
	}
	name, phone := "", ""
	if req.Name != nil {
		name = cleanPersonName(*req.Name, fields, "name", 200)
	}
	if req.Phone != nil {
		phone = cleanPersonName(*req.Phone, fields, "phone", 50)
	}
	if len(fields) > 0 {
		writeError(w, r, errValidation(fields))
		return
	}
	defs, err := s.attributeDefs(ctx, contacts.EntityContact)
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
		org, err := s.resolveOrganization(ctx, q, v, req.OrganizationID)
		if err != nil {
			return err
		}
		if err := lockEmails(ctx, q, emails); err != nil {
			return err
		}
		for _, e := range emails {
			if _, err := q.FindContactByEmail(ctx, e.Email); err == nil {
				return errEmailInUse
			} else if !errors.Is(err, pgx.ErrNoRows) {
				return fmt.Errorf("find contact: %w", err)
			}
		}
		id, err = q.InsertContact(ctx, dbq.InsertContactParams{
			Name: name, Phone: phone, OrganizationID: org, CustomAttributes: encoded, CreatedBy: user.ID,
		})
		if err != nil {
			return fmt.Errorf("create contact: %w", err)
		}
		for _, e := range emails {
			if err := q.InsertContactAddress(ctx, dbq.InsertContactAddressParams{ContactID: id, Email: e.Email, IsPrimary: e.Primary}); err != nil {
				return fmt.Errorf("create contact address: %w", err)
			}
		}
		if err := webhooks.Record(ctx, q, webhooks.Event{Type: webhooks.ContactCreated, ContactID: id}); err != nil {
			return err
		}
		return audit.Write(ctx, q, audit.Entry{Actor: user.ID, IP: clientFrom(r).IP, Action: audit.ContactCreated, TargetType: "contact", TargetID: uuidStr(id)})
	})
	if isUniqueViolation(err) {
		err = errEmailInUse
	}
	if err != nil {
		writeError(w, r, err)
		return
	}
	out, err := s.contactDetail(ctx, v, id)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"contact": out})
}

// lockEmails takes the mail-ingest advisory lock for every address, in a stable order, so a
// contact cannot be created for an address while ingest creates one for the same.
func lockEmails(ctx context.Context, q *dbq.Queries, emails []contactAddressJSON) error {
	list := make([]string, len(emails))
	for i, e := range emails {
		list[i] = e.Email
	}
	slices.Sort(list)
	for _, e := range list {
		if err := q.IngestAdvisoryLock(ctx, "contact:"+e); err != nil {
			return fmt.Errorf("lock contact address: %w", err)
		}
	}
	return nil
}

func (s *Server) patchContact(w http.ResponseWriter, r *http.Request) {
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
	var req contactRequest
	if err := decode(r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	fields := map[string]string{}
	var emails []contactAddressJSON
	if req.Emails != nil {
		var emailFields map[string]string
		emails, emailFields = cleanEmails(req.Emails)
		for k, val := range emailFields {
			fields[k] = val
		}
	}
	if req.Name != nil {
		*req.Name = cleanPersonName(*req.Name, fields, "name", 200)
	}
	if req.Phone != nil {
		*req.Phone = cleanPersonName(*req.Phone, fields, "phone", 50)
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
	defs, err := s.attributeDefs(ctx, contacts.EntityContact)
	if err != nil {
		writeError(w, r, err)
		return
	}

	err = db.InTx(ctx, s.pool, func(q *dbq.Queries) error {
		if emails != nil {
			if err := lockEmails(ctx, q, emails); err != nil {
				return err
			}
		}
		cur, err := q.LockContact(ctx, id)
		if errors.Is(err, pgx.ErrNoRows) {
			return errNotFound
		}
		if err != nil {
			return fmt.Errorf("lock contact: %w", err)
		}
		visible, err := v.ContactVisible(ctx, q, id)
		if err != nil {
			return fmt.Errorf("check visibility: %w", err)
		}
		if !visible {
			return errNotFound
		}
		params := dbq.UpdateContactParams{ID: id, Name: cur.Name, Phone: cur.Phone, OrganizationID: cur.OrganizationID}
		changed := []string{}
		if req.Name != nil {
			params.Name = *req.Name
			changed = append(changed, "name")
		}
		if req.Phone != nil {
			params.Phone = *req.Phone
			changed = append(changed, "phone")
		}
		if req.OrganizationID.set {
			org, err := s.resolveOrganization(ctx, q, v, req.OrganizationID)
			if err != nil {
				return err
			}
			params.OrganizationID = org
			changed = append(changed, "organization")
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
		if err := q.UpdateContact(ctx, params); err != nil {
			return fmt.Errorf("update contact: %w", err)
		}
		if emails != nil {
			if err := s.replaceAddresses(ctx, q, id, emails); err != nil {
				return err
			}
			changed = append(changed, "emails")
		}
		return audit.Write(ctx, q, audit.Entry{
			Actor: user.ID, IP: clientFrom(r).IP, Action: audit.ContactUpdated, TargetType: "contact", TargetID: uuidStr(id),
			Metadata: map[string]any{"fields": changed},
		})
	})
	if isUniqueViolation(err) {
		err = errEmailInUse
	}
	if err != nil {
		writeError(w, r, err)
		return
	}
	out, err := s.contactDetail(ctx, v, id)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"contact": out})
}

// replaceAddresses makes the contact's addresses equal to want. An address owned by another
// contact raises a unique violation, which the caller maps to email_in_use.
func (s *Server) replaceAddresses(ctx context.Context, q *dbq.Queries, id pgtype.UUID, want []contactAddressJSON) error {
	have, err := q.ContactAddressEmails(ctx, id)
	if err != nil {
		return fmt.Errorf("list addresses: %w", err)
	}
	for _, e := range have {
		if !slices.ContainsFunc(want, func(w contactAddressJSON) bool { return w.Email == e }) {
			if err := q.DeleteContactAddress(ctx, dbq.DeleteContactAddressParams{ContactID: id, Email: e}); err != nil {
				return fmt.Errorf("delete address: %w", err)
			}
		}
	}
	primary := ""
	for _, w := range want {
		if w.Primary {
			primary = w.Email
		}
		if !slices.Contains(have, w.Email) {
			if err := q.InsertContactAddress(ctx, dbq.InsertContactAddressParams{ContactID: id, Email: w.Email}); err != nil {
				return err
			}
		}
	}
	if err := q.SetPrimaryContactAddress(ctx, dbq.SetPrimaryContactAddressParams{ContactID: id, Email: primary}); err != nil {
		return fmt.Errorf("set primary address: %w", err)
	}
	return nil
}

type mergeRequest struct {
	SourceID string `json:"source_id"`
}

func (s *Server) mergeContact(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	primary, ok := parseUUID(chi.URLParam(r, "id"))
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
	var req mergeRequest
	if err := decode(r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	source, ok := parseUUID(req.SourceID)
	if !ok {
		writeError(w, r, errValidation(map[string]string{"source_id": "invalid"}))
		return
	}
	res, err := contacts.Merge(ctx, s.pool, v, actorOf(r, user), primary, source)
	switch {
	case errors.Is(err, contacts.ErrMergeSelf):
		writeError(w, r, errValidation(map[string]string{"source_id": "same_contact"}))
		return
	case errors.Is(err, contacts.ErrNotFound):
		writeError(w, r, errNotFound)
		return
	case err != nil:
		writeError(w, r, err)
		return
	}
	out, err := s.contactDetail(ctx, v, primary)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"contact": out,
		"merged":  map[string]any{"addresses": res.Addresses, "conversations": res.Conversations, "notes": res.Notes},
	})
}

type conversationPageJSON struct {
	Conversations []conversationJSON `json:"conversations"`
	NextCursor    *string            `json:"next_cursor"`
}

type rankCursor struct {
	rank int32
	at   time.Time
	id   pgtype.UUID
}

func (c rankCursor) encode() string {
	return base64.RawURLEncoding.EncodeToString([]byte(fmt.Sprintf("%d.%d.%s", c.rank, c.at.UnixMicro(), c.id.String())))
}

func decodeRankCursor(s string) (rankCursor, error) {
	raw, err := base64.RawURLEncoding.DecodeString(s)
	parts := strings.SplitN(string(raw), ".", 3)
	if err != nil || len(parts) != 3 {
		return rankCursor{}, errBadRequest("invalid cursor")
	}
	rank, err1 := strconv.ParseInt(parts[0], 10, 32)
	micros, err2 := strconv.ParseInt(parts[1], 10, 64)
	id, ok := parseUUID(parts[2])
	if err1 != nil || err2 != nil || !ok || rank < 0 || rank > 1 {
		return rankCursor{}, errBadRequest("invalid cursor")
	}
	return rankCursor{rank: int32(rank), at: time.UnixMicro(micros).UTC(), id: id}, nil
}

func pageLimit(r *http.Request) (int32, error) {
	limit := int32(defaultPageSize)
	if v := r.URL.Query().Get("limit"); v != "" {
		n, err := strconv.ParseInt(v, 10, 32)
		if err != nil || n < 1 || n > maxPageSize {
			return 0, errBadRequest("limit must be between 1 and " + strconv.Itoa(maxPageSize))
		}
		limit = int32(n)
	}
	return limit, nil
}

// conversationPage hydrates a page of conversation ids picked by the ranked queries, keeping
// their order.
func (s *Server) conversationPage(ctx context.Context, v contacts.Viewer, ids []pgtype.UUID, ranks []int32, ats []time.Time, limit int32) (conversationPageJSON, error) {
	out := conversationPageJSON{Conversations: []conversationJSON{}}
	if len(ids) > int(limit) {
		ids = ids[:limit]
		last := len(ids) - 1
		next := rankCursor{rank: ranks[last], at: ats[last], id: ids[last]}.encode()
		out.NextCursor = &next
	}
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := s.q.ListConversationsByID(ctx, dbq.ListConversationsByIDParams{Ids: ids, MailboxIds: v.MailboxIDs})
	if err != nil {
		return out, fmt.Errorf("load conversations: %w", err)
	}
	byID := make(map[pgtype.UUID]conversationJSON, len(rows))
	for _, row := range rows {
		byID[row.ID] = toConversationJSON(row)
	}
	for _, id := range ids {
		if c, ok := byID[id]; ok {
			out.Conversations = append(out.Conversations, c)
		}
	}
	if err := s.attachLabels(ctx, out.Conversations); err != nil {
		return out, err
	}
	if err := s.attachUnread(ctx, sessionFrom(ctx).User.ID, out.Conversations); err != nil {
		return out, err
	}
	return out, nil
}

func (s *Server) contactConversations(w http.ResponseWriter, r *http.Request) {
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
	params := dbq.ListContactConversationPageParams{
		ContactID: id, Admin: v.Admin, MailboxIds: v.MailboxIDs, PageSize: limit + 1,
	}
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
	visible, err := v.ContactVisible(ctx, s.q, id)
	if err != nil {
		writeError(w, r, err)
		return
	}
	if !visible {
		writeError(w, r, errNotFound)
		return
	}
	rows, err := s.q.ListContactConversationPage(ctx, params)
	if err != nil {
		writeError(w, r, fmt.Errorf("list contact conversations: %w", err))
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

type timelineItemJSON struct {
	ID           string          `json:"id"`
	Kind         string          `json:"kind"`
	Type         string          `json:"type"`
	At           time.Time       `json:"at"`
	Actor        *refJSON        `json:"actor"`
	User         *refJSON        `json:"user"`
	Conversation *timelineConvJS `json:"conversation"`
	Text         string          `json:"text"`
	Data         json.RawMessage `json:"data"`
}

type timelineConvJS struct {
	ID      string `json:"id"`
	Number  int64  `json:"number"`
	Subject string `json:"subject"`
}

func (s *Server) contactTimeline(w http.ResponseWriter, r *http.Request) {
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
	params := dbq.ListContactTimelineParams{ContactID: id, Admin: v.Admin, MailboxIds: v.MailboxIDs, PageSize: limit + 1}
	if c := r.URL.Query().Get("cursor"); c != "" {
		cur, err := decodeCursor(c)
		if err != nil {
			writeError(w, r, err)
			return
		}
		params.CursorAt = pgtype.Timestamptz{Time: cur.at, Valid: true}
		params.CursorID = cur.id
	}
	visible, err := v.ContactVisible(ctx, s.q, id)
	if err != nil {
		writeError(w, r, err)
		return
	}
	if !visible {
		writeError(w, r, errNotFound)
		return
	}
	rows, err := s.q.ListContactTimeline(ctx, params)
	if err != nil {
		writeError(w, r, fmt.Errorf("list timeline: %w", err))
		return
	}
	out := struct {
		Items      []timelineItemJSON `json:"items"`
		NextCursor *string            `json:"next_cursor"`
	}{Items: []timelineItemJSON{}}
	if len(rows) > int(limit) {
		rows = rows[:limit]
		last := rows[len(rows)-1]
		next := pageCursor{at: last.At.Time, id: last.ID}.encode()
		out.NextCursor = &next
	}
	for _, row := range rows {
		item := timelineItemJSON{
			ID: uuidStr(row.ID), Kind: row.Kind, Type: row.Type, At: row.At.Time.UTC(), Text: row.Body, Data: row.Data,
		}
		if row.ActorName.Valid {
			item.Actor = &refJSON{Name: row.ActorName.String}
		}
		if row.TargetID.Valid {
			item.User = &refJSON{ID: uuidStr(row.TargetID), Name: row.TargetName.String}
		}
		if row.ConversationID.Valid {
			item.Conversation = &timelineConvJS{ID: uuidStr(row.ConversationID), Number: row.ConversationNumber, Subject: row.ConversationSubject}
		}
		out.Items = append(out.Items, item)
	}
	writeJSON(w, http.StatusOK, out)
}
