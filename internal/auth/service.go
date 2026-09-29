package auth

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"net/netip"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"echoo/internal/audit"
	"echoo/internal/db"
	"echoo/internal/db/dbq"
	"echoo/internal/keyring"
)

const (
	sessionIdle      = 7 * 24 * time.Hour
	sessionAbsolute  = 30 * 24 * time.Hour
	mfaPendingTTL    = 5 * time.Minute
	touchInterval    = time.Minute
	lockThreshold    = 10
	lockDuration     = 15 * time.Minute
	totpIssuer       = "Echoo"
	tempPasswordSize = 15 // bytes, 24 base32 characters
)

var (
	ErrInvalidCredentials = errors.New("invalid credentials")
	ErrInvalidCode        = errors.New("invalid code")
	ErrNoSession          = errors.New("no valid session")
	ErrMFANotPending      = errors.New("session is not waiting for a second factor")
	ErrMFAAlreadyEnabled  = errors.New("two-factor authentication is already enabled")
	ErrMFANotStarted      = errors.New("two-factor setup has not been started")
	ErrMFANotEnabled      = errors.New("two-factor authentication is not enabled")
	ErrSSORequired        = errors.New("signing in with a password is switched off, use single sign-on")
)

// errCodeRejected means a second-factor code was well-formed but unknown or already used.
var errCodeRejected = errors.New("code rejected")

// refusedError aborts a transaction when a decision made under the row lock turns the attempt
// down; reason is what gets audited.
type refusedError struct{ reason string }

func (e *refusedError) Error() string { return "refused: " + e.reason }

// allSessions is used as except_id to revoke every session of a user. It must be a valid
// UUID: a NULL would make the comparison unknown and revoke nothing.
var allSessions = pgtype.UUID{Valid: true}

type Client struct {
	IP        *netip.Addr
	UserAgent string
}

// Session is an authenticated request context. Token is only set right after it was issued.
type Session struct {
	dbq.Session
	User  dbq.User
	Token string
}

type Service struct {
	pool   *pgxpool.Pool
	q      *dbq.Queries
	hasher *Hasher
	keys   *keyring.Keyring
	now    func() time.Time
}

func NewService(pool *pgxpool.Pool, hasher *Hasher, keys *keyring.Keyring) *Service {
	return &Service{pool: pool, q: dbq.New(pool), hasher: hasher, keys: keys, now: time.Now}
}

func NormalizeEmail(email string) string { return strings.ToLower(strings.TrimSpace(email)) }

