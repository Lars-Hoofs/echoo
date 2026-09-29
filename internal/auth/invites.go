package auth

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"echoo/internal/audit"
	"echoo/internal/db"
	"echoo/internal/db/dbq"
)

const (
	InvitationTTL = 72 * time.Hour
	ResetTTL      = 30 * time.Minute

	purposeInvitation = "invitation"
	purposeReset      = "password_reset"
)

// ErrLinkInvalid covers every way a link can fail: unknown, used, expired or revoked.
var ErrLinkInvalid = errors.New("link is invalid or expired")

// Invite describes a user to invite; the role check is the caller's job.
type Invite struct {
	Email, Name, Role string
	// CustomRoleID replaces Role when the invited user gets a custom role.
	CustomRoleID pgtype.UUID
	TeamIDs      []pgtype.UUID
}

// InviteUser creates the invited user, their team memberships and a single-use token in tx.
// The account cannot sign in until the invitation is accepted. The returned token goes into
// the emailed link and is not stored anywhere else.
func (s *Service) InviteUser(ctx context.Context, tx pgx.Tx, actor pgtype.UUID, in Invite, c Client) (dbq.User, string, error) {
	// The account gets a random password nobody knows, so its hash is valid but unusable.
	secret, _, err := NewToken()
	if err != nil {
		return dbq.User{}, "", err
	}
	enc, err := s.hasher.Hash(ctx, secret)
	if err != nil {
		return dbq.User{}, "", err
	}
	q := dbq.New(tx)
	role := in.Role
	if in.CustomRoleID.Valid {
		role = "agent"
	}
	user, err := q.CreateInvitedUser(ctx, dbq.CreateInvitedUserParams{
		Email: NormalizeEmail(in.Email), Name: strings.TrimSpace(in.Name), Role: role, PasswordHash: enc,
	})
	if err != nil {
		return dbq.User{}, "", err
	}
	if in.CustomRoleID.Valid {
		if user, err = q.SetUserCustomRole(ctx, dbq.SetUserCustomRoleParams{ID: user.ID, RoleID: in.CustomRoleID}); err != nil {
			return dbq.User{}, "", err
		}
		in.Role = "custom"
	}
	for _, team := range in.TeamIDs {
		if err := q.AddTeamMembers(ctx, dbq.AddTeamMembersParams{TeamID: team, UserIds: []pgtype.UUID{user.ID}}); err != nil {
			return dbq.User{}, "", err
		}
	}
	token, err := s.newAccountToken(ctx, q, user.ID, purposeInvitation, actor, InvitationTTL)
	if err != nil {
		return dbq.User{}, "", err
	}
	err = audit.Write(ctx, q, audit.Entry{Actor: actor, IP: c.IP, Action: audit.UserInvited, TargetType: "user", TargetID: uuidString(user.ID),
		Metadata: map[string]any{"role": in.Role, "teams": len(in.TeamIDs)}})
	return user, token, err
}

// ResendInvitation replaces the open token of a pending invitation with a fresh one.
func (s *Service) ResendInvitation(ctx context.Context, tx pgx.Tx, actor pgtype.UUID, user dbq.User, c Client) (string, error) {
	if !user.InvitedAt.Valid {
		return "", ErrLinkInvalid
	}
	q := dbq.New(tx)
	if err := q.InvalidateAccountTokens(ctx, dbq.InvalidateAccountTokensParams{UserID: user.ID, Purpose: purposeInvitation}); err != nil {
		return "", err
	}
	token, err := s.newAccountToken(ctx, q, user.ID, purposeInvitation, actor, InvitationTTL)
	if err != nil {
		return "", err
	}
	err = audit.Write(ctx, q, audit.Entry{Actor: actor, IP: c.IP, Action: audit.InvitationResent, TargetType: "user", TargetID: uuidString(user.ID)})
	return token, err
}

// RevokeInvitation deletes a user who has not accepted yet, which also voids the link.
func (s *Service) RevokeInvitation(ctx context.Context, actor pgtype.UUID, user dbq.User, c Client) error {
	return db.InTx(ctx, s.pool, func(q *dbq.Queries) error {
		n, err := q.DeleteInvitedUser(ctx, user.ID)
		if err != nil {
			return err
		}
		if n == 0 {
			return ErrLinkInvalid
		}
		return audit.Write(ctx, q, audit.Entry{Actor: actor, IP: c.IP, Action: audit.InvitationRevoked, TargetType: "user", TargetID: uuidString(user.ID)})
	})
}

// LinkInfo is what the page behind a link may show before the token is used.
type LinkInfo struct{ Email, Name string }

func (s *Service) peek(ctx context.Context, token, purpose string) (LinkInfo, error) {
	row, err := s.q.PeekAccountToken(ctx, dbq.PeekAccountTokenParams{TokenHash: HashToken(token), Purpose: purpose})
	if errors.Is(err, pgx.ErrNoRows) {
		return LinkInfo{}, ErrLinkInvalid
	}
	if err != nil {
		return LinkInfo{}, fmt.Errorf("load link: %w", err)
	}
	return LinkInfo{Email: row.Email, Name: row.Name}, nil
}

func (s *Service) PeekInvitation(ctx context.Context, token string) (LinkInfo, error) {
	return s.peek(ctx, token, purposeInvitation)
}

func (s *Service) PeekPasswordReset(ctx context.Context, token string) (LinkInfo, error) {
	return s.peek(ctx, token, purposeReset)
}

