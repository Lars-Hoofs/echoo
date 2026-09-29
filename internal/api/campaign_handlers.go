package api

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"echoo/internal/audit"
	"echoo/internal/auth"
	"echoo/internal/campaigns"
	"echoo/internal/compose"
	"echoo/internal/db"
	"echoo/internal/db/dbq"
	"echoo/internal/mail"
	"echoo/internal/policy"
)

const (
	campaignTestTimeout = 30 * time.Second
	maxScheduleAhead    = 365 * 24 * time.Hour
	recipientPageSize   = 50
	recipientPageMax    = 200
)

var errCampaignState = &apiError{Status: http.StatusConflict, Code: "campaign_state", Message: "the campaign is in a state that does not allow this"}

// campaignService returns the campaign service wired to the request-time job client. The client
// is read here, not when the server is built, because tests and options set it afterwards.
func (s *Server) campaignService() *campaigns.Service {
	if s.jobs == nil {
		return s.campaigns
	}
	return s.campaigns.WithJobs(s.jobs)
}

func (s *Server) newCampaigns() *campaigns.Service {
	d := campaigns.Deps{
		Pool: s.pool, BaseURL: strings.TrimSuffix(s.cfg.BaseURL.String(), "/"), Keyring: s.keys, TLS: s.testTLS,
		MaxRate: s.cfg.CampaignRateLimit(),
	}
	if s.oauth != nil {
		d.Tokens = s.oauth
	}
	return campaigns.NewService(d)
}

// campaignRoutes registers the campaign management routes. They sit in the permission-guarded
// group; the permission behind them is campaigns.manage (see permissions.go).
func (s *Server) campaignRoutes(r chi.Router) {
	testLimiter := auth.NewLimiter(10, time.Minute)
	r.Get("/campaigns", s.listCampaigns)
	r.Post("/campaigns", s.createCampaign)
	r.Post("/campaigns/preview", s.previewCampaign)
	r.Get("/campaigns/{id}", s.getCampaign)
	r.Patch("/campaigns/{id}", s.updateCampaign)
	r.Delete("/campaigns/{id}", s.deleteCampaign)
	r.With(limitByUser(testLimiter)).Post("/campaigns/{id}/test", s.testCampaign)
	r.Post("/campaigns/{id}/start", s.startCampaign)
	r.Post("/campaigns/{id}/pause", s.pauseCampaign)
	r.Post("/campaigns/{id}/resume", s.resumeCampaign)
	r.Post("/campaigns/{id}/cancel", s.cancelCampaign)
	r.Get("/campaigns/{id}/recipients", s.listCampaignRecipients)
	r.Get("/campaigns/{id}/report", s.campaignReport)
	r.Post("/contacts/{id}/resubscribe", s.resubscribeContact)
	r.Post("/contacts/{id}/reactivate-address", s.reactivateAddress)
}

type campaignCountsJSON struct {
	Total   int64            `json:"total"`
	Pending int64            `json:"pending"`
	Queued  int64            `json:"queued"`
	Sent    int64            `json:"sent"`
	Failed  int64            `json:"failed"`
	Skipped int64            `json:"skipped"`
	Reasons map[string]int64 `json:"skipped_reasons"`
}

type campaignJSON struct {
	ID            string             `json:"id"`
	Name          string             `json:"name"`
	Status        string             `json:"status"`
	Mailbox       mailboxRefJSON     `json:"mailbox"`
	Segment       *refJSON           `json:"segment"`
	Subject       string             `json:"subject"`
	BodyHTML      string             `json:"body_html"`
	RatePerMinute int32              `json:"rate_per_minute"`
	ScheduledAt   *time.Time         `json:"scheduled_at"`
	StartedAt     *time.Time         `json:"started_at"`
	FinishedAt    *time.Time         `json:"finished_at"`
	Error         string             `json:"error"`
	CreatedBy     *refJSON           `json:"created_by"`
	CreatedAt     time.Time          `json:"created_at"`
	UpdatedAt     time.Time          `json:"updated_at"`
	Counts        campaignCountsJSON `json:"counts"`
}

