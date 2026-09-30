import { describe, expect, it } from 'vitest'

import { auditFromText, fromText, hasAnyPeriod, toText } from './retention'

describe('fromText', () => {
  it('reads empty fields as keep forever', () => {
    expect(fromText({ closed: '', attachments: ' ', spam: '', trash: '' })).toEqual({
      closed_conversation_months: null,
      attachment_months: null,
      spam_days: null,
      trash_days: null,
    })
  })

  it('reads whole numbers', () => {
    expect(fromText({ closed: '24', attachments: '6', spam: '30', trash: '14' })).toEqual({
      closed_conversation_months: 24,
      attachment_months: 6,
      spam_days: 30,
      trash_days: 14,
    })
  })

  it('rejects zero, decimals and text', () => {
    for (const bad of ['0', '1.5', '-2', 'een', '12 maanden']) {
      expect(fromText({ closed: bad, attachments: '', spam: '', trash: '' })).toBeUndefined()
      expect(fromText({ closed: '', attachments: '', spam: '', trash: bad })).toBeUndefined()
    }
  })
})

describe('auditFromText', () => {
  it('needs at least three months, or nothing to keep forever', () => {
    expect(auditFromText('12')).toBe(12)
    expect(auditFromText('')).toBeNull()
    expect(auditFromText('2')).toBeUndefined()
  })
})

describe('toText and hasAnyPeriod', () => {
  it('round-trips and detects an active period', () => {
    const p = { closed_conversation_months: 6, attachment_months: null, spam_days: null, trash_days: 30 }
    expect(toText(p)).toEqual({ closed: '6', attachments: '', spam: '', trash: '30' })
    expect(hasAnyPeriod(p)).toBe(true)
    expect(hasAnyPeriod({ closed_conversation_months: null, attachment_months: null, spam_days: null, trash_days: 7 })).toBe(true)
    expect(hasAnyPeriod({ closed_conversation_months: null, attachment_months: null, spam_days: null, trash_days: null })).toBe(false)
  })
})
