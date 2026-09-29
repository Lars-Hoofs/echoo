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

func (s *Server) getNotificationSettings(w http.ResponseWriter, r *http.Request) {
	u := sessionFrom(r.Context()).User
	st, err := s.systemMailReady(r.Context())
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"email":                 notificationSettings{Mentions: u.EmailNotifyMentions, Assignments: u.EmailNotifyAssignments, Replies: u.EmailNotifyReplies},
		"system_mail_available": st.Available,
	})
}

func (s *Server) putNotificationSettings(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Mentions    *bool `json:"mentions"`
		Assignments *bool `json:"assignments"`
		Replies     *bool `json:"replies"`
	}
	if err := decode(r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	if req.Mentions == nil || req.Assignments == nil {
		writeError(w, r, errValidation(map[string]string{"mentions": "required"}))
		return
	}
	current := sessionFrom(r.Context()).User
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
	writeJSON(w, http.StatusOK, map[string]any{"email": notificationSettings{Mentions: u.EmailNotifyMentions, Assignments: u.EmailNotifyAssignments, Replies: u.EmailNotifyReplies}})
}