// Login verifies email and password. Users with 2FA get a short-lived session that only
// allows the second-factor step. Every failure mode returns ErrInvalidCredentials.
func (s *Service) Login(ctx context.Context, email, password string, c Client) (*Session, error) {
	user, err := s.q.GetUserByEmail(ctx, NormalizeEmail(email))
	if errors.Is(err, pgx.ErrNoRows) {
		if _, _, err := s.hasher.Verify(ctx, password, ""); err != nil {
			return nil, err
		}
		return nil, s.logFailure(ctx, nil, c, map[string]any{"reason": "unknown_account"})
	}
	if err != nil {
		return nil, fmt.Errorf("load user: %w", err)
	}

	required, err := s.ssoRequired(ctx)
	if err != nil {
		return nil, err
	}
	// With SSO required the password is only good for the owner, who keeps a way in when the
	// provider is down. Everybody else gets the same answer as for a wrong password.
	if required && user.Role != "owner" {
		if _, _, err := s.hasher.Verify(ctx, password, ""); err != nil {
			return nil, err
		}
		return nil, s.logFailure(ctx, &user, c, map[string]any{"reason": "sso_required"})
	}

	if reason := s.blockedReason(user); reason != "" {
		if _, _, err := s.hasher.Verify(ctx, password, ""); err != nil {
			return nil, err
		}
		return nil, s.logFailure(ctx, &user, c, map[string]any{"reason": reason})
	}

	ok, rehash, err := s.hasher.Verify(ctx, password, user.PasswordHash)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, s.recordFailure(ctx, user, c, audit.LoginFailed, "wrong_password")
	}
	var upgraded string
	if rehash {
		if upgraded, err = s.hasher.Hash(ctx, password); err != nil {
			return nil, fmt.Errorf("rehash password: %w", err)
		}
	}

	// Verifying may have waited for a hashing slot, so everything that decides the outcome is
	// re-read under a row lock: lock state, and that the verified password is still current.
	var sess *Session
	err = db.InTx(ctx, s.pool, func(q *dbq.Queries) error {
		cur, err := q.GetUserForUpdate(ctx, user.ID)
		if err != nil {
			return err
		}
		if reason := s.blockedReason(cur); reason != "" {
			return &refusedError{reason: reason}
		}
		if cur.PasswordHash != user.PasswordHash {
			return &refusedError{reason: "password_changed"}
		}
		if rehash {
			if _, err := q.RehashPassword(ctx, dbq.RehashPasswordParams{ID: cur.ID, PasswordHash: user.PasswordHash, PasswordHash_2: upgraded}); err != nil {
				return fmt.Errorf("rehash password: %w", err)
			}
		}
		mfa := cur.TotpEnabledAt.Valid
		if sess, err = s.issue(ctx, q, cur, mfa, c); err != nil {
			return err
		}
		if mfa {
			return nil
		}
		return s.completeLogin(ctx, q, cur, c, "password")
	})
	var refused *refusedError
	if errors.As(err, &refused) {
		return nil, s.logFailure(ctx, &user, c, map[string]any{"reason": refused.reason})
	}
	return sess, err
}

// VerifyMFA completes a login with a TOTP or recovery code and replaces the pending session
// with a full one (session rotation).
func (s *Service) VerifyMFA(ctx context.Context, pending *Session, code string, c Client) (*Session, error) {
	if !pending.MfaPending {
		return nil, ErrMFANotPending
	}
	user := pending.User
	if s.locked(user) {
		return nil, s.logFailure(ctx, &user, c, map[string]any{"reason": "locked", "step": "mfa"})
	}

	method := "totp"
	var (
		step         int64
		recoveryHash []byte
	)
	if LooksLikeRecoveryCode(code) {
		method, recoveryHash = "recovery_code", HashRecoveryCode(code)
	} else {
		secret, err := s.totpSecret(user)
		if err != nil {
			return nil, err
		}
		var ok bool
		if step, ok = VerifyTOTP(secret, code, s.now()); !ok {
			return nil, s.mfaFailure(ctx, user, c)
		}
	}

	// The pending session's copy of the user can be stale, and consuming a code may have waited
	// behind other attempts. The lock state is decided under a row lock, in the same
	// transaction that consumes the code and clears the failure counters.
	var sess *Session
	err := db.InTx(ctx, s.pool, func(q *dbq.Queries) error {
		cur, err := q.GetUserForUpdate(ctx, user.ID)
		if err != nil {
			return err
		}
		if s.locked(cur) {
			return &refusedError{reason: "locked"}
		}
		var n int64
		if recoveryHash != nil {
			n, err = q.UseRecoveryCode(ctx, dbq.UseRecoveryCodeParams{UserID: cur.ID, CodeHash: recoveryHash})
		} else {
			n, err = q.ConsumeTOTPStep(ctx, dbq.ConsumeTOTPStepParams{ID: cur.ID, TotpLastStep: step})
		}
		if err != nil {
			return err
		}
		if n != 1 {
			return errCodeRejected
		}
		if err := q.RevokeSession(ctx, pending.ID); err != nil {
			return err
		}
		if sess, err = s.issue(ctx, q, cur, false, c); err != nil {
			return err
		}
		return s.completeLogin(ctx, q, cur, c, method)
	})
	var refused *refusedError
	switch {
	case errors.As(err, &refused):
		return nil, s.logFailure(ctx, &user, c, map[string]any{"reason": refused.reason, "step": "mfa"})
	case errors.Is(err, errCodeRejected):
		return nil, s.mfaFailure(ctx, user, c)
	}
	return sess, err
}

