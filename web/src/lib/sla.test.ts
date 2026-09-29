import { describe, expect, it } from 'vitest'

import { describeEvent } from './actions'
import { formatDuration, type ConversationSla, slaTimer } from './sla'

const NOW = Date.parse('2026-03-02T10:00:00Z')
const at = (minutes: number) => new Date(NOW + minutes * 60_000).toISOString()

const sla = (over: Partial<ConversationSla> = {}): ConversationSla => ({
  state: 'ok',
  first_response_due_at: null,
  first_response_met_at: null,
  resolution_due_at: null,
  ...over,
})

describe('formatDuration', () => {
  it.each([
    [0, '< 1 min'],
    [59_999, '< 1 min'],
    [60_000, '1 min'],
    [25 * 60_000, '25 min'],
    [59 * 60_000 + 59_000, '59 min'],
    [60 * 60_000, '1 u'],
    [65 * 60_000, '1 u 5 min'],
    [23 * 3_600_000 + 59 * 60_000, '23 u 59 min'],
    [24 * 3_600_000, '1 d'],
    [28 * 3_600_000, '1 d 4 u'],
    [49 * 3_600_000 + 30 * 60_000, '2 d 1 u'],
    [-12 * 60_000, '12 min'],
  ])('%d ms is %s', (ms, want) => {
    expect(formatDuration(ms)).toBe(want)
  })
})

describe('slaTimer', () => {
  it('counts down to the first response and marks it at risk from the server state', () => {
    const t = slaTimer(sla({ state: 'at_risk', first_response_due_at: at(25) }), 'open', NOW)
    expect(t).toMatchObject({ target: 'first_response', breached: false, atRisk: true, short: 'Nog 25 min', long: 'Eerste reactie over 25 min' })
  })

  it('is not at risk while the state is ok', () => {
    expect(slaTimer(sla({ first_response_due_at: at(180) }), 'open', NOW)).toMatchObject({ atRisk: false, short: 'Nog 3 u' })
  })

  it('is breached from the moment the deadline has passed, whatever the stored state says', () => {
    const t = slaTimer(sla({ state: 'ok', first_response_due_at: at(-12) }), 'open', NOW)
    expect(t).toMatchObject({ breached: true, atRisk: false, short: '12 min te laat', long: 'Eerste reactie 12 min te laat' })
    expect(slaTimer(sla({ first_response_due_at: at(0) }), 'open', NOW)?.breached).toBe(false)
    expect(slaTimer(sla({ first_response_due_at: at(0) }), 'open', NOW + 1)?.breached).toBe(true)
  })

  it('moves on to the resolution once the first response is given', () => {
    const t = slaTimer(sla({ first_response_due_at: at(-30), first_response_met_at: at(-40), resolution_due_at: at(90) }), 'open', NOW)
    expect(t).toMatchObject({ target: 'resolution', short: 'Nog 1 u 30 min', long: 'Oplossing over 1 u 30 min' })
  })

  it('shows the first response before the resolution while both are pending', () => {
    expect(slaTimer(sla({ first_response_due_at: at(10), resolution_due_at: at(500) }), 'open', NOW)?.target).toBe('first_response')
  })

  it('stops the resolution clock while waiting but not the first response', () => {
    expect(slaTimer(sla({ first_response_met_at: at(-1), resolution_due_at: at(-100) }), 'waiting', NOW)).toBeNull()
    expect(slaTimer(sla({ first_response_due_at: at(5) }), 'waiting', NOW)?.target).toBe('first_response')
  })

  it('shows nothing for finished conversations, without a policy or without a pending deadline', () => {
    const s = sla({ first_response_due_at: at(5), resolution_due_at: at(50) })
    expect(slaTimer(s, 'closed', NOW)).toBeNull()
    expect(slaTimer(s, 'spam', NOW)).toBeNull()
    expect(slaTimer(null, 'open', NOW)).toBeNull()
    expect(slaTimer(sla(), 'open', NOW)).toBeNull()
  })
})

describe('SLA events in the timeline', () => {
  const ev = (type: string, data: Record<string, unknown>) => ({ id: 'e', type, created_at: at(0), actor: null, user: null, data })

  it('describes them as system events', () => {
    expect(describeEvent(ev('sla_at_risk', { target: 'first_response' }))).toBe('Eerste reactie dreigt te laat te komen')
    expect(describeEvent(ev('sla_breached', { target: 'resolution' }))).toBe('De oplostijd is overschreden')
  })

  it('names the source when a rule or a macro made the change', () => {
    expect(describeEvent(ev('labeled', { label: 'Facturen', source: 'rule' }))).toBe('Een regel voegde label Facturen toe')
    expect(describeEvent({ ...ev('priority_changed', { to: 'high', source: 'macro' }), actor: { id: 'u', name: 'Sanne' } })).toBe('Sanne zette de prioriteit op hoog via een macro')
    expect(describeEvent(ev('resolved', { source: 'system' }))).toBe('Echoo sloot het gesprek automatisch')
  })
})
