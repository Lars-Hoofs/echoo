package auth

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"echoo/internal/db/dbq"
	"echoo/internal/keyring"
	"echoo/internal/testdb"
)

func TestMain(m *testing.M) { testdb.Main(m) }

type fixture struct {
	t    *testing.T
	svc  *Service
	pool *pgxpool.Pool
	q    *dbq.Queries
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	pool := testdb.New(t)
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	keys, err := keyring.Parse("k1:" + base64.StdEncoding.EncodeToString(key))
	if err != nil {
		t.Fatal(err)
	}
	return &fixture{t: t, svc: NewService(pool, NewHasher(fastArgon, 1), keys), pool: pool, q: dbq.New(pool)}
}

const testPassword = "correct horse battery"

// user creates an agent whose password is testPassword and does not need changing.
func (f *fixture) user(email string) dbq.User {
	f.t.Helper()
	ctx := context.Background()
	u, _, err := f.svc.CreateUser(ctx, dbq.User{}.ID, email, "Test", "agent", Client{})
	if err != nil {
		f.t.Fatal(err)
	}
	f.setPassword(u, testPassword)
	return u
}

func (f *fixture) setPassword(u dbq.User, pw string) string {
	f.t.Helper()
	enc, err := f.svc.hasher.Hash(context.Background(), pw)
	if err != nil {
		f.t.Fatal(err)
	}
	if err := f.q.SetUserPassword(context.Background(), dbq.SetUserPasswordParams{ID: u.ID, PasswordHash: enc}); err != nil {
		f.t.Fatal(err)
	}
	return enc
}

func (f *fixture) exec(sql string, args ...any) {
	f.t.Helper()
	if _, err := f.pool.Exec(context.Background(), sql, args...); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fixture) lock(u dbq.User) {
	f.t.Helper()
	f.exec("UPDATE users SET failed_login_count = 10, locked_until = now() + interval '15 minutes' WHERE id = $1", u.ID)
}

func (f *fixture) sessionCount(u dbq.User) int {
	f.t.Helper()
	var n int
	if err := f.pool.QueryRow(context.Background(), "SELECT count(*) FROM sessions WHERE user_id = $1 AND revoked_at IS NULL AND NOT mfa_pending", u.ID).Scan(&n); err != nil {
		f.t.Fatal(err)
	}
	return n
}

// enrollMFA turns on 2FA for the user and returns the TOTP secret and recovery codes.
func (f *fixture) enrollMFA(u dbq.User) ([]byte, []string) {
	f.t.Helper()
	ctx := context.Background()
	sess, err := f.svc.Login(ctx, u.Email, testPassword, Client{})
	if err != nil {
		f.t.Fatal(err)
	}
	setup, err := f.svc.BeginTOTP(ctx, sess)
	if err != nil {
		f.t.Fatal(err)
	}
	secret, err := b32.DecodeString(setup.Secret)
	if err != nil {
		f.t.Fatal(err)
	}
	sess, err = f.svc.Authenticate(ctx, sess.Token)
	if err != nil {
		f.t.Fatal(err)
	}
	codes, err := f.svc.EnableTOTP(ctx, sess, testPassword, TOTPCode(secret, TOTPStep(time.Now())), Client{})
	if err != nil {
		f.t.Fatal(err)
	}
	return secret, codes
}