// AcceptInvitation activates the account with the chosen name and password and signs the
// user in. The token is consumed in the same transaction, so a link works once.
func (s *Service) AcceptInvitation(ctx context.Context, token, name, password string, c Client) (*Session, error) {
	if required, err := s.ssoRequired(ctx); err != nil {
		return nil, err
	} else if required {
		return nil, ErrSSORequired
	}
	if err := ValidatePassword(password); err != nil {
		return nil, err
	}
	enc, err := s.hasher.Hash(ctx, password)
	if err != nil {
		return nil, err
	}
	var sess *Session
	err = db.InTx(ctx, s.pool, func(q *dbq.Queries) error {
		tok, err := q.ConsumeAccountToken(ctx, dbq.ConsumeAccountTokenParams{TokenHash: HashToken(token), Purpose: purposeInvitation})
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrLinkInvalid
		}
		if err != nil {
			return err
		}
		user, err := q.AcceptInvitation(ctx, dbq.AcceptInvitationParams{ID: tok.UserID, Name: name, PasswordHash: enc})
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrLinkInvalid
		}
		if err != nil {
			return err
		}
		if err := audit.Write(ctx, q, audit.Entry{Actor: user.ID, IP: c.IP, Action: audit.InvitationAccepted, TargetType: "user", TargetID: uuidString(user.ID)}); err != nil {
			return err
		}
		if sess, err = s.issue(ctx, q, user, false, c); err != nil {
			return err
		}
		return s.completeLogin(ctx, q, user, c, "invitation")
	})
	return sess, err
}

// PasswordResetToken creates a reset link token for the account with this email in tx. ok is
// false when there is no such active account. Callers must answer the same either way, and
// the two paths run the same statements (lookup, invalidate, insert, audit) so that neither
// response time nor database load tells them apart; for an unknown address they just match
// no rows.
func (s *Service) PasswordResetToken(ctx context.Context, tx pgx.Tx, email string, c Client) (user dbq.User, token string, ok bool, err error) {
	q := dbq.New(tx)
	user, err = q.GetUserByEmail(ctx, NormalizeEmail(email))
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && user.DeactivatedAt.Valid) {
		user = dbq.User{}
	} else if err != nil {
		return dbq.User{}, "", false, fmt.Errorf("load user: %w", err)
	}
	if err := q.InvalidateAccountTokens(ctx, dbq.InvalidateAccountTokensParams{UserID: user.ID, Purpose: purposeReset}); err != nil {
		return dbq.User{}, "", false, fmt.Errorf("invalidate reset tokens: %w", err)
	}
	token, hash, err := NewToken()
	if err != nil {
		return dbq.User{}, "", false, err
	}
	_, err = q.InsertPasswordResetToken(ctx, dbq.InsertPasswordResetTokenParams{
		UserID: user.ID, TokenHash: hash, TtlSeconds: int32(ResetTTL / time.Second), //nolint:gosec // a fixed TTL constant, far below int32
	})
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return dbq.User{}, "", false, fmt.Errorf("store link token: %w", err)
	}
	known := err == nil
	entry := audit.Entry{Actor: user.ID, IP: c.IP, Action: audit.PasswordResetRequested}
	if known {
		entry.TargetType, entry.TargetID = "user", uuidString(user.ID)
	}
	if err := audit.Write(ctx, q, entry); err != nil {
		return dbq.User{}, "", false, err
	}
	return user, token, known, nil
}

// CompletePasswordReset sets a new password with a reset token, signs the account out
// everywhere and lifts a login lockout. It does not sign in: two-factor authentication still
// applies at the next login.
func (s *Service) CompletePasswordReset(ctx context.Context, token, password string, c Client) error {
	if err := ValidatePassword(password); err != nil {
		return err
	}
	enc, err := s.hasher.Hash(ctx, password)
	if err != nil {
		return err
	}
	return db.InTx(ctx, s.pool, func(q *dbq.Queries) error {
		tok, err := q.ConsumeAccountToken(ctx, dbq.ConsumeAccountTokenParams{TokenHash: HashToken(token), Purpose: purposeReset})
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrLinkInvalid
		}
		if err != nil {
			return err
		}
		user, err := q.GetUserForUpdate(ctx, tok.UserID)
		if err != nil {
			return err
		}
		if user.DeactivatedAt.Valid {
			return ErrLinkInvalid
		}
		if err := q.SetUserPassword(ctx, dbq.SetUserPasswordParams{ID: user.ID, PasswordHash: enc, PasswordMustChange: false}); err != nil {
			return err
		}
		if _, err := q.RevokeUserSessions(ctx, dbq.RevokeUserSessionsParams{UserID: user.ID, ExceptID: allSessions}); err != nil {
			return err
		}
		return audit.Write(ctx, q, audit.Entry{Actor: user.ID, IP: c.IP, Action: audit.PasswordResetCompleted, TargetType: "user", TargetID: uuidString(user.ID)})
	})
}

func (s *Service) newAccountToken(ctx context.Context, q *dbq.Queries, userID pgtype.UUID, purpose string, createdBy pgtype.UUID, ttl time.Duration) (string, error) {
	token, hash, err := NewToken()
	if err != nil {
		return "", err
	}
	_, err = q.InsertAccountToken(ctx, dbq.InsertAccountTokenParams{
		UserID: userID, Purpose: purpose, TokenHash: hash, CreatedBy: createdBy, TtlSeconds: int32(ttl / time.Second), //nolint:gosec // a fixed TTL constant, far below int32
	})
	if err != nil {
		return "", fmt.Errorf("store link token: %w", err)
	}
	return token, nil
}
