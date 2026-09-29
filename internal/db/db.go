// Package db opens the connection pool, runs migrations and provides a transaction helper.
package db

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"regexp"
	"strconv"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
	"github.com/pressly/goose/v3/lock"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
	"github.com/riverqueue/river/rivermigrate"

	sqlfiles "echoo/db"
	"echoo/internal/db/dbq"
)

func Open(ctx context.Context, url string) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		return nil, fmt.Errorf("parse database url: %w", err)
	}
	cfg.MaxConns = 10
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("connect to database: %w", err)
	}
	return pool, nil
}

// Migrate applies pending migrations. A Postgres advisory lock makes concurrent starts safe.
func Migrate(ctx context.Context, pool *pgxpool.Pool) error {
	sqlDB := stdlib.OpenDBFromPool(pool)
	// Closing the database/sql wrapper does not close the underlying pool.
	defer func() { _ = sqlDB.Close() }()

	migrations, err := fs.Sub(sqlfiles.Migrations, "migrations")
	if err != nil {
		return err
	}
	locker, err := lock.NewPostgresSessionLocker()
	if err != nil {
		return err
	}
	provider, err := goose.NewProvider(goose.DialectPostgres, sqlDB, migrations, goose.WithSessionLocker(locker))
	if err != nil {
		return fmt.Errorf("init migrations: %w", err)
	}
	if _, err := provider.Up(ctx); err != nil {
		return fmt.Errorf("apply migrations: %w", err)
	}

	// River keeps its own schema for the job queue.
	migrator, err := rivermigrate.New(riverpgxv5.New(pool), nil)
	if err != nil {
		return fmt.Errorf("init job queue migrations: %w", err)
	}
	if _, err := migrator.Migrate(ctx, rivermigrate.DirectionUp, nil); err != nil {
		return fmt.Errorf("apply job queue migrations: %w", err)
	}
	return nil
}

// ErrSchemaBehind means `echoo migrate` has to run before the server can start.
var ErrSchemaBehind = errors.New("database schema is not up to date; run `echoo migrate`")

// CheckSchema verifies that all migrations are applied without changing anything, so the
// server can run as a role that is not allowed to alter the schema.
func CheckSchema(ctx context.Context, pool *pgxpool.Pool) error {
	sqlDB := stdlib.OpenDBFromPool(pool)
	defer func() { _ = sqlDB.Close() }()

	migrations, err := fs.Sub(sqlfiles.Migrations, "migrations")
	if err != nil {
		return err
	}
	provider, err := goose.NewProvider(goose.DialectPostgres, sqlDB, migrations)
	if err != nil {
		return fmt.Errorf("init migrations: %w", err)
	}
	pending, err := provider.HasPending(ctx)
	if err != nil {
		return fmt.Errorf("check migrations: %w", err)
	}
	if pending {
		return ErrSchemaBehind
	}

	migrator, err := rivermigrate.New(riverpgxv5.New(pool), nil)
	if err != nil {
		return fmt.Errorf("init job queue migrations: %w", err)
	}
	res, err := migrator.Validate(ctx, nil)
	if err != nil {
		return fmt.Errorf("check job queue migrations: %w", err)
	}
	if !res.OK {
		return ErrSchemaBehind
	}
	return nil
}

// InTx runs fn in a transaction and commits if fn returns nil. The rollback is deferred
// unconditionally so a panic in fn cannot leak the connection.
func InTx(ctx context.Context, pool *pgxpool.Pool, fn func(q *dbq.Queries) error) (err error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	defer func() {
		if rbErr := tx.Rollback(ctx); rbErr != nil && !errors.Is(rbErr, pgx.ErrTxClosed) {
			err = errors.Join(err, rbErr)
		}
	}()
	if err = fn(dbq.New(tx)); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

var migrationName = regexp.MustCompile(`^(\d+)_.*\.sql$`)

// ExpectedSchemaVersion is the newest migration this binary carries.
func ExpectedSchemaVersion() (int64, error) {
	entries, err := fs.ReadDir(sqlfiles.Migrations, "migrations")
	if err != nil {
		return 0, err
	}
	var newest int64
	for _, e := range entries {
		if m := migrationName.FindStringSubmatch(e.Name()); m != nil {
			v, err := strconv.ParseInt(m[1], 10, 64)
			if err != nil {
				return 0, err
			}
			newest = max(newest, v)
		}
	}
	return newest, nil
}

// AppliedSchemaVersion is the newest migration recorded as applied. It is one cheap query, for
// readiness checks; CheckSchema does the thorough comparison at startup.
func AppliedSchemaVersion(ctx context.Context, pool *pgxpool.Pool) (int64, error) {
	var v int64
	err := pool.QueryRow(ctx, `SELECT COALESCE(max(version_id), 0) FROM goose_db_version WHERE is_applied`).Scan(&v)
	if err != nil {
		return 0, fmt.Errorf("read schema version: %w", err)
	}
	return v, nil
}