func TestLockedAccountRefusesMFAAttempts(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	u := f.user("mfa@example.com")
	secret, codes := f.enrollMFA(u)

	// Pending sessions carry a copy of the user from before the account got locked.
	const attempts = 6
	pending := make([]*Session, attempts)
	for i := range pending {
		var err error
		if pending[i], err = f.svc.Login(ctx, u.Email, testPassword, Client{}); err != nil || !pending[i].MfaPending {
			t.Fatalf("login %d: pending=%v err=%v", i, pending[i], err)
		}
	}
	before := f.sessionCount(u)
	f.lock(u)

	results := make([]error, attempts)
	var wg sync.WaitGroup
	for i := range pending {
		wg.Add(1)
		go func() {
			defer wg.Done()
			code := codes[i%len(codes)]
			if i == 0 {
				code = TOTPCode(secret, TOTPStep(time.Now())+1)
			}
			_, results[i] = f.svc.VerifyMFA(ctx, pending[i], code, Client{})
		}()
	}
	wg.Wait()

	for i, err := range results {
		if !errors.Is(err, ErrInvalidCredentials) {
			t.Errorf("attempt %d: want ErrInvalidCredentials while locked, got %v", i, err)
		}
	}
	if left, err := f.q.CountUnusedRecoveryCodes(ctx, u.ID); err != nil || left != int64(len(codes)) {
		t.Errorf("recovery codes were consumed while locked: %d left, err=%v", left, err)
	}
	if after := f.sessionCount(u); after != before {
		t.Errorf("sessions issued while locked: %d -> %d", before, after)
	}
	cur, err := f.q.GetUser(ctx, u.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !f.svc.locked(cur) || cur.FailedLoginCount != 10 {
		t.Errorf("lock was cleared: locked_until=%v count=%d", cur.LockedUntil, cur.FailedLoginCount)
	}
}

func TestConsumeQueriesRefuseLockedAccount(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	u := f.user("q@example.com")
	_, codes := f.enrollMFA(u)
	f.lock(u)

	n, err := f.q.UseRecoveryCode(ctx, dbq.UseRecoveryCodeParams{UserID: u.ID, CodeHash: HashRecoveryCode(codes[0])})
	if err != nil || n != 0 {
		t.Fatalf("UseRecoveryCode on locked account: n=%d err=%v", n, err)
	}
	n, err = f.q.ConsumeTOTPStep(ctx, dbq.ConsumeTOTPStepParams{ID: u.ID, TotpLastStep: TOTPStep(time.Now()) + 5})
	if err != nil || n != 0 {
		t.Fatalf("ConsumeTOTPStep on locked account: n=%d err=%v", n, err)
	}
}

func TestLoginDoesNotClearALockSetWhileVerifying(t *testing.T) {
	f := newFixture(t)
	u := f.user("race@example.com")

	// Occupy the only hashing slot so Login stalls after it loaded the (unlocked) user.
	f.svc.hasher.sem <- struct{}{}
	done := make(chan error, 1)
	go func() {
		_, err := f.svc.Login(context.Background(), u.Email, testPassword, Client{})
		done <- err
	}()
	time.Sleep(300 * time.Millisecond)
	f.lock(u)
	<-f.svc.hasher.sem

	if err := <-done; !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("login that verified before the lock must be refused, got %v", err)
	}
	cur, err := f.q.GetUser(context.Background(), u.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !f.svc.locked(cur) || f.sessionCount(u) != 0 {
		t.Fatalf("lock cleared or session issued: locked_until=%v sessions=%d", cur.LockedUntil, f.sessionCount(u))
	}
}

func TestLoginDoesNotOverwriteANewerPassword(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	u := f.user("rehash@example.com")

	// A hash with weaker parameters than the service's makes a successful login rehash it.
	old, err := NewHasher(Argon2Params{MemoryKiB: 64, Time: 2, Threads: 1}, 1).Hash(ctx, testPassword)
	if err != nil {
		t.Fatal(err)
	}
	f.exec("UPDATE users SET password_hash = $2 WHERE id = $1", u.ID, old)
	replacement, err := f.svc.hasher.Hash(ctx, "a password set in the meantime")
	if err != nil {
		t.Fatal(err)
	}

	f.svc.hasher.sem <- struct{}{}
	done := make(chan error, 1)
	go func() {
		_, err := f.svc.Login(ctx, u.Email, testPassword, Client{})
		done <- err
	}()
	time.Sleep(300 * time.Millisecond)
	f.exec("UPDATE users SET password_hash = $2, password_changed_at = now() WHERE id = $1", u.ID, replacement)
	<-f.svc.hasher.sem

	if err := <-done; !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("login with a password replaced during verification must be refused, got %v", err)
	}
	cur, err := f.q.GetUser(ctx, u.ID)
	if err != nil {
		t.Fatal(err)
	}
	if cur.PasswordHash != replacement || f.sessionCount(u) != 0 {
		t.Fatalf("newer password was overwritten or a session was issued (sessions=%d)", f.sessionCount(u))
	}
}

