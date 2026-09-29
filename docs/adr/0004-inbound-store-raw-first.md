# 4. Inbound mail: store raw first, advance the cursor last

Status: proposed

## Context
No customer mail may be lost or stored twice, across crashes, reconnects and UIDVALIDITY changes.

## Decision
For each fetched message: write raw bytes to content-addressed blob storage, insert
`raw_messages` with a unique `(mailbox, folder, uidvalidity, uid)` and enqueue the parse job in
one transaction, then advance `last_uid`. Parsing is a separate idempotent job. Echoo never
deletes or moves mail on the IMAP server.

## Consequences
Re-fetches are harmless. Parser bugs never lose mail; failed parses can be retried after a fix.
Blob storage holds a full copy of every message, which must be included in backups and retention.
