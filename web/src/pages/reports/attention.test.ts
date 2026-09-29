import { describe, expect, it } from 'vitest'

import type { LiveReport } from '../../lib/reports'
import { attentionSummary } from './attention'

function live(over: Partial<LiveReport['attention']> = {}, unassigned = 0): LiveReport {
  return {
    open: 0,
    unassigned,
    waiting: 0,
    sla_at_risk: 0,
    sla_breached: 0,
    agents: [],
    at: '2026-09-29T10:00:00Z',
    attention: { unanswered: 0, breached: 0, due_soon: 0, next_due_seconds: 0, oldest_seconds: 0, unassigned: 0, holders: [], window_minutes: 60, ...over },
  }
}

describe('attentionSummary', () => {
  it('names the conversations about to miss their first-response time, and who holds them', () => {
    const s = attentionSummary(
      live({
        unanswered: 5,
        due_soon: 3,
        next_due_seconds: 25 * 60,
        unassigned: 1,
        holders: [
          { id: 'a', name: 'Sanne', count: 2 },
          { id: 'b', name: 'Bram', count: 1 },
        ],
      }),
    )
    expect(s.kind).toBe('due_soon')
    expect(s.headline).toBe('Drie gesprekken missen de eerste-reactietijd, de eerste over 25 min')
    expect(s.body).toBe('Bij Sanne (2) en Bram (1). Één is nog niet toegewezen.')
  })

  it('puts conversations past their time first and mentions the ones about to follow', () => {
    const s = attentionSummary(live({ breached: 1, due_soon: 2, next_due_seconds: 600, holders: [{ id: 'a', name: 'Daan', count: 1 }] }))
    expect(s.kind).toBe('breached')
    expect(s.headline).toBe('Één gesprek is de eerste-reactietijd al voorbij')
    expect(s.body).toBe('Nog twee gesprekken volgen binnen 10 min. Bij Daan (1).')
  })

  it('falls back to waiting conversations without a target', () => {
    const s = attentionSummary(live({ unanswered: 4, oldest_seconds: 3 * 3600 }))
    expect(s.kind).toBe('unanswered')
    expect(s.headline).toBe('Vier gesprekken wachten op een eerste reactie')
    expect(s.body).toContain('Het oudste wacht al 3 u.')
  })

  it('sends open conversations without an owner to the unassigned view', () => {
    const s = attentionSummary(live({}, 14))
    expect(s.kind).toBe('unassigned')
    expect(s.headline).toBe('14 open gesprekken hebben nog geen behandelaar')
    expect(s.view).toBe('zonder-toewijzing')
  })

  it('says plainly when nothing needs attention', () => {
    const s = attentionSummary(live())
    expect(s.kind).toBe('quiet')
    expect(s.headline).toBe('Er wacht nu geen gesprek op een eerste reactie')
  })
})
