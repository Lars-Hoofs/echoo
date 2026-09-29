import { describe, expect, it } from 'vitest'

import { ApiError } from './api'
import { errorMessage, fieldError, probeText } from './errors'

describe('probeText', () => {
  it.each([
    ['IMAP', { ok: true }, 'IMAP: verbonden'],
    ['SMTP', { ok: false, code: 'smtp_auth_failed' }, 'SMTP: inloggen mislukt — controleer gebruikersnaam en wachtwoord'],
    ['IMAP', { ok: false, code: 'imap_no_inbox' }, 'IMAP: verbonden, maar de map INBOX ontbreekt'],
    ['IMAP', { ok: false, code: 'imap_tls_failed' }, 'IMAP: beveiligde verbinding mislukt — controleer de beveiliging en het certificaat'],
    ['SMTP', { ok: false, code: 'smtp_connect_failed' }, 'SMTP: geen verbinding — controleer server en poort'],
    ['SMTP', { ok: false, code: 'something_new' }, 'SMTP: verbinding mislukt'],
  ] as const)('%s %j', (protocol, result, want) => {
    expect(probeText(protocol, result)).toBe(want)
  })
})

describe('account link errors', () => {
  it('has Dutch copy for expired links and missing system mail', () => {
    expect(errorMessage(new ApiError(404, 'link_invalid'))).toBe('Deze link is verlopen of al gebruikt.')
    expect(errorMessage(new ApiError(409, 'system_mail_unavailable'))).toContain('geen systeemmailbox')
  })

  it('explains password length and OAuth-managed fields', () => {
    const err = new ApiError(422, 'validation_failed', { password: 'too_short', imap_host: 'managed_by_oauth' })
    expect(fieldError(err, 'password')).toBe('Gebruik minimaal 12 tekens.')
    expect(fieldError(err, 'imap_host')).toContain('gekoppelde account')
  })

  it('says how to recover a lost OAuth connection when testing a mailbox', () => {
    expect(probeText('IMAP', { ok: false, code: 'oauth_reauth_required' })).toContain('verbind het account opnieuw')
  })
})

describe('virus scan errors', () => {
  it('tells the user a flagged file is blocked', () => {
    expect(errorMessage(new ApiError(403, 'attachment_infected'))).toContain('virus')
  })
})
