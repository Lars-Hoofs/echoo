# Security

## Reporting a vulnerability

Please report vulnerabilities privately through GitHub Security Advisories on this repository.
Do not open public issues for security problems. We aim to acknowledge within 3 working days
and to publish a fix and advisory within 90 days.

## Threat model

Target: OWASP ASVS 4.0 level 2. This document is the threat model; it is updated whenever a
feature changes a trust boundary.

### Assets

| Asset | Why it matters |
|---|---|
| Customer mail (bodies, attachments, addresses) | Personal data under GDPR; often contains invoices, IDs, health or legal info. |
| Mailbox credentials and OAuth tokens | Give full access to the company mailbox, including mail Echoo never imports. |
| Agent accounts and sessions | Act as the company towards customers. |
| API tokens and webhook secrets | Programmatic access to all of the above. |
| Audit log | Evidence after an incident. |
| Encryption keys | Unlock all stored credentials. |

### Actors

- **External sender**: anyone on the internet who can send mail to a connected mailbox.
  Fully untrusted, controls headers, MIME structure, HTML, attachments, and timing.
- **Authenticated agent** with limited mailbox access: trusted to use the product, not trusted
  to see mailboxes outside their teams.
- **Readonly user**: must never mutate state.
- **Admin / owner**: trusted for configuration, but their actions are audited.
- **API client / webhook receiver**: acts with the rights of the token's user; receivers see
  only event payloads.
- **Network attacker**: between browser and Echoo, or Echoo and mail/webhook servers.
- **Host operator**: has the database and the environment. Out of scope as an adversary (they
  hold the keys), but we limit what leaks into logs and backups they may share.

### Trust boundaries

```
 [Internet senders] ──SMTP──▶ [Mail provider] ──IMAP/TLS──▶ ┐
 [Browser: agent] ──HTTPS (cookie+CSRF)──────────────────────▶│ echoo │──▶ [Postgres]
 [API client] ──HTTPS (bearer token)─────────────────────────▶│       │──▶ [Blob store]
                                   [Webhook receivers] ◀──────│       │──▶ [SMTP / ClamAV]
 [Sandboxed iframe: mail HTML] ◀── /render (opaque origin) ───┘
```

The sandboxed iframe is its own boundary: content inside it is treated as attacker-controlled
even after sanitization.

### Threats and mitigations (STRIDE per boundary)

#### A. Inbound mail (sender → Echoo)

