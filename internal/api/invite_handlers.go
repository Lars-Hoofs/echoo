package api

import (
	"context"
	"errors"
	"fmt"
	"html"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"echoo/internal/auth"
	"echoo/internal/db/dbq"
	"echoo/internal/policy"
	"echoo/internal/sysmail"
)

var (
	errSystemMailUnavailable = &apiError{Status: http.StatusConflict, Code: "system_mail_unavailable", Message: "system mail is not configured"}
	errLinkInvalid           = &apiError{Status: http.StatusNotFound, Code: "link_invalid", Message: "the link is invalid, expired or already used"}
)

const maxInviteTeams = 100

// accountLinkRoutes are the public pages behind emailed links and the reset request.
func (s *Server) accountLinkRoutes(r chi.Router) {
	r.With(limitByIP(s.resetIPLimiter)).Post("/auth/password-reset", s.requestPasswordReset)
	r.With(limitByIP(s.linkLimiter)).Get("/auth/password-reset/{token}", s.peekPasswordReset)
	r.With(limitByIP(s.linkLimiter)).Post("/auth/password-reset/{token}", s.completePasswordReset)
	r.With(limitByIP(s.linkLimiter)).Get("/invitations/{token}", s.peekInvitation)
	r.With(limitByIP(s.linkLimiter)).Post("/invitations/{token}/accept", s.acceptInvitation)
}

func (s *Server) systemMailReady(ctx context.Context) (sysmail.Status, error) {
	if s.sysmail == nil || s.jobs == nil {
		return sysmail.Status{}, nil
	}
	return s.sysmail.Status(ctx)
}

func (s *Server) getSystemMail(w http.ResponseWriter, r *http.Request) {
	st, err := s.systemMailReady(r.Context())
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"available": st.Available, "source": st.Source})
}

func (s *Server) enqueueMail(ctx context.Context, tx pgx.Tx, m sysmail.Message) error {
	return s.sysmail.Enqueue(ctx, tx, s.jobs, m)
}

func (s *Server) createInvitation(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Email        string   `json:"email"`
		Name         string   `json:"name"`
		Role         string   `json:"role"`
		CustomRoleID *string  `json:"custom_role_id"`
		TeamIDs      []string `json:"team_ids"`
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
	teams := make([]pgtype.UUID, 0, len(req.TeamIDs))
	seen := map[pgtype.UUID]bool{}
	for _, raw := range req.TeamIDs {
		id, ok := parseUUID(raw)
		if !ok || len(req.TeamIDs) > maxInviteTeams {
			fields["team_ids"] = "invalid"
			break
		}
		if !seen[id] {
			seen[id] = true
			teams = append(teams, id)
		}
	}
	if len(fields) > 0 {
		writeError(w, r, errValidation(fields))
		return
	}
	if err := choice.authorize(r.Context(), s.q, actor); err != nil {
		writeError(w, r, err)
		return
	}
	// Teams decide which mailboxes the new user reads, so choosing them is managing teams.
	if len(teams) > 0 && !policy.SeesAll(actor) && !policy.Has(actor, policy.TeamsManage) {
		writeError(w, r, errForbidden)
		return
	}
	if st, err := s.systemMailReady(r.Context()); err != nil {
		writeError(w, r, err)
		return
	} else if !st.Available {
		writeError(w, r, errSystemMailUnavailable)
		return
	}

	var user dbq.User
	err := pgx.BeginFunc(r.Context(), s.pool, func(tx pgx.Tx) error {
		var token string
		var err error
		user, token, err = s.auth.InviteUser(r.Context(), tx, actor.ID, auth.Invite{Email: email, Name: name, Role: choice.role, CustomRoleID: choice.customID, TeamIDs: teams}, clientFrom(r))
		if err != nil {
			return err
		}
		return s.enqueueMail(r.Context(), tx, s.invitationMail(user, actor.Name, token))
	})
	switch {
	case isUniqueViolation(err):
		writeError(w, r, errValidation(map[string]string{"email": "taken"}))
	case isForeignKeyViolation(err):
		writeError(w, r, errValidation(map[string]string{"team_ids": "unknown_team"}))
	case err != nil:
		writeError(w, r, err)
	default:
		writeJSON(w, http.StatusCreated, map[string]any{"user": toUserJSON(user)})
	}
}

