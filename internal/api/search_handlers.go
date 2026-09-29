package api

import (
	"context"
	"encoding/base64"
	"fmt"
	"maps"
	"math"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"echoo/internal/auth"
	"echoo/internal/db/dbq"
	"echoo/internal/policy"
	"echoo/internal/search"
)

const (
	defaultSearchPageSize = 20
	maxSearchPageSize     = 50
)

func (s *Server) searchRoutes(r chi.Router) {
	// The command bar searches while typing; the limit is per user, well above normal typing speed.
	searchLimiter := auth.NewLimiter(240, time.Minute)
	r.With(limitByUser(searchLimiter)).Get("/search", s.searchConversations)
	s.savedViewRoutes(r)
}

// criteria is a filter set with every name and id resolved, ready to become SQL parameters.
// The list endpoint and the search endpoint both build one.
type criteria struct {
	mailboxIDs                []pgtype.UUID
	statuses                  []string
	snoozedMode               string
	labelIDs, teamIDs         []pgtype.UUID
	assigneeIDs               []pgtype.UUID
	assigneeNone              bool
	priorities                []string
	hasAttachment             bool
	after, before             pgtype.Timestamptz
	contactID, organizationID pgtype.UUID
	number                    pgtype.Int8
	fromPatterns, toPatterns  []string
	// none is set when a named mailbox, label, team or person matched nothing: no row can match.
	none bool
}

// splitRefs separates ids from names. Names match by prefix, case-insensitively.
func splitRefs(ids, names []string) ([]pgtype.UUID, []string) {
	uuids, patterns := []pgtype.UUID{}, []string{}
	for _, v := range ids {
		if id, ok := parseUUID(v); ok {
			uuids = append(uuids, id)
		}
	}
	for _, v := range names {
		if id, ok := parseUUID(v); ok {
			uuids = append(uuids, id)
		} else {
			patterns = append(patterns, search.LikePattern(v, false, true))
		}
	}
	return uuids, patterns
}

func dateStamp(s string) pgtype.Timestamptz {
	if s == "" {
		return pgtype.Timestamptz{}
	}
	d, err := time.Parse(time.DateOnly, s)
	if err != nil {
		return pgtype.Timestamptz{}
	}
	return pgtype.Timestamptz{Time: d, Valid: true}
}

// statusFilter maps status names to the statuses to select and how snoozed conversations count.
func statusFilter(statuses []string, snoozed, defaultOpen bool) ([]string, string) {
	switch {
	case len(statuses) == 0 && !snoozed && defaultOpen:
		return []string{"open"}, "exclude"
	case len(statuses) == 0 && !snoozed:
		return []string{"open", "waiting", "closed", "spam"}, "include"
	case len(statuses) == 0:
		return []string{"open", "waiting"}, "only"
	case snoozed:
		out := slices.Clone(statuses)
		for _, s := range []string{"open", "waiting"} {
			if !slices.Contains(out, s) {
				out = append(out, s)
			}
		}
		return out, "include"
	default:
		return statuses, "exclude"
	}
}

