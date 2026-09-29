package api

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"slices"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"echoo/internal/audit"
	"echoo/internal/auth"
	"echoo/internal/db"
	"echoo/internal/db/dbq"
	"echoo/internal/policy"
	"echoo/internal/webhooks"
)

const (
	maxURLLength     = 2048
	deliveryPageSize = 50
)

type webhookJSON struct {
	ID                  string    `json:"id"`
	URL                 string    `json:"url"`
	Events              []string  `json:"events"`
	IncludeContent      bool      `json:"include_content"`
	AllowHTTP           bool      `json:"allow_http"`
	Enabled             bool      `json:"enabled"`
	DisabledReason      string    `json:"disabled_reason"`
	ConsecutiveFailures int32     `json:"consecutive_failures"`
	CreatedAt           time.Time `json:"created_at"`
}

func toWebhookJSON(h dbq.Webhook) webhookJSON {
	return webhookJSON{
		ID: uuidStr(h.ID), URL: h.Url, Events: h.Events, IncludeContent: h.IncludeContent, AllowHTTP: h.AllowHttp,
		Enabled: h.Enabled, DisabledReason: h.DisabledReason, ConsecutiveFailures: h.ConsecutiveFailures,
		CreatedAt: h.CreatedAt.Time.UTC(),
	}
}

