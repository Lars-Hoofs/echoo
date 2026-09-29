# Single sign-on (OpenID Connect)

Echoo signs people in through any OpenID Connect provider: Microsoft Entra ID, Google Workspace,
Okta, Keycloak, Authentik and others. Only the owner configures it, under
Werkruimte > Inloggen met SSO.

## How it works

1. The login page shows "Inloggen met *label*". It links to `GET /auth/sso/start`.
2. Echoo creates `state`, `nonce` and a PKCE verifier, keeps them in an encrypted cookie that is
   bound to that browser (10 minutes, single use), and redirects to the provider.
3. The provider redirects back to `GET /auth/sso/callback`. Echoo exchanges the code, verifies the
   ID token (signature, issuer, audience, expiry, nonce) and requires a verified email address.
4. The email address is matched to an active user. Without a match, a new user is created only when
   provisioning is on and the address is in an allowed domain. Deactivated users are refused.
5. Echoo issues the same session as for a password login. Failures go back to the login page with a
   short explanation and are written to the audit log as `auth.login_failed` with `method: sso` and a
   reason code (never tokens or provider text).

Register this redirect URI at the provider (the settings page shows it):

```
https://<your echoo host>/auth/sso/callback
```

Request the scopes `openid`, `email` and `profile`. The client authenticates with a client secret.

## Settings

| Setting | Meaning |
|---|---|
| Issuer-URL | The provider's issuer. Echoo fetches `<issuer>/.well-known/openid-configuration` when you save, so a wrong address is found at once. It must be https. |
| Client-ID, client secret | From the provider. The secret is stored encrypted and is never shown again; leave it empty when saving to keep it. |
| Toegestane e-maildomeinen | One per line. Empty means any domain for users who already have an account. New users are only created for listed domains. |
| Tekst op de knop | The label after "Inloggen met". |
| Nieuwe gebruikers automatisch aanmaken | First sign-in creates the account with the chosen role (agent, read-only or a custom role without user or settings management). Needs at least one domain. |
| SSO verplicht | Password login is off for everybody except the owner, who can always sign in with a password when the provider is down. Invitations cannot be accepted while it is on. |
| Tweestapsverificatie van de provider telt | When the ID token's `amr` contains `mfa`, `otp` or `hwk`, Echoo asks for no code and does not require enrolment. Otherwise Echoo's own 2FA rules apply. |
| E-mailadres vertrouwen zonder email_verified | For providers that never send the claim (Entra ID). A claim that says `false` is always refused. |
| Provider op een intern netwerk | Allows a private address or plain http for the issuer. Off by default: requests to the provider go through the SSRF guard. |

## Microsoft Entra ID

1. Entra admin centre > App registrations > New registration. Supported account types: this
   directory only. Redirect URI (platform Web): the URI above.
2. Note the *Application (client) ID* and *Directory (tenant) ID*.
3. Certificates & secrets > New client secret. Copy the value.
4. In Echoo: issuer `https://login.microsoftonline.com/<tenant id>/v2.0`, the client ID and the secret.
5. Entra sends no `email_verified` claim, so switch on "E-mailadres vertrouwen zonder email_verified".
   Use the tenant-specific issuer; the multi-tenant `common` endpoint reports a different issuer per
   tenant and is refused. Add the optional `email` claim to the ID token (Token configuration) so the
   address is present.
6. To let Echoo trust Entra's MFA: enable "Tweestapsverificatie van de provider telt"; Entra reports
   `mfa` in `amr` after a multi-factor sign-in.

## Google Workspace

1. Google Cloud console > APIs & Services > Credentials > Create credentials > OAuth client ID,
   application type Web application. Authorized redirect URI: the URI above.
2. Configure the OAuth consent screen as Internal to limit sign-in to your Workspace.
3. In Echoo: issuer `https://accounts.google.com`, the client ID and the secret, and your Workspace
   domain under allowed domains. Google sends `email_verified`.
4. Google does not report `amr`, so its MFA never replaces Echoo's 2FA.

## Keycloak

1. In the realm: Clients > Create client. Client type OpenID Connect, client authentication on,
   standard flow on. Valid redirect URI: the URI above.
2. Credentials tab: copy the client secret.
3. In Echoo: issuer `https://<keycloak host>/realms/<realm>` (Keycloak 17 and later; older versions
   have `/auth` in front), the client ID and the secret.
4. Make sure users have a verified email address (Users > the user > Email verified). To pass the
   second factor on, add an "Authentication Method Reference" mapper (Keycloak 25 and later) so `amr`
   contains `mfa` or `otp`.
5. On an internal Keycloak (private address or http), switch on "Provider op een intern netwerk".

## Operating notes

- Users are matched on the email address; matching is case-insensitive. A user whose address changes
  at the provider no longer matches and needs a new account or an updated address in Echoo.
- SSO accounts created by provisioning have a random password nobody knows; they can set one through
  "Wachtwoord vergeten" unless SSO is required.
- Changing the issuer or client secret takes effect at once. Discovery results are cached for 15
  minutes and dropped when the settings are saved.
- If the provider is down and SSO is required, the owner signs in with the password.
