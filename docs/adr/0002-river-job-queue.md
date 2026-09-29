# 2. River as the job queue

Status: proposed

## Context
Inbound parsing, sending, rules, SLA ticks and webhooks must be retried, idempotent and visible
to admins. Enqueueing must be atomic with the data change that causes it.

## Decision
Use River (Postgres-backed). Jobs are inserted with `InsertTx` in the same transaction as the
domain change. Periodic jobs (SLA, snooze, retention) use River's periodic scheduler.

## Consequences
No outbox pattern or broker needed. Failed jobs are queryable for the admin "Taken" view.
We depend on River's schema and migrations, run from `echoo migrate`.
