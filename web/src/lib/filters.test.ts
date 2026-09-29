import { describe, expect, it } from 'vitest'

import {
  advancedKey,
  appendFilterParams,
  chipsFor,
  exclusiveEnd,
  hasFilters,
  inclusiveEnd,
  type Lookups,
  sameFilters,
  savedToSearch,
  searchToSaved,
  splitList,
  toggleInList,
  withFilter,
  withoutFilter,
} from './filters'

const lookups: Lookups = {
  mailboxes: new Map([['m1', 'Support'], ['m2', 'Verkoop']]),
  teams: new Map([['t1', 'Team A']]),
  labels: new Map([['l1', 'Factuur']]),
  users: new Map([['u1', 'Jan']]),
}

describe('appendFilterParams', () => {
  it('repeats list values and leaves out what is not set', () => {
    const q = new URLSearchParams()
    appendFilterParams(q, { mailbox: 'a,b', label: 'l1', priority: 'high,urgent', attachment: true, assignee: 'me', after: '2026-01-05', before: '2026-02-01' })
    expect(q.getAll('mailbox_id')).toEqual(['a', 'b'])
    expect(q.getAll('label_id')).toEqual(['l1'])
    expect(q.getAll('priority')).toEqual(['high', 'urgent'])
    expect(q.get('has_attachment')).toBe('true')
    expect(q.get('assignee')).toBe('me')
    expect(q.get('after')).toBe('2026-01-05')
    expect(q.get('before')).toBe('2026-02-01')
    expect(q.has('team_id')).toBe(false)
    expect(q.has('contact_id')).toBe(false)
  })

  it('adds nothing for an empty filter', () => {
    const q = new URLSearchParams()
    appendFilterParams(q, {})
    expect(q.toString()).toBe('')
  })
})

describe('lists in the URL', () => {
  it('splits and toggles comma separated ids', () => {
    expect(splitList('a,b,,c')).toEqual(['a', 'b', 'c'])
    expect(splitList(undefined)).toEqual([])
    expect(toggleInList(undefined, 'a')).toBe('a')
    expect(toggleInList('a', 'b')).toBe('a,b')
    expect(toggleInList('a,b', 'a')).toBe('b')
    expect(toggleInList('a', 'a')).toBeUndefined()
  })
})

describe('advancedKey', () => {
  it('differs when an advanced filter differs', () => {
    expect(advancedKey({})).toBe(advancedKey({}))
    expect(advancedKey({ priority: 'high' })).not.toBe(advancedKey({}))
    expect(advancedKey({ attachment: true })).not.toBe(advancedKey({ after: '2026-01-01' }))
  })
})

describe('saved view conversion', () => {
  it('round-trips the URL filters', () => {
    const search = { status: 'waiting', mailbox: 'm1,m2', label: 'l1', assignee: 'me', priority: 'high', attachment: true, after: '2026-01-01', before: '2026-02-01' } as const
    const saved = searchToSaved(search)
    expect(saved).toEqual({
      status: 'waiting',
      mailbox_ids: ['m1', 'm2'],
      label_ids: ['l1'],
      assignee: 'me',
      priorities: ['high'],
      has_attachment: true,
      after: '2026-01-01',
      before: '2026-02-01',
    })
    expect(savedToSearch(saved)).toEqual(search)
  })

  it('drops the default status and empty lists', () => {
    expect(savedToSearch({ status: 'open', mailbox_ids: [] })).toEqual({})
    expect(searchToSaved({})).toEqual({})
  })
})

describe('sameFilters', () => {
  it('ignores the order of ids and the saved view id', () => {
    expect(sameFilters({ mailbox: 'a,b', weergave: 'v1' }, { mailbox: 'b,a' })).toBe(true)
  })

  it('sees any real difference', () => {
    expect(sameFilters({ priority: 'high' }, { priority: 'high,urgent' })).toBe(false)
    expect(sameFilters({}, { attachment: true })).toBe(false)
    expect(sameFilters({ status: 'closed' }, {})).toBe(false)
  })
})

describe('changing filters', () => {
  it('removes one kind and keeps the rest', () => {
    expect(withoutFilter({ mailbox: 'a', label: 'l1' }, 'mailbox')).toEqual({ label: 'l1' })
  })

  it('sets or clears a value', () => {
    expect(withFilter({}, 'assignee', 'none')).toEqual({ assignee: 'none' })
    expect(withFilter({ assignee: 'none' }, 'assignee', undefined)).toEqual({})
  })

  it('knows when any filter is set', () => {
    expect(hasFilters({})).toBe(false)
    expect(hasFilters({ attachment: true })).toBe(true)
  })
})

describe('period dates', () => {
  it('shows the API exclusive end as an inclusive day', () => {
    expect(inclusiveEnd('2026-03-01')).toBe('2026-02-28')
    expect(exclusiveEnd('2026-02-28')).toBe('2026-03-01')
    expect(exclusiveEnd('2026-12-31')).toBe('2027-01-01')
  })
})

describe('chipsFor', () => {
  it('words each filter as a Dutch sentence', () => {
    const chips = chipsFor(
      { status: 'waiting', mailbox: 'm1,m2', team: 't1', label: 'l1', assignee: 'me', priority: 'high,urgent', attachment: true, after: '2026-01-05', before: '2026-02-01' },
      lookups,
    )
    expect(chips.map((c) => c.text)).toEqual([
      'Status is Wachtend',
      'Mailbox is Support of Verkoop',
      'Team is Team A',
      'Label is Factuur',
      'Toegewezen aan is mij',
      'Prioriteit is Hoog of Urgent',
      'Heeft bijlage',
      expect.stringMatching(/^Vanaf 5 jan\.? 2026$/),
      expect.stringMatching(/^Tot en met 31 jan\.? 2026$/),
    ])
    expect(chips.map((c) => c.key)).toEqual(['status', 'mailbox', 'team', 'label', 'assignee', 'priority', 'attachment', 'after', 'before'])
  })

  it('names people and handles unassigned', () => {
    expect(chipsFor({ assignee: 'none' }, lookups)[0]?.text).toBe('Toegewezen aan is niemand')
    expect(chipsFor({ assignee: 'u1' }, lookups)[0]?.text).toBe('Toegewezen aan is Jan')
    expect(chipsFor({ assignee: 'gone' }, lookups)[0]?.text).toBe('Toegewezen aan is onbekend')
  })

  it('has no chips without filters', () => {
    expect(chipsFor({}, lookups)).toEqual([])
  })

  it('does not count the default status as a filter', () => {
    expect(chipsFor({ status: 'open' }, lookups)).toEqual([])
    expect(hasFilters({ status: 'open' })).toBe(false)
    expect(sameFilters({ status: 'open' }, {})).toBe(true)
    expect(searchToSaved({ status: 'open' })).toEqual({})
  })
})
