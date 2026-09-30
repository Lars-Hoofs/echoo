package api

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"echoo/internal/db/dbq"
	"echoo/internal/mail"
	"echoo/internal/policy"
	"echoo/internal/search"
	"echoo/internal/sniff"
)

const (
	defaultPageSize = 50
	maxPageSize     = 100
)

var (
	conversationViews    = map[string]bool{"mine": true, "unassigned": true, "all": true}
	conversationStatuses = map[string]bool{"open": true, "waiting": true, "closed": true, "spam": true, "snoozed": true}
)

type refJSON struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type contactRefJSON struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Email string `json:"email"`
}

type conversationJSON struct {
	ID             string          `json:"id"`
	Number         int64           `json:"number"`
	Subject        string          `json:"subject"`
	Status         string          `json:"status"`
	Priority       string          `json:"priority"`
	Version        int32           `json:"version"`
	SnoozedUntil   *time.Time      `json:"snoozed_until"`
	Labels         []labelRefJSON  `json:"labels"`
	Preview        string          `json:"preview"`
	LastMessageAt  time.Time       `json:"last_message_at"`
	MessageCount   int32           `json:"message_count"`
	HasAttachments bool            `json:"has_attachments"`
	LastDirection  *string         `json:"last_direction"`
	Mailbox        refJSON         `json:"mailbox"`
	Contact        *contactRefJSON `json:"contact"`
	Assignee       *refJSON        `json:"assignee"`
	Team           *refJSON        `json:"team"`
	SLA            *slaJSON        `json:"sla"`
	// Unread is personal to the requesting user; see unread.sql.
	Unread bool `json:"unread"`
}

// slaJSON is null for conversations without an SLA policy. The clients count down to the due
// times themselves; a resolution deadline is paused while the status is waiting.
type slaJSON struct {
	State              string     `json:"state"`
	FirstResponseDueAt *time.Time `json:"first_response_due_at"`
	FirstResponseMetAt *time.Time `json:"first_response_met_at"`
	ResolutionDueAt    *time.Time `json:"resolution_due_at"`
}

type conversationDetailJSON struct {
	conversationJSON
	CreatedAt        time.Time  `json:"created_at"`
	FirstRespondedAt *time.Time `json:"first_responded_at"`
	ResolvedAt       *time.Time `json:"resolved_at"`
	// CanWrite is false when the caller reads the mailbox but may not change conversations in it.
	CanWrite bool `json:"can_write"`
}

func toConversationJSON(c dbq.ListConversationsByIDRow) conversationJSON {
	out := conversationJSON{
		ID: uuidStr(c.ID), Number: c.Number, Subject: c.Subject, Status: c.Status, Priority: c.Priority,
		Version: c.Version, SnoozedUntil: timeOrNil(c.SnoozedUntil), Labels: []labelRefJSON{},
		Preview: c.Preview, LastMessageAt: c.LastMessageAt.Time.UTC(), MessageCount: c.MessageCount,
		HasAttachments: c.HasAttachments,
		Mailbox:        refJSON{ID: uuidStr(c.MailboxID), Name: c.MailboxName},
	}
	if c.LastDirection.Valid {
		out.LastDirection = &c.LastDirection.String
	}
	if c.ContactID.Valid {
		out.Contact = &contactRefJSON{ID: uuidStr(c.ContactID), Name: c.ContactName.String, Email: c.ContactEmail}
	}
	if c.AssigneeID.Valid {
		out.Assignee = &refJSON{ID: uuidStr(c.AssigneeID), Name: c.AssigneeName.String}
	}
	if c.TeamID.Valid {
		out.Team = &refJSON{ID: uuidStr(c.TeamID), Name: c.TeamName.String}
	}
	if c.SlaPolicyID.Valid {
		out.SLA = &slaJSON{
			State: c.SlaState, FirstResponseDueAt: timeOrNil(c.FirstResponseDueAt),
			FirstResponseMetAt: timeOrNil(c.FirstResponseMetAt), ResolutionDueAt: timeOrNil(c.ResolutionDueAt),
		}
	}
	return out
}

type inboxSummaryJSON struct {
	Mailboxes []inboxMailboxJSON `json:"mailboxes"`
	Teams     []inboxTeamJSON    `json:"teams"`
	Counts    inboxCountsJSON    `json:"counts"`
}

type inboxMailboxJSON struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	EmailAddress string `json:"email_address"`
	OpenCount    int32  `json:"open_count"`
}

