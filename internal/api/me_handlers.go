package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"

	"echoo/internal/audit"
	"echoo/internal/auth"
	"echoo/internal/db"
	"echoo/internal/db/dbq"
	"echoo/internal/policy"
)

type userJSON struct {
	ID           string  `json:"id"`
	Email        string  `json:"email"`
	Name         string  `json:"name"`
	Role         string  `json:"role"`
	CustomRoleID *string `json:"custom_role_id"`
	Theme        string  `json:"theme"`
	Availability string  `json:"availability"`
	// MaxOpen is the user's capacity for auto-assignment; nil means no limit.
	MaxOpen     *int32 `json:"max_open"`
	MFAEnabled  bool   `json:"mfa_enabled"`
	Deactivated bool   `json:"deactivated"`
	// CanWriteConversations is what pickers of assignees and rule targets need to leave out
	// read-only users, whatever their role is called.
	CanWriteConversations bool `json:"can_write_conversations"`
	// InvitedAt is set while the user has not accepted the invitation yet.
	InvitedAt   *time.Time `json:"invited_at"`
	LastLoginAt *time.Time `json:"last_login_at"`
	CreatedAt   time.Time  `json:"created_at"`
}

func toUserJSON(u dbq.User) userJSON {
	out := userJSON{
		ID: uuidStr(u.ID), Email: u.Email, Name: u.Name, Role: u.Role, Theme: u.Theme, Availability: u.Availability,
		MFAEnabled: u.TotpEnabledAt.Valid, Deactivated: u.DeactivatedAt.Valid, InvitedAt: timeOrNil(u.InvitedAt),
		LastLoginAt: timeOrNil(u.LastLoginAt), CreatedAt: u.CreatedAt.Time.UTC(),
		CanWriteConversations: policy.Has(u, policy.ConversationsWrite),
	}
	if u.CustomRoleID.Valid {
		id := uuidStr(u.CustomRoleID)
		out.CustomRoleID = &id
	}
	if u.MaxOpen.Valid {
		out.MaxOpen = &u.MaxOpen.Int32
	}
	return out
}

type securitySettings struct {
	RequireMFA bool `json:"require_mfa"`
}

const securitySettingsKey = "security"

func (s *Server) securitySettings(ctx context.Context) (securitySettings, error) {
	var out securitySettings
	raw, err := s.q.GetSetting(ctx, securitySettingsKey)
	if errors.Is(err, pgx.ErrNoRows) {
		return out, nil
	}
	if err != nil {
		return out, err
	}
	return out, json.Unmarshal(raw, &out)
}

func (s *Server) mfaEnrollmentRequired(ctx context.Context, sess *auth.Session) (bool, error) {
	if sess.User.TotpEnabledAt.Valid || sess.IdpMfa {
		return false, nil
	}
	set, err := s.securitySettings(ctx)
	return set.RequireMFA, err
}

func (s *Server) me(w http.ResponseWriter, r *http.Request) {
	sess := sessionFrom(r.Context())
	enroll, err := s.mfaEnrollmentRequired(r.Context(), sess)
	if err != nil {
		writeError(w, r, err)
		return
	}
	var codesLeft int64
	if sess.User.TotpEnabledAt.Valid {
		if codesLeft, err = s.q.CountUnusedRecoveryCodes(r.Context(), sess.User.ID); err != nil {
			writeError(w, r, err)
			return
		}
	}
	var roleName string
	if sess.User.CustomRoleID.Valid {
		role, err := s.q.GetCustomRole(r.Context(), sess.User.CustomRoleID)
		if err != nil {
			writeError(w, r, err)
			return
		}
		roleName = role.Name
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"user":                     toUserJSON(sess.User),
		"permissions":              policy.Effective(sess.User),
		"custom_role_name":         roleName,
		"csrf_token":               sess.CsrfToken,
		"must_change_password":     sess.User.PasswordMustChange,
		"mfa_enrollment_required":  enroll,
		"recovery_codes_remaining": codesLeft,
	})
}