func (s *Service) mfaFailure(ctx context.Context, user dbq.User, c Client) error {
	if err := s.recordFailure(ctx, user, c, audit.MFAFailed, "wrong_code"); !errors.Is(err, ErrInvalidCredentials) {
		return err
	}
	return ErrInvalidCode
}

// Authenticate resolves a session token. Pending 2FA sessions are returned with MfaPending
// set; callers must restrict what they allow.
func (s *Service) Authenticate(ctx context.Context, token string) (*Session, error) {
	if token == "" {
		return nil, ErrNoSession
	}
	row, err := s.q.GetActiveSession(ctx, HashToken(token))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNoSession
	}
	if err != nil {
		return nil, fmt.Errorf("load session: %w", err)
	}
	now := s.now()
	if row.Session.MfaPending && now.Sub(row.Session.CreatedAt.Time) > mfaPendingTTL {
		return nil, ErrNoSession
	}
	if now.Sub(row.Session.LastSeenAt.Time) > touchInterval {
		err := s.q.TouchSession(ctx, dbq.TouchSessionParams{ID: row.Session.ID, IdleExpiresAt: ts(now.Add(sessionIdle))})
		if err != nil {
			return nil, fmt.Errorf("touch session: %w", err)
		}
	}
	return &Session{Session: row.Session, User: row.User}, nil
}

func (s *Service) Logout(ctx context.Context, sess *Session, c Client) error {
	return db.InTx(ctx, s.pool, func(q *dbq.Queries) error {
		if err := q.RevokeSession(ctx, sess.ID); err != nil {
			return err
		}
		return audit.Write(ctx, q, audit.Entry{Actor: sess.User.ID, IP: c.IP, Action: audit.Logout, TargetType: "session", TargetID: uuidString(sess.ID)})
	})
}

func (s *Service) LogoutOthers(ctx context.Context, sess *Session, c Client) (int64, error) {
	var n int64
	err := db.InTx(ctx, s.pool, func(q *dbq.Queries) error {
		var err error
		if n, err = q.RevokeUserSessions(ctx, dbq.RevokeUserSessionsParams{UserID: sess.User.ID, ExceptID: sess.ID}); err != nil {
			return err
		}
		return audit.Write(ctx, q, audit.Entry{Actor: sess.User.ID, IP: c.IP, Action: audit.LogoutOthers, Metadata: map[string]any{"revoked": n}})
	})
	return n, err
}

func (s *Service) RevokeOwnSession(ctx context.Context, sess *Session, id pgtype.UUID, c Client) (bool, error) {
	var n int64
	err := db.InTx(ctx, s.pool, func(q *dbq.Queries) error {
		var err error
		if n, err = q.RevokeUserSession(ctx, dbq.RevokeUserSessionParams{ID: id, UserID: sess.User.ID}); err != nil || n == 0 {
			return err
		}
		return audit.Write(ctx, q, audit.Entry{Actor: sess.User.ID, IP: c.IP, Action: audit.SessionRevoked, TargetType: "session", TargetID: uuidString(id)})
	})
	return n == 1, err
}

