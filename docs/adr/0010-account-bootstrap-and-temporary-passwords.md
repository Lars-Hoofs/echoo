# 10. Owner bootstrap from the CLI, temporary passwords for new users

Status: accepted

## Context
Echoo needs a first account, and admins need to add colleagues before outbound mail exists
(phase 2). A web "first run" setup page is racy: whoever reaches a fresh install first becomes
owner.

## Decision
The owner is created with `echoo admin create-owner`, run by the operator inside the container.
Admins add users in the UI; Echoo generates a 120-bit temporary password that is shown once.
Both must change it at first sign-in; until then the session can only reach account setup.

## Consequences
No unauthenticated setup endpoint exists. Admins hand over temporary passwords out of band.
When SMTP sending lands (phase 2), an email invitation with a single-use link replaces the
temporary password for new users; the owner bootstrap stays on the CLI.

## Update: invitations and password reset by email
With system mail configured (`ECHOO_SYSTEM_MAILBOX` or `ECHOO_SMTP_URL`) admins invite users by
email: the account exists in an invited state (deactivated, no usable password) until the invitee
opens the single-use link and chooses a password. Temporary passwords remain, shown as "Tijdelijk
wachtwoord tonen", for installs without system mail. Password reset by email is available to every
active account, the owner included; `echoo admin reset-password` stays as the operator recovery.
Details in `docs/mail-oauth.md`.
