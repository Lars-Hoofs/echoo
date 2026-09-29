package auth

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"echoo/internal/audit"
	"echoo/internal/db"
	"echoo/internal/db/dbq"
	"echoo/internal/policy"
)

// SSOIdentity is a sign-in the identity provider vouched for. IdPMFA is only set when the
// workspace trusts the provider's second factor and the provider reported one.
type SSOIdentity struct {
	Email, Name string
	IdPMFA      bool
}

// SSOProvision says what happens to a verified identity without an account.
type SSOProvision struct {
	Enabled      bool
	Role         string
	CustomRoleID pgtype.UUID
}

// SSORefused is a sign-in the provider allowed but Echoo does not. Reason is the audited code
// and the one the login page explains.
type SSORefused struct{ Reason string }

func (e *SSORefused) Error() string { return "sso login refused: " + e.Reason }

const maxUserNameRunes = 200

// SSOLogin signs in the account that belongs to a verified identity, creating it when the
// workspace provisions accounts and the domain is allowed. Users with 2FA still get the code
// step unless the identity carries the provider's second factor. Failures are audited.
func (s *Service) SSOLogin(ctx context.Context, id SSOIdentity, domains []string, prov SSOProvision, c Client) (*Session, error) {
	email := NormalizeEmail(id.Email)
	at := strings.LastIndex(email, "@")
	if at < 1 || at == len(email)-1 || len(email) > 254 || strings.Count(email, "@") != 1 || strings.ContainsAny(email, " \t\r\n") {
		return nil, s.SSOFailure(ctx, nil, c, "email_invalid")
	}
	domain := email[at+1:]
	if len(domains) > 0 && !containsFold(domains, domain) {
		return nil, s.SSOFailure(ctx, nil, c, "domain_not_allowed")
	}
	user, err := s.q.GetUserByEmail(ctx, email)
	if errors.Is(err, pgx.ErrNoRows) {
		if !prov.Enabled || len(domains) == 0 {
			return nil, s.SSOFailure(ctx, nil, c, "no_account")
		}
		user, err = s.provisionSSOUser(ctx, email, id.Name, prov, c)
		var refused *refusedError
		if errors.As(err, &refused) {
			return nil, s.SSOFailure(ctx, nil, c, refused.reason)
		}
		if err != nil {
			return nil, err
		}
	} else if err != nil {
		return nil, fmt.Errorf("load user: %w", err)
	}
	if user.DeactivatedAt.Valid {
		return nil, s.SSOFailure(ctx, &user, c, "deactivated")
	}

	var sess *Session
	err = db.InTx(ctx, s.pool, func(q *dbq.Queries) error {
		cur, err := q.GetUserForUpdate(ctx, user.ID)
		if err != nil {
			return err
		}
		if cur.DeactivatedAt.Valid {
			return &refusedError{reason: "deactivated"}
		}
		// The caller decided on the provider's MFA from settings it read earlier; the owner may
		// have withdrawn that trust since, and the settings change revokes sessions that were
		// already marked, so the decision is confirmed here under a lock on the settings row.
		idpMFA := id.IdPMFA
		if idpMFA {
			set, err := q.GetSSOSettingsForShare(ctx)
			if err != nil {
				return fmt.Errorf("load sso settings: %w", err)
			}
			idpMFA = set.TrustIdpMfa
		}
		pending := cur.TotpEnabledAt.Valid && !idpMFA
		if sess, err = s.issue(ctx, q, cur, pending, c); err != nil {
			return err
		}
		if idpMFA {
			if err := q.MarkSessionIdPMFA(ctx, sess.ID); err != nil {
				return err
			}
			sess.IdpMfa = true
		}
		if pending {
			return nil
		}
		return s.completeLogin(ctx, q, cur, c, "sso")
	})
	var refused *refusedError
	if errors.As(err, &refused) {
		return nil, s.SSOFailure(ctx, &user, c, refused.reason)
	}
	return sess, err
}

func containsFold(list []string, s string) bool {
	for _, item := range list {
		if strings.EqualFold(item, s) {
			return true
		}
	}
	return false
}

// provisionSSOUser creates the account of a first sign-in. Its password is random and never
// shown, so the account can only be entered through the provider until the user asks for a
// password reset.
func (s *Service) provisionSSOUser(ctx context.Context, email, name string, prov SSOProvision, c Client) (dbq.User, error) {
	secret, _, err := NewToken()
	if err != nil {
		return dbq.User{}, err
	}
	enc, err := s.hasher.Hash(ctx, secret)
	if err != nil {
		return dbq.User{}, err
	}
	name = strings.TrimSpace(name)
	if name == "" {
		name = email[:strings.Index(email, "@")]
	}
	if utf8.RuneCountInString(name) > maxUserNameRunes {
		name = string([]rune(name)[:maxUserNameRunes])
	}
	var user dbq.User
	err = db.InTx(ctx, s.pool, func(q *dbq.Queries) error {
		// The default role was checked when it was saved, but it may have been edited since;
		// nobody is provisioned into a role that controls access.
		perms := policy.RolePermissions(prov.Role)
		if prov.CustomRoleID.Valid {
			role, err := q.GetCustomRoleForShare(ctx, prov.CustomRoleID)
			if err != nil {
				return fmt.Errorf("load default role: %w", err)
			}
			perms = role.Permissions
		}
		if policy.Privileged(perms) {
			return &refusedError{reason: "role_privileged"}
		}
		var err error
		user, err = q.CreateSSOUser(ctx, dbq.CreateSSOUserParams{
			Email: email, Name: name, Role: prov.Role, CustomRoleID: prov.CustomRoleID, PasswordHash: enc,
		})
		if err != nil {
			return err
		}
		meta := map[string]any{"role": prov.Role, "sso": true}
		if prov.CustomRoleID.Valid {
			meta["custom_role_id"] = uuidString(prov.CustomRoleID)
		}
		return audit.Write(ctx, q, audit.Entry{Actor: user.ID, IP: c.IP, Action: audit.UserCreated, TargetType: "user", TargetID: uuidString(user.ID), Metadata: meta})
	})
	var refused *refusedError
	if errors.As(err, &refused) {
		return dbq.User{}, err
	}
	if err != nil {
		return dbq.User{}, fmt.Errorf("provision sso user: %w", err)
	}
	return user, nil
}

// SSOFailure audits a refused or failed SSO sign-in and returns the SSORefused to report. user
// is nil while the account is not known. The reason is a code, never provider output.
func (s *Service) SSOFailure(ctx context.Context, user *dbq.User, c Client, reason string) error {
	e := audit.Entry{IP: c.IP, Action: audit.LoginFailed, Metadata: map[string]any{"method": "sso", "reason": reason}}
	if user != nil {
		e.Actor, e.TargetType, e.TargetID = user.ID, "user", uuidString(user.ID)
	}
	if err := audit.Write(ctx, s.q, e); err != nil {
		return err
	}
	return &SSORefused{Reason: reason}
}

// ssoRequired reports whether password sign-in is switched off for everybody but the owner.
func (s *Service) ssoRequired(ctx context.Context) (bool, error) {
	set, err := s.q.GetSSOSettings(ctx)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("load sso settings: %w", err)
	}
	return set.Enabled && set.Required, nil
}
