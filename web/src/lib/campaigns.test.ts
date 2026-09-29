import { describe, expect, it } from 'vitest'

import {
  type CampaignCounts,
  type DraftForm,
  hasErrors,
  isActive,
  parseRate,
  parseSchedule,
  previewSummary,
  progress,
  stepFromSlug,
  unknownVariables,
  validateStep,
} from './campaigns'

const form: DraftForm = {
  name: 'Herfstactie',
  mailboxId: 'mb-1',
  rate: '60',
  segmentId: 'seg-1',
  subject: 'Actie voor {{contact.first_name}}',
  bodyHtml: '<p>Hallo {{contact.first_name}}</p>',
  bodyEmpty: false,
}

const counts = (o: Partial<CampaignCounts>): CampaignCounts => ({ total: 0, pending: 0, queued: 0, sent: 0, failed: 0, skipped: 0, skipped_reasons: {}, ...o })

describe('validateStep', () => {
  it('accepts a complete form on every step', () => {
    for (const step of ['mailbox', 'segment', 'content', 'schedule'] as const) {
      expect(validateStep(step, form, 120)).toEqual({})
    }
  })

  it('needs a name, a mailbox and a rate within the limit on the mailbox step', () => {
    expect(validateStep('mailbox', { ...form, name: '  ' }, 120).name).toBeDefined()
    expect(validateStep('mailbox', { ...form, name: 'x'.repeat(101) }, 120).name).toBeDefined()
    expect(validateStep('mailbox', { ...form, mailboxId: '' }, 120).mailboxId).toBeDefined()
    expect(validateStep('mailbox', { ...form, rate: '0' }, 120).rate).toBeDefined()
    expect(validateStep('mailbox', { ...form, rate: '121' }, 120).rate).toBe('Vul een aantal in van 1 tot 120.')
    expect(validateStep('mailbox', { ...form, rate: '12,5' }, 120).rate).toBeDefined()
    expect(validateStep('mailbox', { ...form, rate: '' }, 120).rate).toBeDefined()
  })

  it('does not check later steps early', () => {
    const empty = { ...form, segmentId: '', subject: '', bodyEmpty: true }
    expect(validateStep('mailbox', empty, 120)).toEqual({})
    expect(validateStep('segment', empty, 120)).toEqual({ segmentId: 'Kies een segment.' })
  })

  it('needs a subject and a text on the content step', () => {
    const errors = validateStep('content', { ...form, subject: ' ', bodyEmpty: true }, 120)
    expect(errors.subject).toBeDefined()
    expect(errors.body).toBeDefined()
    expect(validateStep('content', { ...form, subject: 'x'.repeat(301) }, 120).subject).toBeDefined()
    expect(validateStep('content', { ...form, subject: 'a\nb' }, 120).subject).toBeDefined()
  })

  it('refuses variables a campaign cannot fill', () => {
    const errors = validateStep('content', { ...form, bodyHtml: '<p>Gesprek {{conversation.number}}</p>' }, 120)
    expect(errors.body).toContain('{{conversation.number}}')
    expect(hasErrors(errors)).toBe(true)
    expect(hasErrors({})).toBe(false)
  })
})

describe('unknownVariables', () => {
  it('lists each unknown placeholder once, ignoring spacing', () => {
    expect(unknownVariables('{{ contact.name }} {{nope.thing}}', '{{nope.thing}} {{conversation.number}}')).toEqual(['nope.thing', 'conversation.number'])
  })
  it('knows the placeholders of a campaign', () => {
    expect(unknownVariables('{{contact.name}} {{contact.first_name}} {{contact.email}} {{agent.name}} {{agent.first_name}} {{mailbox.name}}')).toEqual([])
  })
})

describe('parseRate', () => {
  it('accepts whole numbers from 1 to the maximum', () => {
    expect(parseRate('1', 120)).toBe(1)
    expect(parseRate(' 120 ', 120)).toBe(120)
    expect(parseRate('121', 120)).toBeNull()
    expect(parseRate('-5', 120)).toBeNull()
    expect(parseRate('1e2', 120)).toBeNull()
  })
})

describe('parseSchedule', () => {
  const now = new Date('2026-10-01T09:00:00')
  it('sends at once without a moment', () => {
    expect(parseSchedule('now', '', now)).toEqual({ ok: true, at: null })
  })
  it('needs a moment at least a minute ahead and at most a year', () => {
    expect(parseSchedule('later', '', now)).toEqual({ ok: false, error: 'Kies een datum en tijd.' })
    expect(parseSchedule('later', '2026-10-01T09:00', now)).toEqual({ ok: false, error: 'Kies een moment in de toekomst.' })
    expect(parseSchedule('later', '2027-10-02T09:00', now)).toEqual({ ok: false, error: 'Plan maximaal een jaar vooruit.' })
    const ok = parseSchedule('later', '2026-10-02T09:30', now)
    expect(ok).toEqual({ ok: true, at: new Date('2026-10-02T09:30:00').toISOString() })
  })
})

describe('progress', () => {
  it('counts sent, failed and skipped as settled', () => {
    expect(progress(counts({ total: 10, sent: 3, failed: 1, skipped: 1, queued: 2, pending: 3 }))).toEqual({ done: 5, total: 10, percent: 50 })
  })
  it('is empty for a campaign without recipients', () => {
    expect(progress(counts({}))).toEqual({ done: 0, total: 0, percent: 0 })
  })
  it('does not round up to 100 before everything is settled', () => {
    expect(progress(counts({ total: 1000, sent: 999, queued: 1 })).percent).toBe(99)
  })
})

describe('isActive', () => {
  it('follows campaigns that run or still wait for deliveries', () => {
    expect(isActive({ status: 'sending', counts: counts({}) })).toBe(true)
    expect(isActive({ status: 'scheduled', counts: counts({}) })).toBe(true)
    expect(isActive({ status: 'cancelled', counts: counts({ queued: 1 }) })).toBe(true)
    expect(isActive({ status: 'done', counts: counts({ sent: 5, total: 5 }) })).toBe(false)
    expect(isActive({ status: 'draft', counts: counts({}) })).toBe(false)
  })
})

describe('previewSummary', () => {
  it('names what is skipped', () => {
    const p = { total: 10, sendable: 6, unsubscribed: 2, no_address: 1, duplicate: 0, bounced: 1, without_name: 0 }
    expect(previewSummary(p)).toBe('Wordt overgeslagen: 2 afgemeld, 1 zonder e-mailadres, 1 met een adres dat niet werkt.')
    expect(previewSummary({ ...p, unsubscribed: 0, no_address: 0, bounced: 0 })).toBe('Niemand wordt overgeslagen.')
  })
})

describe('stepFromSlug', () => {
  it('falls back to the first step', () => {
    expect(stepFromSlug('content')).toBe('content')
    expect(stepFromSlug('nope')).toBe('mailbox')
    expect(stepFromSlug(undefined)).toBe('mailbox')
  })
})
