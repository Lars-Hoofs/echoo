import { describe, expect, it } from 'vitest'

import { auditActionLabel, auditGroups, auditTarget, deliveryErrorLabel, disabledReasonLabel, eventLabel, webhookEvents } from './platform'

describe('webhook events', () => {
  it('offers exactly the events the API accepts', () => {
    expect(webhookEvents.map((e) => e.value)).toEqual([
      'conversation.created',
      'conversation.updated',
      'conversation.status_changed',
      'conversation.assigned',
      'conversation.automation',
      'message.created',
      'message.sent',
      'message.failed',
      'contact.created',
    ])
  })

  it('labels events, the test message and falls back to the code', () => {
    expect(eventLabel('message.created')).toBe('Bericht ontvangen')
    expect(eventLabel('webhook.test')).toBe('Testbericht')
    expect(eventLabel('something.new')).toBe('something.new')
  })
})

describe('delivery labels', () => {
  it('turns error codes into text and never shows an unknown code', () => {
    expect(deliveryErrorLabel('timeout')).toContain('10 seconden')
    expect(deliveryErrorLabel('boom')).toBe('Onbekende fout.')
  })

  it('explains why a webhook is off', () => {
    expect(disabledReasonLabel('too_many_failures')).toContain('50')
  })
})

describe('audit labels', () => {
  it('labels known actions and falls back to the code', () => {
    expect(auditActionLabel('api_token.created')).toBe('API-token aangemaakt')
    expect(auditActionLabel('automation.rule_created')).toBe('Regel aangemaakt')
    expect(auditActionLabel('future.action')).toBe('future.action')
  })

  it('names what an entry acted on', () => {
    expect(auditTarget('template', '01a0ea49-e161-7ca9', { name: 'Factuurkopie' })).toBe('Standaardantwoord: Factuurkopie')
    expect(auditTarget('user', '01a0ea49-a900-70da', {})).toBe('Gebruiker 01a0ea49')
    expect(auditTarget('', '', {})).toBe('—')
  })

  it('has one filter group per prefix, starting with all actions', () => {
    const prefixes = auditGroups.map((g) => g.prefix)
    expect(prefixes[0]).toBe('')
    expect(new Set(prefixes).size).toBe(prefixes.length)
    expect(prefixes.filter(Boolean).every((p) => p.endsWith('.'))).toBe(true)
  })
})