func (s *Server) invitationMail(user dbq.User, inviter, token string) sysmail.Message {
	link := s.baseURL() + "/uitnodiging/" + token
	text := fmt.Sprintf("Hallo %s,\n\n%s heeft je uitgenodigd voor Echoo, het gedeelde postvak van je team. "+
		"Kies via deze link een wachtwoord om te beginnen:\n\n%s\n\n"+
		"De link is 72 uur geldig en werkt één keer. Verwachtte je deze uitnodiging niet, dan kun je deze e-mail negeren.\n",
		user.Name, inviter, link)
	page := fmt.Sprintf(`<p>Hallo %s,</p><p>%s heeft je uitgenodigd voor Echoo, het gedeelde postvak van je team. `+
		`Kies een wachtwoord om te beginnen.</p><p><a href="%s">Uitnodiging accepteren</a></p>`+
		`<p>De link is 72 uur geldig en werkt één keer. Verwachtte je deze uitnodiging niet, dan kun je deze e-mail negeren.</p>`,
		html.EscapeString(user.Name), html.EscapeString(inviter), html.EscapeString(link))
	return sysmail.Message{To: user.Email, Subject: "Uitnodiging voor Echoo", Text: text, HTML: page}
}

func (s *Server) resendInvitation(w http.ResponseWriter, r *http.Request) {
	target, ok := s.loadManagedUser(w, r)
	if !ok {
		return
	}
	if !target.InvitedAt.Valid {
		writeError(w, r, errLinkInvalid)
		return
	}
	if st, err := s.systemMailReady(r.Context()); err != nil {
		writeError(w, r, err)
		return
	} else if !st.Available {
		writeError(w, r, errSystemMailUnavailable)
		return
	}
	actor := sessionFrom(r.Context()).User
	err := pgx.BeginFunc(r.Context(), s.pool, func(tx pgx.Tx) error {
		token, err := s.auth.ResendInvitation(r.Context(), tx, actor.ID, target, clientFrom(r))
		if err != nil {
			return err
		}
		return s.enqueueMail(r.Context(), tx, s.invitationMail(target, actor.Name, token))
	})
	if err != nil {
		writeError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) revokeInvitation(w http.ResponseWriter, r *http.Request) {
	target, ok := s.loadManagedUser(w, r)
	if !ok {
		return
	}
	err := s.auth.RevokeInvitation(r.Context(), sessionFrom(r.Context()).User.ID, target, clientFrom(r))
	if errors.Is(err, auth.ErrLinkInvalid) {
		writeError(w, r, errLinkInvalid)
		return
	}
	if err != nil {
		writeError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) peekInvitation(w http.ResponseWriter, r *http.Request) {
	info, err := s.auth.PeekInvitation(r.Context(), chi.URLParam(r, "token"))
	s.writeLinkInfo(w, r, info, err)
}

func (s *Server) peekPasswordReset(w http.ResponseWriter, r *http.Request) {
	info, err := s.auth.PeekPasswordReset(r.Context(), chi.URLParam(r, "token"))
	s.writeLinkInfo(w, r, info, err)
}

func (s *Server) writeLinkInfo(w http.ResponseWriter, r *http.Request, info auth.LinkInfo, err error) {
	if errors.Is(err, auth.ErrLinkInvalid) {
		writeError(w, r, errLinkInvalid)
		return
	}
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"email": info.Email, "name": info.Name})
}

func passwordError(err error) *apiError {
	switch {
	case errors.Is(err, auth.ErrPasswordTooShort):
		return errValidation(map[string]string{"password": "too_short"})
	case errors.Is(err, auth.ErrPasswordTooLong):
		return errValidation(map[string]string{"password": "too_long"})
	}
	return nil
}

