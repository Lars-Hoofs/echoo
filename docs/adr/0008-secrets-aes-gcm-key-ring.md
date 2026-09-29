# 8. Secrets at rest with AES-256-GCM and a key ring

Status: proposed

## Context
Mailbox passwords, OAuth tokens, webhook and TOTP secrets must be encrypted and keys rotatable.

## Decision
AES-256-GCM from the Go standard library, random nonce, AAD bound to table/column/row.
Keys come from env or a secret file as an ordered key ring (`id:key`); the first encrypts, all
decrypt. A rotation command re-encrypts old rows.

## Consequences
No extra crypto dependency. Losing the key file makes stored credentials unrecoverable (they can
be re-entered); the key file must be backed up separately from the database.
