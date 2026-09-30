package api

import (
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"echoo/internal/audit"
	"echoo/internal/db"
	"echoo/internal/db/dbq"
	"echoo/internal/ops"
	"echoo/internal/retention"
)

// opsAdminRoutes are the retention settings; they need settings.manage (see permissions.go).
func (s *Server) opsAdminRoutes(r chi.Router) {
	r.Get("/settings/retention", s.getRetention)
	r.Put("/settings/retention", s.putRetention)
	r.Post("/settings/retention/preview", s.previewRetention)
}

// opsOwnerRoutes are for the owner only.
func (s *Server) opsOwnerRoutes(r chi.Router) {
	r.Get("/admin/keys", s.keyStatus)
}

type retentionResponse struct {
	retention.Settings
	LastRun *retention.LastRun `json:"last_run"`
}

func (s *Server) getRetention(w http.ResponseWriter, r *http.Request) {
	set, err := retention.Load(r.Context(), s.q)
	if err != nil {
		writeError(w, r, err)
		return
	}
	last, err := retention.LoadLastRun(r.Context(), s.q)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, retentionResponse{Settings: set, LastRun: last})
}

// decodeRetention reads and validates proposed settings.
func (s *Server) decodeRetention(w http.ResponseWriter, r *http.Request) (retention.Settings, bool) {
	var set retention.Settings
	if err := decode(r, &set); err != nil {
		writeError(w, r, err)
		return set, false
	}
	if set.Mailboxes == nil {
		set.Mailboxes = []retention.MailboxPeriods{}
	}
	ids, err := s.q.RetentionListMailboxIDs(r.Context())
	if err != nil {
		writeError(w, r, err)
		return set, false
	}
	if problems := retention.Validate(set, ids); len(problems) > 0 {
		writeError(w, r, errValidation(problems))
		return set, false
	}
	return set, true
}

func (s *Server) putRetention(w http.ResponseWriter, r *http.Request) {
	set, ok := s.decodeRetention(w, r)
	if !ok {
		return
	}
	actor := sessionFrom(r.Context()).User
	err := db.InTx(r.Context(), s.pool, func(q *dbq.Queries) error {
		if err := retention.Save(r.Context(), q, set, actor.ID); err != nil {
			return err
		}
		overrides := make([]map[string]any, len(set.Mailboxes))
		for i, m := range set.Mailboxes {
			overrides[i] = map[string]any{"mailbox_id": m.MailboxID.String(), "closed_conversation_months": m.ClosedConversationMonths,
				"attachment_months": m.AttachmentMonths, "spam_days": m.SpamDays, "trash_days": m.TrashDays}
		}
		return audit.Write(r.Context(), q, audit.Entry{Actor: actor.ID, IP: clientFrom(r).IP, Action: audit.RetentionChanged, TargetType: "settings", TargetID: "retention",
			Metadata: map[string]any{"global": set.Global, "audit_months": set.AuditMonths, "mailbox_overrides": overrides}})
	})
	if isForeignKeyViolation(err) {
		// A mailbox was deleted between validation and saving.
		writeError(w, r, errValidation(map[string]string{"mailboxes": "unknown mailbox"}))
		return
	}
	if err != nil {
		writeError(w, r, err)
		return
	}
	s.getRetention(w, r)
}

// previewRetention counts what the proposed settings would delete right now, without saving
// or deleting anything, so an admin sees the effect before turning a period on.
func (s *Server) previewRetention(w http.ResponseWriter, r *http.Request) {
	set, ok := s.decodeRetention(w, r)
	if !ok {
		return
	}
	p, err := retention.NewService(s.pool, nil).Preview(r.Context(), set)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, p)
}

type keyStatusEntry struct {
	ID      string           `json:"id"`
	Active  bool             `json:"active"`
	Values  int64            `json:"values"`
	Columns map[string]int64 `json:"columns"`
}

type keyStatusResponse struct {
	Keys []keyStatusEntry `json:"keys"`
	// Unknown lists key ids found in the database that are not configured; those values
	// cannot be read until the key is configured again.
	Unknown []keyStatusEntry `json:"unknown"`
}

// keyStatus tells the owner how many stored secrets each configured key still protects, so
// they know when an old key can be removed. Only the owner may see it: it describes the
// state of the workspace's encryption.
func (s *Server) keyStatus(w http.ResponseWriter, r *http.Request) {
	if s.keys == nil {
		writeError(w, r, &apiError{Status: http.StatusServiceUnavailable, Code: "keys_unavailable", Message: "no encryption keys are configured"})
		return
	}
	usage, err := ops.CountKeyUsage(r.Context(), s.pool)
	if err != nil {
		writeError(w, r, err)
		return
	}
	entry := func(id string) keyStatusEntry {
		cols := usage[id]
		if cols == nil {
			cols = map[string]int64{}
		}
		return keyStatusEntry{ID: id, Active: id == s.keys.ActiveID(), Values: usage.Total(id), Columns: cols}
	}
	resp := keyStatusResponse{Keys: []keyStatusEntry{}, Unknown: []keyStatusEntry{}}
	for _, id := range s.keys.IDs() {
		resp.Keys = append(resp.Keys, entry(id))
	}
	unknown := ops.UnusableKeys(s.keys, usage)
	for _, id := range ops.SortedKeys(unknown) {
		resp.Unknown = append(resp.Unknown, entry(id))
	}
	writeJSON(w, http.StatusOK, resp)
}

// instrument records every request in the metrics registry, when there is one.
func (s *Server) instrument(next http.Handler) http.Handler {
	if s.metrics == nil {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		route := "unmatched"
		if rc := chi.RouteContext(r.Context()); rc != nil && rc.RoutePattern() != "" {
			route = rc.RoutePattern()
		}
		s.metrics.ObserveRequest(route, rec.status, time.Since(start))
	})
}