// ChangePassword verifies the current password, revokes every session including the current
// one, and returns a fresh session.
func (s *Service) ChangePassword(ctx context.Context, sess *Session, current, next string, c Client) (*Session, error) {
	if err := s.verifyPassword(ctx, sess.User, current, c); err != nil {
		return nil, err
	}
	if err := ValidatePassword(next); err != nil {
		return nil, err
	}
	enc, err := s.hasher.Hash(ctx, next)
	if err != nil {
		return nil, err
	}
	var fresh *Session
	err = db.InTx(ctx, s.pool, func(q *dbq.Queries) error {
		cur, err := q.GetUserForUpdate(ctx, sess.User.ID)
		if err != nil {
			return err
		}
		if s.locked(cur) {
			return &refusedError{reason: "locked"}
		}
		if cur.PasswordHash != sess.User.PasswordHash {
			return &refusedError{reason: "password_changed"}
		}
		if err := q.SetUserPassword(ctx, dbq.SetUserPasswordParams{ID: cur.ID, PasswordHash: enc, PasswordMustChange: false}); err != nil {
			return err
		}
		if _, err := q.RevokeUserSessions(ctx, dbq.RevokeUserSessionsParams{UserID: cur.ID, ExceptID: allSessions}); err != nil {
			return err
		}
		cur.PasswordMustChange = false
		if fresh, err = s.issue(ctx, q, cur, false, c); err != nil {
			return err
		}
		return audit.Write(ctx, q, audit.Entry{Actor: cur.ID, IP: c.IP, Action: audit.PasswordChanged, TargetType: "user", TargetID: uuidString(cur.ID)})
	})
	var refused *refusedError
	if errors.As(err, &refused) {
		return nil, s.logFailure(ctx, &sess.User, c, map[string]any{"reason": refused.reason, "step": "password_change"})
	}
	return fresh, err
}

type TOTPSetup struct {
	Secret string
	URI    string
}

// BeginTOTP stores a new, not yet enabled secret. Calling it again replaces the pending one.
func (s *Service) BeginTOTP(ctx context.Context, sess *Session) (*TOTPSetup, error) {
	if sess.User.TotpEnabledAt.Valid {
		return nil, ErrMFAAlreadyEnabled
	}
	secret, err := NewTOTPSecret()
	if err != nil {
		return nil, err
	}
	enc, err := s.keys.Encrypt(secret, totpAAD(sess.User.ID))
	if err != nil {
		return nil, err
	}
	if err := s.q.SetPendingTOTPSecret(ctx, dbq.SetPendingTOTPSecretParams{ID: sess.User.ID, TotpSecretEnc: enc}); err != nil {
		return nil, fmt.Errorf("store totp secret: %w", err)
	}
	return &TOTPSetup{Secret: EncodeTOTPSecret(secret), URI: TOTPURI(totpIssuer, sess.User.Email, secret)}, nil
}

// EnableTOTP confirms enrollment with the account password and a first code, issues recovery
// codes and signs out other sessions that were established without the second factor.
func (s *Service) EnableTOTP(ctx context.Context, sess *Session, password, code string, c Client) ([]string, error) {
	if sess.User.TotpEnabledAt.Valid {
		return nil, ErrMFAAlreadyEnabled
	}
	if sess.User.TotpSecretEnc == nil {
		return nil, ErrMFANotStarted
	}
	if err := s.verifyPassword(ctx, sess.User, password, c); err != nil {
		return nil, err
	}
	secret, err := s.totpSecret(sess.User)
	if err != nil {
		return nil, err
	}
	step, ok := VerifyTOTP(secret, code, s.now())
	if !ok {
		return nil, ErrInvalidCode
	}
	codes, hashes, err := NewRecoveryCodes()
	if err != nil {
		return nil, err
	}
	err = db.InTx(ctx, s.pool, func(q *dbq.Queries) error {
		n, err := q.EnableTOTP(ctx, dbq.EnableTOTPParams{ID: sess.User.ID, TotpLastStep: step, TotpSecretEnc: sess.User.TotpSecretEnc})
		if err != nil {
			return err
		}
		if n != 1 {
			return ErrMFANotStarted
		}
		if err := replaceRecoveryCodes(ctx, q, sess.User.ID, hashes); err != nil {
			return err
		}
		if _, err := q.RevokeUserSessions(ctx, dbq.RevokeUserSessionsParams{UserID: sess.User.ID, ExceptID: sess.ID}); err != nil {
			return err
		}
		return audit.Write(ctx, q, audit.Entry{Actor: sess.User.ID, IP: c.IP, Action: audit.MFAEnabled, TargetType: "user", TargetID: uuidString(sess.User.ID)})
	})
	if err != nil {
		return nil, err
	}
	return codes, nil
}

