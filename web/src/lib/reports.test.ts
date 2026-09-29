import { describe, expect, it } from 'vitest'

import {
  bucketFull,
  bucketLabel,
  capitalize,
  countWord,
  durationParts,
  exportUrl,
  formatDelta,
  formatDuration,
  formatNumber,
  formatPercent,
  formatPointsDelta,
  formatRating,
  parseReportSearch,
  periodLabel,
  ratePercent,
  reportParams,
  slaPercent,
} from './reports'

describe('formatNumber', () => {
  it('groups thousands with a dot', () => {
    expect(formatNumber(1284)).toBe('1.284')
    expect(formatNumber(87)).toBe('87')
    expect(formatNumber(1_250_000)).toBe('1.250.000')
  })
})

describe('formatDuration', () => {
  it('reads naturally at every scale', () => {
    expect(formatDuration(null)).toBe('—')
    expect(formatDuration(10)).toBe('< 1 min')
    expect(formatDuration(42 * 60)).toBe('42 min')
    expect(formatDuration(3600)).toBe('1 u')
    expect(formatDuration(6 * 3600 + 10 * 60)).toBe('6 u 10 min')
    expect(formatDuration(2 * 86400 + 3 * 3600)).toBe('2 d 3 u')
    expect(formatDuration(3 * 86400)).toBe('3 d')
  })
  it('rounds to the nearest minute', () => {
    expect(formatDuration(89)).toBe('1 min')
    expect(formatDuration(3599)).toBe('1 u')
  })
})

describe('percentages and ratings', () => {
  it('uses a decimal comma', () => {
    expect(formatPercent(94.1)).toBe('94,1 %')
    expect(formatPercent(100)).toBe('100,0 %')
    expect(formatPercent(null)).toBe('—')
    expect(formatRating(4.3)).toBe('4,3')
    expect(formatRating(null)).toBe('—')
  })
  it('has no percentage without judged conversations', () => {
    expect(ratePercent({ met: 0, total: 0 })).toBeNull()
    expect(ratePercent({ met: 47, total: 50 })).toBe(94)
  })
})

describe('formatDelta', () => {
  it('shows the change against the previous period', () => {
    expect(formatDelta(106, 100)).toBe('+6 %')
    expect(formatDelta(97, 100)).toBe('−3 %')
    expect(formatDelta(100, 100)).toBe('0 %')
  })
  it('is not applicable without a base', () => {
    expect(formatDelta(5, 0)).toBe('n.v.t.')
    expect(formatDelta(null, 5)).toBe('n.v.t.')
    expect(formatDelta(5, null)).toBe('n.v.t.')
  })
})

describe('period and bucket labels', () => {
  it('spells out the period', () => {
    expect(periodLabel('2026-09-01', '2026-09-28')).toBe('1 t/m 28 september')
    expect(periodLabel('2026-08-28', '2026-09-03')).toBe('28 augustus t/m 3 september')
    expect(periodLabel('2026-09-28', '2026-09-28')).toBe('28 september')
    expect(periodLabel('2025-12-20', '2026-01-05')).toBe('20 december 2025 t/m 5 januari 2026')
  })
  it('labels buckets', () => {
    expect(bucketLabel('2026-09-28')).toBe('28 sep')
    expect(bucketFull('2026-09-28', 'day')).toBe('maandag 28 september')
    expect(bucketFull('2026-09-28', 'week')).toBe('Week van 28 september')
  })
})

describe('search params', () => {
  it('drops what is invalid', () => {
    expect(parseReportSearch({ periode: '30d', van: 'x', tot: '2026-09-28', mailbox: '', team: 't1', zzz: 1 })).toEqual({
      periode: '30d',
      tot: '2026-09-28',
      team: 't1',
    })
    expect(parseReportSearch({ periode: 'jaar' })).toEqual({})
  })
  it('builds the API query', () => {
    expect(reportParams({}).toString()).toBe('period=7d')
    expect(reportParams({ periode: 'custom' }).toString()).toBe('period=7d')
    expect(reportParams({ periode: 'custom', van: '2026-09-01', tot: '2026-09-28', mailbox: 'm1' }).toString()).toBe(
      'period=custom&from=2026-09-01&to=2026-09-28&mailbox=m1',
    )
    expect(exportUrl('csat', { periode: 'today' }, { by: 'team' })).toBe('/api/v1/reports/csat/export?period=today&by=team')
  })
})

describe('durationParts', () => {
  it('splits a duration into numbers and units', () => {
    expect(durationParts(null)).toEqual([{ value: '—' }])
    expect(durationParts(10)).toEqual([{ value: '< 1', unit: 'min' }])
    expect(durationParts(42 * 60)).toEqual([{ value: '42', unit: 'min' }])
    expect(durationParts(2 * 3600)).toEqual([{ value: '2', unit: 'u' }])
    expect(durationParts(6 * 3600 + 10 * 60)).toEqual([{ value: '6', unit: 'u' }, { value: '10', unit: 'min' }])
    expect(durationParts(51 * 3600)).toEqual([{ value: '2', unit: 'd' }, { value: '3', unit: 'u' }])
  })
  it('agrees with formatDuration on the rounding', () => {
    expect(durationParts(89 * 60 + 40)).toEqual([{ value: '1', unit: 'u' }, { value: '30', unit: 'min' }])
    expect(formatDuration(89 * 60 + 40)).toBe('1 u 30 min')
  })
})

describe('formatPointsDelta', () => {
  it('states the change in points', () => {
    expect(formatPointsDelta(88.5, 86.4)).toBe('+2,1 pt')
    expect(formatPointsDelta(80, 84.3)).toBe('−4,3 pt')
    expect(formatPointsDelta(90, 90)).toBe('0 pt')
    expect(formatPointsDelta(90, null)).toBe('n.v.t.')
  })
})

describe('slaPercent', () => {
  const metrics = (fr: [number, number], res: [number, number]) =>
    ({ sla_first_response: { met: fr[0], total: fr[1] }, sla_resolution: { met: res[0], total: res[1] } }) as Parameters<typeof slaPercent>[0]
  it('prefers the first response and falls back to the resolution', () => {
    expect(slaPercent(metrics([9, 10], [1, 10]))).toBe(90)
    expect(slaPercent(metrics([0, 0], [1, 4]))).toBe(25)
    expect(slaPercent(metrics([0, 0], [0, 0]))).toBeNull()
  })
})

describe('countWord', () => {
  it('writes small counts in words and larger ones as digits', () => {
    expect(countWord(3)).toBe('drie')
    expect(countWord(12)).toBe('twaalf')
    expect(countWord(13)).toBe('13')
    expect(countWord(1284)).toBe('1.284')
    expect(capitalize(countWord(1))).toBe('Één')
  })
})
