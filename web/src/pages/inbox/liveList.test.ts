import { describe, expect, it } from 'vitest'

import { leadingNewIds, newConversationsLabel } from './liveList'

describe('leadingNewIds', () => {
  const known = new Set(['b', 'c'])

  it('returns the ids above the first known one', () => {
    expect(leadingNewIds(['x', 'y', 'b', 'c'], known)).toEqual(['x', 'y'])
  })

  it('ignores unknown ids further down, which come from loading another page', () => {
    expect(leadingNewIds(['b', 'c', 'later'], known)).toEqual([])
  })

  it('treats a whole list of unknown ids as new', () => {
    expect(leadingNewIds(['x', 'y'], known)).toEqual(['x', 'y'])
  })
})

describe('newConversationsLabel', () => {
  it('uses the singular for one', () => {
    expect(newConversationsLabel(1)).toBe('1 nieuw gesprek')
    expect(newConversationsLabel(3)).toBe('3 nieuwe gesprekken')
  })
})
