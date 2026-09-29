# 1. Single binary, PostgreSQL as the only stateful dependency

Status: proposed

## Context
Target is 10–50 agents, self-hosted by small teams. Every extra service (Redis, broker, search
cluster) adds operations work, memory and attack surface.

## Decision
One Go binary serves HTTP, SSE and the embedded SPA, and runs background workers. PostgreSQL
holds data, the job queue (River), full-text search and realtime fan-out (LISTEN/NOTIFY).
Blobs go to a local volume or S3-compatible storage.

## Consequences
Simple deploy and backup (`pg_dump` + blobs). Horizontal scaling is possible later because
coordination already goes through Postgres (advisory locks per mailbox, River, NOTIFY), but it
is not a goal of the MVP.
