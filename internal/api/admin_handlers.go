package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/mail"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"echoo/internal/audit"
	"echoo/internal/auth"
	"echoo/internal/db"
	"echoo/internal/db/dbq"
	"echoo/internal/policy"
)

func validEmail(s string) bool {
	if len(s) > 254 {
		return false
	}
	addr, err := mail.ParseAddress(s)
	return err == nil && addr.Address == s && addr.Name == ""
}

func (s *Server) listUsers(w http.ResponseWriter, r *http.Request) {
	users, err := s.q.ListUsers(r.Context())
	if err != nil {
		writeError(w, r, err)
		return
	}
	out := make([]userJSON, len(users))
	for i, u := range users {
		out[i] = toUserJSON(u)
	}
	writeJSON(w, http.StatusOK, map[string]any{"users": out})
}

func (s *Server) createUser(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Email        string  `json:"email"`
		Name         string  `json:"name"`
		Role         string  `json:"role"`
		CustomRoleID *string `json:"custom_role_id"`
	}
	if err := decode(r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	actor := sessionFrom(r.Context()).User
	email := auth.NormalizeEmail(req.Email)
	fields := map[string]string{}
	if !validEmail(email) {
		fields["email"] = "invalid"
	}
	name, ok := cleanText(req.Name, 200)
	if !ok {
		fields["name"] = "invalid"
	}
	choice, _ := parseRoleChoice(req.Role, req.CustomRoleID, fields)
	if len(fields) > 0 {
		writeError(w, r, errValidation(fields))
		return
	}
	if err := choice.authorize(r.Context(), s.q, actor); err != nil {
		writeError(w, r, err)
		return
	}
	var (
		user dbq.User
		temp string
		err  error
	)
	if choice.role == policy.RoleCustom {
		user, temp, err = s.auth.CreateUserWithCustomRole(r.Context(), actor.ID, email, name, choice.customID, clientFrom(r))
	} else {
		user, temp, err = s.auth.CreateUser(r.Context(), actor.ID, email, name, choice.role, clientFrom(r))
	}
	if isUniqueViolation(err) {
		writeError(w, r, errValidation(map[string]string{"email": "taken"}))
		return
	}
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"user": toUserJSON(user), "temporary_password": temp})
}

// loadManagedUser fetches the target and checks that the actor may manage it. Forbidden is
// distinguishable from missing here on purpose: admins can list all users anyway.
func (s *Server) loadManagedUser(w http.ResponseWriter, r *http.Request) (dbq.User, bool) {
	id, ok := parseUUID(chi.URLParam(r, "id"))
	if !ok {
		writeError(w, r, errNotFound)
		return dbq.User{}, false
	}
	target, err := s.q.GetUser(r.Context(), id)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, r, errNotFound)
		return dbq.User{}, false
	}
	if err != nil {
		writeError(w, r, err)
		return dbq.User{}, false
	}
	if !policy.CanManageUser(sessionFrom(r.Context()).User, target) {
		writeError(w, r, errForbidden)
		return dbq.User{}, false
	}
	return target, true
}