// buildCriteria resolves f and q against the database. defaultOpen makes an unset status mean
// open (the list) instead of any status (search).
func (s *Server) buildCriteria(ctx context.Context, user dbq.User, scope policy.Scope, f search.Filters, q search.Query, defaultOpen bool) (criteria, error) {
	c := criteria{
		mailboxIDs: scope.Read, labelIDs: []pgtype.UUID{}, teamIDs: []pgtype.UUID{}, assigneeIDs: []pgtype.UUID{},
		priorities: []string{}, fromPatterns: []string{}, toPatterns: []string{},
	}

	statuses := slices.Clone(q.Statuses)
	snoozed := q.Snoozed
	if f.Status == "snoozed" {
		snoozed = true
	} else if f.Status != "" {
		statuses = append(statuses, f.Status)
	}
	c.statuses, c.snoozedMode = statusFilter(statuses, snoozed, defaultOpen)

	if len(f.MailboxIDs)+len(q.Mailboxes) > 0 {
		ids, patterns := splitRefs(f.MailboxIDs, q.Mailboxes)
		got, err := s.q.ResolveMailboxNames(ctx, dbq.ResolveMailboxNamesParams{ScopeIds: scope.Read, Ids: ids, Patterns: patterns})
		if err != nil {
			return c, fmt.Errorf("resolve mailboxes: %w", err)
		}
		c.mailboxIDs = got
		c.none = c.none || len(got) == 0
	}
	if len(f.LabelIDs)+len(q.Labels) > 0 {
		ids, patterns := splitRefs(f.LabelIDs, q.Labels)
		got, err := s.q.ResolveLabelNames(ctx, dbq.ResolveLabelNamesParams{Ids: ids, Patterns: patterns})
		if err != nil {
			return c, fmt.Errorf("resolve labels: %w", err)
		}
		c.labelIDs = got
		c.none = c.none || len(got) == 0
	}
	if len(f.TeamIDs)+len(q.Teams) > 0 {
		ids, patterns := splitRefs(f.TeamIDs, q.Teams)
		got, err := s.q.ResolveTeamNames(ctx, dbq.ResolveTeamNamesParams{Ids: ids, Patterns: patterns})
		if err != nil {
			return c, fmt.Errorf("resolve teams: %w", err)
		}
		c.teamIDs = got
		c.none = c.none || len(got) == 0
	}

	var assigneeRefs []string
	if id, ok := parseUUID(f.Assignee); ok {
		c.assigneeIDs = append(c.assigneeIDs, id)
	}
	if f.Assignee == "me" || q.AssigneeMe {
		c.assigneeIDs = append(c.assigneeIDs, user.ID)
	}
	c.assigneeNone = f.Assignee == "none" || q.AssigneeNone
	assigneeRefs = q.Assignees
	if len(assigneeRefs) > 0 {
		ids, patterns := splitRefs(nil, assigneeRefs)
		got, err := s.q.ResolveUserNames(ctx, dbq.ResolveUserNamesParams{Ids: ids, Patterns: patterns})
		if err != nil {
			return c, fmt.Errorf("resolve users: %w", err)
		}
		c.assigneeIDs = append(c.assigneeIDs, got...)
	}
	assigneeAsked := f.Assignee != "" || q.AssigneeMe || q.AssigneeNone || len(q.Assignees) > 0
	c.none = c.none || (assigneeAsked && len(c.assigneeIDs) == 0 && !c.assigneeNone)

	c.priorities = append(c.priorities, f.Priorities...)
	for _, p := range q.Priorities {
		if !slices.Contains(c.priorities, p) {
			c.priorities = append(c.priorities, p)
		}
	}
	c.hasAttachment = f.HasAttachment || q.HasAttachment
	c.after, c.before = dateStamp(f.After), dateStamp(f.Before)
	if q.After != nil {
		c.after = pgtype.Timestamptz{Time: *q.After, Valid: true}
	}
	if q.Before != nil {
		c.before = pgtype.Timestamptz{Time: *q.Before, Valid: true}
	}
	c.contactID, _ = parseUUID(f.ContactID)
	c.organizationID, _ = parseUUID(f.OrganizationID)
	if q.Number != nil {
		c.number = pgtype.Int8{Int64: *q.Number, Valid: true}
	}
	for _, v := range q.From {
		c.fromPatterns = append(c.fromPatterns, search.AddressPattern(v))
	}
	for _, v := range q.To {
		c.toPatterns = append(c.toPatterns, search.AddressPattern(v))
	}
	return c, nil
}

func (c criteria) filteredParams(cursor *pageCursor, size int32) dbq.ListConversationPageFilteredParams {
	p := dbq.ListConversationPageFilteredParams{
		MailboxIds: c.mailboxIDs, Statuses: c.statuses, SnoozedMode: c.snoozedMode,
		LabelIds: c.labelIDs, AssigneeIds: c.assigneeIDs, AssigneeNone: c.assigneeNone, TeamIds: c.teamIDs,
		Priorities: c.priorities, HasAttachment: c.hasAttachment, After: c.after, Before: c.before,
		ContactID: c.contactID, OrganizationID: c.organizationID, Number: c.number,
		FromPatterns: c.fromPatterns, ToPatterns: c.toPatterns, PageSize: size,
	}
	if cursor != nil {
		p.CursorAt = pgtype.Timestamptz{Time: cursor.at, Valid: true}
		p.CursorID = cursor.id
	}
	return p
}

