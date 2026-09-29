import { queryOptions } from '@tanstack/react-query'

import { api } from './api'

// What the login page needs to know: whether to offer SSO and whether the password form is closed.
export interface SsoInfo {
  enabled: boolean
  label: string
  required: boolean
}

export const ssoInfoQuery = queryOptions({
  queryKey: ['sso-info'],
  queryFn: () => api<SsoInfo>('GET', '/auth/sso'),
  staleTime: 60_000,
})

// The reasons /auth/sso/callback reports in ?sso_error=. Anything else gets the generic text.
const ssoMessages: Record<string, string> = {
  not_configured: 'Inloggen met SSO staat niet aan.',
  no_account: 'Er is geen account voor dit e-mailadres. Vraag een beheerder om je toe te voegen.',
  role_privileged: 'Nieuwe accounts krijgen via SSO een rol met beheerrechten. Vraag de eigenaar de standaardrol aan te passen.',
  deactivated: 'Dit account is gedeactiveerd of nog niet geactiveerd.',
  domain_not_allowed: 'Dit e-mailadres hoort niet bij een toegestaan domein.',
  email_unverified: 'Je identiteitsprovider heeft dit e-mailadres niet als geverifieerd doorgegeven.',
  email_missing: 'Je identiteitsprovider heeft geen e-mailadres doorgegeven.',
  email_invalid: 'Je identiteitsprovider heeft een ongeldig e-mailadres doorgegeven.',
  idp_error: 'Je identiteitsprovider heeft de aanmelding geweigerd.',
  invalid_state: 'De aanmelding is verlopen of vanuit een andere browser gestart. Probeer het opnieuw.',
}

export function ssoErrorMessage(code: string): string {
  return ssoMessages[code] ?? 'Inloggen met SSO is niet gelukt. Probeer het opnieuw; blijft het misgaan, meld het dan bij je beheerder.'
}

export interface SsoSettings {
  enabled: boolean
  issuer_url: string
  client_id: string
  client_secret_set: boolean
  allowed_domains: string[]
  button_label: string
  required: boolean
  auto_provision: boolean
  default_role: 'agent' | 'readonly' | 'custom'
  default_custom_role_id: string | null
  trust_idp_mfa: boolean
  trust_missing_email_verified: boolean
  allow_internal_issuer: boolean
  redirect_uri: string
}

// parseDomains turns the text box (one domain per line, or comma separated) into a list.
export function parseDomains(text: string): string[] {
  return text
    .split(/[\s,;]+/)
    .map((d) => d.trim().replace(/^@/, '').toLowerCase())
    .filter((d, i, all) => d !== '' && all.indexOf(d) === i)
}