type mailboxRefJSON struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Address string `json:"address"`
}

type campaignRow = dbq.CampaignGetRow

func toCampaignJSON(c campaignRow, counts campaignCountsJSON) campaignJSON {
	out := campaignJSON{
		ID: uuidStr(c.ID), Name: c.Name, Status: c.Status, Subject: c.Subject, BodyHTML: c.BodyHtml,
		Mailbox:       mailboxRefJSON{ID: uuidStr(c.MailboxID), Name: c.MailboxName, Address: c.MailboxAddress},
		RatePerMinute: c.RatePerMinute, ScheduledAt: timeOrNil(c.ScheduledAt), StartedAt: timeOrNil(c.StartedAt),
		FinishedAt: timeOrNil(c.FinishedAt), Error: c.Error, CreatedAt: c.CreatedAt.Time.UTC(), UpdatedAt: c.UpdatedAt.Time.UTC(),
		Counts: counts,
	}
	if c.SegmentName != "" || c.SegmentID.Valid {
		out.Segment = &refJSON{ID: uuidStr(c.SegmentID), Name: c.SegmentName}
	}
	if c.CreatedBy.Valid {
		out.CreatedBy = &refJSON{ID: uuidStr(c.CreatedBy), Name: c.CreatorName.String}
	}
	return out
}

func emptyCounts() campaignCountsJSON { return campaignCountsJSON{Reasons: map[string]int64{}} }

func (s *Server) campaignCounts(r *http.Request, ids []pgtype.UUID) (map[pgtype.UUID]campaignCountsJSON, error) {
	rows, err := s.q.CampaignCounts(r.Context(), ids)
	if err != nil {
		return nil, fmt.Errorf("count recipients: %w", err)
	}
	out := make(map[pgtype.UUID]campaignCountsJSON, len(ids))
	for _, id := range ids {
		out[id] = emptyCounts()
	}
	for _, row := range rows {
		c := out[row.CampaignID]
		c.Total += row.N
		switch row.State {
		case "pending":
			c.Pending += row.N
		case "queued":
			c.Queued += row.N
		case "sent":
			c.Sent += row.N
		case "failed":
			c.Failed += row.N
		case "skipped":
			c.Skipped += row.N
			c.Reasons[row.SkipReason] += row.N
		}
		out[row.CampaignID] = c
	}
	return out, nil
}

func (s *Server) campaignScope(r *http.Request) (dbq.User, policy.Scope, error) {
	user := sessionFrom(r.Context()).User
	scope, err := policy.MailboxScope(r.Context(), s.q, user)
	return user, scope, err
}

// campaignFor loads the campaign of the URL. A campaign on a mailbox the user cannot read does
// not exist as far as they can tell; one they can read but not write is forbidden for changes.
func (s *Server) campaignFor(r *http.Request, write bool) (campaignRow, error) {
	_, scope, err := s.campaignScope(r)
	if err != nil {
		return campaignRow{}, err
	}
	id, ok := parseUUID(chi.URLParam(r, "id"))
	if !ok {
		return campaignRow{}, errNotFound
	}
	row, err := s.q.CampaignGet(r.Context(), id)
	if errors.Is(err, pgx.ErrNoRows) {
		return campaignRow{}, errNotFound
	}
	if err != nil {
		return campaignRow{}, fmt.Errorf("load campaign: %w", err)
	}
	if !slices.Contains(scope.Read, row.MailboxID) {
		return campaignRow{}, errNotFound
	}
	if write && !slices.Contains(scope.Write, row.MailboxID) {
		return campaignRow{}, errForbidden
	}
	return row, nil
}

func (s *Server) respondCampaign(w http.ResponseWriter, r *http.Request, id pgtype.UUID, status int) {
	row, err := s.q.CampaignGet(r.Context(), id)
	if err != nil {
		writeError(w, r, fmt.Errorf("load campaign: %w", err))
		return
	}
	counts, err := s.campaignCounts(r, []pgtype.UUID{id})
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, status, map[string]any{"campaign": toCampaignJSON(row, counts[id])})
}

