package api

import (
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"echoo/internal/audit"
	"echoo/internal/auth"
	"echoo/internal/db"
	"echoo/internal/db/dbq"
	"echoo/internal/keyring"
	"echoo/internal/policy"
	"echoo/internal/sso"
)

const (
	ssoFlowTTL         = 10 * time.Minute
	maxSSOSecretLength = 2000
	maxSSODomains      = 50
	maxSSOButtonRunes  = 40
	loginPath          = "/inloggen"
)

var ssoDomainPattern = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)+$`)

func (s *Server) ssoRoutes(r chi.Router) {
	r.Get("/settings/sso", s.getSSOSettings)
	r.Put("/settings/sso", s.putSSOSettings)
}

func (s *Server) ssoRedirectURI() string {
	return strings.TrimSuffix(s.cfg.BaseURL.String(), "/") + "/auth/sso/callback"
}

func ssoSecretAAD() []byte { return keyring.AAD("sso_settings", "client_secret_enc", "singleton") }

// ssoAvailable is true when the settings are complete enough to start a sign-in.
func ssoAvailable(set dbq.SsoSetting) bool {
	return set.Enabled && set.IssuerUrl != "" && set.ClientID != "" && len(set.ClientSecretEnc) > 0
}

// loadSSO returns the stored settings, or an empty disabled row when none were saved yet.
func (s *Server) loadSSO(r *http.Request) (dbq.SsoSetting, error) {
	set, err := s.q.GetSSOSettings(r.Context())
	if errors.Is(err, pgx.ErrNoRows) {
		return dbq.SsoSetting{ButtonLabel: "SSO", DefaultRole: "agent", AllowedDomains: []string{}}, nil
	}
	return set, err
}

func (s *Server) ssoConfig(set dbq.SsoSetting) (sso.Config, error) {
	if s.keys == nil {
		return sso.Config{}, errors.New("no keyring configured for the SSO client secret")
	}
	secret, err := s.keys.Decrypt(set.ClientSecretEnc, ssoSecretAAD())
	if err != nil {
		return sso.Config{}, err
	}
	return sso.Config{
		IssuerURL: set.IssuerUrl, ClientID: set.ClientID, ClientSecret: string(secret),
		AllowInternalIssuer: set.AllowInternalIssuer, TrustMissingEmailVerified: set.TrustMissingEmailVerified,
	}, nil
}

// ssoInfo tells the login page whether to show the SSO button and hide the password form.
func (s *Server) ssoInfo(w http.ResponseWriter, r *http.Request) {
	set, err := s.loadSSO(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	if !ssoAvailable(set) {
		writeJSON(w, http.StatusOK, map[string]any{"enabled": false, "label": "", "required": false})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"enabled": true, "label": set.ButtonLabel, "required": set.Required})
}

type ssoSettingsJSON struct {
	Enabled                   bool     `json:"enabled"`
	IssuerURL                 string   `json:"issuer_url"`
	ClientID                  string   `json:"client_id"`
	ClientSecretSet           bool     `json:"client_secret_set"`
	AllowedDomains            []string `json:"allowed_domains"`
	ButtonLabel               string   `json:"button_label"`
	Required                  bool     `json:"required"`
	AutoProvision             bool     `json:"auto_provision"`
	DefaultRole               string   `json:"default_role"`
	DefaultCustomRoleID       *string  `json:"default_custom_role_id"`
	TrustIdpMFA               bool     `json:"trust_idp_mfa"`
	TrustMissingEmailVerified bool     `json:"trust_missing_email_verified"`
	AllowInternalIssuer       bool     `json:"allow_internal_issuer"`
	RedirectURI               string   `json:"redirect_uri"`
}

func (s *Server) toSSOSettingsJSON(set dbq.SsoSetting) ssoSettingsJSON {
	out := ssoSettingsJSON{
		Enabled: set.Enabled, IssuerURL: set.IssuerUrl, ClientID: set.ClientID, ClientSecretSet: len(set.ClientSecretEnc) > 0,
		AllowedDomains: set.AllowedDomains, ButtonLabel: set.ButtonLabel, Required: set.Required, AutoProvision: set.AutoProvision,
		DefaultRole: set.DefaultRole, TrustIdpMFA: set.TrustIdpMfa, TrustMissingEmailVerified: set.TrustMissingEmailVerified,
		AllowInternalIssuer: set.AllowInternalIssuer, RedirectURI: s.ssoRedirectURI(),
	}
	if set.DefaultCustomRoleID.Valid {
		id := uuidStr(set.DefaultCustomRoleID)
		out.DefaultCustomRoleID = &id
	}
	return out
}

func (s *Server) getSSOSettings(w http.ResponseWriter, r *http.Request) {
	set, err := s.loadSSO(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, s.toSSOSettingsJSON(set))
}

type ssoSettingsRequest struct {
	Enabled                   bool     `json:"enabled"`
	IssuerURL                 string   `json:"issuer_url"`
	ClientID                  string   `json:"client_id"`
	ClientSecret              string   `json:"client_secret"`
	AllowedDomains            []string `json:"allowed_domains"`
	ButtonLabel               string   `json:"button_label"`
	Required                  bool     `json:"required"`
	AutoProvision             bool     `json:"auto_provision"`
	DefaultRole               string   `json:"default_role"`
	DefaultCustomRoleID       *string  `json:"default_custom_role_id"`
	TrustIdpMFA               bool     `json:"trust_idp_mfa"`
	TrustMissingEmailVerified bool     `json:"trust_missing_email_verified"`
	AllowInternalIssuer       bool     `json:"allow_internal_issuer"`
}

func cleanSSODomains(in []string, fields map[string]string) []string {
	out := []string{}
	if len(in) > maxSSODomains {
		fields["allowed_domains"] = "too_many"
		return out
	}
	for _, d := range in {
		d = strings.ToLower(strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(d), "@")))
		if !ssoDomainPattern.MatchString(d) || len(d) > 253 {
			fields["allowed_domains"] = "invalid"
			return out
		}
		if !slices.Contains(out, d) {
			out = append(out, d)
		}
	}
	return out
}

func (s *Server) putSSOSettings(w http.ResponseWriter, r *http.Request) {
	var req ssoSettingsRequest
	if err := decode(r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	cur, err := s.loadSSO(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	fields := map[string]string{}
	req.IssuerURL = strings.TrimSpace(req.IssuerURL)
	req.ClientID = strings.TrimSpace(req.ClientID)
	if req.IssuerURL != "" && !sso.ValidIssuerURL(req.IssuerURL, req.AllowInternalIssuer) {
		fields["issuer_url"] = "invalid"
	}
	if len(req.ClientID) > 500 {
		fields["client_id"] = "invalid"
	}
	if len(req.ClientSecret) > maxSSOSecretLength {
		fields["client_secret"] = "invalid"
	}
	label, ok := cleanText(req.ButtonLabel, maxSSOButtonRunes)
	if !ok {
		fields["button_label"] = "invalid"
	}
	domains := cleanSSODomains(req.AllowedDomains, fields)
	var customRole pgtype.UUID
	switch {
	case req.DefaultCustomRoleID != nil && req.DefaultRole != "":
		fields["default_role"] = "ambiguous"
	case req.DefaultCustomRoleID != nil:
		id, ok := parseUUID(*req.DefaultCustomRoleID)
		if !ok {
			fields["default_custom_role_id"] = "invalid"
		}
		customRole = id
		req.DefaultRole = policy.RoleCustom
	case req.DefaultRole != policy.RoleAgent && req.DefaultRole != policy.RoleReadonly:
		fields["default_role"] = "invalid"
	}
	hasSecret := len(cur.ClientSecretEnc) > 0 || req.ClientSecret != ""
	if req.Enabled {
		if req.IssuerURL == "" {
			fields["issuer_url"] = "required"
		}
		if req.ClientID == "" {
			fields["client_id"] = "required"
		}
		if !hasSecret {
			fields["client_secret"] = "required"
		}
	}
	if req.Required && !req.Enabled {
		fields["required"] = "needs_enabled"
	}
	if req.AutoProvision && len(domains) == 0 && fields["allowed_domains"] == "" {
		fields["allowed_domains"] = "required_for_provisioning"
	}
	if len(fields) > 0 {
		writeError(w, r, errValidation(fields))
		return
	}

	var secret []byte
	if req.ClientSecret != "" {
		if s.keys == nil {
			writeError(w, r, errors.New("no keyring configured for the SSO client secret"))
			return
		}
		if secret, err = s.keys.Encrypt([]byte(req.ClientSecret), ssoSecretAAD()); err != nil {
			writeError(w, r, err)
			return
		}
	}
	if req.Enabled {
		if err := s.sso.Discover(r.Context(), sso.Config{IssuerURL: req.IssuerURL, AllowInternalIssuer: req.AllowInternalIssuer}); err != nil {
			sso.LogFailure(r.Context(), err)
			code := "unreachable"
			if e := (*sso.Error)(nil); errors.As(err, &e) && e.Reason == sso.ReasonInsecure {
				code = "insecure"
			}
			writeError(w, r, errValidation(map[string]string{"issuer_url": code}))
			return
		}
	}

	actor := sessionFrom(r.Context()).User
	var saved dbq.SsoSetting
	err = db.InTx(r.Context(), s.pool, func(q *dbq.Queries) error {
		if customRole.Valid {
			role, err := q.GetCustomRoleForShare(r.Context(), customRole)
			if errors.Is(err, pgx.ErrNoRows) {
				return errValidation(map[string]string{"default_custom_role_id": "unknown"})
			}
			if err != nil {
				return err
			}
			// Everybody from the allowed domains gets this role at their first sign-in.
			if policy.Privileged(role.Permissions) {
				return errValidation(map[string]string{"default_custom_role_id": "privileged"})
			}
		}
		if saved, err = q.UpsertSSOSettings(r.Context(), dbq.UpsertSSOSettingsParams{
			Enabled: req.Enabled, IssuerUrl: req.IssuerURL, ClientID: req.ClientID, ClientSecretEnc: secret,
			AllowedDomains: domains, ButtonLabel: label, Required: req.Required, AutoProvision: req.AutoProvision,
			DefaultRole: req.DefaultRole, DefaultCustomRoleID: customRole, TrustIdpMfa: req.TrustIdpMFA,
			TrustMissingEmailVerified: req.TrustMissingEmailVerified, AllowInternalIssuer: req.AllowInternalIssuer,
			UpdatedBy: actor.ID,
		}); err != nil {
			return err
		}
		// Sessions that skipped the second factor because the IdP vouched for it must not
		// outlive that trust.
		var revoked int64
		if cur.TrustIdpMfa && !saved.TrustIdpMfa {
			if revoked, err = q.RevokeIdPMFASessions(r.Context()); err != nil {
				return err
			}
		}
		return audit.Write(r.Context(), q, audit.Entry{
			Actor: actor.ID, IP: clientFrom(r).IP, Action: audit.SSOSettingsChanged, TargetType: "settings", TargetID: "sso",
			Metadata: map[string]any{
				"enabled": saved.Enabled, "required": saved.Required, "auto_provision": saved.AutoProvision,
				"issuer_url": saved.IssuerUrl, "secret_changed": secret != nil, "idp_mfa_sessions_revoked": revoked,
			},
		})
	})
	if err != nil {
		writeError(w, r, err)
		return
	}
	s.sso.Forget()
	writeJSON(w, http.StatusOK, s.toSSOSettingsJSON(saved))
}

func (s *Server) ssoCookieName() string {
	if s.cfg.SecureCookies() {
		return "__Host-echoo_sso"
	}
	return "echoo_sso"
}

type ssoFlowCookie struct {
	sso.Flow
	Expires int64 `json:"exp"`
}

func ssoFlowAAD() []byte { return keyring.AAD("sso", "flow", "cookie") }

// setFlowCookie keeps the flow values in the browser that started the sign-in, encrypted and
// tamper-proof. SameSite=Lax lets it come back with the provider's redirect, which is a
// top-level navigation, and keeps it away from cross-site subrequests.
func (s *Server) setFlowCookie(w http.ResponseWriter, flow sso.Flow) error {
	if s.keys == nil {
		return errors.New("no keyring configured for SSO")
	}
	raw, err := json.Marshal(ssoFlowCookie{Flow: flow, Expires: time.Now().Add(ssoFlowTTL).Unix()})
	if err != nil {
		return err
	}
	sealed, err := s.keys.Encrypt(raw, ssoFlowAAD())
	if err != nil {
		return err
	}
	http.SetCookie(w, &http.Cookie{ //nolint:gosec // Secure is only false for http://localhost development (enforced by config)
		Name: s.ssoCookieName(), Value: base64.RawURLEncoding.EncodeToString(sealed), Path: "/",
		MaxAge: int(ssoFlowTTL / time.Second), HttpOnly: true, Secure: s.cfg.SecureCookies(), SameSite: http.SameSiteLaxMode,
	})
	return nil
}

// takeFlowCookie reads the flow cookie and deletes it, so a flow can be finished only once.
func (s *Server) takeFlowCookie(w http.ResponseWriter, r *http.Request) (sso.Flow, bool) {
	c, err := r.Cookie(s.ssoCookieName())
	if err != nil || s.keys == nil {
		return sso.Flow{}, false
	}
	http.SetCookie(w, &http.Cookie{ //nolint:gosec // same attributes as setFlowCookie
		Name: s.ssoCookieName(), Value: "", Path: "/", MaxAge: -1,
		HttpOnly: true, Secure: s.cfg.SecureCookies(), SameSite: http.SameSiteLaxMode,
	})
	sealed, err := base64.RawURLEncoding.DecodeString(c.Value)
	if err != nil {
		return sso.Flow{}, false
	}
	raw, err := s.keys.Decrypt(sealed, ssoFlowAAD())
	if err != nil {
		return sso.Flow{}, false
	}
	var fc ssoFlowCookie
	if err := json.Unmarshal(raw, &fc); err != nil || time.Now().Unix() > fc.Expires || fc.State == "" || fc.Nonce == "" || fc.Verifier == "" {
		return sso.Flow{}, false
	}
	return fc.Flow, true
}

func loginError(w http.ResponseWriter, r *http.Request, reason string) {
	http.Redirect(w, r, loginPath+"?"+url.Values{"sso_error": {reason}}.Encode(), http.StatusSeeOther)
}

// ssoFail audits the failure and sends the browser back to the login page with the reason.
func (s *Server) ssoFail(w http.ResponseWriter, r *http.Request, reason string) {
	var refused *auth.SSORefused
	if err := s.auth.SSOFailure(r.Context(), nil, clientFrom(r), reason); !errors.As(err, &refused) {
		slog.ErrorContext(r.Context(), "audit sso failure", "error", err)
	}
	loginError(w, r, reason)
}

func (s *Server) ssoStart(w http.ResponseWriter, r *http.Request) {
	set, err := s.loadSSO(r)
	if err != nil {
		slog.ErrorContext(r.Context(), "load sso settings", "error", err)
		loginError(w, r, "server_error")
		return
	}
	if !ssoAvailable(set) {
		loginError(w, r, "not_configured")
		return
	}
	cfg, err := s.ssoConfig(set)
	if err != nil {
		slog.ErrorContext(r.Context(), "sso configuration", "error", err)
		loginError(w, r, "server_error")
		return
	}
	authURL, flow, err := s.sso.Begin(r.Context(), cfg)
	if err != nil {
		sso.LogFailure(r.Context(), err)
		s.ssoFail(w, r, ssoReason(err))
		return
	}
	if err := s.setFlowCookie(w, flow); err != nil {
		slog.ErrorContext(r.Context(), "sso flow cookie", "error", err)
		loginError(w, r, "server_error")
		return
	}
	// The URL comes from the discovery document of the issuer only the owner can configure.
	http.Redirect(w, r, authURL, http.StatusFound) //nolint:gosec // see above
}

func ssoReason(err error) string {
	var e *sso.Error
	if errors.As(err, &e) {
		return e.Reason
	}
	return "server_error"
}

func (s *Server) ssoCallback(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	flow, ok := s.takeFlowCookie(w, r)
	query := r.URL.Query()
	if !ok || subtle.ConstantTimeCompare([]byte(query.Get("state")), []byte(flow.State)) != 1 {
		s.ssoFail(w, r, "invalid_state")
		return
	}
	if query.Get("error") != "" {
		// The provider's error text is not stored; the code only says the provider refused.
		s.ssoFail(w, r, "idp_error")
		return
	}
	set, err := s.loadSSO(r)
	if err != nil {
		slog.ErrorContext(ctx, "load sso settings", "error", err)
		loginError(w, r, "server_error")
		return
	}
	if !ssoAvailable(set) {
		s.ssoFail(w, r, "not_configured")
		return
	}
	cfg, err := s.ssoConfig(set)
	if err != nil {
		slog.ErrorContext(ctx, "sso configuration", "error", err)
		loginError(w, r, "server_error")
		return
	}
	code := query.Get("code")
	if code == "" {
		s.ssoFail(w, r, "invalid_request")
		return
	}
	id, err := s.sso.Complete(ctx, cfg, flow, code)
	if err != nil {
		sso.LogFailure(ctx, err)
		s.ssoFail(w, r, ssoReason(err))
		return
	}

	prov := auth.SSOProvision{Enabled: set.AutoProvision, Role: set.DefaultRole, CustomRoleID: set.DefaultCustomRoleID}
	sess, err := s.auth.SSOLogin(ctx, auth.SSOIdentity{Email: id.Email, Name: id.Name, IdPMFA: set.TrustIdpMfa && id.MFA}, set.AllowedDomains, prov, clientFrom(r))
	var refused *auth.SSORefused
	if errors.As(err, &refused) {
		loginError(w, r, refused.Reason)
		return
	}
	if err != nil {
		slog.ErrorContext(ctx, "sso login", "error", err)
		loginError(w, r, "server_error")
		return
	}
	s.setSessionCookie(w, sess)
	if sess.MfaPending {
		http.Redirect(w, r, loginPath+"?mfa=1", http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}
