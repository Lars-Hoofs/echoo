// Package testdb provides isolated, migrated PostgreSQL databases for integration tests.
// One container is started per test binary; each test gets its own database cloned from a
// migrated template, which takes milliseconds.
package testdb

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"sync/atomic"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"

	"echoo/internal/db"
	"echoo/internal/mail"
)

const (
	image    = "postgres:18-alpine"
	template = "echoo_template"
)

type Env struct {
	container *postgres.PostgresContainer
	baseURL   *url.URL
	counter   atomic.Int64
}

var env *Env

// Main starts the container, runs the tests and removes the container. Use it from TestMain.
func Main(m *testing.M) {
	ctx := context.Background()
	mail.SetMessageIDKey([]byte("testdb-message-id-key-0123456789"))
	var err error
	env, err = start(ctx)
	if err != nil {
		fmt.Fprintln(os.Stderr, "testdb:", err)
		os.Exit(1)
	}
	code := m.Run()
	if err := testcontainers.TerminateContainer(env.container); err != nil {
		fmt.Fprintln(os.Stderr, "testdb: terminate:", err)
	}
	os.Exit(code)
}

func start(ctx context.Context) (*Env, error) {
	c, err := postgres.Run(ctx, image,
		postgres.WithDatabase(template), postgres.WithUsername("echoo"), postgres.WithPassword("echoo"),
		postgres.BasicWaitStrategies())
	if err != nil {
		return nil, fmt.Errorf("start postgres: %w", err)
	}
	dsn, err := c.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		return nil, err
	}
	u, err := url.Parse(dsn)
	if err != nil {
		return nil, err
	}
	pool, err := db.Open(ctx, dsn)
	if err != nil {
		return nil, err
	}
	err = db.Migrate(ctx, pool)
	pool.Close()
	if err != nil {
		return nil, err
	}
	return &Env{container: c, baseURL: u}, nil
}

func (e *Env) urlFor(name string) string {
	u := *e.baseURL
	u.Path = "/" + name
	return u.String()
}

// New returns a pool on a fresh copy of the migrated template database.
func New(t *testing.T) *pgxpool.Pool {
	t.Helper()
	if env == nil {
		t.Fatal("testdb.Main was not called from TestMain")
	}
	ctx := context.Background()
	name := fmt.Sprintf("t_%d", env.counter.Add(1))

	admin, err := pgx.Connect(ctx, env.urlFor("postgres"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = admin.Close(ctx) }()
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+pgx.Identifier{name}.Sanitize()+" TEMPLATE "+template); err != nil {
		t.Fatal(err)
	}
	pool, err := db.Open(ctx, env.urlFor(name))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return pool
}