func (s *Server) updateUser(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name         *string `json:"name"`
		Role         *string `json:"role"`
		CustomRoleID *string `json:"custom_role_id"`
		Deactivated  *bool   `json:"deactivated"`
	}
	if err := decode(r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	id, ok := parseUUID(chi.URLParam(r, "id"))
	if !ok {
		writeError(w, r, errNotFound)
		return
	}
	actor := sessionFrom(r.Context()).User
	var choice *roleChoice
	if req.Role != nil || req.CustomRoleID != nil {
		fields := map[string]string{}
		role := ""
		if req.Role != nil {
			role = *req.Role
		}
		c, _ := parseRoleChoice(role, req.CustomRoleID, fields)
		if len(fields) > 0 {
			writeError(w, r, errValidation(fields))
			return
		}
		choice = &c
	}
	if req.Name != nil {
		name, ok := cleanText(*req.Name, 200)
		if !ok {
			writeError(w, r, errValidation(map[string]string{"name": "invalid"}))
			return
		}
		req.Name = &name
	}

	ip := clientFrom(r).IP
	var target dbq.User
	err := db.InTx(r.Context(), s.pool, func(q *dbq.Queries) error {
		// The target is locked before it is authorized, so a concurrent role change cannot slip
		// between the check and the update.
		var err error
		if target, err = q.GetUserForUpdate(r.Context(), id); errors.Is(err, pgx.ErrNoRows) {
			return errNotFound
		} else if err != nil {
			return err
		}
		if !policy.CanManageUser(actor, target) {
			return errForbidden
		}
		if choice != nil {
			if err := choice.authorize(r.Context(), q, actor); err != nil {
				return err
			}
		}
		// An invited user is kept deactivated until they accept; revoke the invitation instead.
		if target.InvitedAt.Valid && req.Deactivated != nil {
			return errValidation(map[string]string{"deactivated": "invited"})
		}
		entry := func(action string, meta map[string]any) audit.Entry {
			return audit.Entry{Actor: actor.ID, IP: ip, Action: action, TargetType: "user", TargetID: uuidStr(target.ID), Metadata: meta}
		}
		if req.Name != nil && *req.Name != target.Name {
			if target, err = q.UpdateUserName(r.Context(), dbq.UpdateUserNameParams{ID: target.ID, Name: *req.Name}); err != nil {
				return err
			}
			if err := audit.Write(r.Context(), q, entry(audit.UserRenamed, nil)); err != nil {
				return err
			}
		}
		if choice != nil && (choice.role != target.Role || (choice.role == policy.RoleCustom && choice.customID != target.CustomRoleID)) {
			from := target.Role
			meta := map[string]any{"from": from}
			if from == policy.RoleCustom {
				meta["from_custom_role_id"] = uuidStr(target.CustomRoleID)
			}
			if choice.role == policy.RoleCustom {
				target, err = q.SetUserCustomRole(r.Context(), dbq.SetUserCustomRoleParams{ID: target.ID, RoleID: choice.customID})
				meta["custom_role_id"] = uuidStr(choice.customID)
			} else {
				target, err = q.UpdateUserRole(r.Context(), dbq.UpdateUserRoleParams{ID: target.ID, Role: choice.role})
			}
			if err != nil {
				return err
			}
			meta["to"] = target.Role
			if err := audit.Write(r.Context(), q, entry(audit.UserRoleChanged, meta)); err != nil {
				return err
			}
		}
		if req.Deactivated != nil && *req.Deactivated != target.DeactivatedAt.Valid {
			if target, err = q.SetUserDeactivated(r.Context(), dbq.SetUserDeactivatedParams{ID: target.ID, Deactivated: *req.Deactivated}); err != nil {
				return err
			}
			action := audit.UserReactivated
			if *req.Deactivated {
				action = audit.UserDeactivated
				if _, err := q.RevokeUserSessions(r.Context(), dbq.RevokeUserSessionsParams{UserID: target.ID, ExceptID: pgtype.UUID{Valid: true}}); err != nil {
					return err
				}
			}
			if err := audit.Write(r.Context(), q, entry(action, nil)); err != nil {
				return err
			}
		}
		if choice != nil || req.Deactivated != nil {
			return notifyScopeChanged(r.Context(), q)
		}
		return nil
	})
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"user": toUserJSON(target)})
}

func (s *Server) resetUserPassword(w http.ResponseWriter, r *http.Request) {
	target, ok := s.loadManagedUser(w, r)
	if !ok {
		return
	}
	temp, err := s.auth.ResetPassword(r.Context(), sessionFrom(r.Context()).User.ID, target, clientFrom(r))
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"temporary_password": temp})
}

