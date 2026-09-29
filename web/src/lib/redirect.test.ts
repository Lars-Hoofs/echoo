import { describe, expect, it } from 'vitest'

import { safeReturnPath } from './redirect'

describe('safeReturnPath', () => {
  it('accepts relative paths', () => {
    expect(safeReturnPath('/instellingen/profiel?x=1')).toBe('/instellingen/profiel?x=1')
  })
  it.each(['//evil.example', '/\\evil.example', 'https://evil.example', 'javascript:alert(1)', '', 42, undefined])(
    'rejects %s',
    (v) => {
      expect(safeReturnPath(v)).toBeUndefined()
    },
  )
})
