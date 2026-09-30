# Echoo architecture

Status: proposal, awaiting approval (step 1).

## 1. Shape of the system

Echoo is one Go binary plus one PostgreSQL database. The binary serves the API, the embedded
frontend, the SSE stream, and runs the background workers (IMAP sync, outbound mail, rules,
SLA timers, webhooks). There is no Redis, no message broker, no search cluster.

```
                       ┌────────────────────────────── echoo (single process) ─────────────────────────────┐
 Browser ──HTTPS──▶    │  net/http + chi                                                                   │
  (React SPA,          │   ├─ /            embedded SPA (go:embed), immutable hashed assets                 │
   go:embed)           │   ├─ /api/v1      JSON API (session cookie + CSRF, or bearer API token)            │
                       │   ├─ /api/v1/events  SSE stream (per user, filtered by policy)                     │
                       │   ├─ /render/...  sanitized mail HTML, served into sandboxed iframes              │
                       │   └─ /healthz /readyz /metrics                                                     │
                       │                                                                                    │
                       │  domain services: auth · policy · inbox · threading · rules · sla · search        │
                       │                                                                                    │
                       │  workers (River, Postgres-backed):                                                 │
                       │   imap.sync · mail.parse · mail.send · rules.apply · sla.tick · snooze.wake ·     │
                       │   webhook.deliver · retention.purge · attachment.scan · keys.rotate               │
                       │                                                                                    │
                       │  mailbox supervisors: one goroutine per mailbox (IMAP IDLE, poll fallback),        │
                       │  guarded by a Postgres advisory lock so only one instance syncs a mailbox          │
                       └───────────┬──────────────────────────────┬────────────────────┬───────────────────┘
                                   │ pgx pool                      │ IMAP/SMTP (TLS)     │ hardened HTTP client
                                   ▼                               ▼                     ▼
                             PostgreSQL                     mail providers        webhook targets,
                     (data, jobs, FTS, LISTEN/NOTIFY)                              ClamAV (optional)
                                   │
                         blob store: local volume or S3-compatible (raw .eml + attachments)
```

### Why one process
10–50 agents and a handful of mailboxes is small. A single process keeps deployment, memory
(<100 MB idle target) and failure modes simple. The design still allows running a second
instance later: jobs are coordinated by River through Postgres, mailbox sync is guarded by
advisory locks, and SSE fan-out goes through `LISTEN/NOTIFY`, not in-process memory only.
A `--workers=false` flag can split web and worker roles later without code changes; we do not
build that in the MVP.

## 2. Packages

Follows the requested layout, with a few additions where a concern needs a single owner.