func (s *Service) DisableTOTP(ctx context.Context, sess *Session, password string, c Client) error {
	if !sess.User.TotpEnabledAt.Valid {
		return ErrMFANotEnabled
	}
	if err := s.verifyPassword(ctx, sess.User, password, c); err != nil {
		return err
	}
	return db.InTx(ctx, s.pool, func(q *dbq.Queries) error {
		if err := clearTOTP(ctx, q, sess.User.ID); err != nil {
			return err
		}
		return audit.Write(ctx, q, audit.Entry{Actor: sess.User.ID, IP: c.IP, Action: audit.MFADisabled, TargetType: "user", TargetID: uuidString(sess.User.ID)})
	})
}

func (s *Service) RegenerateRecoveryCodes(ctx context.Context, sess *Session, password string, c Client) ([]string, error) {
	if !sess.User.TotpEnabledAt.Valid {
		return nil, ErrMFANotEnabled
	}
	if err := s.verifyPassword(ctx, sess.User, password, c); err != nil {
		return nil, err
	}
	codes, hashes, err := NewRecoveryCodes()
	if err != nil {
		return nil, err
	}
	err = db.InTx(ctx, s.pool, func(q *dbq.Queries) error {
		if err := replaceRecoveryCodes(ctx, q, sess.User.ID, hashes); err != nil {
			return err
		}
		return audit.Write(ctx, q, audit.Entry{Actor: sess.User.ID, IP: c.IP, Action: audit.RecoveryCodesRegenerated, TargetType: "user", TargetID: uuidString(sess.User.ID)})
	})
	if err != nil {
		return nil, err
	}
	return codes, nil
}

// CreateUser creates an account with a generated temporary password that must be changed
// at first login. The caller is responsible for the authorization check.
func (s *Service) CreateUser(ctx context.Context, actor pgtype.UUID, email, name, role string, c Client) (dbq.User, string, error) {
	return s.createUser(ctx, actor, email, name, role, pgtype.UUID{}, c)
}

// CreateUserWithCustomRole is CreateUser for a user whose rights come from a custom role.
func (s *Service) CreateUserWithCustomRole(ctx context.Context, actor pgtype.UUID, email, name string, roleID pgtype.UUID, c Client) (dbq.User, string, error) {
	return s.createUser(ctx, actor, email, name, "custom", roleID, c)
}

func (s *Service) createUser(ctx context.Context, actor pgtype.UUID, email, name, role string, customRoleID pgtype.UUID, c Client) (dbq.User, string, error) {
	temp, err := TemporaryPassword()
	if err != nil {
		return dbq.User{}, "", err
	}
	enc, err := s.hasher.Hash(ctx, temp)
	if err != nil {
		return dbq.User{}, "", err
	}
	var user dbq.User
	err = db.InTx(ctx, s.pool, func(q *dbq.Queries) error {
		var err error
		// The row is created with a built-in role; a custom role is attached right after, in
		// the same transaction, so the copy of its permissions comes from one place.
		base := role
		if customRoleID.Valid {
			base = "agent"
		}
		user, err = q.CreateUser(ctx, dbq.CreateUserParams{
			Email: NormalizeEmail(email), Name: strings.TrimSpace(name), Role: base,
			PasswordHash: enc, PasswordMustChange: true,
		})
		if err != nil {
			return err
		}
		meta := map[string]any{"role": role}
		if customRoleID.Valid {
			if user, err = q.SetUserCustomRole(ctx, dbq.SetUserCustomRoleParams{ID: user.ID, RoleID: customRoleID}); err != nil {
				return err
			}
			meta["custom_role_id"] = uuidString(customRoleID)
		}
		return audit.Write(ctx, q, audit.Entry{Actor: actor, IP: c.IP, Action: audit.UserCreated, TargetType: "user", TargetID: uuidString(user.ID), Metadata: meta})
	})
	return user, temp, err
}

