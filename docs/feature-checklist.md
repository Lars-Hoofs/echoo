# Feature checklist (target: Chatwoot feature parity for email)

Source: https://www.chatwoot.com/features (fetched 2026-09-28). Channels other than email are
out of scope by decision of the product owner. Status: [x] done, [ ] to do, [-] out of scope.

## Channels
- [x] Email via IMAP/SMTP (IDLE, polling fallback, threading, bounces)
- [x] Email via OAuth for Google Workspace and Microsoft 365 (IMAP/SMTP XOAUTH2), see docs/mail-oauth.md
- [-] Live chat widget, WhatsApp, Messenger, Instagram, TikTok, X, Telegram, LINE, SMS, voice

## Inbox and collaboration
- [x] Shared inbox, conversation list, conversation view (read)
- [x] Reply, forward, new outbound conversation, undo send, drafts, attachments upload
- [x] Rich text editor, signatures (user and mailbox), branded email template
- [x] HTML mail rendered sanitized in a sandboxed iframe, remote images blocked/allowed per sender, attachment download
- [x] Private notes with @mentions
- [x] Status (open/waiting/closed/spam), snooze, priority, assign agent/team
- [x] Trash: delete (single and bulk), view read-only, restore, delete for good, empty; emptied after `trash_days` (default 30) by retention
- [x] Sender blocklist per mailbox (address or domain): new conversations from a blocked sender start as spam; "Spam en afzender blokkeren" in the conversation
- [x] Labels (CRUD, apply, filter)
- [x] Conversation timeline events (assigned, status, labels)
- [x] Collision detection (viewing/typing) and realtime updates (SSE)
- [x] Read receipts for agents (seen by agent) and delivery status of outbound mail
- [x] Bulk actions
- [x] Conversation filters and saved views
- [x] Search (full text) with filters
- [x] Keyboard shortcuts and command bar (Ctrl/Cmd+K)
- [x] Canned responses with variables
- [x] Notifications (in app, email) for mentions, assignments, customer replies, SLA risk/breach and low CSAT; email opt-in per user
- [x] Per-agent read state: unread rows in bold with a dot, unread count on My inbox, mark as unread from the conversation header
- [x] Agent availability (online/busy/offline)

## Mobile
- [x] Native iOS and Android apps (`mobile/`, React Native/Expo): inbox views and status filter, conversation with actions (assign to me, close/reopen, snooze, priority, status, unread), reply with canned responses and undo, internal notes, notifications, search, availability; see docs/mobile.md
- [x] Push notifications: APNs (iOS), FCM (Android) and self-hosted Web Push for browsers and the installed web app; per-user kinds, device list, test notification
- [x] Installable web app (manifest, service worker, icons) with a phone tab bar and safe-area layout
- [ ] Single sign-on inside the native apps (SSO-only workspaces use the web app on the phone for now)

## Automation and routing
- [x] Automation rules (conditions → actions)
- [x] Macros
- [x] Auto-assignment (round robin, respects availability) and agent capacity
- [x] SLA policies (first response, resolution) with business hours and visible timers
- [x] Business hours and auto-reply outside hours
- [x] Auto-resolve inactive conversations

## Contacts (CRM)
- [x] Contact profiles with full conversation history, organizations
- [x] Contact notes
- [x] Custom attributes (contact, organization and conversation)
- [x] Contact segments (saved filters)
- [x] CSV import and export
- [x] Merge contacts
- [x] GDPR export and erase

## Reports
- [x] Overview dashboard (volume, first response, resolution, SLA), see docs/reports.md
- [x] Agent, team, mailbox and label reports
- [x] CSAT surveys (rating link after resolve) and CSAT report
- [x] Live view (open, unassigned, per agent now)
- [x] Export reports (CSV)

## Knowledge base
- [x] Help center portal (categories, articles, search), public pages, editor (public site at /hulp, agent side under Kennisbank, articles insertable in replies)

## Campaigns
- [x] One-off email campaigns to a contact segment (Campagnes; permission `campaigns.manage`; recipient preview, test mail to yourself, scheduling, pacing per mailbox, pause/resume/cancel, per-recipient report as CSV; one-click unsubscribe per RFC 8058 with a public page, unsubscribed and bouncing addresses are skipped; see docs/architecture.md section 3.7)

## Platform
- [x] REST API with personal API tokens (OpenAPI document)
- [x] Outgoing webhooks (HMAC, SSRF-safe, delivery log, retries)
- [x] Roles and custom permissions (built-in roles are presets of a closed permission list; custom roles, Werkruimte > Rollen)
- [x] SSO via OpenID Connect (Entra ID, Google, Keycloak and others; PKCE, optional provisioning, SSO required; see docs/sso.md)
- [x] 2FA (TOTP), audit log (write side)
- [x] Audit log viewer, failed jobs viewer with retry
- [x] Invitations and password reset by email (system mail via a mailbox or ECHOO_SMTP_URL)
- [x] Data retention settings and purge jobs (Werkruimte > Privacy en retentie: closed conversations, attachments, spam, audit log; per mailbox or workspace; preview before saving)
- [x] Prometheus metrics (separate listener), readiness checks, backup and restore scripts, key rotation, orphan scan, operations docs (docs/operations.md: upgrade, SPF/DKIM/DMARC, reverse proxy, troubleshooting)
- [-] Multilingual UI and RTL (UI is Dutch only by requirement)
- [-] Slack, Linear, Google Translate, dashboard apps, calling, chatbots