| Package | Responsibility |
|---|---|
| `cmd/echoo` | `serve` (applies pending migrations under an advisory lock first; with the restricted database role of the Compose setup that is a no-op because `migrate` already ran), `migrate` (applies migrations and exits; needs the schema owner's role), `admin create-owner`, `genkey`, `healthcheck`; `admin rotate-keys` follows with the first encrypted mailbox credentials. |
| `internal/config` | Parse and validate `ECHOO_*` env vars and `*_FILE` secret files. Fails fast on startup. |
| `internal/db` | sqlc-generated code, pgx pool, transaction helper. No hand-written SQL strings elsewhere. |
| `internal/auth` | Passwords (argon2id), sessions, TOTP, rate limiting, API tokens, CSRF. |
| `internal/policy` | The only place that decides "may user X do Y on Z". Produces mailbox-scope sets used by every query. |
| `internal/keyring` | AES-256-GCM envelope for credentials; key ring with key IDs and rotation. |
| `internal/mail` | IMAP supervisor and fetch, MIME parsing, outbound MIME building, SMTP send, DSN parsing. |
| `internal/threading` | Pure functions: given a parsed message and lookup results, pick the conversation. Heavily tested. |
| `internal/sanitize` | HTML allowlist sanitation, link rewriting, remote-image blocking, phishing heuristics. |
| `internal/inbox` | Conversations, messages, notes, assignment, status, labels, snooze, trash, drafts, templates. |
| `internal/realtime` | SSE hub, presence and typing state, NOTIFY bridge. |
| `internal/rules` | Condition/action evaluation. Declarative JSON, no scripting. |
| `internal/sla` | Business-hours clock, due-time calculation, breach detection. |
| `internal/search` | Query parser (filters + free text) into sqlc queries with keyset pagination. |
| `internal/jobs` | River setup, job definitions, admin view of failed jobs and retry. |
| `internal/api` | HTTP handlers, request validation, error mapping, security headers, webhooks out. |
| `internal/mailauth` | OAuth2 for Google and Microsoft mailboxes: providers, encrypted authorization state (PKCE), XOAUTH2 SASL client, token source that refreshes under an advisory lock. See `docs/mail-oauth.md`. |
| `internal/sysmail` | System mail (invitations, password resets, notification email) through a mailbox or `ECHOO_SMTP_URL`; the `sysmail.send` job and the notification mail job. |
| `internal/netguard` | SSRF-safe HTTP client (dial-time IP checks). Used by webhooks and the image proxy. |
| `internal/storage` | Blob store interface with two implementations: filesystem and S3; both can also delete (`Deleter`, used for erasure and retention purges) and prove they accept writes (`Prober`, for `/readyz`); the filesystem store can also list itself (`Walker`, for the orphan scan). |
| `internal/contacts` | The CRM: contact and organization visibility (SQL), custom attribute validation, filters and segments (`ResolveSegment` for campaigns), CSV import job and safe CSV export, merge, GDPR export job and erasure, purge job for exports and import files. |
| `internal/campaigns` | One-off email campaigns: draft validation, recipient materialization from a segment (visibility of the user who starts it), the paced dispatcher job (`campaigns.tick`), pause/resume/cancel, unsubscribe tokens and the public unsubscribe logic, CSV report. See section 3.7. |
| `internal/audit` | Append-only audit writer. |
| `internal/retention` | Retention purges (closed conversations, attachments, spam, trash, audit log) and manual purges from the trash, with a dry-run preview, and the expired-upload sweep; batched, audited with counts, files removed through the reference-checked deletion queue. |
| `internal/ops` | The tools behind `echoo admin rotate-keys` and `echoo admin blobs`: key usage per column, re-encryption, and the database-versus-blob-store comparison. |
| `internal/metrics` | Prometheus metrics on a separate listener (`ECHOO_METRICS_ADDR`), request instrumentation and the periodic database-derived numbers. |
| `internal/logging` | slog handler that adds the request ID to every line logged with a request context and scrubs email addresses from values. |
| `web/` | React SPA. Built by Vite, embedded via `go:embed`. |

`internal/storage` is the only interface with two real implementations. Everything else is
concrete types; no repositories-over-sqlc layers.

## 3. Critical flows

### 3.1 Inbound mail (never lose, never duplicate)

```
IMAP IDLE/poll ─▶ FETCH new UIDs (> last_uid for this UIDVALIDITY)
   for each message:
     1. write raw bytes to blob store under sha256 (idempotent, content-addressed)
     2. INSERT raw_messages (mailbox, folder, uidvalidity, uid, sha256, ...)
          ON CONFLICT (mailbox_id, folder_id, uidvalidity, uid) DO NOTHING
        + enqueue mail.parse job            ── same transaction (River InsertTx)
     3. after the batch commits: UPDATE folder sync state last_uid
```

- The UID cursor only advances after the raw message and its parse job are committed. A crash
  between steps re-fetches, and the unique constraint absorbs the duplicate.
- A changed UIDVALIDITY triggers a full resync of that folder; duplicates are then caught at the
  Message-ID level (see data model: `messages` unique on `(mailbox_id, message_id_hash, raw_sha256)`).
- Echoo never deletes or moves mail on the IMAP server in the MVP. It only sets `\Seen` when
  configured. The mail server remains the source of truth and the backup of last resort.
- `mail.parse` is idempotent: it upserts by `raw_message_id`. Parse failures are kept as
  `raw_messages.parse_status = 'failed'` with the error, visible in the admin job view, and can
  be retried after a fix. The raw bytes are never discarded because parsing failed.
- IDLE is restarted every 25 minutes (RFC 2177 recommends < 29). If the server lacks IDLE, or
  IDLE fails three times, the supervisor falls back to polling every 60 s and retries IDLE later.
  Reconnects use exponential backoff with jitter, capped at 5 minutes; the current state per
  mailbox (`connected`, `polling`, `backoff`, `auth_failed`) is shown in settings.

### 3.2 Threading

Order of checks, implemented in `internal/threading` as a pure function over a lookup interface:

1. `In-Reply-To` and then `References` (newest first) matched against Message-IDs we know in the
   same mailbox, including IDs we generated for outbound mail and IDs only referenced so far
   (`thread_refs`). This also handles out-of-order arrival.
2. Echoo's own reply token: outbound Message-IDs have the form
   `<conv-{shortid}.{random}@{mailbox-domain}>`, so a reply from a client that drops `References`
   but keeps `In-Reply-To` still lands.
3. Fallback: normalized subject (strip `Re:`, `Fwd:`, `AW:`, `WG:`, `Antw:`, `Doorst:`, `SV:`,
   `[list-tag]`, ticket tags, whitespace, case) **plus** overlap of external participants,
   **within 30 days** of the last message in the candidate conversation, **and** only if the
   candidate is not closed for longer than 7 days. Otherwise a new conversation.
4. Never merge on subject alone. "Vraag", "Factuur" or an empty subject must not collide.

Known quirks with dedicated test fixtures: Outlook `Thread-Index`/`Thread-Topic` without
`References`; Gmail rewriting Message-IDs on send-as; mailing lists (`List-Id`, subject tags,
`Reply-To` munging); forwards (`Fwd:` starting a new thread by default, not joining the
original); auto-replies and out-of-office (`Auto-Submitted`, `X-Autoreply`, `Precedence`),
which are threaded but never trigger auto-replies back; changed subjects mid-thread; duplicate
Message-IDs from broken senders; missing Message-ID (we synthesize one from the raw hash).

### 3.3 Outbound mail

SMTP has no idempotency. We cannot promise exactly-once across a crash that happens after the
server accepted the message and before we recorded that. The design makes that window small,
detectable and never silently retried:

```
Agent sends ─▶ tx: INSERT messages (direction=out) + outbound (status=queued,
                   idempotency_key = client-generated UUID, unique) + enqueue mail.send
mail.send:   tx: UPDATE outbound SET status='sending', attempt=attempt+1
                 WHERE id=$1 AND status IN ('queued','retry')          ── claim
             SMTP DATA ... 250 OK
             tx: UPDATE outbound SET status='sent', smtp_response=...
             optional: IMAP APPEND to Sent folder
```

- Double clicks, retries by the browser, or API clients resending: absorbed by the unique
  `idempotency_key`.
- Temporary SMTP errors (4xx, network before DATA completes) move to `retry` with exponential
  backoff (30 s, 2 m, 10 m, 30 m, 2 h; max 6 attempts) and then `failed`.
- Permanent errors (5xx) go straight to `failed` with the server response shown on the message.
- A job that finds a row stuck in `sending` (crash between 250 OK and commit) does **not** resend.
  It searches the mailbox's Sent folder over IMAP for our Message-ID. Found: `sent`. Not found or
  no Sent folder: `uncertain`, shown to the agent and admin with a "Opnieuw versturen" action.
  A human decides; the system never guesses twice.
- Bounces: inbound DSNs (`multipart/report; report-type=delivery-status`) are parsed, matched to
  the original by `Original-Envelope-Id`/`Message-ID`, and set the outbound status to `bounced`
  with the diagnostic code. The DSN itself is kept but not shown as a customer message.
- Undo send: sends are delayed by a configurable 0–30 s (default 5 s) via `scheduled_at`; the
  agent can cancel while `queued`.

### 3.4 Realtime (SSE)

- One SSE connection per browser tab at `/api/v1/events`, authenticated by the session cookie.
- Events carry **IDs and types only** (`conversation.updated {id, version}`), never content.
  The client refetches through the normal API, so authorization is enforced in one place.
  The hub also filters events by the user's mailbox scope, so users do not learn that hidden
  conversations exist.
- Presence and typing: the client posts a heartbeat every 15 s (`viewing`, `typing`) for the
  open conversation. State lives in memory with a 30 s TTL and is fanned out through
  `LISTEN/NOTIFY` so a second instance would see it too. No presence writes hit tables.
- Heartbeat comment every 25 s keeps proxies from closing idle streams. The client reconnects
  with `Last-Event-ID`; missed events trigger a list refetch instead of a replay log.

### 3.5 Rules, SLA, snooze

- Exact semantics of rules, SLA clocks, business hours and auto-assignment: `docs/sla.md`.
- Rules are evaluated in `rules.evaluate` (queued in the ingest transaction) after a message is parsed and threaded, in a defined
  order (`position`), with a per-conversation loop guard (a rule's actions cannot re-trigger the
  same rule within one evaluation). Auto-replies are rate-limited per contact (max one per
  24 h per rule) and suppressed for auto-submitted mail and bulk/list mail.
- SLA due times are stored as timestamps on the conversation (`first_response_due_at`,
  `resolution_due_at`) and computed with business hours and holidays. A periodic job (every
  minute) marks at-risk and breached conversations and emits events; the UI renders live
  timers from the stored timestamps without polling.
- Snooze stores `snoozed_until`; a periodic job wakes due conversations. A new inbound message
  wakes a snoozed conversation immediately.

### 3.6 Public help center

`/hulp` is the one part of Echoo that anonymous visitors read (see SECURITY.md, section I). The
agent side lives in the SPA under `/kennisbank` and the API under `/api/v1/kb/…`. The public
side is Go: `internal/kb` holds the sanitizer, slugs, the feedback token and the embedded
templates and stylesheet (`internal/kb/site`); `internal/api/kb_public.go` serves them. Pages come
from a handful of indexed queries (`db/queries/helpcenter.sql`, GIN index on the article
`fts` column, Dutch plus simple configuration like message search) and render in a few
milliseconds with 1,000 articles; the tests keep the median under 50 ms. Responses carry an
ETag and, for articles, Last-Modified; a minute of `max-age` lets a CDN in front absorb load.

There is one portal per installation. To serve it on its own domain, let a reverse proxy map
`https://hulp.example.nl/hulp/…` (same paths) to Echoo and enter `hulp.example.nl` as custom
domain in Kennisbank, Beheer; it only changes the absolute URLs in canonical links, the sitemap,
`robots.txt` and the links agents insert into replies. Point the proxy's `/robots.txt` at Echoo too.

### 3.7 Campaigns

A campaign is a draft (name, mailbox, segment, subject, sanitized body, rate) that an admin, or a
user with `campaigns.manage`, starts now or schedules. Nothing is resolved until it starts.

- **Recipients.** The `campaigns.tick` job (every 5 seconds, and once right after a start or resume)
  advances every campaign that is due, sending, or still waiting for deliveries. For a campaign
  without recipients it resolves the segment with `contacts.ResolveSegment` using the visibility of
  the *user who started the campaign*, in one transaction, into `campaign_recipients` (`pending`, or `skipped` with a reason:
  `unsubscribed`, `no_address`, `bounced`, `duplicate`). A campaign whose segment was deleted or whose
  starter was deactivated is cancelled with `error` set; one on a disabled mailbox is paused.
- **One conversation per recipient.** The message is queued through `send.Enqueue` in its own
  conversation, created `closed` (so a campaign does not flood the open inbox) with a `created`
  event (reason `outbound`, which the reports already leave out of first-response times) and a
  `campaign` event naming the campaign. The message's Message-ID is registered in `thread_refs` as for
  every outbound message, so a reply threads into exactly that conversation, reopens it, and shows up
  in the inbox like any customer mail. A lightweight message record would have needed a second
  threading path; a closed conversation costs one row and works with search, contacts, SLA and
  webhooks as they are. The trade-off: reports that count new conversations include campaign mail.
- **Pacing.** Each run queues at most one tick's share of the rate (`ceil(rate * 5 / 60)`) and never
  more than what is left of the mailbox's rate over the last 60 seconds, counted over all campaigns
  on that mailbox by `campaign_recipients.queued_at`. The rate is capped by `ECHOO_CAMPAIGN_MAX_RATE`.
  The actual SMTP delivery, retries and backoff are the normal `mail.send` job.
- **Exactly once.** A recipient is claimed with `FOR UPDATE SKIP LOCKED` under the campaign's row lock
  and queued in the same transaction that marks it `queued`. The send-queue idempotency key is derived
  from campaign and recipient, and looked up before a new message is created, so a retry finds the
  earlier message instead of queueing another. `queued` recipients become `sent` or `failed` when the
  dispatcher copies the final outcome of the outbound row; an `uncertain` delivery is reported as
  failed and never resent. Cancelling skips pending recipients and stops messages still `queued` or
  `retry`; a message that is being delivered at that moment still goes out.
- **Compliance.** Every message carries `List-Unsubscribe` (https link), `List-Unsubscribe-Post:
  List-Unsubscribe=One-Click` and `Precedence: bulk`, never `Auto-Submitted`, plus a footer link. The
  headers are stored on the outbound row (`outbound.extra_headers`); the send package accepts only
  those three names. `/afmelden/<token>` shows a confirmation on GET and unsubscribes on POST, which is
  also the RFC 8058 one-click request. Unsubscribing sets `contacts.unsubscribed_at` for all future
  campaigns and is re-checked just before each message is queued. A permanent bounce marks the address
  (`contact_addresses.bounced_at`), and later campaigns skip it. DKIM-signing of the List-Unsubscribe
  headers, which Gmail and Yahoo expect for bulk senders, is up to the SMTP provider.

## 4. Frontend

- React + TypeScript strict, Vite, TanStack Router (file-less, code-defined routes with lazy
  route components) and TanStack Query.
- Route-level code splitting: inbox shell is the initial bundle; settings, dashboard, contacts
  and the rich text editor load on demand. Budget: initial JS ≤ 150 kB gzip, measured in CI.
- Conversation list: keyset pagination from the API, `@tanstack/react-virtual` for rendering,
  row height fixed (36 px compact / 52 px comfortable) so virtualization is cheap.
- Optimistic updates for status, assignment, labels, snooze, with rollback and a toast on
  conflict. Conversations carry a `version`; the API rejects stale writes with 409.
- Mail HTML is never injected into the app DOM. It renders in `<iframe sandbox>` loaded from
  `/render/messages/{id}` (see SECURITY.md).
- Charts are hand-written SVG components (bar, line, sparkline, stacked bar). Four chart types
  do not justify a charting library, and it keeps the palette and bundle under control.

## 5. Dependencies

Every dependency is attack surface; each one below replaces code we would otherwise have to
get right ourselves. Versions are pinned in `go.mod`/`package-lock.json` in phase 1.

### Go

| Dependency | Why |
|---|---|
| `github.com/go-chi/chi/v5` | Routing + middleware composition; stdlib `ServeMux` lacks route groups/middleware scoping. |
| `github.com/jackc/pgx/v5` | Fastest, maintained Postgres driver; needed for LISTEN/NOTIFY and COPY. |
| `sqlc` (build tool only) | Type-safe queries generated from SQL; nothing at runtime. |
| `github.com/pressly/goose/v3` | Migrations embedded in the binary, SQL files, supports Go migrations for data backfills. |
| `github.com/riverqueue/river` | Transactional job enqueue in the same Postgres transaction as the data; retries, backoff, unique jobs, periodic jobs. |
| `github.com/emersion/go-imap/v2` | IMAP client with IDLE, UIDPLUS, CONDSTORE. |
| `github.com/emersion/go-message` | MIME parsing and building, charset handling (same ecosystem as go-imap). |
| `github.com/emersion/go-smtp` + `go-sasl` | SMTP client with context-friendly deadlines, STARTTLS/implicit TLS, XOAUTH2 for phase 2. Chosen over `net/smtp` (frozen, no XOAUTH2). |
| `github.com/microcosm-cc/bluemonday` | Allowlist HTML sanitizer. |
| `golang.org/x/crypto` | argon2id. |
| `golang.org/x/net/html` | Tokenizer for link rewriting and text extraction (already pulled in by bluemonday). |
| `github.com/gabriel-vasile/mimetype` | Content sniffing over many more types than `http.DetectContentType`; no transitive deps. |
| `github.com/minio/minio-go/v7` | S3-compatible storage; much smaller than the AWS SDK. Only linked in, used when configured. |
| `github.com/VictoriaMetrics/metrics` | Prometheus exposition without the dependency tree of `client_golang`. |
| `github.com/go-webauthn/webauthn` | Passkeys (stretch goal, phase 7). |
| `github.com/testcontainers/testcontainers-go` | Test-only: real Postgres in integration tests. |

Deliberately not added: an ORM, a validation framework (explicit validation functions per
request type), a config library, a logging library (`slog`), a TOTP library (RFC 6238 is
~50 lines on `crypto/hmac`, tested against the RFC vectors), a UUID library (UUIDv7 via
Postgres 18 `uuidv7()` or a 20-line generator).

### Frontend

`react`, `react-dom`, `@tanstack/react-router`, `@tanstack/react-query`,
`@tanstack/react-virtual`, individual `@radix-ui/react-*` primitives (dialog, dropdown-menu,
popover, tooltip, tabs, select, checkbox, toggle-group), `cmdk` (accessible command palette,
built on Radix), `@tiptap/react` with a minimal extension set (the composer needs a real
editing model; contenteditable by hand is a bug farm), `tailwindcss` v4. Dev: `vite`,
`typescript`, `vitest`, `@playwright/test`, `eslint`. Icons: `lucide-react` (tree-shaken, only
imported icons ship), with status icons drawn as our own SVGs because they carry meaning.

## 6. Configuration

All via environment, validated at startup; secrets can come from `ECHOO_<NAME>_FILE`.

| Variable | Meaning |
|---|---|
| `ECHOO_DATABASE_URL` | Postgres DSN. |
| `ECHOO_LISTEN_ADDR` | Default `:8080`. |
| `ECHOO_BASE_URL` | Public URL; used for cookies (`Secure`), CSRF origin check, links in mail. |
| `ECHOO_ENCRYPTION_KEYS` | Key ring, e.g. `k2:base64...,k1:base64...`; first is active. |
| `ECHOO_STORAGE` | `fs:///data/blobs` or `s3://bucket?endpoint=...`. |
| `ECHOO_MAX_ATTACHMENT_MB` | Default 25. |
| `ECHOO_CLAMAV_ADDR` | Optional `clamav:3310` or `tcp://clamav:3310`: a clamd that scans inbound attachments and composer uploads. Empty turns scanning off. |
| `ECHOO_TRUSTED_PROXIES` | CIDRs allowed to set `X-Forwarded-For`; empty means ignore the header. |
| `ECHOO_OAUTH_GOOGLE_CLIENT_ID` / `_SECRET`, `ECHOO_OAUTH_MICROSOFT_CLIENT_ID` / `_SECRET` / `_TENANT` | OAuth2 mailboxes; a provider without credentials is hidden (`docs/mail-oauth.md`). |
| `ECHOO_SYSTEM_MAILBOX` or `ECHOO_SMTP_URL` | Sender of system mail: an existing mailbox, or a dedicated relay (`smtps://user:pass@host:465?from=...`). Neither set: no invitations or email reset, temporary passwords remain. |
| `ECHOO_CAMPAIGN_MAX_RATE` | Highest sending rate a campaign may ask for, in messages per minute per mailbox (1-1000, default 120). Set it to what the mail provider allows. |
| `ECHOO_OUTBOUND_ALLOWED_DOMAINS` | Empty (default) or a comma-separated list such as `example.com`: every delivery (replies, system mail, campaigns) to a recipient outside these domains fails permanently before a connection is made. For trial runs against live mailboxes. |
| `ECHOO_METRICS_ADDR` | Empty (default, off) or a listen address such as `127.0.0.1:9090` for the Prometheus endpoint, on a listener of its own. |
| `ECHOO_LOG_LEVEL` | `info` default. |

## 7. Operations

- `/healthz`: process alive. `/readyz`: DB reachable, schema at the version this binary carries,
  storage accepts a write (both cached 30 s, 5 s after a failure). The Docker healthcheck uses
  the `echoo healthcheck` subcommand (distroless has no curl).
- Graceful shutdown: stop accepting HTTP, close SSE streams, stop IMAP supervisors (logout),
  let River finish in-flight jobs up to 25 s, then exit.
- Logging: `slog` JSON, request ID on every line logged while handling a request (added by the
  handler from the request context), never message bodies or subjects, and email addresses in
  values are replaced by `[address]` by the handler as a last line of defense. Access log lines
  hold route patterns, never paths, queries or client addresses.
- Admin "Taken" view lists failed and retrying jobs from River with the error and a retry button.
- Backup: `scripts/backup.sh` (`pg_dump -Fc` plus the blob volume, timestamped, N copies kept) and
  `scripts/restore.sh`; for S3 storage the bucket is backed up with the provider's tools. Blobs
  are content-addressed and write-once, so a blob copy taken after the dump is consistent.
- Metrics: optional Prometheus endpoint on a separate listener; labels are route patterns,
  states, statuses and job kinds only. The full runbook (install, upgrade, key rotation,
  retention, DNS, reverse proxy, troubleshooting) is [operations.md](operations.md).

## 8. Performance plan

| Budget | How |
|---|---|
| 10k conversations list stays smooth | Keyset pagination on `(last_message_at DESC, id DESC)` per mailbox/status, virtualized rows, 50 rows per page, no `COUNT(*)` on the hot path (counts come from a small per-mailbox counter table updated in the same tx). |
| API p95 < 100 ms | Every list/search query has a matching index; `EXPLAIN (ANALYZE, BUFFERS)` output for the top 10 queries is recorded in `docs/queries.md` in phase 3 against a seeded 100k-message dataset. |
| Initial JS small | Route splitting, no chart lib, TipTap loaded only when the composer opens. |
| Idle RSS < 100 MB | One goroutine + one TLS connection per mailbox, bounded pgx pool (10), River worker counts bounded, no in-memory caches of message bodies. Measured in CI with a smoke test. |

## 9. Deviations from the brief, and why

1. **Package additions** (`policy`, `keyring`, `sanitize`, `netguard`, `realtime`, `audit`,
   `config`). Each owns one security-relevant concern; merging them into `auth` or `api`
   would blur who is responsible.
2. **Exactly-once sending is not achievable over SMTP.** The brief says "nooit dubbel versturen".
   The design guarantees no automatic resend when the outcome is unknown and makes the
   `uncertain` state visible. I would rather state this limit than hide it.
3. **No Postgres row-level security in the MVP.** RLS with a pooled connection needs
   `SET LOCAL` per transaction and makes sqlc queries harder to reason about. Instead every
   scoped query takes an explicit `mailbox_ids` parameter produced by `policy`, and IDOR tests
   cover every endpoint. RLS can be added in phase 7 as defense in depth.
4. **go-smtp instead of net/smtp or go-mail**, see dependencies.
5. **Full-text search uses two configurations** (`dutch` and `simple`). Customer mail at Dutch
   companies is often English; `simple` keeps English words, codes and order numbers findable
   without stemming them wrongly. Trigram indexes cover addresses and subjects.
