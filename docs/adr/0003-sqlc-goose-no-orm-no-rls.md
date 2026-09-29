# 3. sqlc and goose; explicit scoping instead of row-level security

Status: proposed

## Context
Queries must be type-safe and parameterized; authorization must be provable with tests.

## Decision
SQL is written by hand in `db/queries`, compiled with sqlc; migrations are goose SQL files
embedded in the binary. Every query on tenant-visible data takes a `mailbox_ids` parameter
produced by `internal/policy`. Postgres RLS is not used in the MVP.

## Consequences
Scoping is visible in each query and covered by table-driven IDOR tests over every route.
RLS remains an option for defense in depth in phase 7; adopting it later requires
`SET LOCAL` per transaction.
