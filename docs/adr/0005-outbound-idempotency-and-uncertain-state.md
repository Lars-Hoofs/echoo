# 5. Outbound mail: idempotency key and an explicit "uncertain" state

Status: proposed

## Context
SMTP offers no idempotency. A crash after the server accepted a message but before we record
it leaves the outcome unknown. Blind retries risk double sends; blind giving up risks lost replies.

## Decision
Each send has a client-generated idempotency key (unique). A send job claims the row
(`queued|retry → sending`). Temporary failures retry with exponential backoff; permanent
failures stop. A row found in `sending` after a crash is checked against the Sent folder by
Message-ID; if not provably sent, it becomes `uncertain` and a human decides.

## Consequences
No automatic double sends. Rare `uncertain` messages need a human click; this is shown clearly
in the UI and the admin job view.