func (s *Server) listCampaigns(w http.ResponseWriter, r *http.Request) {
	_, scope, err := s.campaignScope(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	rows, err := s.q.CampaignList(r.Context())
	if err != nil {
		writeError(w, r, fmt.Errorf("list campaigns: %w", err))
		return
	}
	rows = slices.DeleteFunc(rows, func(c dbq.CampaignListRow) bool { return !slices.Contains(scope.Read, c.MailboxID) })
	ids := make([]pgtype.UUID, len(rows))
	for i, c := range rows {
		ids[i] = c.ID
	}
	counts, err := s.campaignCounts(r, ids)
	if err != nil {
		writeError(w, r, err)
		return
	}
	out := make([]campaignJSON, len(rows))
	for i, c := range rows {
		out[i] = toCampaignJSON(dbq.CampaignGetRow(c), counts[c.ID])
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"campaigns": out,
		"limits":    map[string]int{"default_rate": campaigns.DefaultRate, "max_rate": s.cfg.CampaignRateLimit()},
	})
}

func (s *Server) getCampaign(w http.ResponseWriter, r *http.Request) {
	c, err := s.campaignFor(r, false)
	if err != nil {
		writeError(w, r, err)
		return
	}
	s.respondCampaign(w, r, c.ID, http.StatusOK)
}

type campaignRequest struct {
	Name          *string `json:"name"`
	MailboxID     *string `json:"mailbox_id"`
	SegmentID     *string `json:"segment_id"`
	Subject       *string `json:"subject"`
	BodyHTML      *string `json:"body_html"`
	RatePerMinute *int    `json:"rate_per_minute"`
}

// draftFrom applies a request on top of the current values, checks the mailbox and segment
// against what the user may use, and sanitizes the body. It returns the draft and the name of
// the segment, which the campaign keeps.
func (s *Server) draftFrom(r *http.Request, user dbq.User, scope policy.Scope, cur campaigns.Draft, curSegmentName string, req campaignRequest) (campaigns.Draft, string, error) {
	d, segmentName := cur, curSegmentName
	fields := map[string]string{}
	if req.Name != nil {
		d.Name = strings.TrimSpace(*req.Name)
	}
	if req.Subject != nil {
		d.Subject = strings.TrimSpace(*req.Subject)
	}
	if req.BodyHTML != nil {
		d.BodyHTML = compose.Sanitize(*req.BodyHTML, compose.SanitizeOptions{})
	}
	if req.RatePerMinute != nil {
		d.Rate = *req.RatePerMinute
	}
	if req.MailboxID != nil {
		id, ok := parseUUID(*req.MailboxID)
		if !ok {
			fields["mailbox_id"] = "invalid"
		}
		d.MailboxID = id
	}
	if d.MailboxID.Valid && !slices.Contains(scope.Write, d.MailboxID) {
		fields["mailbox_id"] = "unknown"
	}
	if req.SegmentID != nil {
		d.SegmentID, segmentName = pgtype.UUID{}, ""
		if *req.SegmentID != "" {
			id, ok := parseUUID(*req.SegmentID)
			seg, err := s.q.GetSegment(r.Context(), id)
			switch {
			case !ok || errors.Is(err, pgx.ErrNoRows) || (err == nil && seg.OwnerUserID != user.ID && !seg.Shared):
				fields["segment_id"] = "unknown"
			case err != nil:
				return d, "", fmt.Errorf("load segment: %w", err)
			default:
				d.SegmentID, segmentName = seg.ID, seg.Name
			}
		}
	}
	for k, v := range d.Validate(s.cfg.CampaignRateLimit(), false) {
		if _, taken := fields[k]; !taken {
			fields[k] = v
		}
	}
	if len(fields) > 0 {
		return d, "", errValidation(fields)
	}
	return d, segmentName, nil
}