func (s *Server) resetUserMFA(w http.ResponseWriter, r *http.Request) {
	target, ok := s.loadManagedUser(w, r)
	if !ok {
		return
	}
	if err := s.auth.ResetMFA(r.Context(), sessionFrom(r.Context()).User.ID, target, clientFrom(r)); err != nil {
		writeError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type teamJSON struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	MemberCount int32     `json:"member_count"`
	CreatedAt   time.Time `json:"created_at"`
}

func (s *Server) listTeams(w http.ResponseWriter, r *http.Request) {
	rows, err := s.q.ListTeams(r.Context())
	if err != nil {
		writeError(w, r, err)
		return
	}
	out := make([]teamJSON, len(rows))
	for i, t := range rows {
		out[i] = teamJSON{ID: uuidStr(t.ID), Name: t.Name, MemberCount: t.MemberCount, CreatedAt: t.CreatedAt.Time.UTC()}
	}
	writeJSON(w, http.StatusOK, map[string]any{"teams": out})
}

func decodeTeamName(w http.ResponseWriter, r *http.Request) (string, bool) {
	var req struct {
		Name string `json:"name"`
	}
	if err := decode(r, &req); err != nil {
		writeError(w, r, err)
		return "", false
	}
	name, ok := cleanText(req.Name, 100)
	if !ok {
		writeError(w, r, errValidation(map[string]string{"name": "invalid"}))
		return "", false
	}
	return name, true
}

func (s *Server) createTeam(w http.ResponseWriter, r *http.Request) {
	name, ok := decodeTeamName(w, r)
	if !ok {
		return
	}
	actor := sessionFrom(r.Context()).User
	var team dbq.Team
	err := db.InTx(r.Context(), s.pool, func(q *dbq.Queries) error {
		var err error
		if team, err = q.CreateTeam(r.Context(), name); err != nil {
			return err
		}
		return audit.Write(r.Context(), q, audit.Entry{Actor: actor.ID, IP: clientFrom(r).IP, Action: audit.TeamCreated, TargetType: "team", TargetID: uuidStr(team.ID), Metadata: map[string]any{"name": name}})
	})
	if isUniqueViolation(err) {
		writeError(w, r, errValidation(map[string]string{"name": "taken"}))
		return
	}
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"team": teamJSON{ID: uuidStr(team.ID), Name: team.Name, CreatedAt: team.CreatedAt.Time.UTC()}})
}

func (s *Server) renameTeam(w http.ResponseWriter, r *http.Request) {
	id, ok := parseUUID(chi.URLParam(r, "id"))
	if !ok {
		writeError(w, r, errNotFound)
		return
	}
	name, ok := decodeTeamName(w, r)
	if !ok {
		return
	}
	actor := sessionFrom(r.Context()).User
	var team dbq.Team
	err := db.InTx(r.Context(), s.pool, func(q *dbq.Queries) error {
		var err error
		if team, err = q.RenameTeam(r.Context(), dbq.RenameTeamParams{ID: id, Name: name}); err != nil {
			return err
		}
		return audit.Write(r.Context(), q, audit.Entry{Actor: actor.ID, IP: clientFrom(r).IP, Action: audit.TeamRenamed, TargetType: "team", TargetID: uuidStr(id), Metadata: map[string]any{"name": name}})
	})
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		writeError(w, r, errNotFound)
	case isUniqueViolation(err):
		writeError(w, r, errValidation(map[string]string{"name": "taken"}))
	case err != nil:
		writeError(w, r, err)
	default:
		writeJSON(w, http.StatusOK, map[string]any{"team": teamJSON{ID: uuidStr(team.ID), Name: team.Name, CreatedAt: team.CreatedAt.Time.UTC()}})
	}
}

func (s *Server) deleteTeam(w http.ResponseWriter, r *http.Request) {
	id, ok := parseUUID(chi.URLParam(r, "id"))
	if !ok {
		writeError(w, r, errNotFound)
		return
	}
	actor := sessionFrom(r.Context()).User
	var n int64
	err := db.InTx(r.Context(), s.pool, func(q *dbq.Queries) error {
		var err error
		if n, err = q.DeleteTeam(r.Context(), id); err != nil || n == 0 {
			return err
		}
		if err := audit.Write(r.Context(), q, audit.Entry{Actor: actor.ID, IP: clientFrom(r).IP, Action: audit.TeamDeleted, TargetType: "team", TargetID: uuidStr(id)}); err != nil {
			return err
		}
		return notifyScopeChanged(r.Context(), q)
	})
	switch {
	case err != nil:
		writeError(w, r, err)
	case n == 0:
		writeError(w, r, errNotFound)
	default:
		w.WriteHeader(http.StatusNoContent)
	}
}

func (s *Server) listTeamMembers(w http.ResponseWriter, r *http.Request) {
	id, ok := parseUUID(chi.URLParam(r, "id"))
	if !ok {
		writeError(w, r, errNotFound)
		return
	}
	if _, err := s.q.GetTeam(r.Context(), id); errors.Is(err, pgx.ErrNoRows) {
		writeError(w, r, errNotFound)
		return
	} else if err != nil {
		writeError(w, r, err)
		return
	}
	ids, err := s.q.ListTeamMemberIDs(r.Context(), id)
	if err != nil {
		writeError(w, r, err)
		return
	}
	out := make([]string, len(ids))
	for i, u := range ids {
		out[i] = uuidStr(u)
	}
	writeJSON(w, http.StatusOK, map[string]any{"user_ids": out})
}