func (c criteria) searchParams(q search.Query, cursor *searchCursor, size int32) dbq.SearchConversationPageParams {
	plain := strings.TrimSpace(strings.ReplaceAll(q.Text, `"`, ""))
	p := dbq.SearchConversationPageParams{
		MailboxIds: c.mailboxIDs, Text: q.Text, PlainText: plain, Exact: strings.Contains(q.Text, `"`), Statuses: c.statuses, SnoozedMode: c.snoozedMode,
		LabelIds: c.labelIDs, AssigneeIds: c.assigneeIDs, AssigneeNone: c.assigneeNone, TeamIds: c.teamIDs,
		Priorities: c.priorities, HasAttachment: c.hasAttachment, After: c.after, Before: c.before, Number: c.number,
		FromPatterns: c.fromPatterns, ToPatterns: c.toPatterns, PageSize: size,
	}
	// An address has no spaces; matching shorter fragments would make the pattern scan everything.
	if len(plain) >= 3 && !strings.ContainsAny(plain, " \t") {
		p.EmailPattern = pgtype.Text{String: search.LikePattern(strings.ToLower(plain), true, true), Valid: true}
	}
	if cursor != nil {
		p.CursorScore = pgtype.Float8{Float64: cursor.score, Valid: true}
		p.CursorAt = pgtype.Timestamptz{Time: cursor.at, Valid: true}
		p.CursorID = cursor.id
	}
	return p
}

// searchCursor is the position of the last result: score, recency, id.
type searchCursor struct {
	score float64
	at    time.Time
	id    pgtype.UUID
}

func (c searchCursor) encode() string {
	raw := strconv.FormatFloat(c.score, 'g', -1, 64) + "." + strconv.FormatInt(c.at.UnixMicro(), 10) + "." + c.id.String()
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

func decodeSearchCursor(s string) (searchCursor, error) {
	invalid := errBadRequest("invalid cursor")
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return searchCursor{}, invalid
	}
	// The score contains a dot, so split from the right: micros and id never do.
	str := string(raw)
	i := strings.LastIndex(str, ".")
	if i < 0 {
		return searchCursor{}, invalid
	}
	head, idStr := str[:i], str[i+1:]
	j := strings.LastIndex(head, ".")
	if j < 0 {
		return searchCursor{}, invalid
	}
	score, err := strconv.ParseFloat(head[:j], 64)
	if err != nil || math.IsNaN(score) || math.IsInf(score, 0) {
		return searchCursor{}, invalid
	}
	micros, err := strconv.ParseInt(head[j+1:], 10, 64)
	if err != nil {
		return searchCursor{}, invalid
	}
	at := time.UnixMicro(micros).UTC()
	if at.Year() < 1970 || at.Year() > 9999 {
		return searchCursor{}, invalid
	}
	id, ok := parseUUID(idStr)
	if !ok {
		return searchCursor{}, invalid
	}
	return searchCursor{score: score, at: at, id: id}, nil
}

type searchResultJSON struct {
	Conversation conversationJSON `json:"conversation"`
	// Snippet is plain text with matches wrapped in U+27E6 and U+27E7. It is empty when the
	// conversation matched on its subject or contact only.
	Snippet string `json:"snippet"`
}

type searchResponseJSON struct {
	Results    []searchResultJSON `json:"results"`
	NextCursor *string            `json:"next_cursor"`
}