// ResetPassword sets a new temporary password for another user and signs them out.
func (s *Service) ResetPassword(ctx context.Context, actor pgtype.UUID, target dbq.User, c Client) (string, error) {
	temp, err := TemporaryPassword()
	if err != nil {
		return "", err
	}
	enc, err := s.hasher.Hash(ctx, temp)
	if err != nil {
		return "", err
	}
	err = db.InTx(ctx, s.pool, func(q *dbq.Queries) error {
		if err := q.SetUserPassword(ctx, dbq.SetUserPasswordParams{ID: target.ID, PasswordHash: enc, PasswordMustChange: true}); err != nil {
			return err
		}
		if _, err := q.RevokeUserSessions(ctx, dbq.RevokeUserSessionsParams{UserID: target.ID, ExceptID: allSessions}); err != nil {
			return err
		}
		return audit.Write(ctx, q, audit.Entry{Actor: actor, IP: c.IP, Action: audit.UserPasswordReset, TargetType: "user", TargetID: uuidString(target.ID)})
	})
	return temp, err
}

// ResetMFA removes another user's second factor, for a lost device, and signs them out.
func (s *Service) ResetMFA(ctx context.Context, actor pgtype.UUID, target dbq.User, c Client) error {
	return db.InTx(ctx, s.pool, func(q *dbq.Queries) error {
		if err := clearTOTP(ctx, q, target.ID); err != nil {
			return err
		}
		if _, err := q.RevokeUserSessions(ctx, dbq.RevokeUserSessionsParams{UserID: target.ID, ExceptID: allSessions}); err != nil {
			return err
		}
		return audit.Write(ctx, q, audit.Entry{Actor: actor, IP: c.IP, Action: audit.UserMFAReset, TargetType: "user", TargetID: uuidString(target.ID)})
	})
}

// CreateOwner bootstraps the first account from the command line.
func (s *Service) CreateOwner(ctx context.Context, email, name string) (string, error) {
	_, temp, err := s.CreateUser(ctx, pgtype.UUID{}, email, name, "owner", Client{})
	return temp, err
}

// TemporaryPassword returns 24 base32 characters in groups of four (120 bits).
func TemporaryPassword() (string, error) {
	b := make([]byte, tempPasswordSize)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	raw := strings.ToLower(b32.EncodeToString(b))
	var groups []string
	for i := 0; i < len(raw); i += 4 {
		groups = append(groups, raw[i:i+4])
	}
	return strings.Join(groups, "-"), nil
}

func (s *Service) verifyPassword(ctx context.Context, user dbq.User, password string, c Client) error {
	if s.locked(user) {
		return ErrInvalidCredentials
	}
	ok, _, err := s.hasher.Verify(ctx, password, user.PasswordHash)
	if err != nil {
		return err
	}
	if !ok {
		return s.recordFailure(ctx, user, c, audit.LoginFailed, "wrong_password_reauth")
	}
	return nil
}

// blockedReason says why the account may not sign in right now, or "" when it may.
func (s *Service) blockedReason(u dbq.User) string {
	switch {
	case u.DeactivatedAt.Valid:
		return "deactivated"
	case s.locked(u):
		return "locked"
	case u.PasswordMustChange && u.TempPasswordExpiresAt.Valid && !u.TempPasswordExpiresAt.Time.After(s.now()):
		return "temp_password_expired"
	}
	return ""
}

func (s *Service) locked(u dbq.User) bool {
	return u.LockedUntil.Valid && u.LockedUntil.Time.After(s.now())
}