type deliveryJSON struct {
	ID         string    `json:"id"`
	Event      string    `json:"event"`
	Status     string    `json:"status"`
	StatusCode *int32    `json:"status_code"`
	Error      string    `json:"error"`
	DurationMs *int32    `json:"duration_ms"`
	Attempt    int32     `json:"attempt"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

func toDeliveryJSON(d dbq.WebhookDelivery) deliveryJSON {
	out := deliveryJSON{
		ID: uuidStr(d.ID), Event: d.Event, Status: d.Status, Error: d.Error, Attempt: d.Attempt,
		CreatedAt: d.CreatedAt.Time.UTC(), UpdatedAt: d.UpdatedAt.Time.UTC(),
	}
	if d.StatusCode.Valid {
		out.StatusCode = &d.StatusCode.Int32
	}
	if d.DurationMs.Valid {
		out.DurationMs = &d.DurationMs.Int32
	}
	return out
}

// validateWebhookURL returns a field error code, or "" when the URL may be used. The
// destination is checked again on every connection; this check only gives early feedback.
func validateWebhookURL(ctx context.Context, raw string, allowHTTP bool) string {
	if len(raw) == 0 || len(raw) > maxURLLength {
		return "invalid"
	}
	if code := webhooks.CheckURL(raw, allowHTTP); code != "" {
		return code
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "invalid"
	}
	host, ok := normalizeHost(u.Hostname())
	if !ok {
		return "invalid"
	}
	if checkHostAtSave(ctx, host) {
		return "internal_host"
	}
	return ""
}

func validEvents(events []string) bool {
	if len(events) == 0 {
		return false
	}
	for _, e := range events {
		if !webhooks.ValidEvent(e) {
			return false
		}
	}
	return true
}

func dedupe(in []string) []string {
	out := make([]string, 0, len(in))
	for _, v := range in {
		if !slices.Contains(out, v) {
			out = append(out, v)
		}
	}
	return out
}

func (s *Server) listWebhooks(w http.ResponseWriter, r *http.Request) {
	rows, err := s.q.ListWebhooks(r.Context())
	if err != nil {
		writeError(w, r, err)
		return
	}
	out := make([]webhookJSON, len(rows))
	for i, h := range rows {
		out[i] = toWebhookJSON(h)
	}
	writeJSON(w, http.StatusOK, map[string]any{"webhooks": out})
}

func webhookAudit(r *http.Request, action string, id pgtype.UUID, meta map[string]any) audit.Entry {
	return audit.Entry{
		Actor: sessionFrom(r.Context()).User.ID, IP: clientFrom(r).IP, Action: action,
		TargetType: "webhook", TargetID: uuidStr(id), Metadata: meta,
	}
}

func (s *Server) createWebhook(w http.ResponseWriter, r *http.Request) {
	var req struct {
		URL            string   `json:"url"`
		Events         []string `json:"events"`
		IncludeContent bool     `json:"include_content"`
		AllowHTTP      bool     `json:"allow_http"`
	}
	if err := decode(r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	actor := sessionFrom(r.Context()).User
	fields := map[string]string{}
	if req.AllowHTTP && actor.Role != policy.RoleOwner {
		fields["allow_http"] = "owner_only"
		req.AllowHTTP = false
	}
	if req.IncludeContent && !policy.SeesAll(actor) {
		fields["include_content"] = "needs_full_access"
		req.IncludeContent = false
	}
	if code := validateWebhookURL(r.Context(), req.URL, req.AllowHTTP); code != "" {
		fields["url"] = code
	}
	if !validEvents(req.Events) {
		fields["events"] = "invalid"
	}
	if len(fields) > 0 {
		writeError(w, r, errValidation(fields))
		return
	}

	secret, err := newWebhookSecret()
	if err != nil {
		writeError(w, r, err)
		return
	}
	var hook dbq.Webhook
	err = db.InTx(r.Context(), s.pool, func(q *dbq.Queries) error {
		id, err := q.NewUUID(r.Context())
		if err != nil {
			return err
		}
		enc, err := s.keys.Encrypt([]byte(secret), webhooks.SecretAAD(id))
		if err != nil {
			return err
		}
		hook, err = q.InsertWebhook(r.Context(), dbq.InsertWebhookParams{
			ID: id, Url: req.URL, SecretEnc: enc, Events: dedupe(req.Events),
			IncludeContent: req.IncludeContent, AllowHttp: req.AllowHTTP, CreatedBy: actor.ID,
		})
		if err != nil {
			return err
		}
		return audit.Write(r.Context(), q, webhookAudit(r, audit.WebhookCreated, hook.ID, map[string]any{
			"host": hostOf(req.URL), "events": hook.Events, "include_content": hook.IncludeContent, "allow_http": hook.AllowHttp,
		}))
	})
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"webhook": toWebhookJSON(hook), "secret": secret})
}

func (s *Server) updateWebhook(w http.ResponseWriter, r *http.Request) {
	id, ok := parseUUID(chi.URLParam(r, "id"))
	if !ok {
		writeError(w, r, errNotFound)
		return
	}
	var req struct {
		URL            *string   `json:"url"`
		Events         *[]string `json:"events"`
		IncludeContent *bool     `json:"include_content"`
		AllowHTTP      *bool     `json:"allow_http"`
		Enabled        *bool     `json:"enabled"`
	}
	if err := decode(r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	actor := sessionFrom(r.Context()).User
	var hook dbq.Webhook
	err := db.InTx(r.Context(), s.pool, func(q *dbq.Queries) error {
		cur, err := q.GetWebhook(r.Context(), id)
		if err != nil {
			return err
		}
		next := cur
		if req.URL != nil {
			next.Url = *req.URL
		}
		if req.Events != nil {
			next.Events = dedupe(*req.Events)
		}
		if req.IncludeContent != nil {
			next.IncludeContent = *req.IncludeContent
		}
		if req.AllowHTTP != nil {
			next.AllowHttp = *req.AllowHTTP
		}
		if req.Enabled != nil {
			next.Enabled = *req.Enabled
		}
		fields := map[string]string{}
		if next.AllowHttp && !cur.AllowHttp && actor.Role != policy.RoleOwner {
			fields["allow_http"] = "owner_only"
			next.AllowHttp = false
		}
		// Redirecting a hook that an owner or admin gave more reach than the actor could give it
		// would send that reach to a destination of the actor's choice.
		if next.Url != cur.Url || !slices.Equal(next.Events, cur.Events) {
			if cur.IncludeContent && !policy.SeesAll(actor) {
				fields["include_content"] = "needs_full_access"
			}
			if cur.AllowHttp && actor.Role != policy.RoleOwner {
				fields["allow_http"] = "owner_only"
			}
		}
		// Content payloads carry message text from every mailbox, not just the actor's.
		if next.IncludeContent && !cur.IncludeContent && !policy.SeesAll(actor) {
			fields["include_content"] = "needs_full_access"
			next.IncludeContent = false
		}
		if code := validateWebhookURL(r.Context(), next.Url, next.AllowHttp); code != "" {
			fields["url"] = code
		}
		if !validEvents(next.Events) {
			fields["events"] = "invalid"
		}
		if len(fields) > 0 {
			return errValidation(fields)
		}
		hook, err = q.UpdateWebhook(r.Context(), dbq.UpdateWebhookParams{
			ID: id, Url: next.Url, Events: next.Events, IncludeContent: next.IncludeContent,
			AllowHttp: next.AllowHttp, Enabled: next.Enabled,
		})
		if err != nil {
			return err
		}
		return audit.Write(r.Context(), q, webhookAudit(r, audit.WebhookUpdated, hook.ID, map[string]any{
			"host": hostOf(hook.Url), "events": hook.Events, "include_content": hook.IncludeContent,
			"allow_http": hook.AllowHttp, "enabled": hook.Enabled,
		}))
	})
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, r, errNotFound)
		return
	}
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"webhook": toWebhookJSON(hook)})
}

func (s *Server) deleteWebhook(w http.ResponseWriter, r *http.Request) {
	id, ok := parseUUID(chi.URLParam(r, "id"))
	if !ok {
		writeError(w, r, errNotFound)
		return
	}
	err := db.InTx(r.Context(), s.pool, func(q *dbq.Queries) error {
		hook, err := q.GetWebhook(r.Context(), id)
		if err != nil {
			return err
		}
		if _, err := q.DeleteWebhook(r.Context(), id); err != nil {
			return err
		}
		return audit.Write(r.Context(), q, webhookAudit(r, audit.WebhookDeleted, id, map[string]any{"host": hostOf(hook.Url)}))
	})
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, r, errNotFound)
		return
	}
	if err != nil {
		writeError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) testWebhook(w http.ResponseWriter, r *http.Request) {
	id, ok := parseUUID(chi.URLParam(r, "id"))
	if !ok {
		writeError(w, r, errNotFound)
		return
	}
	s.enqueueDelivery(w, r, id, func(ctx context.Context, q *dbq.Queries) (int64, error) {
		return q.InsertTestWebhookEvent(ctx)
	}, audit.WebhookTested)
}

func (s *Server) resendWebhookDelivery(w http.ResponseWriter, r *http.Request) {
	hookID, ok1 := parseUUID(chi.URLParam(r, "id"))
	delID, ok2 := parseUUID(chi.URLParam(r, "deliveryId"))
	if !ok1 || !ok2 {
		writeError(w, r, errNotFound)
		return
	}
	s.enqueueDelivery(w, r, hookID, func(ctx context.Context, q *dbq.Queries) (int64, error) {
		old, err := q.GetWebhookDelivery(ctx, delID)
		if err != nil {
			return 0, err
		}
		if old.WebhookID != hookID {
			return 0, pgx.ErrNoRows
		}
		return old.EventID, nil
	}, audit.WebhookDeliveryResent)
}

// enqueueDelivery creates a new delivery for hookID, for the event that eventFor returns, and
// schedules it in the same transaction.
func (s *Server) enqueueDelivery(w http.ResponseWriter, r *http.Request, hookID pgtype.UUID, eventFor func(context.Context, *dbq.Queries) (int64, error), action string) {
	if s.jobs == nil {
		writeError(w, r, errors.New("job queue is not configured"))
		return
	}
	var del dbq.WebhookDelivery
	err := pgx.BeginFunc(r.Context(), s.pool, func(tx pgx.Tx) error {
		q := dbq.New(tx)
		hook, err := q.GetWebhook(r.Context(), hookID)
		if err != nil {
			return err
		}
		eventID, err := eventFor(r.Context(), q)
		if err != nil {
			return err
		}
		ev, err := q.GetWebhookEvent(r.Context(), eventID)
		if err != nil {
			return err
		}
		if del, err = q.InsertWebhookDelivery(r.Context(), dbq.InsertWebhookDeliveryParams{WebhookID: hookID, EventID: eventID, Event: ev.Type}); err != nil {
			return err
		}
		if err := webhooks.EnqueueDelivery(r.Context(), s.jobs, tx, del.ID); err != nil {
			return err
		}
		return audit.Write(r.Context(), q, webhookAudit(r, action, hook.ID, map[string]any{"event": ev.Type}))
	})
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, r, errNotFound)
		return
	}
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"delivery": toDeliveryJSON(del)})
}

func (s *Server) listWebhookDeliveries(w http.ResponseWriter, r *http.Request) {
	id, ok := parseUUID(chi.URLParam(r, "id"))
	if !ok {
		writeError(w, r, errNotFound)
		return
	}
	var before pgtype.UUID
	if v := r.URL.Query().Get("before"); v != "" {
		if before, ok = parseUUID(v); !ok {
			writeError(w, r, errBadRequest("before must be a delivery id"))
			return
		}
	}
	if _, err := s.q.GetWebhook(r.Context(), id); errors.Is(err, pgx.ErrNoRows) {
		writeError(w, r, errNotFound)
		return
	} else if err != nil {
		writeError(w, r, err)
		return
	}
	rows, err := s.q.ListWebhookDeliveries(r.Context(), dbq.ListWebhookDeliveriesParams{WebhookID: id, BeforeID: before, PageSize: deliveryPageSize})
	if err != nil {
		writeError(w, r, err)
		return
	}
	out := make([]deliveryJSON, len(rows))
	for i, d := range rows {
		out[i] = toDeliveryJSON(d)
	}
	var next *string
	if len(rows) == deliveryPageSize {
		v := uuidStr(rows[len(rows)-1].ID)
		next = &v
	}
	writeJSON(w, http.StatusOK, map[string]any{"deliveries": out, "next_before": next})
}

func newWebhookSecret() (string, error) {
	tok, _, err := auth.NewToken()
	if err != nil {
		return "", err
	}
	return "whsec_" + tok, nil
}

// hostOf is what the audit log records about a URL: the path or query may hold a secret.
func hostOf(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return u.Host
}
