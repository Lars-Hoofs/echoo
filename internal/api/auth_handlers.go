package api

import (
	"errors"
	"net/http"

	"echoo/internal/auth"
)

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if err := decode(r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	if req.Email == "" || req.Password == "" || len(req.Password) > 4*auth.MaxPasswordLength {
		writeError(w, r, errInvalidCredentials)
		return
	}
	sess, err := s.auth.Login(r.Context(), req.Email, req.Password, clientFrom(r))
	if errors.Is(err, auth.ErrInvalidCredentials) {
		writeError(w, r, errInvalidCredentials)
		return
	}
	if err != nil {
		writeError(w, r, err)
		return
	}
	s.setSessionCookie(w, sess)
	writeJSON(w, http.StatusOK, map[string]any{"mfa_required": sess.MfaPending, "csrf_token": sess.CsrfToken})
}

var (
	errInvalidCredentials = &apiError{Status: http.StatusUnauthorized, Code: "invalid_credentials", Message: "email or password is incorrect, or the account is temporarily locked"}
	errInvalidCode        = &apiError{Status: http.StatusUnauthorized, Code: "invalid_code", Message: "the code is incorrect or was already used"}
)

func (s *Server) verifyMFA(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Code string `json:"code"`
	}
	if err := decode(r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	sess, err := s.auth.VerifyMFA(r.Context(), sessionFrom(r.Context()), req.Code, clientFrom(r))
	switch {
	case errors.Is(err, auth.ErrInvalidCode), errors.Is(err, auth.ErrInvalidCredentials), errors.Is(err, auth.ErrMFANotStarted):
		writeError(w, r, errInvalidCode)
		return
	case errors.Is(err, auth.ErrMFANotPending):
		writeError(w, r, &apiError{Status: http.StatusConflict, Code: "mfa_not_pending", Message: "session is already fully signed in"})
		return
	case err != nil:
		writeError(w, r, err)
		return
	}
	s.setSessionCookie(w, sess)
	writeJSON(w, http.StatusOK, map[string]any{"csrf_token": sess.CsrfToken})
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	if err := s.auth.Logout(r.Context(), sessionFrom(r.Context()), clientFrom(r)); err != nil {
		writeError(w, r, err)
		return
	}
	s.clearSessionCookie(w)
	w.WriteHeader(http.StatusNoContent)
}
