import { describe, expect, it } from 'vitest'

import { oauthErrorText, syncStatus } from './mailbox'

const now = new Date(2026, 8, 28, 15, 0)

describe('syncStatus', () => {
  const base = { last_synced_at: new Date(2026, 8, 28, 14, 2).toISOString(), disabled_at: null, imap_host: 'imap.voorbeeld.nl', auth_type: 'password' as const, sync_reason: null }

  it('shows the last sync time of today', () => {
    expect(syncStatus({ ...base, sync_state: 'connected' }, now)).toEqual({
      glyph: 'ok',
      text: 'Verbonden via IMAP IDLE · laatst gesynchroniseerd 14:02',
    })
  })

  it('shows the date once the last sync is not from today', () => {
    const status = syncStatus({ ...base, last_synced_at: new Date(2026, 8, 27, 9, 30).toISOString(), sync_state: 'polling' }, now)
    expect(status.text).toContain('laatst gesynchroniseerd 27 sep')
  })

  it('leaves the time out when nothing was synced yet', () => {
    expect(syncStatus({ ...base, sync_state: 'connected', last_synced_at: null }, now).text).toBe('Verbonden via IMAP IDLE')
  })

  it('reports failures and the disabled state in words', () => {
    expect(syncStatus({ ...base, sync_state: 'auth_failed' }, now)).toEqual({ glyph: 'error', text: 'Inloggen mislukt' })
    expect(syncStatus({ ...base, sync_state: 'backoff' }, now).glyph).toBe('problem')
    expect(syncStatus({ ...base, sync_state: 'connected', disabled_at: new Date().toISOString() }, now)).toEqual({
      glyph: 'off',
      text: 'Uitgeschakeld',
    })
    expect(syncStatus({ ...base, sync_state: 'disabled', last_synced_at: null }, now).glyph).toBe('waiting')
  })

  it('names the host and the reason of a failure, with the last sync time', () => {
    expect(syncStatus({ ...base, sync_state: 'auth_failed', sync_reason: 'auth_failed' }, now)).toEqual({
      glyph: 'error',
      text: 'Verbinding met imap.voorbeeld.nl mislukt: wachtwoord geweigerd. Laatst gesynchroniseerd om 14:02.',
    })
    expect(syncStatus({ ...base, sync_state: 'backoff', sync_reason: 'tls_failed' }, now)).toEqual({
      glyph: 'problem',
      text: 'Verbinding met imap.voorbeeld.nl mislukt: beveiligde verbinding mislukt. Laatst gesynchroniseerd om 14:02.',
    })
  })

  it('has Dutch copy for every reason and does not blame the password of an OAuth account', () => {
    const reasons = ['connect_failed', 'tls_failed', 'auth_failed', 'oauth_reauth_required', 'internal_destination', 'no_inbox', 'unknown'] as const
    const texts = reasons.map((sync_reason) => syncStatus({ ...base, last_synced_at: null, sync_state: 'backoff', sync_reason }, now).text)
    expect(new Set(texts).size).toBe(reasons.length)
    for (const text of texts) expect(text).toMatch(/^Verbinding met imap\.voorbeeld\.nl mislukt: .+\.$/)
    expect(syncStatus({ ...base, auth_type: 'oauth_google', sync_state: 'auth_failed', sync_reason: 'auth_failed' }, now).text).toContain('toegang geweigerd')
  })

  it('dates the last sync when it is not from today', () => {
    const text = syncStatus({ ...base, last_synced_at: new Date(2026, 8, 27, 9, 30).toISOString(), sync_state: 'backoff', sync_reason: 'connect_failed' }, now).text
    expect(text).toContain('Laatst gesynchroniseerd op 27 sep')
  })

  it('asks to reconnect an OAuth account whose token was rejected', () => {
    const status = syncStatus({ ...base, sync_state: 'auth_failed', needs_reconnect: true }, now)
    expect(status.glyph).toBe('error')
    expect(status.text).toContain('verbind het account opnieuw')
  })
})

describe('oauthErrorText', () => {
  it('explains known callback failures and has a fallback', () => {
    expect(oauthErrorText('email_mismatch')).toContain('ander account')
    expect(oauthErrorText('state_expired')).toContain('Begin opnieuw')
    expect(oauthErrorText('something_new')).toBe('Koppelen is niet gelukt. Probeer het opnieuw.')
  })
})