func (s *Server) setTeamMembers(w http.ResponseWriter, r *http.Request) {
	id, ok := parseUUID(chi.URLParam(r, "id"))
	if !ok {
		writeError(w, r, errNotFound)
		return
	}
	var req struct {
		UserIDs []string `json:"user_ids"`
	}
	if err := decode(r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	if len(req.UserIDs) > 1000 {
		writeError(w, r, errValidation(map[string]string{"user_ids": "too_many"}))
		return
	}
	seen := map[pgtype.UUID]bool{}
	ids := make([]pgtype.UUID, 0, len(req.UserIDs))
	for _, raw := range req.UserIDs {
		u, ok := parseUUID(raw)
		if !ok {
			writeError(w, r, errValidation(map[string]string{"user_ids": "invalid"}))
			return
		}
		if !seen[u] {
			seen[u] = true
			ids = append(ids, u)
		}
	}
	actor := sessionFrom(r.Context()).User
	err := db.InTx(r.Context(), s.pool, func(q *dbq.Queries) error {
		var before []pgtype.UUID
		// keepScope locks the actor first, then the team: the other order deadlocks with a
		// concurrent request of the same actor.
		err := keepScope(r.Context(), q, actor, func() error {
			if _, err := q.GetTeamForUpdate(r.Context(), id); err != nil {
				return err
			}
			var err error
			if before, err = q.ListTeamMemberIDs(r.Context(), id); err != nil {
				return err
			}
			if err := q.DeleteTeamMembers(r.Context(), id); err != nil {
				return err
			}
			if len(ids) == 0 {
				return nil
			}
			return q.AddTeamMembers(r.Context(), dbq.AddTeamMembersParams{TeamID: id, UserIds: ids})
		})
		if err != nil {
			return err
		}
		added, removed := diffIDs(before, ids)
		meta := map[string]any{"count": len(ids), "added": added, "removed": removed}
		if err := audit.Write(r.Context(), q, audit.Entry{Actor: actor.ID, IP: clientFrom(r).IP, Action: audit.TeamMembersChanged, TargetType: "team", TargetID: uuidStr(id), Metadata: meta}); err != nil {
			return err
		}
		return notifyScopeChanged(r.Context(), q)
	})
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		writeError(w, r, errNotFound)
	case isForeignKeyViolation(err):
		writeError(w, r, errValidation(map[string]string{"user_ids": "unknown_user"}))
	case err != nil:
		writeError(w, r, err)
	default:
		w.WriteHeader(http.StatusNoContent)
	}
}

// diffIDs returns the IDs only in next (added) and only in prev (removed), in input order.
func diffIDs(prev, next []pgtype.UUID) (added, removed []string) {
	inPrev := make(map[pgtype.UUID]bool, len(prev))
	for _, u := range prev {
		inPrev[u] = true
	}
	inNext := make(map[pgtype.UUID]bool, len(next))
	added, removed = []string{}, []string{}
	for _, u := range next {
		inNext[u] = true
		if !inPrev[u] {
			added = append(added, uuidStr(u))
		}
	}
	for _, u := range prev {
		if !inNext[u] {
			removed = append(removed, uuidStr(u))
		}
	}
	return added, removed
}

func (s *Server) getSecuritySettings(w http.ResponseWriter, r *http.Request) {
	set, err := s.securitySettings(r.Context())
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, set)
}

func (s *Server) putSecuritySettings(w http.ResponseWriter, r *http.Request) {
	var req securitySettings
	if err := decode(r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	raw, err := json.Marshal(req)
	if err != nil {
		writeError(w, r, err)
		return
	}
	actor := sessionFrom(r.Context()).User
	err = db.InTx(r.Context(), s.pool, func(q *dbq.Queries) error {
		if err := q.UpsertSetting(r.Context(), dbq.UpsertSettingParams{Key: securitySettingsKey, Value: raw, UpdatedBy: actor.ID}); err != nil {
			return err
		}
		return audit.Write(r.Context(), q, audit.Entry{Actor: actor.ID, IP: clientFrom(r).IP, Action: audit.SecuritySettingsChanged, TargetType: "settings", TargetID: securitySettingsKey, Metadata: map[string]any{"require_mfa": req.RequireMFA}})
	})
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, req)
}