func (s *Server) updateMe(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name  *string `json:"name"`
		Theme *string `json:"theme"`
	}
	if err := decode(r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	fields := map[string]string{}
	if req.Name != nil {
		name, ok := cleanText(*req.Name, 200)
		if !ok {
			fields["name"] = "invalid"
		}
		*req.Name = name
	}
	if req.Theme != nil && *req.Theme != "system" && *req.Theme != "light" && *req.Theme != "dark" {
		fields["theme"] = "invalid"
	}
	if len(fields) > 0 {
		writeError(w, r, errValidation(fields))
		return
	}
	sess := sessionFrom(r.Context())
	user := sess.User
	err := db.InTx(r.Context(), s.pool, func(q *dbq.Queries) error {
		var err error
		if req.Name != nil && *req.Name != user.Name {
			if user, err = q.UpdateUserName(r.Context(), dbq.UpdateUserNameParams{ID: user.ID, Name: *req.Name}); err != nil {
				return err
			}
			if err := audit.Write(r.Context(), q, audit.Entry{Actor: user.ID, IP: clientFrom(r).IP, Action: audit.UserRenamed, TargetType: "user", TargetID: uuidStr(user.ID)}); err != nil {
				return err
			}
		}
		if req.Theme != nil {
			user, err = q.UpdateUserTheme(r.Context(), dbq.UpdateUserThemeParams{ID: user.ID, Theme: *req.Theme})
		}
		return err
	})
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"user": toUserJSON(user)})
}

func passwordFieldError(err error) *apiError {
	switch {
	case errors.Is(err, auth.ErrPasswordTooShort):
		return errValidation(map[string]string{"new_password": "too_short"})
	case errors.Is(err, auth.ErrPasswordTooLong):
		return errValidation(map[string]string{"new_password": "too_long"})
	}
	return nil
}

func (s *Server) changePassword(w http.ResponseWriter, r *http.Request) {
	var req struct {
		CurrentPassword string `json:"current_password"`
		NewPassword     string `json:"new_password"`
	}
	if err := decode(r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	if err := auth.ValidatePassword(req.NewPassword); err != nil {
		writeError(w, r, passwordFieldError(err))
		return
	}
	if req.NewPassword == req.CurrentPassword {
		writeError(w, r, errValidation(map[string]string{"new_password": "same_as_current"}))
		return
	}
	sess, err := s.auth.ChangePassword(r.Context(), sessionFrom(r.Context()), req.CurrentPassword, req.NewPassword, clientFrom(r))
	if errors.Is(err, auth.ErrInvalidCredentials) {
		writeError(w, r, errValidation(map[string]string{"current_password": "incorrect"}))
		return
	}
	if err != nil {
		writeError(w, r, err)
		return
	}
	s.setSessionCookie(w, sess)
	writeJSON(w, http.StatusOK, map[string]any{"csrf_token": sess.CsrfToken})
}

func (s *Server) beginTOTP(w http.ResponseWriter, r *http.Request) {
	setup, err := s.auth.BeginTOTP(r.Context(), sessionFrom(r.Context()))
	if errors.Is(err, auth.ErrMFAAlreadyEnabled) {
		writeError(w, r, &apiError{Status: http.StatusConflict, Code: "mfa_already_enabled", Message: err.Error()})
		return
	}
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"secret": setup.Secret, "uri": setup.URI})
}