func (s *Server) searchConversations(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	params := r.URL.Query()
	size := int32(defaultSearchPageSize)
	if v := params.Get("limit"); v != "" {
		n, err := strconv.ParseInt(v, 10, 32)
		if err != nil || n < 1 || n > maxSearchPageSize {
			writeError(w, r, errBadRequest("limit must be between 1 and "+strconv.Itoa(maxSearchPageSize)))
			return
		}
		size = int32(n)
	}
	var cursor *searchCursor
	if v := params.Get("cursor"); v != "" {
		c, err := decodeSearchCursor(v)
		if err != nil {
			writeError(w, r, err)
			return
		}
		cursor = &c
	}

	q := search.Parse(params.Get("q"))
	out := searchResponseJSON{Results: []searchResultJSON{}}
	if q.Empty() {
		writeJSON(w, http.StatusOK, out)
		return
	}
	user := sessionFrom(ctx).User
	scope, err := policy.MailboxScope(ctx, s.q, user)
	if err != nil {
		writeError(w, r, err)
		return
	}
	crit, err := s.buildCriteria(ctx, user, scope, search.Filters{}, q, false)
	if err != nil {
		writeError(w, r, err)
		return
	}
	if crit.none || len(crit.mailboxIDs) == 0 {
		writeJSON(w, http.StatusOK, out)
		return
	}

	type hit struct {
		id        pgtype.UUID
		at        time.Time
		score     float64
		messageID pgtype.UUID
	}
	var hits []hit
	if q.HasText() {
		rows, err := s.q.SearchConversationPage(ctx, crit.searchParams(q, cursor, size+1))
		if err != nil {
			writeError(w, r, fmt.Errorf("search conversations: %w", err))
			return
		}
		for _, row := range rows {
			hits = append(hits, hit{row.ID, row.LastMessageAt.Time, row.Score, row.BestMessageID})
		}
	} else {
		var listCursor *pageCursor
		if cursor != nil {
			listCursor = &pageCursor{at: cursor.at, id: cursor.id}
		}
		rows, err := s.q.ListConversationPageFiltered(ctx, crit.filteredParams(listCursor, size+1))
		if err != nil {
			writeError(w, r, fmt.Errorf("filter conversations: %w", err))
			return
		}
		for _, row := range rows {
			hits = append(hits, hit{id: row.ID, at: row.LastMessageAt.Time})
		}
	}
	if len(hits) > int(size) {
		hits = hits[:size]
		last := hits[len(hits)-1]
		next := searchCursor{score: last.score, at: last.at, id: last.id}.encode()
		out.NextCursor = &next
	}
	if len(hits) == 0 {
		writeJSON(w, http.StatusOK, out)
		return
	}

	ids := make([]pgtype.UUID, len(hits))
	messageIDs := []pgtype.UUID{}
	for i, h := range hits {
		ids[i] = h.id
		if h.messageID.Valid {
			messageIDs = append(messageIDs, h.messageID)
		}
	}
	convs, err := s.hydrateConversations(ctx, ids, scope.Read)
	if err != nil {
		writeError(w, r, err)
		return
	}
	snippets := map[pgtype.UUID]string{}
	if len(messageIDs) > 0 {
		rows, err := s.q.SearchSnippets(ctx, dbq.SearchSnippetsParams{Text: q.Text, Exact: strings.Contains(q.Text, `"`), MessageIds: messageIDs, MailboxIds: scope.Read})
		if err != nil {
			writeError(w, r, fmt.Errorf("search snippets: %w", err))
			return
		}
		for _, row := range rows {
			snippets[row.ID] = row.Snippet
		}
	}
	for _, h := range hits {
		conv, ok := convs[uuidStr(h.id)]
		if !ok {
			continue
		}
		out.Results = append(out.Results, searchResultJSON{Conversation: conv, Snippet: snippets[h.messageID]})
	}
	writeJSON(w, http.StatusOK, out)
}

