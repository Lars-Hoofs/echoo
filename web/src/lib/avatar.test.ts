import { describe, expect, it } from 'vitest'

import { initials } from './avatar'

describe('initials', () => {
  it.each([
    ['Sanne de Vries', 'SV'],
    ['sanne', 'S'],
    ['Jan', 'J'],
    ['jan.jansen@example.com', 'JJ'],
    ['Émile Zola', 'ÉZ'],
    ['', '?'],
  ])('%s', (name, want) => {
    expect(initials(name)).toBe(want)
  })
})
