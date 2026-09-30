import { describe, expect, it } from 'vitest'

import { describePattern, normalizePattern, patternKind } from './blocklist'

describe('normalizePattern', () => {
  it('trims, lower-cases and drops a leading @', () => {
    expect(normalizePattern('  Spam@Example.NL ')).toBe('spam@example.nl')
    expect(normalizePattern('@Example.nl')).toBe('example.nl')
    expect(normalizePattern('   ')).toBe('')
  })
})

describe('patternKind and describePattern', () => {
  it('tells an address from a domain', () => {
    expect(patternKind('spam@example.nl')).toBe('address')
    expect(patternKind('example.nl')).toBe('domain')
    expect(describePattern('spam@example.nl')).toBe('Alle nieuwe mail van spam@example.nl')
    expect(describePattern('example.nl')).toBe('Alle nieuwe mail van adressen op example.nl')
  })
})