// hydrateConversations loads list rows with their labels, keyed by id. Ids outside the
// readable mailboxes are simply absent.
func (s *Server) hydrateConversations(ctx context.Context, ids, readable []pgtype.UUID) (map[string]conversationJSON, error) {
	rows, err := s.q.ListConversationsByID(ctx, dbq.ListConversationsByIDParams{Ids: ids, MailboxIds: readable})
	if err != nil {
		return nil, fmt.Errorf("load conversations: %w", err)
	}
	list := make([]conversationJSON, len(rows))
	for i, row := range rows {
		list[i] = toConversationJSON(row)
	}
	if err := s.attachLabels(ctx, list); err != nil {
		return nil, err
	}
	if err := s.attachUnread(ctx, sessionFrom(ctx).User.ID, list); err != nil {
		return nil, err
	}
	byID := make(map[string]conversationJSON, len(list))
	for _, c := range list {
		byID[c.ID] = c
	}
	return byID, nil
}

// advancedFilters reads the structured list parameters. It reports whether any of them needs
// the filtered query; plain requests keep using the original list queries.
func advancedFilters(q url.Values, status string) (search.Filters, bool, error) {
	f := search.Filters{
		Status: status, MailboxIDs: q["mailbox_id"], LabelIDs: q["label_id"], TeamIDs: q["team_id"],
		Assignee: q.Get("assignee"), Priorities: q["priority"], After: q.Get("after"), Before: q.Get("before"),
		ContactID: q.Get("contact_id"), OrganizationID: q.Get("organization_id"),
	}
	switch v := q.Get("has_attachment"); v {
	case "", "false":
	case "true":
		f.HasAttachment = true
	default:
		return f, false, errBadRequest("has_attachment must be true or false")
	}
	if bad := f.Validate(); bad != nil {
		return f, false, errBadRequest(slices.Sorted(maps.Keys(bad))[0] + " is invalid")
	}
	advanced := f.Assignee != "" || len(f.Priorities) > 0 || f.HasAttachment || f.After != "" || f.Before != "" ||
		f.ContactID != "" || f.OrganizationID != "" || len(f.MailboxIDs) > 1 || len(f.LabelIDs) > 1 || len(f.TeamIDs) > 1
	return f, advanced, nil
}

// listFiltered serves the list endpoint when advanced filters are present.
func (s *Server) listFiltered(w http.ResponseWriter, r *http.Request, f listFilter, filters search.Filters, user dbq.User, scope policy.Scope) {
	ctx := r.Context()
	out := conversationListJSON{Conversations: []conversationJSON{}}
	switch f.view {
	case "mine":
		if filters.Assignee != "" && filters.Assignee != "me" {
			writeJSON(w, http.StatusOK, out)
			return
		}
		filters.Assignee = "me"
	case "unassigned":
		if filters.Assignee != "" && filters.Assignee != "none" {
			writeJSON(w, http.StatusOK, out)
			return
		}
		filters.Assignee = "none"
	}
	crit, err := s.buildCriteria(ctx, user, scope, filters, search.Query{}, true)
	if err != nil {
		writeError(w, r, err)
		return
	}
	if crit.none || len(crit.mailboxIDs) == 0 {
		writeJSON(w, http.StatusOK, out)
		return
	}
	rows, err := s.q.ListConversationPageFiltered(ctx, crit.filteredParams(f.cursor, f.limit+1))
	if err != nil {
		writeError(w, r, fmt.Errorf("list filtered conversations: %w", err))
		return
	}
	if len(rows) > int(f.limit) {
		rows = rows[:f.limit]
		last := rows[len(rows)-1]
		next := pageCursor{at: last.LastMessageAt.Time, id: last.ID}.encode()
		out.NextCursor = &next
	}
	ids := make([]pgtype.UUID, len(rows))
	for i, row := range rows {
		ids[i] = row.ID
	}
	if len(ids) == 0 {
		writeJSON(w, http.StatusOK, out)
		return
	}
	convs, err := s.hydrateConversations(ctx, ids, scope.Read)
	if err != nil {
		writeError(w, r, err)
		return
	}
	for _, id := range ids {
		if c, ok := convs[uuidStr(id)]; ok {
			out.Conversations = append(out.Conversations, c)
		}
	}
	writeJSON(w, http.StatusOK, out)
}
