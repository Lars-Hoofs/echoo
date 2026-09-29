import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { applyPresence, getViewers, resetPresence, seedPresence, setPresenceClock, typingLabel, viewersLabel } from './presence'

describe('presence store', () => {
  let t: number
  beforeEach(() => {
    vi.useFakeTimers()
    t = 1_000_000
    setPresenceClock(() => t)
  })
  afterEach(() => {
    resetPresence()
    vi.useRealTimers()
  })

  const advance = (ms: number) => {
    t += ms
    vi.advanceTimersByTime(ms)
  }

  it('tracks viewers and removes them on left', () => {
    applyPresence({ conversation_id: 'c1', user_id: 'u1', state: 'viewing', name: 'Sanne' })
    expect(getViewers('c1')).toEqual([{ id: 'u1', name: 'Sanne', typing: false }])
    applyPresence({ conversation_id: 'c1', user_id: 'u1', state: 'left' })
    expect(getViewers('c1')).toEqual([])
  })

  it('keeps a stable snapshot when nothing changed', () => {
    applyPresence({ conversation_id: 'c1', user_id: 'u1', state: 'viewing', name: 'Sanne' })
    const first = getViewers('c1')
    applyPresence({ conversation_id: 'c1', user_id: 'u1', state: 'viewing', name: 'Sanne' })
    expect(getViewers('c1')).toBe(first)
  })

  it('lets typing lapse after 8 seconds while the viewer stays', () => {
    applyPresence({ conversation_id: 'c1', user_id: 'u1', state: 'typing', name: 'Sanne' })
    expect(getViewers('c1')[0]?.typing).toBe(true)
    advance(8_100)
    expect(getViewers('c1')).toEqual([{ id: 'u1', name: 'Sanne', typing: false }])
  })

  it('expires viewers after 30 seconds without a heartbeat', () => {
    applyPresence({ conversation_id: 'c1', user_id: 'u1', state: 'viewing', name: 'Sanne' })
    advance(29_000)
    expect(getViewers('c1')).toHaveLength(1)
    advance(1_100)
    expect(getViewers('c1')).toEqual([])
  })

  it('a viewing beat does not cancel typing', () => {
    applyPresence({ conversation_id: 'c1', user_id: 'u1', state: 'typing', name: 'Sanne' })
    advance(3_000)
    applyPresence({ conversation_id: 'c1', user_id: 'u1', state: 'viewing' })
    expect(getViewers('c1')).toEqual([{ id: 'u1', name: 'Sanne', typing: true }])
  })

  it('ignores events without ids or with an unknown state', () => {
    applyPresence({ user_id: 'u1', state: 'viewing' })
    applyPresence({ conversation_id: 'c1', state: 'viewing' })
    applyPresence({ conversation_id: 'c1', user_id: 'u1', state: 'dancing' })
    expect(getViewers('c1')).toEqual([])
  })

  it('seeds from the server and orders by name', () => {
    seedPresence('c1', [
      { user_id: 'u2', name: 'Piet', typing: true },
      { user_id: 'u1', name: 'Anna', typing: false },
    ])
    expect(getViewers('c1').map((v) => v.name)).toEqual(['Anna', 'Piet'])
    expect(getViewers('c1')[1]?.typing).toBe(true)
  })
})

describe('labels', () => {
  const v = (name: string, typing = false) => ({ id: name, name, typing })

  it('names one, two or more viewers', () => {
    expect(viewersLabel([v('Sanne')])).toBe('Sanne')
    expect(viewersLabel([v('Sanne'), v('Piet')])).toBe('Sanne en Piet')
    expect(viewersLabel([v('Sanne'), v('Piet'), v('Kees')])).toBe('Sanne, Piet en 1 andere')
    expect(viewersLabel([v('Sanne'), v('Piet'), v('Kees'), v('Els')])).toBe('Sanne, Piet en 2 anderen')
  })

  it('describes who is typing', () => {
    expect(typingLabel([v('Sanne'), v('Piet')])).toBe('')
    expect(typingLabel([v('Sanne', true), v('Piet')])).toBe('Sanne typt een antwoord.')
    expect(typingLabel([v('Sanne', true), v('Piet', true)])).toBe('Sanne en Piet typen een antwoord.')
    expect(typingLabel([v('A', true), v('B', true), v('C', true)])).toBe('Meerdere collega’s typen een antwoord.')
  })
})