func (s *Server) acceptInvitation(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name     string `json:"name"`
		Password string `json:"password"`
	}
	if err := decode(r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	name, ok := cleanText(req.Name, 200)
	if !ok {
		writeError(w, r, errValidation(map[string]string{"name": "invalid"}))
		return
	}
	if len(req.Password) > 4*auth.MaxPasswordLength {
		writeError(w, r, errValidation(map[string]string{"password": "too_long"}))
		return
	}
	sess, err := s.auth.AcceptInvitation(r.Context(), chi.URLParam(r, "token"), name, req.Password, clientFrom(r))
	if pe := passwordError(err); pe != nil {
		writeError(w, r, pe)
		return
	}
	if errors.Is(err, auth.ErrLinkInvalid) {
		writeError(w, r, errLinkInvalid)
		return
	}
	if errors.Is(err, auth.ErrSSORequired) {
		writeError(w, r, &apiError{Status: http.StatusConflict, Code: "sso_required", Message: err.Error()})
		return
	}
	if err != nil {
		writeError(w, r, err)
		return
	}
	s.setSessionCookie(w, sess)
	writeJSON(w, http.StatusOK, map[string]any{"csrf_token": sess.CsrfToken})
}

// requestPasswordReset answers 202 whether or not the address belongs to an account, so it
// cannot be used to find out who has one.
func (s *Server) requestPasswordReset(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Email string `json:"email"`
	}
	if err := decode(r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	if st, err := s.systemMailReady(r.Context()); err != nil {
		writeError(w, r, err)
		return
	} else if !st.Available {
		writeError(w, r, errSystemMailUnavailable)
		return
	}
	email := auth.NormalizeEmail(req.Email)
	if validEmail(email) && s.resetAccountLimiter.Allow(email) {
		err := pgx.BeginFunc(r.Context(), s.pool, func(tx pgx.Tx) error {
			user, token, ok, err := s.auth.PasswordResetToken(r.Context(), tx, email, clientFrom(r))
			if err != nil {
				return err
			}
			// Unknown addresses enqueue an empty message that the worker drops, so both
			// answers cost the same queue insert.
			msg := sysmail.Message{}
			if ok {
				msg = s.resetMail(user, token)
			}
			return s.enqueueMail(r.Context(), tx, msg)
		})
		if err != nil {
			writeError(w, r, err)
			return
		}
	}
	writeJSON(w, http.StatusAccepted, map[string]any{})
}

func (s *Server) resetMail(user dbq.User, token string) sysmail.Message {
	link := s.baseURL() + "/wachtwoord-herstellen/" + token
	text := fmt.Sprintf("Hallo %s,\n\nVia deze link kies je een nieuw wachtwoord voor Echoo:\n\n%s\n\n"+
		"De link is 30 minuten geldig en werkt één keer. Alle sessies worden afgemeld zodra je het wachtwoord wijzigt. "+
		"Heb je dit niet aangevraagd, dan hoef je niets te doen.\n", user.Name, link)
	page := fmt.Sprintf(`<p>Hallo %s,</p><p>Kies een nieuw wachtwoord voor Echoo.</p><p><a href="%s">Wachtwoord kiezen</a></p>`+
		`<p>De link is 30 minuten geldig en werkt één keer. Alle sessies worden afgemeld zodra je het wachtwoord wijzigt. `+
		`Heb je dit niet aangevraagd, dan hoef je niets te doen.</p>`, html.EscapeString(user.Name), html.EscapeString(link))
	return sysmail.Message{To: user.Email, Subject: "Wachtwoord herstellen voor Echoo", Text: text, HTML: page}
}

func (s *Server) completePasswordReset(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Password string `json:"password"`
	}
	if err := decode(r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	if len(req.Password) > 4*auth.MaxPasswordLength {
		writeError(w, r, errValidation(map[string]string{"password": "too_long"}))
		return
	}
	err := s.auth.CompletePasswordReset(r.Context(), chi.URLParam(r, "token"), req.Password, clientFrom(r))
	if pe := passwordError(err); pe != nil {
		writeError(w, r, pe)
		return
	}
	if errors.Is(err, auth.ErrLinkInvalid) {
		writeError(w, r, errLinkInvalid)
		return
	}
	if err != nil {
		writeError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