type inboxTeamJSON struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	OpenCount int32  `json:"open_count"`
}

type inboxCountsJSON struct {
	Mine       int32 `json:"mine"`
	Unassigned int32 `json:"unassigned"`
	All        int32 `json:"all"`
	UnreadMine int32 `json:"unread_mine"`
}

func (s *Server) inboxSummary(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	user := sessionFrom(ctx).User
	scope, err := policy.MailboxScope(ctx, s.q, user)
	if err != nil {
		writeError(w, r, err)
		return
	}
	mailboxes, err := s.q.InboxMailboxCounts(ctx, scope.Read)
	if err != nil {
		writeError(w, r, fmt.Errorf("mailbox counts: %w", err))
		return
	}
	teams, err := s.q.InboxTeamCounts(ctx, dbq.InboxTeamCountsParams{
		MailboxIds: scope.Read, AllTeams: policy.SeesAll(user), UserID: user.ID,
	})
	if err != nil {
		writeError(w, r, fmt.Errorf("team counts: %w", err))
		return
	}
	counts, err := s.q.InboxOpenCounts(ctx, dbq.InboxOpenCountsParams{MailboxIds: scope.Read, UserID: user.ID})
	if err != nil {
		writeError(w, r, fmt.Errorf("open counts: %w", err))
		return
	}

	unreadMine, err := s.q.InboxUnreadMineCount(ctx, dbq.InboxUnreadMineCountParams{MailboxIds: scope.Read, UserID: user.ID})
	if err != nil {
		writeError(w, r, fmt.Errorf("unread counts: %w", err))
		return
	}

	out := inboxSummaryJSON{
		Mailboxes: make([]inboxMailboxJSON, 0, len(mailboxes)),
		Teams:     make([]inboxTeamJSON, 0, len(teams)),
		Counts:    inboxCountsJSON{Mine: counts.Mine, Unassigned: counts.Unassigned, All: counts.Total, UnreadMine: unreadMine},
	}
	for _, m := range mailboxes {
		out.Mailboxes = append(out.Mailboxes, inboxMailboxJSON{ID: uuidStr(m.ID), Name: m.Name, EmailAddress: m.EmailAddress, OpenCount: m.OpenCount})
	}
	for _, t := range teams {
		out.Teams = append(out.Teams, inboxTeamJSON{ID: uuidStr(t.ID), Name: t.Name, OpenCount: t.OpenCount})
	}
	writeJSON(w, http.StatusOK, out)
}

type conversationListJSON struct {
	Conversations []conversationJSON `json:"conversations"`
	NextCursor    *string            `json:"next_cursor"`
}

type listFilter struct {
	view, status string
	mailboxID    pgtype.UUID
	teamID       pgtype.UUID
	labelID      pgtype.UUID
	cursor       *pageCursor
	limit        int32
	// filters and advanced carry the structured filters; see advancedFilters.
	filters  search.Filters
	advanced bool
}

func parseListFilter(r *http.Request) (listFilter, error) {
	q := r.URL.Query()
	f := listFilter{view: "all", status: "open", limit: defaultPageSize}
	if v := q.Get("view"); v != "" {
		if !conversationViews[v] {
			return f, errBadRequest("view must be mine, unassigned or all")
		}
		f.view = v
	}
	if v := q.Get("status"); v != "" {
		if !conversationStatuses[v] {
			return f, errBadRequest("status must be open, waiting, closed, spam or snoozed")
		}
		f.status = v
	}
	for _, p := range []struct {
		name string
		dst  *pgtype.UUID
	}{{"mailbox_id", &f.mailboxID}, {"team_id", &f.teamID}, {"label_id", &f.labelID}} {
		if v := q.Get(p.name); v != "" {
			id, ok := parseUUID(v)
			if !ok {
				return f, errBadRequest(p.name + " must be a UUID")
			}
			*p.dst = id
		}
	}
	filters, advanced, err := advancedFilters(q, f.status)
	if err != nil {
		return f, err
	}
	f.filters, f.advanced = filters, advanced
	if v := q.Get("limit"); v != "" {
		n, err := strconv.ParseInt(v, 10, 32)
		if err != nil || n < 1 || n > maxPageSize {
			return f, errBadRequest("limit must be between 1 and " + strconv.Itoa(maxPageSize))
		}
		f.limit = int32(n)
	}
	if v := q.Get("cursor"); v != "" {
		c, err := decodeCursor(v)
		if err != nil {
			return f, err
		}
		f.cursor = &c
	}
	return f, nil
}

