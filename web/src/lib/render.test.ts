import { describe, expect, it } from 'vitest'

import { frameHeight, warningText } from './render'

const frame = {} as Window
const other = {} as Window
const ok = { type: 'echoo:render-height', height: 240.2 }

describe('frameHeight', () => {
  it('accepts a numeric height from the frame and rounds it up', () => {
    expect(frameHeight({ source: frame, data: ok }, frame)).toBe(241)
  })

  it('ignores messages from any other window', () => {
    expect(frameHeight({ source: other, data: ok }, frame)).toBeUndefined()
    expect(frameHeight({ source: null, data: ok }, frame)).toBeUndefined()
  })

  it('ignores everything when the frame is not mounted', () => {
    expect(frameHeight({ source: frame, data: ok }, null)).toBeUndefined()
    expect(frameHeight({ source: frame, data: ok }, undefined)).toBeUndefined()
  })

  it.each([
    ['not an object', 'height'],
    ['null', null],
    ['wrong type', { type: 'other', height: 10 }],
    ['missing type', { height: 10 }],
    ['string height', { type: 'echoo:render-height', height: '10' }],
    ['NaN', { type: 'echoo:render-height', height: Number.NaN }],
    ['Infinity', { type: 'echoo:render-height', height: Infinity }],
    ['negative', { type: 'echoo:render-height', height: -1 }],
    ['missing height', { type: 'echoo:render-height' }],
  ])('rejects %s', (_name, data) => {
    expect(frameHeight({ source: frame, data }, frame)).toBeUndefined()
  })

  it('caps absurd heights so a hostile document cannot make the page endless', () => {
    expect(frameHeight({ source: frame, data: { type: 'echoo:render-height', height: 1e9 } }, frame)).toBe(50_000)
  })
})

describe('warningText', () => {
  it('describes each known kind with its detail', () => {
    expect(warningText({ kind: 'auth_failed', detail: 'spf, dkim' })).toContain('SPF, DKIM')
    expect(warningText({ kind: 'reply_to_mismatch', detail: 'other.test' })).toContain('other.test')
    expect(warningText({ kind: 'link_mismatch', detail: 'bank.example -> evil.test' })).toBe('Een link toont bank.example, maar leidt naar evil.test.')
  })

  it('falls back to generic copy for unknown kinds and malformed details', () => {
    expect(warningText({ kind: 'new_kind', detail: 'x' })).not.toContain('x')
    expect(warningText({ kind: 'link_mismatch', detail: 'broken' })).toContain('andere site')
  })

  it('never uses exclamation marks', () => {
    for (const kind of ['display_name_spoof', 'reply_to_mismatch', 'punycode_domain', 'mixed_script_domain', 'auth_failed', 'link_mismatch', 'x']) {
      expect(warningText({ kind, detail: 'a -> b' })).not.toContain('!')
    }
  })
})
