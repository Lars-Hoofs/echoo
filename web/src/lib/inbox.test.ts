import { describe, expect, it } from 'vitest'

import { parseInboxSearch, slugFromView, viewFromSlug } from './inbox'

describe('view slugs', () => {
  it.each([
    ['mine', 'mine'],
    ['zonder-toewijzing', 'unassigned'],
    ['alle', 'all'],
  ] as const)('%s maps to API view %s and back', (slug, view) => {
    expect(viewFromSlug(slug)).toBe(view)
    expect(slugFromView(view)).toBe(slug)
  })

  it('rejects unknown slugs, including inherited object keys', () => {
    expect(viewFromSlug('all')).toBeUndefined()
    expect(viewFromSlug('unassigned')).toBeUndefined()
    expect(viewFromSlug('toString')).toBeUndefined()
    expect(viewFromSlug('')).toBeUndefined()
  })
})

describe('parseInboxSearch', () => {
  it('keeps non-default status, mailbox and team', () => {
    expect(parseInboxSearch({ status: 'closed', mailbox: 'm1', team: 't1' })).toEqual({ status: 'closed', mailbox: 'm1', team: 't1' })
  })

  it('accepts the snoozed view and a label filter', () => {
    expect(parseInboxSearch({ status: 'snoozed', label: 'l1' })).toEqual({ status: 'snoozed', label: 'l1' })
    expect(parseInboxSearch({ label: '' })).toEqual({})
  })

  it('drops the default status, empty values and unknown values', () => {
    expect(parseInboxSearch({ status: 'open', mailbox: '', team: 5 })).toEqual({})
    expect(parseInboxSearch({ status: 'archived' })).toEqual({})
  })
})

describe('parseInboxSearch advanced filters', () => {
  it('keeps valid advanced filters and the saved view', () => {
    expect(
      parseInboxSearch({ assignee: 'me', priority: 'high,urgent', attachment: true, after: '2026-01-05', before: '2026-02-01', contact: 'c1', organization: 'o1', weergave: 'v1' }),
    ).toEqual({ assignee: 'me', priority: 'high,urgent', attachment: true, after: '2026-01-05', before: '2026-02-01', contact: 'c1', organization: 'o1', weergave: 'v1' })
  })

  it('drops unknown priorities, bad dates and non-true attachment', () => {
    expect(parseInboxSearch({ priority: 'high,enorm', attachment: 'yes', after: 'gisteren', before: '2026-13' })).toEqual({ priority: 'high' })
    expect(parseInboxSearch({ priority: 'enorm' })).toEqual({})
  })

  it('ignores inherited property names as priorities', () => {
    expect(parseInboxSearch({ priority: 'toString' })).toEqual({})
  })
})