// recordFailure counts a failed attempt towards the account lockout and always returns
// ErrInvalidCredentials unless storing the failure itself fails.
func (s *Service) recordFailure(ctx context.Context, user dbq.User, c Client, action, reason string) error {
	err := db.InTx(ctx, s.pool, func(q *dbq.Queries) error {
		res, err := q.RecordLoginFailure(ctx, dbq.RecordLoginFailureParams{
			ID: user.ID, LockThreshold: lockThreshold, LockSeconds: int32(lockDuration / time.Second),
		})
		if err != nil {
			return err
		}
		entry := audit.Entry{Actor: user.ID, IP: c.IP, Action: action, TargetType: "user", TargetID: uuidString(user.ID), Metadata: map[string]any{"reason": reason}}
		if err := audit.Write(ctx, q, entry); err != nil {
			return err
		}
		if res.NewlyLocked.Bool {
			return audit.Write(ctx, q, audit.Entry{Actor: user.ID, IP: c.IP, Action: audit.AccountLocked, TargetType: "user", TargetID: uuidString(user.ID)})
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("record login failure: %w", err)
	}
	return ErrInvalidCredentials
}

// logFailure audits a failed login without touching counters (unknown, locked or deactivated).
func (s *Service) logFailure(ctx context.Context, user *dbq.User, c Client, meta map[string]any) error {
	e := audit.Entry{IP: c.IP, Action: audit.LoginFailed, Metadata: meta}
	if user != nil {
		e.Actor, e.TargetType, e.TargetID = user.ID, "user", uuidString(user.ID)
	}
	if err := audit.Write(ctx, s.q, e); err != nil {
		return err
	}
	return ErrInvalidCredentials
}

func (s *Service) completeLogin(ctx context.Context, q *dbq.Queries, user dbq.User, c Client, method string) error {
	if err := q.RecordLoginSuccess(ctx, user.ID); err != nil {
		return err
	}
	return audit.Write(ctx, q, audit.Entry{Actor: user.ID, IP: c.IP, Action: audit.LoginSucceeded, TargetType: "user", TargetID: uuidString(user.ID), Metadata: map[string]any{"method": method}})
}

func (s *Service) issue(ctx context.Context, q *dbq.Queries, user dbq.User, mfaPending bool, c Client) (*Session, error) {
	token, hash, err := NewToken()
	if err != nil {
		return nil, err
	}
	csrf, _, err := NewToken()
	if err != nil {
		return nil, err
	}
	now := s.now()
	expires := now.Add(sessionAbsolute)
	if mfaPending {
		expires = now.Add(mfaPendingTTL)
	}
	ua := c.UserAgent
	if len(ua) > 512 {
		ua = ua[:512]
	}
	row, err := q.CreateSession(ctx, dbq.CreateSessionParams{
		UserID: user.ID, TokenHash: hash, CsrfToken: csrf, MfaPending: mfaPending,
		IdleExpiresAt: ts(now.Add(sessionIdle)), ExpiresAt: ts(expires), Ip: c.IP, UserAgent: ua,
	})
	if err != nil {
		return nil, fmt.Errorf("create session: %w", err)
	}
	return &Session{Session: row, User: user, Token: token}, nil
}

func (s *Service) totpSecret(user dbq.User) ([]byte, error) {
	if user.TotpSecretEnc == nil {
		return nil, ErrMFANotStarted
	}
	return s.keys.Decrypt(user.TotpSecretEnc, totpAAD(user.ID))
}

func replaceRecoveryCodes(ctx context.Context, q *dbq.Queries, userID pgtype.UUID, hashes [][]byte) error {
	if err := q.DeleteRecoveryCodes(ctx, userID); err != nil {
		return err
	}
	rows := make([]dbq.InsertRecoveryCodesParams, len(hashes))
	for i, h := range hashes {
		rows[i] = dbq.InsertRecoveryCodesParams{UserID: userID, CodeHash: h}
	}
	_, err := q.InsertRecoveryCodes(ctx, rows)
	return err
}

func clearTOTP(ctx context.Context, q *dbq.Queries, userID pgtype.UUID) error {
	if err := q.DisableTOTP(ctx, userID); err != nil {
		return err
	}
	return q.DeleteRecoveryCodes(ctx, userID)
}

func totpAAD(userID pgtype.UUID) []byte {
	return keyring.AAD("users", "totp_secret_enc", uuidString(userID))
}

func ts(t time.Time) pgtype.Timestamptz { return pgtype.Timestamptz{Time: t, Valid: true} }

func uuidString(id pgtype.UUID) string {
	if !id.Valid {
		return ""
	}
	return id.String()
}