func (s *Server) createCampaign(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	user, scope, err := s.campaignScope(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	var req campaignRequest
	if err := decode(r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	d, segmentName, err := s.draftFrom(r, user, scope, campaigns.Draft{Rate: min(campaigns.DefaultRate, s.cfg.CampaignRateLimit())}, "", req)
	if err != nil {
		writeError(w, r, err)
		return
	}
	var id pgtype.UUID
	err = db.InTx(ctx, s.pool, func(q *dbq.Queries) error {
		var err error
		id, err = q.CampaignInsert(ctx, dbq.CampaignInsertParams{
			Name: d.Name, MailboxID: d.MailboxID, SegmentID: d.SegmentID, SegmentName: segmentName, Subject: d.Subject,
			BodyHtml: d.BodyHTML, RatePerMinute: int32(d.Rate), CreatedBy: user.ID, //nolint:gosec // Validate bounds the rate to 1..1000
		})
		if err != nil {
			return err
		}
		return audit.Write(ctx, q, audit.Entry{Actor: user.ID, IP: clientFrom(r).IP, Action: audit.CampaignCreated, TargetType: "campaign", TargetID: uuidStr(id)})
	})
	if err != nil {
		writeError(w, r, err)
		return
	}
	s.respondCampaign(w, r, id, http.StatusCreated)
}

func (s *Server) updateCampaign(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	c, err := s.campaignFor(r, true)
	if err != nil {
		writeError(w, r, err)
		return
	}
	user, scope, err := s.campaignScope(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	var req campaignRequest
	if err := decode(r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	cur := campaigns.Draft{
		Name: c.Name, MailboxID: c.MailboxID, SegmentID: c.SegmentID, Subject: c.Subject, BodyHTML: c.BodyHtml, Rate: int(c.RatePerMinute),
	}
	d, segmentName, err := s.draftFrom(r, user, scope, cur, c.SegmentName, req)
	if err != nil {
		writeError(w, r, err)
		return
	}
	err = db.InTx(ctx, s.pool, func(q *dbq.Queries) error {
		n, err := q.CampaignUpdateDraft(ctx, dbq.CampaignUpdateDraftParams{
			ID: c.ID, Name: d.Name, MailboxID: d.MailboxID, SegmentID: d.SegmentID, SegmentName: segmentName, Subject: d.Subject,
			BodyHtml: d.BodyHTML, RatePerMinute: int32(d.Rate), //nolint:gosec // Validate bounds the rate to 1..1000
		})
		if err != nil {
			return err
		}
		if n == 0 {
			return errCampaignState
		}
		return audit.Write(ctx, q, audit.Entry{Actor: user.ID, IP: clientFrom(r).IP, Action: audit.CampaignUpdated, TargetType: "campaign", TargetID: uuidStr(c.ID)})
	})
	if err != nil {
		writeError(w, r, err)
		return
	}
	s.respondCampaign(w, r, c.ID, http.StatusOK)
}

func (s *Server) deleteCampaign(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	c, err := s.campaignFor(r, true)
	if err != nil {
		writeError(w, r, err)
		return
	}
	user := sessionFrom(ctx).User
	err = db.InTx(ctx, s.pool, func(q *dbq.Queries) error {
		n, err := q.CampaignDelete(ctx, c.ID)
		if err != nil {
			return err
		}
		if n == 0 {
			return errCampaignState
		}
		return audit.Write(ctx, q, audit.Entry{
			Actor: user.ID, IP: clientFrom(r).IP, Action: audit.CampaignDeleted, TargetType: "campaign", TargetID: uuidStr(c.ID),
			Metadata: map[string]any{"status": c.Status},
		})
	})
	if err != nil {
		writeError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) previewCampaign(w http.ResponseWriter, r *http.Request) {
	user := sessionFrom(r.Context()).User
	var req struct {
		SegmentID string `json:"segment_id"`
	}
	if err := decode(r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	id, ok := parseUUID(req.SegmentID)
	if !ok {
		writeError(w, r, errValidation(map[string]string{"segment_id": "invalid"}))
		return
	}
	p, err := s.campaigns.Preview(r.Context(), user, id)
	if errors.Is(err, campaigns.ErrSegmentUnavailable) {
		writeError(w, r, errValidation(map[string]string{"segment_id": "unknown"}))
		return
	}
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, p)
}

func (s *Server) testCampaign(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), campaignTestTimeout)
	defer cancel()
	c, err := s.campaignFor(r, true)
	if err != nil {
		writeError(w, r, err)
		return
	}
	user := sessionFrom(ctx).User
	if c.Subject == "" || compose.IsEmpty(c.BodyHtml) {
		writeError(w, r, errValidation(map[string]string{"subject": "required"}))
		return
	}
	err = s.campaigns.SendTest(ctx, c.ID, mail.Address{Name: user.Name, Address: user.Email})
	var failed *campaigns.TestSendError
	if errors.As(err, &failed) {
		slog.WarnContext(ctx, "campaign test mail failed", "campaign_id", uuidStr(c.ID), "reason", failed.Reason)
		writeError(w, r, &apiError{Status: http.StatusBadGateway, Code: "test_send_failed", Message: "the test mail could not be sent", Fields: map[string]string{"reason": failed.Reason}})
		return
	}
	if err != nil {
		writeError(w, r, err)
		return
	}
	err = db.InTx(ctx, s.pool, func(q *dbq.Queries) error {
		return audit.Write(ctx, q, audit.Entry{Actor: user.ID, IP: clientFrom(r).IP, Action: audit.CampaignTestSent, TargetType: "campaign", TargetID: uuidStr(c.ID)})
	})
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"sent_to": user.Email})
}

// campaignAction runs a state change and its audit entry in one transaction and answers with
// the campaign as it is afterwards.
func (s *Server) campaignAction(w http.ResponseWriter, r *http.Request, action func(tx pgx.Tx, c campaignRow) (auditAction string, err error)) {
	ctx := r.Context()
	c, err := s.campaignFor(r, true)
	if err != nil {
		writeError(w, r, err)
		return
	}
	user := sessionFrom(ctx).User
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		name, err := action(tx, c)
		if err != nil {
			return err
		}
		return audit.Write(ctx, dbq.New(tx), audit.Entry{Actor: user.ID, IP: clientFrom(r).IP, Action: name, TargetType: "campaign", TargetID: uuidStr(c.ID)})
	})
	if errors.Is(err, campaigns.ErrConflict) {
		writeError(w, r, errCampaignState)
		return
	}
	var notReady *campaigns.NotReadyError
	if errors.As(err, &notReady) {
		writeError(w, r, errValidation(notReady.Fields))
		return
	}
	if err != nil {
		writeError(w, r, err)
		return
	}
	s.respondCampaign(w, r, c.ID, http.StatusOK)
}

