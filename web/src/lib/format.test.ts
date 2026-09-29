import { describe, expect, it } from 'vitest'

import { describeUserAgent, formatBytes, formatRelative, splitNumber } from './format'

describe('describeUserAgent', () => {
  it.each([
    ['Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/140.0 Safari/537.36', 'Chrome op macOS'],
    ['Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/140.0 Safari/537.36 Edg/140.0', 'Edge op Windows'],
    ['Mozilla/5.0 (iPhone; CPU iPhone OS 18_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.0 Mobile/15E148 Safari/604.1', 'Safari op iOS'],
    ['Mozilla/5.0 (X11; Linux x86_64; rv:130.0) Gecko/20100101 Firefox/130.0', 'Firefox op Linux'],
    ['curl/8.0', 'Onbekende browser'],
  ])('%s', (ua, want) => {
    expect(describeUserAgent(ua)).toBe(want)
  })
})

describe('formatRelative', () => {
  const now = new Date(2026, 8, 28, 15, 0)
  const ago = (ms: number) => new Date(now.getTime() - ms).toISOString()

  it.each([
    [10_000, 'nu'],
    [5 * 60_000, '5 min'],
    [59 * 60_000, '59 min'],
    [3 * 3_600_000, '3 u'],
    [2 * 86_400_000, '2 d'],
  ])('%i ms ago', (ms, want) => {
    expect(formatRelative(ago(ms), now)).toBe(want)
  })

  it('shows a date after a week, with the year only for other years', () => {
    expect(formatRelative(new Date(2026, 2, 12, 9, 0).toISOString(), now)).toBe('12 mrt')
    expect(formatRelative(new Date(2025, 2, 12, 9, 0).toISOString(), now)).toBe('12 mrt 2025')
  })

  it('is empty without a time', () => {
    expect(formatRelative(null, now)).toBe('')
  })
})

describe('formatBytes', () => {
  it.each([
    [512, '512 B'],
    [1500, '1,5 kB'],
    [340_000, '340 kB'],
    [2_400_000, '2,4 MB'],
  ])('%i', (n, want) => {
    expect(formatBytes(n)).toBe(want)
  })
})

describe('splitNumber', () => {
  it.each([
    [1284, 0, '1.284', ''],
    [94.14, 1, '94', ',1'],
    [7, 2, '7', ',00'],
    [-3.5, 1, '-3', ',5'],
    [1234567.891, 2, '1.234.567', ',89'],
  ])('%s with %s decimals', (value, digits, integer, decimals) => {
    expect(splitNumber(value, digits)).toEqual({ integer, decimals })
  })
})
