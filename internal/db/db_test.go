package db_test

import (
	"errors"
	"testing"

	"echoo/internal/db"
	"echoo/internal/db/dbq"
	"echoo/internal/testdb"
)

func TestMain(m *testing.M) { testdb.Main(m) }

func TestInTxReleasesTheConnectionOnPanic(t *testing.T) {
	pool := testdb.New(t)
	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("expected the panic to propagate")
			}
		}()
		_ = db.InTx(t.Context(), pool, func(*dbq.Queries) error { panic("boom") })
	}()
	if n := pool.Stat().AcquiredConns(); n != 0 {
		t.Fatalf("%d connections still checked out after a panic in a transaction", n)
	}
}

func TestInTxRollsBackOnErrorAndCommitsOnSuccess(t *testing.T) {
	pool := testdb.New(t)
	boom := errors.New("boom")
	err := db.InTx(t.Context(), pool, func(q *dbq.Queries) error {
		if _, err := q.CreateTeam(t.Context(), "rolled back"); err != nil {
			return err
		}
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatalf("want the callback's error, got %v", err)
	}
	if err := db.InTx(t.Context(), pool, func(q *dbq.Queries) error {
		_, err := q.CreateTeam(t.Context(), "committed")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	teams, err := dbq.New(pool).ListTeams(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(teams) != 1 || teams[0].Name != "committed" {
		t.Fatalf("unexpected teams %+v", teams)
	}
}
