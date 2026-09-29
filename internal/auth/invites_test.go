package auth

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"echoo/internal/db/dbq"
)

func (f *fixture) invite(email string) (userID, token string) {
	f.t.Helper()
	err := pgx.BeginFunc(context.Background(), f.pool, func(tx pgx.Tx) error {
		u, tok, err := f.svc.InviteUser(context.Background(), tx, pgtype.UUID{}, Invite{Email: email, Name: "Invitee", Role: "agent"}, Client{})
		userID, token = u.ID.String(), tok
		return err
	})
	if err != nil {
		f.t.Fatal(err)
	}
	return userID, token
}

func TestAnInvitationCanBeAcceptedOnlyOnceEvenConcurrently(t *testing.T) {
	f := newFixture(t)
	_, token := f.invite("new@example.com")

	var wg sync.WaitGroup
	var accepted, invalid atomic.Int32
	for range 6 {
		wg.Go(func() {
			_, err := f.svc.AcceptInvitation(context.Background(), token, "Invitee", testPassword, Client{})
			switch {
			case err == nil:
				accepted.Add(1)
			case errors.Is(err, ErrLinkInvalid):
				invalid.Add(1)
			default:
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
	wg.Wait()
	if accepted.Load() != 1 || invalid.Load() != 5 {
		t.Fatalf("%d accepted, %d refused; want 1 and 5", accepted.Load(), invalid.Load())
	}
}

func TestInvitedUsersCannotLogInAndAcceptingActivates(t *testing.T) {
	f := newFixture(t)
	_, token := f.invite("new@example.com")
	ctx := context.Background()

	if _, err := f.svc.Login(ctx, "new@example.com", testPassword, Client{}); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("login before accepting: %v", err)
	}
	if _, err := f.svc.AcceptInvitation(ctx, token, "Invitee", "short", Client{}); !errors.Is(err, ErrPasswordTooShort) {
		t.Fatalf("short password: %v", err)
	}
	// A refused password must not consume the link.
	if _, err := f.svc.PeekInvitation(ctx, token); err != nil {
		t.Fatalf("link consumed by a refused password: %v", err)
	}
	sess, err := f.svc.AcceptInvitation(ctx, token, "Invitee", testPassword, Client{})
	if err != nil || sess.MfaPending || sess.User.InvitedAt.Valid || sess.User.DeactivatedAt.Valid {
		t.Fatalf("accept: %+v, %v", sess, err)
	}
	if _, err := f.svc.Login(ctx, "new@example.com", testPassword, Client{}); err != nil {
		t.Fatalf("login after accepting: %v", err)
	}
}

func TestPasswordResetTokensAreReplacedBySecondRequests(t *testing.T) {
	f := newFixture(t)
	f.user("agent@example.com")
	ctx := context.Background()
	request := func() string {
		var token string
		err := pgx.BeginFunc(ctx, f.pool, func(tx pgx.Tx) error {
			_, tok, ok, err := f.svc.PasswordResetToken(ctx, tx, "Agent@Example.com", Client{})
			if err == nil && !ok {
				t.Fatal("an existing account got no token")
			}
			token = tok
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
		return token
	}
	first, second := request(), request()
	if err := f.svc.CompletePasswordReset(ctx, first, "another long password", Client{}); !errors.Is(err, ErrLinkInvalid) {
		t.Fatalf("the older link must be void: %v", err)
	}
	if err := f.svc.CompletePasswordReset(ctx, second, "another long password", Client{}); err != nil {
		t.Fatal(err)
	}
	if err := f.svc.CompletePasswordReset(ctx, second, "yet another long password", Client{}); !errors.Is(err, ErrLinkInvalid) {
		t.Fatalf("a used link must be void: %v", err)
	}
}

func TestInvitedAndCreatedUsersCanHaveACustomRole(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	role, err := f.q.CreateCustomRole(ctx, dbq.CreateCustomRoleParams{Name: "Lead", Permissions: []string{"conversations.read", "reports.view"}})
	if err != nil {
		t.Fatal(err)
	}

	var invited dbq.User
	err = pgx.BeginFunc(ctx, f.pool, func(tx pgx.Tx) error {
		var err error
		invited, _, err = f.svc.InviteUser(ctx, tx, pgtype.UUID{}, Invite{Email: "lead@example.com", Name: "Lead", CustomRoleID: role.ID}, Client{})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	created, _, err := f.svc.CreateUserWithCustomRole(ctx, pgtype.UUID{}, "made@example.com", "Made", role.ID, Client{})
	if err != nil {
		t.Fatal(err)
	}
	for _, u := range []dbq.User{invited, created} {
		if u.Role != "custom" || u.CustomRoleID != role.ID || len(u.Permissions) != 2 {
			t.Errorf("%s: role %q, role id %v, permissions %v", u.Email, u.Role, u.CustomRoleID, u.Permissions)
		}
	}
}