func TestRehashOnlyUpgradesTheVerifiedHash(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	u := f.user("rehash2@example.com")
	n, err := f.q.RehashPassword(ctx, dbq.RehashPasswordParams{ID: u.ID, PasswordHash: "not-the-current-hash", PasswordHash_2: "x"})
	if err != nil || n != 0 {
		t.Fatalf("rehash of a stale hash: n=%d err=%v", n, err)
	}
}

func TestChangePasswordRefusedWhenPasswordChangedMeanwhile(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	u := f.user("change@example.com")
	sess, err := f.svc.Login(ctx, u.Email, testPassword, Client{})
	if err != nil {
		t.Fatal(err)
	}
	replacement := f.setPassword(u, "reset by an administrator")

	if _, err := f.svc.ChangePassword(ctx, sess, testPassword, "my brand new passphrase", Client{}); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("want ErrInvalidCredentials, got %v", err)
	}
	cur, err := f.q.GetUser(ctx, u.ID)
	if err != nil {
		t.Fatal(err)
	}
	if cur.PasswordHash != replacement {
		t.Fatal("password set in the meantime was overwritten")
	}
}

func TestEnableTOTPIsBoundToTheVerifiedSecret(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	u := f.user("bind@example.com")
	sess, err := f.svc.Login(ctx, u.Email, testPassword, Client{})
	if err != nil {
		t.Fatal(err)
	}
	first, err := f.svc.BeginTOTP(ctx, sess)
	if err != nil {
		t.Fatal(err)
	}
	secret, err := b32.DecodeString(first.Secret)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := f.svc.Authenticate(ctx, sess.Token)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.BeginTOTP(ctx, sess); err != nil {
		t.Fatal(err)
	}

	_, err = f.svc.EnableTOTP(ctx, snapshot, testPassword, TOTPCode(secret, TOTPStep(time.Now())), Client{})
	if !errors.Is(err, ErrMFANotStarted) {
		t.Fatalf("code for a replaced secret must not enable 2FA, got %v", err)
	}
	cur, err := f.q.GetUser(ctx, u.ID)
	if err != nil {
		t.Fatal(err)
	}
	if cur.TotpEnabledAt.Valid {
		t.Fatal("2FA was enabled with a secret the user never confirmed")
	}
}

func TestEnableTOTPNeedsThePassword(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	u := f.user("pw@example.com")
	sess, err := f.svc.Login(ctx, u.Email, testPassword, Client{})
	if err != nil {
		t.Fatal(err)
	}
	setup, err := f.svc.BeginTOTP(ctx, sess)
	if err != nil {
		t.Fatal(err)
	}
	secret, err := b32.DecodeString(setup.Secret)
	if err != nil {
		t.Fatal(err)
	}
	sess, err = f.svc.Authenticate(ctx, sess.Token)
	if err != nil {
		t.Fatal(err)
	}
	code := TOTPCode(secret, TOTPStep(time.Now()))
	if _, err := f.svc.EnableTOTP(ctx, sess, "", code, Client{}); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("want ErrInvalidCredentials without password, got %v", err)
	}
	if _, err := f.svc.EnableTOTP(ctx, sess, "wrong password entirely", code, Client{}); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("want ErrInvalidCredentials with wrong password, got %v", err)
	}
	if _, err := f.svc.EnableTOTP(ctx, sess, testPassword, code, Client{}); err != nil {
		t.Fatal(err)
	}
}