| Threat | Mitigation |
|---|---|
| Stored XSS via HTML mail | Allowlist sanitizer (bluemonday) at render time with a versioned policy; no `script`, `iframe`, `object`, `embed`, `form`, `base`, `meta`, event handlers, `javascript:`/`data:` URLs (except `data:image/*` for inline images), no `style` attributes outside a small CSS property allowlist. Output served from `/render/messages/{id}` with `Content-Security-Policy: default-src 'none'; script-src 'sha256-<resize script>'; img-src 'self' data:; style-src 'unsafe-inline'; frame-ancestors 'self'` into `<iframe sandbox="allow-scripts allow-popups allow-popups-to-escape-sandbox">` without `allow-same-origin`. Only our hashed resize script can execute; injected scripts are blocked by CSP, and even a script that ran would sit in an opaque origin without access to the app, its cookies or API (see ADR 0006). |
| Tracking pixels / IP leak on open | Remote images blocked by default; `src` rewritten to a placeholder. Allowed per sender/domain; allowed images are fetched through `/render/proxy` using the SSRF-safe client, size-capped (5 MB), type-checked (image/* only after sniffing), cached by hash, so the sender never sees the agent's IP or user agent. Subresources of the rendered document (inline `cid:` images, proxied images) are requested by a cookie-less opaque origin, so they authenticate with an HMAC signature (key derived from the keyring, one hour expiry) that `/render/messages/{id}` only issues after its mailbox scope check; without a valid signature they answer 404. Plain `http://` image URLs are upgraded to https before proxying. |
| Phishing aimed at agents | Links rewritten to show the real host on hover and in a confirmation popover when text and `href` hosts differ; warnings for: display-name spoofing of a known contact or of the mailbox's own domain, `From`/`Reply-To` domain mismatch, punycode/mixed-script domains, failed `Authentication-Results` (SPF/DKIM/DMARC as reported by the receiving server; we do not re-verify). |
| Malicious attachments | Size limit (configurable, default 25 MB per file, 50 MB per message); content sniffing, declared type is only used for display; dangerous types (`.exe`, `.js`, `.html`, `.svg`, macro Office, archives with those) flagged; optional ClamAV scan (`ECHOO_CLAMAV_ADDR`, clamd `INSTREAM` over TCP; the address is operator-set, so internal hosts are allowed): inbound attachments are scanned by the parse job before they are stored as attachments and get `scan_status` `clean`, `infected` or `error` (`not_scanned` when no scanner is configured, and for files ingested before it was enabled); an `infected` attachment answers 403 `attachment_infected` on download and is not served as an inline image, the UI shows it as blocked; an `error` (clamd unreachable, no answer, or a file clamd refuses) does not block: the file stays downloadable and the UI warns that it was not scanned; clamd is given 5 s to connect and 30 s per file, and one message gets at most 90 s of scanning in total: attachments still waiting when the budget runs out are recorded as `error` without being sent, so a hanging clamd cannot make the parse job time out (this is fail-open by design, so an attacker who can stall clamd gets unscanned files through with the warning); clamd itself refuses streams above its `StreamMaxLength`, which defaults to 25 MB, so with a larger `ECHOO_MAX_ATTACHMENT_MB` the operator must raise `StreamMaxLength` (and `MaxFileSize`) in clamd.conf or those files end as `error`, and Echoo does not send files over 100 MiB at all; composer uploads are scanned synchronously and an infected file is refused (422 `attachment_infected`) and never stored, a scan error lets the upload through with a log line, and the sent copy is recorded as `not_scanned`; downloads served with `Content-Disposition: attachment`, `X-Content-Type-Options: nosniff`, `Content-Type` from sniffing or `application/octet-stream`, and `Content-Security-Policy: sandbox`. SVG is never rendered inline. |
| Parser DoS (zip bombs, deep MIME nesting, huge headers) | Max raw message size 50 MB (bigger messages are stored raw and marked `skipped`), MIME depth ≤ 20, parts ≤ 500, header block ≤ 256 kB, per-job timeout 5 minutes (at most 90 s of it for virus scanning, see the attachments row), decompression not performed on attachments. |
| Duplicate/replayed mail causing double processing | Idempotency on `(mailbox, folder, uidvalidity, uid)` and on Message-ID + content hash; rules are evaluated once per message (`rule_runs`). |
| Thread injection (a sender forges a reference to write into a conversation they should not reach) | Outbound Message-IDs are `echoo.<conversation>.<random>.<tag>@domain`, where the tag is the first 8 bytes of HMAC-SHA256 over conversation and random part, keyed by a key derived from the keyring (purpose `message-id`). The Message-ID token fallback in threading only accepts an id whose tag verifies, and only for a conversation in the same mailbox; untagged ids from before this change and forged ids are ignored as tokens (the tag verifies against keys derived from every configured keyring key, so rotation keeps old ids working until their key is removed; ids stored in `thread_refs` keep matching by hash either way). A reply that references one of our real Message-IDs is threaded even when its sender is a stranger, because customers forward mail to colleagues; the composer never follows that sender: `GET /conversations/{id}/reply-defaults` keeps To on the customer of the latest inbound message from a known participant (the contact's addresses, everyone we wrote to or from, and the parties of the first inbound message) and returns the newcomer, and any unknown addresses on their message, as `suggested_cc`, which the UI offers as an explicit "add to Cc" action. Satisfaction surveys are only sent to a known participant. Residual risk: an attacker who obtains a real outbound Message-ID (for example from a forwarded mail) can still add a message to that conversation; it is visible to agents as an unknown sender and cannot redirect replies. |
| Mail loops via auto-reply | Never auto-reply to `Auto-Submitted`, `Precedence: bulk/list/junk`, `List-Id`, `MAILER-DAEMON`, noreply addresses, or our own mailboxes; max one auto-reply per contact per rule per 24 h. |
| Header injection into outbound replies | Addresses parsed with `net/mail`, re-serialized by go-message; CR/LF rejected in any user-provided header value; subject encoded per RFC 2047. |

#### B. Browser ↔ Echoo

| Threat | Mitigation |
|---|---|
| Credential stuffing / brute force | argon2id with the OWASP parameters (19 MiB, t=2, p=1), at most 2 hashes in parallel so login bursts cannot blow the memory budget; hashes are upgraded transparently when parameters change. Minimum length 12, max 128 characters, no composition rules. Breach check against the HIBP k-anonymity API is planned for phase 7 (off by default, since it is an outbound call). Rate limits: 10 login/2FA attempts per IP per minute (in memory; IPv6 clients are bucketed by /64, since one subscriber controls a whole /64); per account, 10 consecutive failures (password or 2FA code) lock the account for 15 minutes, and the count starts again at 1 once a lock has expired. Every transition into locked is audited. The lock is decided at the moment it matters, not before waiting for a hashing slot: the session-issuing transaction re-reads the user under `SELECT ... FOR UPDATE` and refuses when the account is locked or when the verified password is no longer the current one (concurrent reset or change), and the queries that consume a TOTP step or a recovery code refuse locked accounts themselves. A login or 2FA attempt that was already in flight when the account locked therefore can neither succeed nor clear the lock. Password rehashing only replaces the hash that was verified. Temporary passwords (new accounts, admin resets) stop working after 72 hours, with the same generic error. Unknown, locked, expired and deactivated accounts get the same error and run a dummy hash so timing does not reveal them. |
| Session theft | Opaque 256-bit tokens, only the SHA-256 is stored. Cookie `__Host-echoo_session`, `HttpOnly; Secure; SameSite=Lax; Path=/` (plain `echoo_session` without `Secure` only for `http://localhost` development, which config enforces). New token on login, on completing 2FA (the pending session is revoked) and on password change (all sessions revoked). Role and deactivation are re-read from the database on every request, so they apply immediately without rotation; deactivation also revokes all sessions. 7 d idle / 30 d absolute expiry; a pending 2FA session lives 5 minutes and only reaches the 2FA and logout endpoints. "Alle andere sessies afmelden" and per-session sign-out; the sessions list shows browser and IP. |
| Missing 2FA | TOTP (RFC 6238) with ±1 step window and replay protection (the last accepted step is stored and must increase). 10 single-use recovery codes of 80 bits each, stored as SHA-256 (80 bits makes a slow hash unnecessary). Enabling 2FA requires the account password again (a stolen session alone cannot enroll an attacker's authenticator) and a code for the exact secret that was generated, and signs out other sessions. Admins can require 2FA for the whole workspace; users without it can then only reach account setup. Admins can reset another user's 2FA (lost phone), which is audited and signs that user out. |
| CSRF | `SameSite=Lax` plus a synchronizer token: a random per-session token, returned by login and `GET /me`, required in `X-CSRF-Token` on every non-GET request and compared in constant time. All non-GET API requests also need an `Origin` equal to `ECHOO_BASE_URL` (or `Sec-Fetch-Site: same-origin` when no Origin is sent), which also protects the login form against login CSRF. |
| API token abuse | Personal tokens (`ech_` plus 256 random bits, only the SHA-256 is stored; the first 8 characters are kept to recognise a token in the UI). A token acts as its user and never has more rights: role, deactivation and account-setup requirements are re-read on every request, and the `read` scope rejects every non-GET request. Bearer authentication applies only when an `Authorization` header is present; the session cookie is then ignored entirely, so a request that carries both uses only the token. Without a cookie there is nothing for a cross-site page to ride on (browsers cannot attach the header without a CORS preflight, and Echoo sends no CORS headers), so the Origin and CSRF checks are skipped only in that case; a cookie request without a valid CSRF token and Origin is still refused. Tokens cannot create or revoke tokens, change passwords or 2FA, or list and end sessions: those routes answer `session_required`. Expiry is optional (at most five years), revoking is immediate, and admins can list and revoke every token. 600 requests per token per minute, and 60 failed token lookups per client per minute, both with 429. `last_used_at` is written at most once per minute. Creating and revoking tokens is audited; the token itself never reaches the log. |
| XSS in the app itself | React escaping; `dangerouslySetInnerHTML` banned by lint rule with no exceptions (mail HTML only ever goes to the iframe). Strict CSP: `default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data: blob:; font-src 'self'; connect-src 'self'; frame-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'; object-src 'none'`. The production build has no inline scripts or `<style>` blocks; React, Radix and TipTap set styles through the CSSOM (`element.style`), which CSP does not block, so no `unsafe-inline` is needed. The only exception is a per-request nonce in `style-src`, handed to Radix through a meta tag, for the few `<style>` elements it injects for scroll locking. |
| Clickjacking | `frame-ancestors 'none'` and `X-Frame-Options: DENY` on the app; `/render` allows only `frame-ancestors 'self'`. |
| Transport | HSTS `max-age=63072000; includeSubDomains` when `ECHOO_BASE_URL` is https; TLS terminates at the reverse proxy (documented Caddy/Traefik examples). `Referrer-Policy: no-referrer` (mail links must not leak internal URLs), `Permissions-Policy` denying camera/mic/geolocation, `Cross-Origin-Opener-Policy: same-origin`, `Cross-Origin-Resource-Policy: same-origin`. |
| CORS abuse | No CORS headers at all. The SPA is same-origin; API clients are server-side. |
| Request smuggling / resource exhaustion | `http.Server` timeouts (read header 5 s, read 30 s, write 60 s, idle 120 s; SSE route exempt from write timeout but capped at 10 streams per user), body limit 1 MB for JSON, streamed upload limit per attachment, JSON decoder with `DisallowUnknownFields`. |
| Account takeover through emailed links | Invitation (72 h) and password reset (30 min) tokens are 256 random bits, stored only as SHA-256, single use (consumed by a conditional update), voided by a newer link and never logged or audited. Requesting a reset answers 202 whether or not the account exists and runs the same statements either way (lookup, invalidate, token insert matching no row, audit entry without target, one queued job that the worker drops when its message is empty), so neither response time nor database activity separates known from unknown addresses; it is limited per IP (429) and per account (silently, 3 per hour), and skips deactivated and invited accounts. Completing a reset ends every session, lifts the login lockout and does not sign in, so 2FA is still required. The link pages send `Referrer-Policy: no-referrer`. System mail jobs hold the message encrypted, because it contains the link. |
| Forged OAuth callback (login CSRF, code injection) | The `state` is encrypted and authenticated with the keyring (AES-256-GCM), expires after 10 minutes, is bound to the admin's session and provider, and carries the PKCE verifier (S256), so a state or code from another session or browser is refused. Only admins can start or finish a connection; reconnecting only accepts the mailbox's own account. |
| Spoofed client IP | `X-Forwarded-For` only honored from `ECHOO_TRUSTED_PROXIES`; private ranges are never trusted automatically. The flip side: behind a reverse proxy with an empty `ECHOO_TRUSTED_PROXIES`, every client shares the proxy's address, so one attacker exhausts the per-IP login limit for everyone and audit entries record the proxy. Echoo logs a warning at startup when the base URL is https and no proxy is trusted; setting it is part of the operator checklist. |

#### C. Authorization inside Echoo

| Threat | Mitigation |
|---|---|
| IDOR (reading another team's conversation by ID) | Every query that takes a conversation/message/attachment ID also takes the `mailbox_ids` scope from `policy`; not-found and not-allowed return the same 404. UUIDv7 IDs are not the defense, only a speed bump. |
| Privilege escalation | Central rules in `internal/policy`. What a user may do is a closed list of permission keys: the four built-in roles are presets (owner and admin hold all, agent and readonly what they always could), custom roles pick keys, and every check in a handler is `policy.Has(user, permission)`. Management routes are guarded by one table (`internal/api/permissions.go`) keyed by route pattern: a route missing from it is refused for everybody, and `authz_test.go` proves that each built-in role passes exactly the routes it passed before. Nobody grants what they do not hold, and roles granting a workspace-wide permission that can be turned into wider access, and the users holding them, are the owner's alone (`policy.Privileged`: `users.manage`, `settings.manage`, `teams.manage`, `mailboxes.manage`, `contacts.erase`, `contacts.import`, `webhooks.manage`, `automation.manage`); admins never manage admins, nobody changes their own role or the owner, and ownership is never assigned through role changes (a single-owner unique index backs this up). Custom roles never widen which mailboxes are visible: team access still decides that, and as defense in depth (for roles created before those permissions were privileged) a change of team members or mailbox access that would give the actor themselves a mailbox, or write access to one, they did not have is refused and rolled back unless the actor sees everything (`keepScope`, which first locks the actor's user row, before the team or mailbox row, so two concurrent requests of one actor cannot each pass and widen together). Workspace configuration that acts on data is scoped too: automation rules, their runs and per-mailbox automation settings are only manageable for mailboxes the actor can write (workspace-wide rules, rule ordering and the automation settings need an owner or admin), GDPR export and erasure need an owner or admin (the export job re-checks this for the requester and only exports conversations in the requester's mailboxes), a contact import never reads or overwrites a contact its owner cannot see (that row fails with `contact_not_visible`), `include_content` on a webhook needs an owner or admin, and so does changing the URL or events of a webhook that has `include_content` (owner or admin) or `allow_http` (owner) on, since that would send its reach to a destination the actor chose. An auto-reply rule may only use a canned response the actor can use (422 `actions`), and the engine only sends global templates or ones of the conversation's own mailbox. Inviting a user into teams needs `teams.manage` or an owner or admin, the assignment page shows non-admins only their writable mailboxes and no agents, and agent capacity is for owners and admins. A contact import does not attach an existing organization its owner cannot see. The target user is loaded under a row lock and authorized inside the same transaction that changes it, so a concurrent role change cannot slip between check and update. Role and permission changes apply on the next request and are audited. |
| Single sign-on abuse (OIDC) | Authorization code flow with PKCE (S256), `state` and `nonce` kept in an AES-GCM encrypted cookie (`__Host-echoo_sso`, `HttpOnly; Secure; SameSite=Lax`, 10 minutes, deleted on first use), so only the browser that started the flow can finish it. The ID token is verified with go-oidc (signature against the provider's key set, issuer, audience, expiry) plus the nonce; the email must be `email_verified` (a missing claim is only accepted when the owner enables it for providers such as Entra ID), match an active account or a provisioned one in an allowed domain, and deactivated or pending-invitation accounts are refused. Sessions are issued exactly as for a password login. Every failure is audited with a reason code and never with provider text or tokens. Discovery and token requests go through the `netguard` dialer with a 10 s timeout and a 1 MiB response cap; plain-HTTP or internal issuers need the owner-only `allow_internal_issuer`, and a discovery document that advertises plain-HTTP endpoints is refused. The client secret is encrypted with the keyring and write-only. "SSO required" turns off password login for everybody but the owner (break-glass) and also blocks invitation acceptance; the provider's MFA only replaces Echoo's 2FA when the owner opts in and the provider reports `mfa`, `otp` or `hwk`; turning that option off revokes every session that was established on the provider's MFA and audits how many. Only the owner configures SSO, and provisioned users never get a role that grants access control. |
| Leaks via side channels | SSE events filtered by scope and content-free; search results scoped in SQL, not filtered afterwards; counts per mailbox only for visible mailboxes; mentions of a user who lacks access to the mailbox are rejected. |
| Rule/template abuse | Rules can only act within their mailbox; "forward to external address" is an admin-only action with an allowlist of domains. Template variables are a fixed allowlist, HTML-escaped. |
| Contacts and organizations visible beyond a user's mailboxes | Visibility is decided in SQL (`contact_visible`, `organization_visible`, migration 00015), never after the fact: a contact is visible to a user when they can read at least one (not deleted) conversation of that contact; a contact without conversations is visible only to the user who created it; admins see every contact. An organization is visible when one of its contacts is, or, without contacts, to its creator. The rule applies to list, search, filters, segments, detail, notes, timeline, merge, edit, CSV export and counts; `conversation_count` and conversation lists only include readable conversations. Contacts and organizations the caller may not see answer 404, like conversations. Segments are personal (owner only) or shared; a shared segment still resolves per viewer, so it never widens what someone sees. |
| Bulk changes to contacts | CSV import and custom field definitions are admin-only, because a dedupe-by-email import can update contacts the importer cannot see. Merge needs both contacts visible to the caller. Everything is audited (`contact.created`, `contact.updated`, `contact.merged`, `contact.import_started`, `contact.exported`, `custom_attribute.*`, `contact_segment.*`) with IDs and counts, never names or addresses. |
| Spreadsheet formula injection | Every CSV Echoo writes (contact export, import error report) prefixes cells that start with `=`, `+`, `-`, `@`, tab or carriage return with an apostrophe. Imported files must be UTF-8, at most 20 MB and 50000 rows, are parsed with the standard library, and every value is validated (email syntax, attribute types, lengths) before it reaches the database. Filters and segments become SQL only through a fixed grammar with bound parameters; attribute keys are slugs that are validated against the definitions and bound as values. |
| Reports leaking other teams' numbers | Report queries take the mailbox scope from `policy` in SQL (`mailbox_id = ANY(...)`), never filter afterwards; a `mailbox` filter outside the scope answers 404 like any other resource. Owners and admins see all mailboxes and all agents; everyone else sees only readable mailboxes and, in the per-agent tables (reports, satisfaction, live), only their own row, and gets 403 when filtering on another agent, so per-person performance is not visible to colleagues. Totals over a readable mailbox include everyone's work in it. Exports use the same code path and are written to the audit log (`report.exported`), so the CSV cannot show more than the page. CSV cells that start with `=`, `+`, `-`, `@`, tab or CR get a leading apostrophe. Report routes are limited to 120 requests per user per minute; the aggregate queries run with a service-wide cap on concurrent queries so they cannot starve the inbox of database connections. |
| Composer input (replies, notes, templates, signatures) | Editor HTML is sanitized on the server with an allowlist (p, br, strong, em, u, s, links to http/https/mailto, lists, quotes, code, h1-h3; `cid:` images only when they reference the sender's own upload) before it is stored or sent, so a crafted request cannot put markup into outgoing mail. The branded layout escapes the mailbox name and the workspace footer. Uploads get their type from their content, are limited per file (`ECHOO_MAX_ATTACHMENT_MB`, 25 by default) and per message (50 MB), belong to the uploader and expire after 24 hours; another user's upload id is reported like a missing one. Recipients are parsed as plain addresses (no line breaks, at most 50), and an idempotency key can only be replayed by the author of the message it created. Undo send is limited to the author. |

Required tests: `internal/api/authz_test.go` walks the router and fails when a route is not
classified in its access table, then checks every route anonymously (401) and as agent and
readonly against admin routes (403). From phase 2 on the same table covers mailbox scoping: an
agent without access to mailbox B must get 404 on every B resource.

#### D. Secrets at rest

- Mailbox passwords, OAuth tokens, webhook secrets and TOTP secrets are encrypted with
  AES-256-GCM (Go stdlib), random 96-bit nonce, AAD = `table.column.row_id`. Volume is low
  (thousands of encryptions, far below GCM nonce limits).
- Keys come from `ECHOO_ENCRYPTION_KEYS` or `ECHOO_ENCRYPTION_KEYS_FILE` as `id:base64(32 bytes)`
  entries; the first is active for writes, all are accepted for reads. `echoo admin rotate-keys`
  re-encrypts every value still on an old key ID (mailbox secrets, OAuth tokens, 2FA and webhook
  secrets, the SSO client secret), is idempotent, leaves values it cannot read untouched and
  reports them, and ends with a check per retired key; the owner sees the count per key under
  Privacy en retentie and in `GET /api/v1/admin/keys`. The procedure, and what removing an old
  key invalidates (survey links, Message-ID tags and knowledge base feedback tokens are signed
  with a key derived from the active one and verified against all configured keys), is in
  `docs/operations.md`. Rotations are audited (`keys.rotated`).
- Secrets are write-only in the UI and API: after saving, only "ingesteld" and the last change
  date are shown. `slog` values of secret types implement `LogValue()` returning `[REDACTED]`.
- Password hashes, session and token hashes are not encrypted (they are already one-way).

#### E. Outbound requests (Echoo → internet)

| Threat | Mitigation |
|---|---|
| SSRF via webhooks or image proxy | `netguard` client: resolves the host, rejects loopback, private (RFC 1918, RFC 4193), link-local (incl. 169.254.169.254), CGNAT, multicast, unspecified, documentation, benchmarking (198.18.0.0/15), reserved (192.0.0.0/24, 240.0.0.0/4) NAT64 (64:ff9b::/96, 64:ff9b:1::/48) and site-local (fec0::/10) ranges, and IPv4-mapped, IPv4-compatible (::/96), 6to4 (2002::/16) and Teredo (2001::/32, server and client address) IPv6 that carry one of those; checks happen in `net.Dialer.Control` on the actual connected IP (defeats DNS rebinding); a webhook URL is checked when it is saved (early feedback); its scheme and port are checked again before every delivery (a row that no longer passes fails without retry as `url_not_allowed`) and its destination address on every connection. Webhooks accept only `https` on ports 443 and 8443; plain `http` (also port 80) needs the per-webhook `allow_http` flag, which only the owner can set, and never lifts the internal-network rule. Redirects are not followed (a 3xx counts as a failed delivery), no proxy is used, 10 s total timeout, the response body is read up to 64 KB and discarded, and delivery errors are stored as codes, never as Go error text, which contains the URL. |
| Webhook forgery at receivers | `Echoo-Signature: t=<unix>,v1=<hex HMAC-SHA256 of "t.body">` with a per-webhook secret (generated by the server, shown once, stored encrypted with the keyring, bound to its row by AAD). Receivers should reject timestamps older than five minutes. |
| Webhook data exposure | Payloads are built from the database when they are delivered and hold IDs, statuses and timestamps only; subject, preview, names and addresses are added only when an owner or admin (someone who sees every mailbox) turned on `include_content` for that webhook. The outbox stores event IDs, no content. Deliveries are logged for 14 days (status code, duration, attempt, error code, no bodies). A webhook is disabled after 50 consecutive failed attempts, which is audited. |
| SMTP/IMAP downgrade | TLS required (implicit or STARTTLS; STARTTLS failure is fatal, no plaintext fallback). Certificate verification on; an admin may add a custom CA, never "skip verify". |
| Mailbox host pointing at internal services | IMAP/SMTP hosts are admin-configured; admins are trusted here, but the connection test uses the same private-range rules unless "interne mailserver toestaan" is enabled, which is audited. A stored password only follows the server it was saved for: the IMAP password is reused only while host, port, TLS mode and username of the IMAP side are unchanged, the SMTP password likewise for the SMTP side (`checkSecrets`), on the connection test and on save, so changing one of them without typing the password again is a 422 and stored secrets cannot be sent to a server the editor chose. A connection test that used a stored password is audited as `mailbox.stored_secret_tested`. The rule is also enforced at connection time for every IMAP sync, SMTP delivery, Sent-folder check/APPEND and probe: `netguard.Dialer` rejects blocked IPs in `net.Dialer.Control` on the address actually connected to, so a hostname that later resolves to an internal address (DNS rebinding, changed record) fails as a connection error (`destination address is not allowed (internal network)`) instead of being dialed. |
| SSRF via Web Push endpoints | A browser subscription names the URL Echoo posts to, so registration accepts only `https` endpoints without port or credentials on the known push services (`fcm.googleapis.com`, `updates.push.services.mozilla.com`, `*.push.apple.com`, `*.notify.windows.com`), checked again before every push; requests go through the `netguard` dialer and never follow redirects. APNs and FCM hosts are fixed; an FCM key file whose `token_uri` is not Google's is refused, so the signed assertion never goes elsewhere. |
| Push content exposure | Web Push payloads are encrypted to the browser (RFC 8291) and hold the kind, conversation number and subject. APNs and FCM payloads, which Apple and Google can read, hold the kind and conversation number only. No message text in any push. A device registration belongs to its session: revoking the session (logout, "log out everywhere", admin) deletes it in the same transaction (trigger), and deactivated users get no pushes. |

#### F. Supply chain and runtime

- Go modules and npm packages pinned (`go.sum`, `package-lock.json`, `npm ci`); Dependabot or
  Renovate with grouped updates; `govulncheck`, `npm audit --audit-level=high`, and an image
  scan (Trivy or Grype) gate CI; CycloneDX SBOM attached to releases; images signed with cosign.
- Image: multi-stage, `gcr.io/distroless/static-debian13:nonroot`, static binary
  (`CGO_ENABLED=0`), runs as UID 65532, `read_only: true` root filesystem, `cap_drop: [ALL]`,
  `no-new-privileges`. Phase 1 needs no writable mount at all; a data volume is added with
  blob storage.
- Postgres in Compose is not published to the host and sits on an internal network. Three
  roles exist (`docker/postgres-init/01-roles.sh`, created when the volume is first
  initialised): `postgres`, the bootstrap superuser, which only the init script uses; `echoo`,
  a non-superuser that owns the database and every table and is used only by the one-shot
  `migrate` service (`echoo migrate`); and `echoo_app`, the role the server runs as. `echoo_app`
  owns nothing and has only SELECT/INSERT/UPDATE/DELETE (and sequence usage) on the tables
  `echoo` creates. The server container is never given the owner's or the superuser's
  credentials. Consequences: an SQL injection or a compromised server process cannot alter or
  remove tables, create objects, disable or replace the `audit_log` triggers, or turn off
  trigger firing (`session_replication_role` needs superuser).
- GitHub Actions pinned by commit SHA, minimal `permissions:`.

#### H. Public survey page (customer → Echoo)

The satisfaction survey (`/tevredenheid/<token>`) is the only page an unauthenticated visitor can
use to write data. It is deliberately tiny: server-rendered HTML, one inline stylesheet, no script,
no cookies, no third-party requests, no analytics.

| Threat | Mitigation |
|---|---|
| Guessing or enumerating surveys | The token is `conversation id + expiry` under an HMAC-SHA256 with a key derived from the encryption keyring (verified against every configured key, so links survive a rotation until the old key is removed), checked before any database lookup, so a forged token learns nothing about which conversations exist. It must also match the SHA-256 stored with the survey. Invalid and unknown tokens both answer 404; a genuine token past its 30 days answers 410. 30 requests per client per minute (in memory, per IP or IPv6 /64), then 429 with a readable page. |
| Ratings from mail scanners and link previews | The links in the email only open the page (GET records nothing); the rating is stored by a POST from the page's own form. |
| CSRF and cross-site posting | The form carries a value bound to the token (HMAC), compared in constant time, and the POST must carry `Sec-Fetch-Site: same-origin`, or, from browsers that do not send it, an `Origin` equal to `ECHOO_BASE_URL` (under `Referrer-Policy: no-referrer` browsers send `Origin: null` on form posts, so Origin alone would refuse real customers). There is no cookie to ride on. |
| Stored XSS through the comment | The comment is plain text (control characters rejected, 1000 characters), escaped by `html/template` on the page and by React in the app. The page's CSP is `default-src 'none'; font-src 'self'; style-src 'sha256-<its stylesheet>'; form-action 'self'; frame-ancestors 'none'; base-uri 'none'`, with no script source at all. |
| Altering or flooding ratings | One answer per conversation; it can be changed for 7 days after the first answer, then the row is locked in SQL. Bodies are limited to 16 kB. |
| Tracking of customers | Links are unique per conversation (needed to attribute the rating) but the mail has no pixel, the page loads nothing external, sends no referrer and is `noindex, no-store`. The comment and rating are personal data of the contact and go with a GDPR erase of the conversation. |
| Mail loops and abuse of the survey mail | Sent once per conversation, as an automated message (`Auto-Submitted`), never in reply to automatic mail, bounces, bulk mail, no-reply or own addresses. |

#### I. Public help center (visitor → Echoo)

The knowledge base is served without login under `/hulp` (pages, search, `sitemap.xml`,
`robots.txt`, article images, a feedback form). Only published articles are ever readable; drafts
and archived articles answer 404, exactly like unknown addresses. Pages are server-rendered by
`html/template`, carry no script and set no cookies. The CSP is `default-src 'none'; style-src 'self';
font-src 'self'; img-src 'self'; form-action 'self'; frame-ancestors 'none'; base-uri 'none'`, and the stylesheet is served under a
content-hash name.

| Threat | Mitigation |
|---|---|
| Stored XSS through an article | Agents may write articles, so their HTML is untrusted for the public: it is reduced by a bluemonday allowlist on write and again on render (headings, text, lists, code, tables, links to `http(s)`, `mailto:` and `/hulp/…`, images only from `/hulp/i/<uuid>`, no classes or styles). Titles, category names, snippets and the search query are escaped by the template; search snippets are built from markers and escaped part by part, never marked up by the database. |
| Reading drafts or private images | Public queries filter on `status = 'published'`. An image is public only while a published article references it; before that only a signed-in session can fetch it. Uploads are limited to PNG, JPEG, GIF and WebP by content, 5 MB; SVG is refused. |
| Feedback and view-count stuffing | Feedback is a form POST with an HMAC token bound to the article and valid for two days, 30 posts per client per hour and 2 per article. Views count once per client and article per 30 minutes. Both are in memory per IP (IPv6 by /64), so they need `ECHOO_TRUSTED_PROXIES` behind a proxy. Search is limited to 60 requests per client per minute. The counters are plain numbers and identify nobody. |
| Redirect abuse | Only slugs an article really had redirect (301), always to `/hulp/a/<current slug>`; old slugs stay reserved so another article cannot take them over. |
| Wrong host in canonical links | The optional custom domain is validated as a host name and only used to build absolute URLs; it never decides what is served. |
| Crawlers finding the app | `/robots.txt` allows `/hulp` and disallows everything else. |
| Editing rights | Owners, admins and agents write articles; only admins manage the portal and categories; readonly users see published articles. Publishing, unpublishing, archiving, deleting, category and portal changes are audited. Revisions (last 20) make an edit reversible. |

#### J. Campaigns and the unsubscribe page

Campaign mail goes to many people at once, so the risks are sending too much, sending to people who
opted out, and the one public write endpoint it adds (`/afmelden/<token>`, same static, script-free page
and CSP as the survey).

| Threat | Mitigation |
|---|---|
| A user mails contacts they may not see | Recipients are resolved with the visibility (`contact_visible`, mailbox scope) of the user who starts the campaign, stored as `campaigns.started_by`, not of whoever wrote the draft. Start re-checks that the segment is the starter's own or shared (422 `segment_id` otherwise), and materialization checks it again; a starter who is deactivated before a scheduled campaign runs cancels it. A campaign is only visible and changeable on mailboxes the user can read and write. Managing campaigns needs `campaigns.manage`. |
| A user reads recipients they may not see | The recipient list and the CSV report contain only recipients whose contact the reader may see (`contact_visible` with the reader's own scope); owners and admins see all, and a recipient without a contact (erased or merged away) is for them only. Counts on the campaign are aggregates and include everyone. |
| Mailing someone who opted out | `contacts.unsubscribed_at` is checked when recipients are resolved and again right before each message is queued; unsubscribing wins over every other skip reason. Every message has `List-Unsubscribe`, `List-Unsubscribe-Post` (RFC 8058) and a footer link. Hard bounces mark the address so later campaigns skip it. |
| Guessing or forging unsubscribe links | The token is `recipient id + expiry` under an HMAC-SHA256 keyed with a per-recipient random secret stored in the database (not derived from the encryption keys, so rotation cannot break old mail), checked in constant time before the expiry. Invalid, tampered and unknown tokens answer 404 with the same page; a genuine token past its two years answers 410. 60 requests per client per minute, then 429. The masked address is shown, never the full one. |
| Scanners and previews unsubscribing people | GET only shows the form; the POST unsubscribes. The POST has no CSRF or origin check because RFC 8058 clients send none: the token is the credential, and a forged request needs it and does what its holder wants. Repeating it changes nothing and is audited once. |
| Flooding a provider, or the mailbox being blocked | Rate per campaign, capped by `ECHOO_CAMPAIGN_MAX_RATE`, and enforced as a sliding minute over all campaigns of a mailbox; pause and cancel take effect within one tick. |
| Header injection through campaign data | Only `List-Unsubscribe`, `List-Unsubscribe-Post` and `Precedence` may be added to a message, values with CR, LF or NUL are refused, and contact names are HTML-escaped in the body and refused in the subject when they contain line breaks. Template placeholders are limited to a fixed list and never left in the mail. |
| Formula injection in the report | The CSV export goes through the same writer as the contact export, which prefixes cells that start a formula. |
| Personal data in the report | Address and name in `campaign_recipients` are snapshots that are blanked when the contact is erased; the report export and every campaign action is audited. |

#### G. Repudiation and privacy

- Audit log for: logins, failed logins, 2FA changes, session
  revocation, role/team changes, mailbox create/update/delete, credential changes, rule and
  webhook changes, API token create/revoke, exports, GDPR erasures, permanent deletes, key
  rotation, report exports, and settings changes. Retention default 1 year. Team membership changes record the
  IDs added and removed, not just a count.
- The audit log is append-only against the application: triggers reject row changes and
  truncation, and the role the server connects as cannot remove them (see F). The single way to
  shorten it is the `audit_log_purge` database function (`SECURITY DEFINER`, owned by the schema
  owner): it never touches entries younger than 60 days, writes an `audit.purged` entry with the
  count of each batch before deleting it, and the trigger only accepts a delete from inside it
  (the owner is checked with `current_user`, so `echoo_app` cannot set the flag itself). A
  compromised server can therefore at worst trim history older than 60 days, the function's
  floor, and every trim is itself on record. It is not
  tamper-evident against anyone holding the `echoo` owner or `postgres` credentials, which
  live in `./conf` on the host; there is no hash chain. Operators who need that should ship the
  log to a system the host cannot rewrite. A hand-written setup that runs the server with an
  owner-level or superuser `ECHOO_DATABASE_URL` loses the protection against a compromised
  server; the Compose setup does not.
- Logs contain no mail content, no subjects and no addresses; request logs record route
  patterns, not raw paths with query strings or client addresses. Every line logged while
  handling a request carries its `request_id` (added by the log handler from the request
  context), and the handler replaces email addresses in string and error values with
  `[address]`, because mail servers quote recipients in their error messages.
- Retention settings, workspace-wide or overridden per mailbox (Privacy en retentie, needs
  `settings.manage`): delete closed conversations after N months, delete attachments (and the raw
  copy that contains them) after N months, spam after N days, audit log after N months (default
  12). A daily job deletes in batches of 1000 with `SKIP LOCKED`, audits every batch with counts
  (`retention.purged`, no names or addresses), removes files only through the reference-checked
  deletion queue, and leaves conversations with mail still waiting to be sent alone. A dry-run
  preview counts what a setting would delete before it is saved. Composer uploads that were
  never attached are purged after 24 hours, files included. Backups are outside the purge: they
  keep the data until they age out.
- `echoo admin blobs --check` finds referenced files missing from the store and, on filesystem
  storage, files nothing refers to; `--delete-orphans --yes` removes the latter through the same
  reference-checked queue and is audited.
- GDPR export and erasure per contact, both for owners and admins only (they need `contacts.erase` and see every mailbox; the export job checks this again for the requester) and audited:
  - Export ("Gegevens exporteren") is a background job that builds a ZIP with `contact.json`
    (profile, organization, notes, attributes, every conversation with its message text) and the
    attachment files. Only the requesting admin can download it, once; the file is deleted right
    after the download, or after 24 hours, whichever comes first.
  - Erasure ("Contact wissen", typed confirmation of the primary address) replaces the contact's
    name and address on messages with a tombstone, deletes bodies, subjects, attachments, raw
    `.eml` copies, drafts, thread references and delivery responses of conversations in which the
    contact is the only external party (the conversation and message rows stay as empty shells
    for statistics), drops the raw copies of other messages that name the contact, blanks the satisfaction survey
    comments of all the contact's conversations, removes the
    contact, its addresses, notes and earlier exports, the address-keyed auto-reply and image
    allowlist rows and undelivered webhook outbox events, and writes an audit entry with counts
    only. Search vectors are generated from the blanked columns, so the text is no longer
    searchable; webhook payloads are built from the database at delivery time, so they carry
    only IDs afterwards.
  - Files are content-addressed and shared, so a blob is deleted only when no attachment, raw
    message, upload, image cache entry, import or export refers to it any more. Deletions are
    queued in the same transaction and carried out afterwards; a failed deletion stays queued
    and is retried hourly.
  - Limits: a message that a shared conversation keeps still contains whatever the other party
    quoted; a later mail from the same address creates a new contact (erasure is not a block
    list); import error reports (kept a week) hold the rows that failed, which may include the
    erased contact. Backups age out on the operator's schedule; erasure cannot reach existing
    backups, so a restored backup must be followed by re-running the erasures of the audit log.

### Residual risks (accepted, documented)

1. **SMTP delivery outcome can be unknown** after a crash at the wrong moment. Handled by the
   `uncertain` state and human decision, not by automatic resend.
2. **The operator can read everything.** Encryption at rest protects against DB-dump leaks
   without the key file, not against the host.
3. **Sanitizer bypasses** are possible in principle; the hash-only CSP and the opaque-origin
   sandboxed iframe are the second and third layers.
4. **Authentication-Results trust**: phishing warnings rely on the receiving provider's
   headers, which we only trust from the first `Authentication-Results` with the provider's
   authserv-id (configurable per mailbox).
5. **Readonly users can still see personal data**; they are for auditors/managers, not for
   anonymous access.

### Out of scope

Compromised host, database superuser or the schema owner's credentials, compromised mail provider, malicious admin/owner
(mitigated only by audit), physical access, denial of service beyond per-request limits
(put Echoo behind a reverse proxy with connection limits).

## Operator checklist

- Serve only over HTTPS behind a reverse proxy; set `ECHOO_BASE_URL` to the https URL.
- Set `ECHOO_TRUSTED_PROXIES` to the address range your proxy connects to Echoo from. Without
  it all users share one login rate-limit bucket and audit entries show the proxy's address
  (Echoo warns at startup).
- Keep the database credentials separate: the server gets only `conf/database_url`
  (`echoo_app`); `conf/migrate_database_url` (owner) and `conf/db_admin_password` (superuser)
  are for the migrate service and bootstrap and should not be copied to other systems.
- Hand out temporary passwords promptly; they expire after 72 hours. Nobody manages the owner
  in the UI; to recover the owner (expired temporary password, lost password) run
  `docker compose exec echoo /echoo admin reset-password --email <owner>` on the host. It is
  audited and signs out all of that account's sessions.
- Store `ECHOO_ENCRYPTION_KEYS` in a secret file, not in `docker-compose.yml`; back it up
  separately from database backups.
- Back up with `scripts/backup.sh` (`pg_dump -Fc` plus the blob volume), keep the copies
  encrypted and off the host, keep `conf/encryption_keys` apart from them, and test a restore
  quarterly with `scripts/restore.sh` (`docs/operations.md`).
- Do not publish the metrics port: `ECHOO_METRICS_ADDR` opens an unauthenticated listener, meant
  for loopback or a private network. Its labels never hold user data, but the numbers describe
  the workspace.
- Configure SPF, DKIM and DMARC for every connected domain (see `docs/operations.md`).
- Require 2FA for all users in settings.
