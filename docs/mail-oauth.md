# OAuth2 mailboxes and system mail

Echoo connects to Google Workspace / Gmail and Microsoft 365 / Outlook.com mailboxes with
OAuth2 (IMAP and SMTP with the XOAUTH2 mechanism) instead of a password. Microsoft has turned
off basic authentication for Exchange Online, and Google only allows app passwords on accounts
that have 2-step verification and no admin restrictions, so most companies need this.

Providers without client credentials in the environment are not offered in the mailbox form.

## Configuration

Every variable also accepts a `_FILE` variant that reads the value from a file (a Docker secret).
Client ID and secret of a provider must be set together.

| Variable | Meaning |
|---|---|
| `ECHOO_OAUTH_GOOGLE_CLIENT_ID`, `ECHOO_OAUTH_GOOGLE_CLIENT_SECRET` | OAuth client of the Google Cloud project |
| `ECHOO_OAUTH_MICROSOFT_CLIENT_ID`, `ECHOO_OAUTH_MICROSOFT_CLIENT_SECRET` | App registration in Microsoft Entra |
| `ECHOO_OAUTH_MICROSOFT_TENANT` | `common` (default: work and personal accounts), `organizations`, `consumers`, a tenant ID or a verified domain |

The redirect URI to register at the provider is `ECHOO_BASE_URL` plus `/oauth/callback/google`
or `/oauth/callback/microsoft`. The mailbox form shows the exact value.

## Google Workspace and Gmail

1. In the [Google Cloud Console](https://console.cloud.google.com/) create a project (or pick
   one) and open *APIs & Services > OAuth consent screen*.
   - Workspace: choose user type **Internal**. No app verification is needed.
   - Personal Gmail or a workspace outside your organization: choose **External** and publish
     the app (*In production*). An app left in *Testing* gets refresh tokens that expire after
     7 days. `https://mail.google.com/` is a restricted scope, so Google requires verification
     of external apps beyond 100 users.
2. Add the scope `https://mail.google.com/` (plus the basic `openid` and `email`).
3. *Credentials > Create credentials > OAuth client ID*, type **Web application**, and add the
   redirect URI `https://<your host>/oauth/callback/google`.
4. Set `ECHOO_OAUTH_GOOGLE_CLIENT_ID` and `ECHOO_OAUTH_GOOGLE_CLIENT_SECRET`, restart Echoo.
5. In Echoo: *Instellingen > Mailboxen > Mailbox toevoegen*, choose **Google**, enter a name and
   click *Verbinden met Google*. Sign in with the mailbox account.

IMAP must be enabled for the account (it is by default in Workspace). The Gmail API does not
need to be enabled.

Servers used: `imap.gmail.com:993` (TLS) and `smtp.gmail.com:465` (TLS). Gmail stores sent mail
itself, so the Sent-folder setting stays empty.

## Microsoft 365 and Outlook.com

1. In the [Microsoft Entra admin center](https://entra.microsoft.com/) open *App registrations >
   New registration*. Pick the account types you need (single tenant is the safest for a company;
   then set `ECHOO_OAUTH_MICROSOFT_TENANT` to the tenant ID). Add a **Web** redirect URI
   `https://<your host>/oauth/callback/microsoft`.
2. *Certificates & secrets > New client secret*. Copy the value now.
3. *API permissions > Add a permission > APIs my organization uses > Office 365 Exchange
   Online > Delegated permissions*: `IMAP.AccessAsUser.All` and `SMTP.Send`. Under Microsoft
   Graph add the delegated permissions `offline_access`, `openid` and `email`. Use *Grant admin
   consent* so users are not asked individually.
4. Set `ECHOO_OAUTH_MICROSOFT_CLIENT_ID`, `ECHOO_OAUTH_MICROSOFT_CLIENT_SECRET` and, when not
   using `common`, `ECHOO_OAUTH_MICROSOFT_TENANT`. Restart Echoo.
5. In Echoo choose **Microsoft 365** in the mailbox form and click *Verbinden met Microsoft 365*.

Exchange Online must allow it for the mailbox: IMAP must be enabled, and *Authenticated SMTP*
must not be disabled for the organization or the mailbox
(`Set-CASMailbox -Identity <mailbox> -SmtpClientAuthenticationDisabled $false`).

Servers used: `outlook.office365.com:993` (TLS) and `smtp.office365.com:587` (STARTTLS).
Microsoft keeps a copy of sent mail itself. A shared mailbox that cannot sign in on its own is
not supported: the account that signs in is the mailbox.

## How it works

- An admin clicks the connect button. Echoo returns the provider's authorization URL
  (`GET /api/v1/mailboxes/oauth/{provider}/start`). The `state` parameter is encrypted and
  authenticated with the encryption key, expires after 10 minutes, is bound to the admin's
  session and carries the PKCE verifier (the flow uses PKCE with S256).
- The provider redirects to `/oauth/callback/{provider}`. Echoo checks the state against the
  session, exchanges the code, reads the account's email address from the `id_token` and creates
  the mailbox (or updates the one being reconnected). The address of the account becomes the
  mailbox address and login name. Reconnecting only succeeds with the same account.
- The refresh and access token are stored in `mailboxes.oauth_token_enc`, encrypted like the
  other secrets and bound to the mailbox row. They are never shown in the UI or written to logs
  or the audit log.
- Access tokens are refreshed shortly before they expire. Refreshing takes a Postgres advisory
  lock per mailbox, so the sync, the send job and the connection test never race, which matters
  for providers that rotate refresh tokens. A rotated refresh token is saved.
- When the provider rejects the refresh token (`invalid_grant`: the password changed, the user
  or an admin revoked access, or the token expired unused) the mailbox is marked
  `auth_failed` with the reason `oauth_reauth_required` and the settings page shows
  *Opnieuw verbinden*. Outbound messages of such a mailbox fail with that reason instead of
  being retried. Network problems and provider outages are retried with backoff and do not mark
  the mailbox.

Connecting and reconnecting are audited (`mailbox.created`, `mailbox.oauth_connected`), as are
failed attempts (`mailbox.oauth_failed`, with a reason code and nothing else).

## System mail

Invitations, password resets and notification emails are sent by Echoo itself. Configure one of:

- `ECHOO_SYSTEM_MAILBOX`: the address of an existing mailbox in Echoo, which then also sends
  system mail (password or OAuth mailbox).
- `ECHOO_SMTP_URL`: a dedicated relay, `smtps://user:password@smtp.example.com:465?from=noreply@example.com`
  for implicit TLS or `smtp://user:password@smtp.example.com:587?from=noreply@example.com` for
  STARTTLS. Encode special characters in the password. `from` is required unless the user name
  is an email address. Add `&allow_internal=true` for a relay on a private network; without it
  Echoo refuses internal addresses like it does for mailbox servers. TLS is always required and
  certificates are always verified.

Set only one of them; both accept `_FILE`. Without either, invitations and password reset by
email are off: the users page then hands out temporary passwords as before, and the login page
tells someone who asks for a reset to contact an administrator.

Messages are queued as `sysmail.send` jobs (8 attempts with backoff). The job arguments hold the
message encrypted, because the message contains single-use links.

Links: invitations are valid for 72 hours and password resets for 30 minutes. Both work once,
are stored only as a SHA-256 hash and are voided by a newer link for the same user. A password
reset ends all sessions of the account and lifts a login lockout; two-factor authentication is
still required at the next login.
