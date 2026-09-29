package api

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"echoo/internal/audit"
	"echoo/internal/db"
	"echoo/internal/db/dbq"
	"echoo/internal/policy"
)

const (
	maxRoleNameRunes        = 60
	maxRoleDescriptionRunes = 300
)

var errRoleInUse = &apiError{Status: http.StatusConflict, Code: "role_in_use", Message: "the role is still assigned to users or used as the SSO default role"}

func (s *Server) roleRoutes(r chi.Router) {
	r.Get("/roles", s.listRoles)
	r.Post("/roles", s.createRole)
	r.Patch("/roles/{id}", s.updateRole)
	r.Delete("/roles/{id}", s.deleteRole)
}

type builtinRoleJSON struct {
	ID          string   `json:"id"`
	Permissions []string `json:"permissions"`
}

type customRoleJSON struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	Permissions []string  `json:"permissions"`
	MemberCount int32     `json:"member_count"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

func toCustomRoleJSON(r dbq.CustomRole, members int32) customRoleJSON {
	return customRoleJSON{
		ID: uuidStr(r.ID), Name: r.Name, Description: r.Description, Permissions: canonicalOrder(r.Permissions),
		MemberCount: members, CreatedAt: r.CreatedAt.Time.UTC(), UpdatedAt: r.UpdatedAt.Time.UTC(),
	}
}

// canonicalOrder returns set sorted the way the permission list is defined, so the editor and
// the audit log show the same order every time.
func canonicalOrder(set []string) []string {
	out := []string{}
	for _, p := range policy.Permissions() {
		if slices.Contains(set, string(p)) {
			out = append(out, string(p))
		}
	}
	return out
}

func (s *Server) listRoles(w http.ResponseWriter, r *http.Request) {
	rows, err := s.q.ListCustomRoles(r.Context())
	if err != nil {
		writeError(w, r, err)
		return
	}
	custom := make([]customRoleJSON, len(rows))
	for i, row := range rows {
		custom[i] = toCustomRoleJSON(dbq.CustomRole{
			ID: row.ID, Name: row.Name, Description: row.Description, Permissions: row.Permissions,
			CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
		}, row.MemberCount)
	}
	builtin := []builtinRoleJSON{}
	for _, role := range []string{policy.RoleOwner, policy.RoleAdmin, policy.RoleAgent, policy.RoleReadonly} {
		builtin = append(builtin, builtinRoleJSON{ID: role, Permissions: policy.RolePermissions(role)})
	}
	keys := []string{}
	for _, p := range policy.Permissions() {
		keys = append(keys, string(p))
	}
	writeJSON(w, http.StatusOK, map[string]any{"permissions": keys, "builtin": builtin, "roles": custom})
}

type roleRequest struct {
	Name        *string   `json:"name"`
	Description *string   `json:"description"`
	Permissions *[]string `json:"permissions"`
}

// validPermissions checks a submitted list against the closed permission list and its
// requirements, and returns it without duplicates.
func validPermissions(in []string, fields map[string]string) []string {
	out := []string{}
	for _, p := range in {
		if !policy.ValidPermission(p) {
			fields["permissions"] = "unknown"
			return nil
		}
		if !slices.Contains(out, p) {
			out = append(out, p)
		}
	}
	if len(out) == 0 {
		fields["permissions"] = "required"
		return nil
	}
	if p, needs := policy.MissingRequirement(out); p != "" {
		fields["permissions"] = "requires_" + string(needs)
		return nil
	}
	return canonicalOrder(out)
}

func roleAudit(r *http.Request, actor dbq.User, action string, id pgtype.UUID, meta map[string]any) audit.Entry {
	return audit.Entry{Actor: actor.ID, IP: clientFrom(r).IP, Action: action, TargetType: "role", TargetID: uuidStr(id), Metadata: meta}
}

func (s *Server) createRole(w http.ResponseWriter, r *http.Request) {
	var req roleRequest
	if err := decode(r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	fields := map[string]string{}
	var name, description string
	if req.Name != nil {
		var ok bool
		if name, ok = cleanText(*req.Name, maxRoleNameRunes); !ok {
			fields["name"] = "invalid"
		}
	} else {
		fields["name"] = "required"
	}
	if req.Description != nil {
		if len([]rune(*req.Description)) > maxRoleDescriptionRunes {
			fields["description"] = "invalid"
		}
		description = *req.Description
	}
	var perms []string
	if req.Permissions != nil {
		perms = validPermissions(*req.Permissions, fields)
	} else {
		fields["permissions"] = "required"
	}
	if len(fields) > 0 {
		writeError(w, r, errValidation(fields))
		return
	}
	actor := sessionFrom(r.Context()).User
	if !policy.CanGrant(actor, perms) {
		writeError(w, r, errForbidden)
		return
	}
	var role dbq.CustomRole
	err := db.InTx(r.Context(), s.pool, func(q *dbq.Queries) error {
		var err error
		if role, err = q.CreateCustomRole(r.Context(), dbq.CreateCustomRoleParams{Name: name, Description: description, Permissions: perms}); err != nil {
			return err
		}
		return audit.Write(r.Context(), q, roleAudit(r, actor, audit.RoleCreated, role.ID, map[string]any{"name": role.Name, "permissions": perms}))
	})
	if isUniqueViolation(err) {
		writeError(w, r, errValidation(map[string]string{"name": "taken"}))
		return
	}
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"role": toCustomRoleJSON(role, 0)})
}

func (s *Server) updateRole(w http.ResponseWriter, r *http.Request) {
	id, ok := parseUUID(chi.URLParam(r, "id"))
	if !ok {
		writeError(w, r, errNotFound)
		return
	}
	var req roleRequest
	if err := decode(r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	fields := map[string]string{}
	var name, description *string
	if req.Name != nil {
		v, ok := cleanText(*req.Name, maxRoleNameRunes)
		if !ok {
			fields["name"] = "invalid"
		}
		name = &v
	}
	if req.Description != nil {
		if len([]rune(*req.Description)) > maxRoleDescriptionRunes {
			fields["description"] = "invalid"
		}
		description = req.Description
	}
	var perms []string
	if req.Permissions != nil {
		perms = validPermissions(*req.Permissions, fields)
	}
	if len(fields) > 0 {
		writeError(w, r, errValidation(fields))
		return
	}
	actor := sessionFrom(r.Context()).User
	var role dbq.CustomRole
	var members int32
	err := db.InTx(r.Context(), s.pool, func(q *dbq.Queries) error {
		cur, err := q.GetCustomRoleForUpdate(r.Context(), id)
		if errors.Is(err, pgx.ErrNoRows) {
			return errNotFound
		}
		if err != nil {
			return err
		}
		next := dbq.UpdateCustomRoleParams{ID: id, Name: cur.Name, Description: cur.Description, Permissions: cur.Permissions}
		if name != nil {
			next.Name = *name
		}
		if description != nil {
			next.Description = *description
		}
		if req.Permissions != nil {
			next.Permissions = perms
		}
		if !policy.CanGrant(actor, cur.Permissions) || !policy.CanGrant(actor, next.Permissions) {
			return errForbidden
		}
		if role, err = q.UpdateCustomRole(r.Context(), next); err != nil {
			return err
		}
		if err := q.SyncCustomRoleMembers(r.Context(), dbq.SyncCustomRoleMembersParams{CustomRoleID: id, Permissions: role.Permissions}); err != nil {
			return err
		}
		added, removed := diff(role.Permissions, cur.Permissions), diff(cur.Permissions, role.Permissions)
		meta := map[string]any{"name": role.Name, "added": added, "removed": removed}
		if err := audit.Write(r.Context(), q, roleAudit(r, actor, audit.RoleUpdated, id, meta)); err != nil {
			return err
		}
		if len(added)+len(removed) > 0 {
			if err := notifyScopeChanged(r.Context(), q); err != nil {
				return err
			}
		}
		rows, err := q.ListCustomRoles(r.Context())
		if err != nil {
			return err
		}
		for _, row := range rows {
			if row.ID == id {
				members = row.MemberCount
			}
		}
		return nil
	})
	if isUniqueViolation(err) {
		writeError(w, r, errValidation(map[string]string{"name": "taken"}))
		return
	}
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"role": toCustomRoleJSON(role, members)})
}

// diff returns what is in a and not in b, in canonical order.
func diff(a, b []string) []string {
	return canonicalOrder(slices.DeleteFunc(slices.Clone(a), func(p string) bool { return slices.Contains(b, p) }))
}

func (s *Server) deleteRole(w http.ResponseWriter, r *http.Request) {
	id, ok := parseUUID(chi.URLParam(r, "id"))
	if !ok {
		writeError(w, r, errNotFound)
		return
	}
	actor := sessionFrom(r.Context()).User
	err := db.InTx(r.Context(), s.pool, func(q *dbq.Queries) error {
		cur, err := q.GetCustomRoleForUpdate(r.Context(), id)
		if errors.Is(err, pgx.ErrNoRows) {
			return errNotFound
		}
		if err != nil {
			return err
		}
		if !policy.CanGrant(actor, cur.Permissions) {
			return errForbidden
		}
		if _, err := q.DeleteCustomRole(r.Context(), id); err != nil {
			return err
		}
		return audit.Write(r.Context(), q, roleAudit(r, actor, audit.RoleDeleted, id, map[string]any{"name": cur.Name}))
	})
	if isRestrictViolation(err) {
		writeError(w, r, errRoleInUse)
		return
	}
	if err != nil {
		writeError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// isRestrictViolation matches a delete that a foreign key with ON DELETE RESTRICT refused.
func isRestrictViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23001"
}

// roleChoice is the role a request asks for: a built-in one by name or a custom one by id.
type roleChoice struct {
	role     string
	customID pgtype.UUID
}

// parseRoleChoice reads the role and custom_role_id fields of a request; exactly one is set.
func parseRoleChoice(role string, customRoleID *string, fields map[string]string) (roleChoice, bool) {
	switch {
	case role != "" && customRoleID != nil:
		fields["role"] = "ambiguous"
	case customRoleID != nil:
		id, ok := parseUUID(*customRoleID)
		if !ok {
			fields["custom_role_id"] = "invalid"
			return roleChoice{}, false
		}
		return roleChoice{role: policy.RoleCustom, customID: id}, true
	case policy.ValidRole(role):
		return roleChoice{role: role}, true
	default:
		fields["role"] = "invalid"
	}
	return roleChoice{}, false
}

// authorize checks that actor may hand out the chosen role. An unknown custom role is a
// validation error, so the caller can tell it from a missing right.
func (c roleChoice) authorize(ctx context.Context, q *dbq.Queries, actor dbq.User) error {
	if c.role != policy.RoleCustom {
		if !policy.CanAssignRole(actor, c.role) {
			return errForbidden
		}
		return nil
	}
	role, err := q.GetCustomRoleForShare(ctx, c.customID)
	if errors.Is(err, pgx.ErrNoRows) {
		return errValidation(map[string]string{"custom_role_id": "unknown"})
	}
	if err != nil {
		return err
	}
	if !policy.CanGrant(actor, role.Permissions) {
		return errForbidden
	}
	return nil
}