func (s *Server) enableTOTP(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Password string `json:"password"`
		Code     string `json:"code"`
	}
	if err := decode(r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	codes, err := s.auth.EnableTOTP(r.Context(), sessionFrom(r.Context()), req.Password, req.Code, clientFrom(r))
	switch {
	case errors.Is(err, auth.ErrInvalidCredentials):
		writeError(w, r, errValidation(map[string]string{"password": "incorrect"}))
	case errors.Is(err, auth.ErrInvalidCode):
		writeError(w, r, errInvalidCode)
	case errors.Is(err, auth.ErrMFAAlreadyEnabled), errors.Is(err, auth.ErrMFANotStarted):
		writeError(w, r, &apiError{Status: http.StatusConflict, Code: "mfa_state_conflict", Message: err.Error()})
	case err != nil:
		writeError(w, r, err)
	default:
		writeJSON(w, http.StatusOK, map[string]any{"recovery_codes": codes})
	}
}

func (s *Server) disableTOTP(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Password string `json:"password"`
	}
	if err := decode(r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	err := s.auth.DisableTOTP(r.Context(), sessionFrom(r.Context()), req.Password, clientFrom(r))
	switch {
	case errors.Is(err, auth.ErrInvalidCredentials):
		writeError(w, r, errValidation(map[string]string{"password": "incorrect"}))
	case errors.Is(err, auth.ErrMFANotEnabled):
		writeError(w, r, &apiError{Status: http.StatusConflict, Code: "mfa_state_conflict", Message: err.Error()})
	case err != nil:
		writeError(w, r, err)
	default:
		w.WriteHeader(http.StatusNoContent)
	}
}

func (s *Server) regenerateRecoveryCodes(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Password string `json:"password"`
	}
	if err := decode(r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	codes, err := s.auth.RegenerateRecoveryCodes(r.Context(), sessionFrom(r.Context()), req.Password, clientFrom(r))
	switch {
	case errors.Is(err, auth.ErrInvalidCredentials):
		writeError(w, r, errValidation(map[string]string{"password": "incorrect"}))
	case errors.Is(err, auth.ErrMFANotEnabled):
		writeError(w, r, &apiError{Status: http.StatusConflict, Code: "mfa_state_conflict", Message: err.Error()})
	case err != nil:
		writeError(w, r, err)
	default:
		writeJSON(w, http.StatusOK, map[string]any{"recovery_codes": codes})
	}
}

func (s *Server) listSessions(w http.ResponseWriter, r *http.Request) {
	sess := sessionFrom(r.Context())
	rows, err := s.q.ListUserSessions(r.Context(), sess.User.ID)
	if err != nil {
		writeError(w, r, err)
		return
	}
	type sessionJSON struct {
		ID         string    `json:"id"`
		Current    bool      `json:"current"`
		CreatedAt  time.Time `json:"created_at"`
		LastSeenAt time.Time `json:"last_seen_at"`
		IP         string    `json:"ip"`
		UserAgent  string    `json:"user_agent"`
	}
	out := make([]sessionJSON, 0, len(rows))
	for _, row := range rows {
		ip := ""
		if row.Ip != nil {
			ip = row.Ip.String()
		}
		out = append(out, sessionJSON{
			ID: uuidStr(row.ID), Current: row.ID == sess.ID, CreatedAt: row.CreatedAt.Time.UTC(),
			LastSeenAt: row.LastSeenAt.Time.UTC(), IP: ip, UserAgent: row.UserAgent,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"sessions": out})
}

func (s *Server) revokeSession(w http.ResponseWriter, r *http.Request) {
	id, ok := parseUUID(chi.URLParam(r, "id"))
	if !ok {
		writeError(w, r, errNotFound)
		return
	}
	sess := sessionFrom(r.Context())
	if id == sess.ID {
		writeError(w, r, &apiError{Status: http.StatusConflict, Code: "current_session", Message: "use logout to end the current session"})
		return
	}
	found, err := s.auth.RevokeOwnSession(r.Context(), sess, id, clientFrom(r))
	if err != nil {
		writeError(w, r, err)
		return
	}
	if !found {
		writeError(w, r, errNotFound)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) revokeOtherSessions(w http.ResponseWriter, r *http.Request) {
	n, err := s.auth.LogoutOthers(r.Context(), sessionFrom(r.Context()), clientFrom(r))
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"revoked": n})
}
