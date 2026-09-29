import { formatDateTime } from './format'

export type TlsMode = 'implicit' | 'starttls'
export type AuthType = 'password' | 'oauth_google' | 'oauth_microsoft'

export const authTypeLabel: Record<AuthType, string> = {
  password: 'IMAP/SMTP',
  oauth_google: 'Google',
  oauth_microsoft: 'Microsoft 365',
}

export interface OAuthProvider {
  id: 'google' | 'microsoft'
  name: string
  redirect_uri: string
}

export interface Mailbox {
  id: string
  name: string
  email_address: string
  display_name: string
  imap_host: string
  imap_port: number
  imap_tls: TlsMode
  imap_username: string
  imap_password_set: boolean
  smtp_host: string
  smtp_port: number
  smtp_tls: TlsMode
  smtp_username: string
  smtp_password_set: boolean
  sent_folder: string
  send_delay_seconds: number
  allow_internal_host: boolean
  auth_type: AuthType
  oauth_connected_at: string | null
  needs_reconnect: boolean
  sync_state: 'connected' | 'polling' | 'backoff' | 'auth_failed' | 'disabled'
  sync_reason: SyncReason | null
  last_synced_at: string | null
  disabled_at: string | null
}

// Why the last sync failed; a closed set the server derives, never the mail server's own text.
export type SyncReason = 'connect_failed' | 'tls_failed' | 'auth_failed' | 'oauth_reauth_required' | 'internal_destination' | 'no_inbox' | 'unknown'

export type AccessLevel = 'read' | 'write'

export interface ProbeResult {
  ok: boolean
  code?: string
}

export type SyncGlyph = 'ok' | 'waiting' | 'problem' | 'error' | 'off'

const timeOnly = new Intl.DateTimeFormat('nl-NL', { hour: '2-digit', minute: '2-digit' })

function lastSynced(iso: string | null, now: Date): string {
  if (!iso) return ''
  const d = new Date(iso)
  const sameDay = d.toDateString() === now.toDateString()
  return ` · laatst gesynchroniseerd ${sameDay ? timeOnly.format(d) : formatDateTime(iso, now)}`
}

function failureReason(reason: SyncReason, authType: AuthType): string {
  switch (reason) {
    case 'connect_failed':
      return 'geen verbinding met de server'
    case 'tls_failed':
      return 'beveiligde verbinding mislukt'
    case 'auth_failed':
      return authType === 'password' ? 'wachtwoord geweigerd' : 'toegang geweigerd'
    case 'oauth_reauth_required':
      return 'toegang ingetrokken of verlopen'
    case 'internal_destination':
      return 'het adres verwijst naar een intern netwerk'
    case 'no_inbox':
      return 'de map INBOX bestaat niet'
    case 'unknown':
      return 'onbekende fout'
  }
}

function failureText(mb: Pick<Mailbox, 'imap_host' | 'auth_type' | 'last_synced_at'>, reason: SyncReason, now: Date): string {
  const failed = `Verbinding met ${mb.imap_host} mislukt: ${failureReason(reason, mb.auth_type)}.`
  if (!mb.last_synced_at) return failed
  const d = new Date(mb.last_synced_at)
  const when = d.toDateString() === now.toDateString() ? `om ${timeOnly.format(d)}` : `op ${formatDateTime(mb.last_synced_at, now)}`
  return `${failed} Laatst gesynchroniseerd ${when}.`
}

// The status line of a mailbox: text says it all, the glyph only repeats it.
export function syncStatus(
  mb: Pick<Mailbox, 'sync_state' | 'sync_reason' | 'last_synced_at' | 'disabled_at' | 'imap_host' | 'auth_type'> & { needs_reconnect?: boolean },
  now = new Date(),
): { glyph: SyncGlyph; text: string } {
  if (mb.disabled_at) return { glyph: 'off', text: 'Uitgeschakeld' }
  if (mb.needs_reconnect) return { glyph: 'error', text: 'Toegang ingetrokken of verlopen: verbind het account opnieuw' }
  switch (mb.sync_state) {
    case 'connected':
      return { glyph: 'ok', text: `Verbonden via IMAP IDLE${lastSynced(mb.last_synced_at, now)}` }
    case 'polling':
      return { glyph: 'ok', text: `Verbonden, controleert elke minuut${lastSynced(mb.last_synced_at, now)}` }
    case 'backoff':
      return {
        glyph: 'problem',
        text: mb.sync_reason ? failureText(mb, mb.sync_reason, now) : `Verbinding verbroken, opnieuw proberen${lastSynced(mb.last_synced_at, now)}`,
      }
    case 'auth_failed':
      return { glyph: 'error', text: mb.sync_reason ? failureText(mb, mb.sync_reason, now) : 'Inloggen mislukt' }
    case 'disabled':
      return { glyph: 'waiting', text: 'Wacht op de eerste synchronisatie' }
  }
}

const oauthErrors: Record<string, string> = {
  state_expired: 'De koppeling duurde te lang. Begin opnieuw.',
  state_invalid: 'De koppeling kon niet worden bevestigd. Begin opnieuw vanuit Echoo.',
  provider_denied: 'De aanbieder heeft de toegang geweigerd.',
  provider_unavailable: 'Deze aanbieder is niet ingesteld.',
  exchange_failed: 'De aanbieder wees de koppeling af. Controleer het client-ID, het geheim en de omleidings-URI.',
  no_refresh_token: 'De aanbieder gaf geen toestemming voor blijvende toegang. Probeer opnieuw en sta alle gevraagde rechten toe.',
  email_mismatch: 'Je hebt met een ander account ingelogd dan het e-mailadres van deze mailbox.',
  email_taken: 'Er bestaat al een mailbox met dit e-mailadres.',
  mailbox_missing: 'Deze mailbox bestaat niet meer.',
  forbidden: 'Alleen beheerders kunnen een mailbox koppelen.',
}

export const oauthErrorText = (code: string): string => oauthErrors[code] ?? 'Koppelen is niet gelukt. Probeer het opnieuw.'