func (s *Server) startCampaign(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ScheduledAt *time.Time `json:"scheduled_at"`
	}
	if err := decode(r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	now := time.Now()
	if at := req.ScheduledAt; at != nil && (!at.After(now) || at.After(now.Add(maxScheduleAhead))) {
		writeError(w, r, errValidation(map[string]string{"scheduled_at": "invalid"}))
		return
	}
	s.campaignAction(w, r, func(tx pgx.Tx, c campaignRow) (string, error) {
		scheduled, err := s.campaignService().Start(r.Context(), tx, c.ID, sessionFrom(r.Context()).User, req.ScheduledAt)
		if scheduled {
			return audit.CampaignScheduled, err
		}
		return audit.CampaignStarted, err
	})
}

func (s *Server) pauseCampaign(w http.ResponseWriter, r *http.Request) {
	s.campaignAction(w, r, func(tx pgx.Tx, c campaignRow) (string, error) {
		return audit.CampaignPaused, s.campaigns.Pause(r.Context(), tx, c.ID)
	})
}

func (s *Server) resumeCampaign(w http.ResponseWriter, r *http.Request) {
	s.campaignAction(w, r, func(tx pgx.Tx, c campaignRow) (string, error) {
		return audit.CampaignResumed, s.campaignService().Resume(r.Context(), tx, c.ID)
	})
}

func (s *Server) cancelCampaign(w http.ResponseWriter, r *http.Request) {
	s.campaignAction(w, r, func(tx pgx.Tx, c campaignRow) (string, error) {
		return audit.CampaignCancelled, s.campaigns.Cancel(r.Context(), tx, c.ID)
	})
}