// pageCursor is the position of the last row of a page. Microseconds match the precision
// of timestamptz, so a row is never skipped or repeated by rounding.
type pageCursor struct {
	at time.Time
	id pgtype.UUID
}

func (c pageCursor) encode() string {
	return base64.RawURLEncoding.EncodeToString([]byte(strconv.FormatInt(c.at.UnixMicro(), 10) + "." + c.id.String()))
}

func decodeCursor(s string) (pageCursor, error) {
	invalid := errBadRequest("invalid cursor")
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return pageCursor{}, invalid
	}
	micros, id, ok := strings.Cut(string(raw), ".")
	if !ok {
		return pageCursor{}, invalid
	}
	n, err := strconv.ParseInt(micros, 10, 64)
	if err != nil {
		return pageCursor{}, invalid
	}
	at := time.UnixMicro(n).UTC()
	if at.Year() < 1970 || at.Year() > 9999 {
		return pageCursor{}, invalid
	}
	uid, ok := parseUUID(id)
	if !ok {
		return pageCursor{}, invalid
	}
	return pageCursor{at: at, id: uid}, nil
}

func (s *Server) listConversations(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	f, err := parseListFilter(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	user := sessionFrom(ctx).User
	scope, err := policy.MailboxScope(ctx, s.q, user)
	if err != nil {
		writeError(w, r, err)
		return
	}
	if f.advanced {
		s.listFiltered(w, r, f, f.filters, user, scope)
		return
	}
	// Narrowing to one mailbox only ever shrinks the readable set, so a mailbox outside the
	// scope yields an empty list and reveals nothing about whether it exists.
	readable := scope.Read
	if f.mailboxID.Valid {
		readable = []pgtype.UUID{}
		for _, id := range scope.Read {
			if id == f.mailboxID {
				readable = append(readable, id)
			}
		}
	}

	out := conversationListJSON{Conversations: []conversationJSON{}}
	if len(readable) == 0 {
		writeJSON(w, http.StatusOK, out)
		return
	}

	// One extra row tells whether another page follows without a COUNT.
	page := pageQuery{filter: f, mailboxIDs: readable, userID: user.ID}
	picked, err := page.run(ctx, s.q)
	if err != nil {
		writeError(w, r, fmt.Errorf("list conversation page: %w", err))
		return
	}
	if len(picked) > int(f.limit) {
		picked = picked[:f.limit]
		last := picked[len(picked)-1]
		next := last.encode()
		out.NextCursor = &next
	}
	if len(picked) == 0 {
		writeJSON(w, http.StatusOK, out)
		return
	}
	ids := make([]pgtype.UUID, len(picked))
	for i, p := range picked {
		ids[i] = p.id
	}
	rows, err := s.q.ListConversationsByID(ctx, dbq.ListConversationsByIDParams{Ids: ids, MailboxIds: readable})
	if err != nil {
		writeError(w, r, fmt.Errorf("load conversations: %w", err))
		return
	}
	for _, row := range rows {
		out.Conversations = append(out.Conversations, toConversationJSON(row))
	}
	if err := s.attachLabels(ctx, out.Conversations); err != nil {
		writeError(w, r, err)
		return
	}
	if err := s.attachUnread(ctx, user.ID, out.Conversations); err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

type pageQuery struct {
	filter     listFilter
	mailboxIDs []pgtype.UUID
	userID     pgtype.UUID
}

func (p pageQuery) run(ctx context.Context, q *dbq.Queries) ([]pageCursor, error) {
	f := p.filter
	var cursorAt pgtype.Timestamptz
	var cursorID pgtype.UUID
	if f.cursor != nil {
		cursorAt = pgtype.Timestamptz{Time: f.cursor.at, Valid: true}
		cursorID = f.cursor.id
	}
	size := f.limit + 1
	// Snoozed is a view over open and waiting conversations, not a status of its own.
	statuses, snoozed := []string{f.status}, false
	if f.status == "snoozed" {
		statuses, snoozed = []string{"open", "waiting"}, true
	}
	if f.view == "mine" {
		rows, err := q.ListConversationPageAssigned(ctx, dbq.ListConversationPageAssignedParams{
			UserID: p.userID, Statuses: statuses, Snoozed: snoozed, MailboxIds: p.mailboxIDs,
			CursorAt: cursorAt, CursorID: cursorID, TeamID: f.teamID, LabelID: f.labelID, PageSize: size,
		})
		if err != nil {
			return nil, err
		}
		out := make([]pageCursor, len(rows))
		for i, row := range rows {
			out[i] = pageCursor{at: row.LastMessageAt.Time, id: row.ID}
		}
		return out, nil
	}
	rows, err := q.ListConversationPageByMailbox(ctx, dbq.ListConversationPageByMailboxParams{
		MailboxIds: p.mailboxIDs, Statuses: statuses, Snoozed: snoozed, CursorAt: cursorAt, CursorID: cursorID,
		UnassignedOnly: f.view == "unassigned", TeamID: f.teamID, LabelID: f.labelID, PageSize: size,
	})
	if err != nil {
		return nil, err
	}
	out := make([]pageCursor, len(rows))
	for i, row := range rows {
		out[i] = pageCursor{at: row.LastMessageAt.Time, id: row.ID}
	}
	return out, nil
}

type attachmentJSON struct {
	ID          string `json:"id"`
	Filename    string `json:"filename"`
	Size        int64  `json:"size"`
	SniffedType string `json:"sniffed_type"`
	Inline      bool   `json:"inline"`
	DownloadURL string `json:"download_url"`
	Dangerous   bool   `json:"dangerous"`
	// ScanStatus is not_scanned, clean, infected or error; infected files cannot be downloaded.
	ScanStatus string `json:"scan_status"`
}

// messageJSON has no body_html on purpose: HTML is only ever served sanitized, inside a
// sandboxed iframe, never as part of this JSON.
type messageJSON struct {
	ID             string           `json:"id"`
	Kind           string           `json:"kind"`
	Direction      *string          `json:"direction"`
	From           mail.Address     `json:"from"`
	To             []mail.Address   `json:"to"`
	Cc             []mail.Address   `json:"cc"`
	Subject        string           `json:"subject"`
	BodyText       string           `json:"body_text"`
	SentAt         *time.Time       `json:"sent_at"`
	ReceivedAt     time.Time        `json:"received_at"`
	Author         *refJSON         `json:"author"`
	Attachments    []attachmentJSON `json:"attachments"`
	OutboundStatus *string          `json:"outbound_status"`
	OutboundError  *string          `json:"outbound_error"`

	HasHTML          bool          `json:"has_html"`
	RenderURL        string        `json:"render_url"`
	BlockedImages    int           `json:"blocked_images"`
	PhishingWarnings []warningJSON `json:"phishing_warnings"`
}

type contactDetailJSON struct {
	contactRefJSON
	Organization      *refJSON `json:"organization"`
	ConversationCount int32    `json:"conversation_count"`
}

type conversationDetailResponse struct {
	Conversation conversationDetailJSON `json:"conversation"`
	Messages     []messageJSON          `json:"messages"`
	Contact      *contactDetailJSON     `json:"contact"`
	CSAT         *csatRatingJSON        `json:"csat"`
}

func decodeAddresses(raw []byte) ([]mail.Address, error) {
	out := []mail.Address{}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *Server) getConversation(w http.ResponseWriter, r *http.Request) {
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
	rows, err := s.q.ListConversationsByID(ctx, dbq.ListConversationsByIDParams{Ids: []pgtype.UUID{id}, MailboxIds: scope.Read})
	if err != nil {
		writeError(w, r, fmt.Errorf("load conversation: %w", err))
		return
	}
	if len(rows) == 0 {
		writeError(w, r, errNotFound)
		return
	}
	out, err := s.conversationDetail(ctx, rows[0], scope)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// conversationDetail builds the full view of one conversation the caller may read: messages,
// attachments, labels, contact and rating.
func (s *Server) conversationDetail(ctx context.Context, conv dbq.ListConversationsByIDRow, scope policy.Scope) (conversationDetailResponse, error) {
	id := conv.ID
	msgRows, err := s.q.ListConversationMessages(ctx, id)
	if err != nil {
		return conversationDetailResponse{}, fmt.Errorf("load messages: %w", err)
	}
	messageIDs := make([]pgtype.UUID, len(msgRows))
	for i, m := range msgRows {
		messageIDs[i] = m.ID
	}
	attRows, err := s.q.ListMessageAttachments(ctx, messageIDs)
	if err != nil {
		return conversationDetailResponse{}, fmt.Errorf("load attachments: %w", err)
	}
	attachments := map[pgtype.UUID][]attachmentJSON{}
	for _, a := range attRows {
		attachments[a.MessageID] = append(attachments[a.MessageID], attachmentJSON{
			ID: uuidStr(a.ID), Filename: a.Filename, Size: a.SizeBytes, SniffedType: a.SniffedType, Inline: a.Disposition == "inline",
			DownloadURL: "/api/v1/attachments/" + uuidStr(a.ID) + "/download", Dangerous: sniff.Dangerous(a.Filename, a.SniffedType),
			ScanStatus: a.ScanStatus,
		})
	}

	renderInfo, err := s.messageRenderInfos(ctx, conv)
	if err != nil {
		return conversationDetailResponse{}, err
	}
	messages := make([]messageJSON, 0, len(msgRows))
	for _, m := range msgRows {
		to, err := decodeAddresses(m.ToAddrs)
		if err != nil {
			return conversationDetailResponse{}, fmt.Errorf("decode to of message %s: %w", uuidStr(m.ID), err)
		}
		cc, err := decodeAddresses(m.CcAddrs)
		if err != nil {
			return conversationDetailResponse{}, fmt.Errorf("decode cc of message %s: %w", uuidStr(m.ID), err)
		}
		msg := messageJSON{
			ID: uuidStr(m.ID), Kind: m.Kind, From: mail.Address{Name: m.FromName, Address: m.FromAddr},
			To: to, Cc: cc, Subject: m.Subject, BodyText: m.BodyText,
			SentAt: timeOrNil(m.SentAt), ReceivedAt: m.ReceivedAt.Time.UTC(),
			Attachments: attachments[m.ID],
		}
		info := renderInfo[m.ID]
		msg.HasHTML, msg.RenderURL, msg.BlockedImages = info.hasHTML, info.renderURL(m.ID), info.blockedImages
		msg.PhishingWarnings = info.warnings
		if msg.PhishingWarnings == nil {
			msg.PhishingWarnings = []warningJSON{}
		}
		if msg.Attachments == nil {
			msg.Attachments = []attachmentJSON{}
		}
		if m.Direction.Valid {
			msg.Direction = &m.Direction.String
		}
		if m.AuthorID.Valid {
			msg.Author = &refJSON{ID: uuidStr(m.AuthorID), Name: m.AuthorName.String}
		}
		if m.OutboundStatus.Valid {
			msg.OutboundStatus = &m.OutboundStatus.String
			if m.OutboundError.String != "" {
				msg.OutboundError = &m.OutboundError.String
			}
		}
		messages = append(messages, msg)
	}

	out := conversationDetailResponse{
		Conversation: conversationDetailJSON{
			conversationJSON: toConversationJSON(conv),
			CreatedAt:        conv.CreatedAt.Time.UTC(),
			FirstRespondedAt: timeOrNil(conv.FirstRespondedAt),
			ResolvedAt:       timeOrNil(conv.ResolvedAt),
			CanWrite:         slices.Contains(scope.Write, conv.MailboxID),
		},
		Messages: messages,
	}
	labels, err := s.conversationLabels(ctx, []pgtype.UUID{id})
	if err != nil {
		return conversationDetailResponse{}, err
	}
	if l := labels[id]; l != nil {
		out.Conversation.Labels = l
	}
	one := []conversationJSON{out.Conversation.conversationJSON}
	if err := s.attachUnread(ctx, sessionFrom(ctx).User.ID, one); err != nil {
		return conversationDetailResponse{}, err
	}
	out.Conversation.Unread = one[0].Unread
	if conv.ContactID.Valid {
		c, err := s.q.GetConversationContact(ctx, dbq.GetConversationContactParams{ID: conv.ContactID, MailboxIds: scope.Read})
		if err != nil {
			return conversationDetailResponse{}, fmt.Errorf("load contact: %w", err)
		}
		out.Contact = &contactDetailJSON{
			contactRefJSON:    contactRefJSON{ID: uuidStr(c.ID), Name: c.Name, Email: c.Email},
			ConversationCount: c.ConversationCount,
		}
		if c.OrganizationID.Valid {
			out.Contact.Organization = &refJSON{ID: uuidStr(c.OrganizationID), Name: c.OrganizationName.String}
		}
	}
	if out.CSAT, err = s.conversationRating(ctx, id, scope.Read); err != nil {
		return out, err
	}
	return out, nil
}
