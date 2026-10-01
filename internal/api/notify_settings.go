package api

import (
	"net/http"

	"echoo/internal/db/dbq"
)

type notificationSettings struct {
	Mentions    bool `json:"mentions"`
	Assignments bool `json:"assignments"`
	Replies     bool `json:"replies"`
}

// pushSettings are the kinds a user wants on their phones and browsers.
type pushSettings struct {
	Mentions    bool `json:"mentions"`
	Assignments bool `json:"assignments"`
	Replies     bool `json:"replies"`
	SLA         bool `json:"sla"`
}

func pushSettingsOf(u dbq.User) pushSettings {
	return pushSettings{Mentions: u.PushNotifyMentions, Assignments: u.PushNotifyAssignments, Replies: u.PushNotifyReplies, SLA: u.PushNotifySla}
}

func (s *Server) getNotificationSettings(w http.ResponseWriter, r *http.Request) {
	u := sessionFrom(r.Context()).User
	st, err := s.systemMailReady(r.Context())
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"email":                 notificationSettings{Mentions: u.EmailNotifyMentions, Assignments: u.EmailNotifyAssignments, Replies: u.EmailNotifyReplies},
		"push":                  pushSettingsOf(u),
		"system_mail_available": st.Available,
	})
}

func (s *Server) putNotificationSettings(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Mentions    *bool `json:"mentions"`
		Assignments *bool `json:"assignments"`
		Replies     *bool `json:"replies"`
		// Push is optional, so clients that only know the email toggles keep working.
		Push *pushSettings `json:"push"`
	}
	if err := decode(r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	current := sessionFrom(r.Context()).User
	// A client that only changes push sends only push; the email choices stay as stored.
	if req.Mentions == nil && req.Assignments == nil && req.Replies == nil && req.Push != nil {
		req.Mentions, req.Assignments = &current.EmailNotifyMentions, &current.EmailNotifyAssignments
	}
	if req.Mentions == nil || req.Assignments == nil {
		writeError(w, r, errValidation(map[string]string{"mentions": "required"}))
		return
	}
	// Clients that predate the replies toggle omit it; that keeps the stored choice.
	replies := current.EmailNotifyReplies
	if req.Replies != nil {
		replies = *req.Replies
	}
	u, err := s.q.SetUserNotifyPrefs(r.Context(), dbq.SetUserNotifyPrefsParams{
		ID: current.ID, EmailNotifyMentions: *req.Mentions, EmailNotifyAssignments: *req.Assignments, EmailNotifyReplies: replies,
	})
	if err != nil {
		writeError(w, r, err)
		return
	}
	if p := req.Push; p != nil {
		err := s.q.PushSetPrefs(r.Context(), dbq.PushSetPrefsParams{ID: current.ID, Mentions: p.Mentions, Assignments: p.Assignments, Replies: p.Replies, Sla: p.SLA})
		if err != nil {
			writeError(w, r, err)
			return
		}
		u.PushNotifyMentions, u.PushNotifyAssignments, u.PushNotifyReplies, u.PushNotifySla = p.Mentions, p.Assignments, p.Replies, p.SLA
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"email": notificationSettings{Mentions: u.EmailNotifyMentions, Assignments: u.EmailNotifyAssignments, Replies: u.EmailNotifyReplies},
		"push":  pushSettingsOf(u),
	})
}