type campaignRecipientJSON struct {
	ID             string     `json:"id"`
	Email          string     `json:"email"`
	Name           string     `json:"name"`
	State          string     `json:"state"`
	SkipReason     string     `json:"skip_reason"`
	Delivery       string     `json:"delivery"`
	Error          string     `json:"error"`
	ConversationID string     `json:"conversation_id"`
	QueuedAt       *time.Time `json:"queued_at"`
	FinishedAt     *time.Time `json:"finished_at"`
}

var campaignRecipientStates = []string{"pending", "queued", "sent", "failed", "skipped"}

// The recipient list and the report show names and addresses of contacts, so they follow the
// contact visibility of the reader: a reader with mailbox access to the campaign but not to a
// contact does not see that recipient.
func (s *Server) listCampaignRecipients(w http.ResponseWriter, r *http.Request) {
	c, err := s.campaignFor(r, false)
	if err != nil {
		writeError(w, r, err)
		return
	}
	_, viewer, err := s.contactViewer(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	q := r.URL.Query()
	state := q.Get("state")
	if state != "" && !slices.Contains(campaignRecipientStates, state) {
		writeError(w, r, errValidation(map[string]string{"state": "invalid"}))
		return
	}
	limit := recipientPageSize
	if raw := q.Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > recipientPageMax {
			writeError(w, r, errValidation(map[string]string{"limit": "invalid"}))
			return
		}
		limit = n
	}
	after := pgtype.UUID{Valid: true}
	if raw := q.Get("after"); raw != "" {
		id, ok := parseUUID(raw)
		if !ok {
			writeError(w, r, errValidation(map[string]string{"after": "invalid"}))
			return
		}
		after = id
	}
	rows, err := s.q.CampaignListRecipients(r.Context(), dbq.CampaignListRecipientsParams{
		CampaignID: c.ID, State: state, After: after, Admin: viewer.Admin, UserID: viewer.UserID, MailboxIds: viewer.MailboxIDs, Batch: int32(limit + 1),
	})
	if err != nil {
		writeError(w, r, fmt.Errorf("list recipients: %w", err))
		return
	}
	var next *string
	if len(rows) > limit {
		rows = rows[:limit]
		id := uuidStr(rows[limit-1].ID)
		next = &id
	}
	out := make([]campaignRecipientJSON, len(rows))
	for i, row := range rows {
		out[i] = campaignRecipientJSON{
			ID: uuidStr(row.ID), Email: row.Email, Name: row.Name, State: row.State, SkipReason: row.SkipReason,
			Delivery: row.Delivery, Error: row.Error, ConversationID: uuidStr(row.ConversationID),
			QueuedAt: timeOrNil(row.QueuedAt), FinishedAt: timeOrNil(row.FinishedAt),
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"recipients": out, "next": next})
}

func (s *Server) campaignReport(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	c, err := s.campaignFor(r, false)
	if err != nil {
		writeError(w, r, err)
		return
	}
	user, viewer, err := s.contactViewer(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	err = db.InTx(ctx, s.pool, func(q *dbq.Queries) error {
		return audit.Write(ctx, q, audit.Entry{Actor: user.ID, IP: clientFrom(r).IP, Action: audit.CampaignReportLoaded, TargetType: "campaign", TargetID: uuidStr(c.ID)})
	})
	if err != nil {
		writeError(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="campagne-`+uuidStr(c.ID)+`.csv"`)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	rc := http.NewResponseController(w)
	// Once the header is out the status cannot change, so a failure ends the stream and is
	// logged; the client sees a truncated file.
	err = s.campaigns.WriteReport(ctx, c.ID, viewer, w, func() error {
		if err := rc.SetWriteDeadline(time.Now().Add(30 * time.Second)); err != nil && !errors.Is(err, http.ErrNotSupported) {
			return err
		}
		return nil
	})
	if err != nil {
		slog.ErrorContext(ctx, "campaign report failed", "campaign_id", uuidStr(c.ID), "err", err, "request_id", requestIDFrom(ctx))
	}
}
