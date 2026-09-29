import { describe, expect, it } from 'vitest'

import { bandScale, barRects, durationUnit, labelIndexes, linearScale, niceTicks, niceTicksInUnit, rulerMax, valueLabelIndexes } from './scale'

describe('niceTicks', () => {
  it('rounds the top up to a nice number', () => {
    expect(niceTicks(87)).toEqual({ ticks: [0, 50, 100], max: 100 })
    expect(niceTicks(10)).toEqual({ ticks: [0, 5, 10], max: 10 })
    expect(niceTicks(1284).max).toBe(1500)
  })
  it('handles empty and tiny data', () => {
    expect(niceTicks(0)).toEqual({ ticks: [0, 1], max: 1 })
    expect(niceTicks(1).ticks[0]).toBe(0)
    expect(niceTicks(3, 3).ticks).toEqual([0, 1, 2, 3])
  })
  it('always covers the maximum', () => {
    for (const max of [0.3, 7, 99, 100, 101, 5432, 123456]) expect(niceTicks(max).max).toBeGreaterThanOrEqual(max)
  })
})

describe('niceTicksInUnit', () => {
  it('scales ticks to whole units', () => {
    const t = niceTicksInUnit(7500, 3600)
    expect(t.ticks.every((v) => v % 3600 === 0 || v % 1800 === 0)).toBe(true)
    expect(t.max).toBeGreaterThanOrEqual(7500)
  })
  it('picks the unit from the maximum', () => {
    expect(durationUnit(600)).toBe(60)
    expect(durationUnit(5400)).toBe(3600)
    expect(durationUnit(3 * 3600)).toBe(3600)
    expect(durationUnit(3 * 86400)).toBe(86400)
  })
})

describe('linearScale', () => {
  it('maps the domain to the range, also inverted', () => {
    const y = linearScale([0, 100], [200, 0])
    expect(y(0)).toBe(200)
    expect(y(50)).toBe(100)
    expect(y(100)).toBe(0)
  })
  it('survives an empty domain', () => {
    expect(linearScale([5, 5], [10, 20])(5)).toBe(10)
  })
})

describe('bandScale and barRects', () => {
  it('spaces bands evenly with padding', () => {
    const b = bandScale(4, [0, 400], 0.2)
    expect(b.step).toBe(100)
    expect(b.width).toBe(80)
    expect(b.x(0)).toBe(10)
    expect(b.center(1)).toBe(150)
  })
  it('builds bars from the baseline up', () => {
    const b = bandScale(2, [0, 200], 0)
    const y = linearScale([0, 10], [100, 0])
    expect(barRects([5, 0], b, y, 100)).toEqual([
      { x: 0, y: 50, width: 100, height: 50 },
      { x: 100, y: 100, width: 100, height: 0 },
    ])
  })
  it('has no bands for no data', () => {
    expect(bandScale(0, [0, 100]).step).toBe(0)
  })
})

describe('labelIndexes', () => {
  it('shows every label when they fit', () => {
    expect(labelIndexes(5, 8)).toEqual([0, 1, 2, 3, 4])
  })
  it('thins out evenly', () => {
    expect(labelIndexes(30, 6)).toEqual([0, 5, 10, 15, 20, 25])
    expect(labelIndexes(0, 6)).toEqual([])
  })
})

describe('valueLabelIndexes', () => {
  it('labels every bar when they fit', () => {
    expect(valueLabelIndexes([3, 1, 2], true)).toEqual([0, 1, 2])
  })
  it('labels only the highest and the latest bar otherwise', () => {
    expect(valueLabelIndexes([3, 9, 2, 4], false)).toEqual([1, 3])
    expect(valueLabelIndexes([3, 2, 9], false)).toEqual([2])
    expect(valueLabelIndexes([], false)).toEqual([])
  })
})

describe('rulerMax', () => {
  it('ends the ruler on a round duration with room to spare', () => {
    expect(rulerMax(60 * 60)).toBe(5400)
    expect(rulerMax(108 * 60)).toBe(10800)
    expect(rulerMax(10 * 60)).toBe(900)
  })
  it('always covers the value', () => {
    for (const v of [1, 899, 3600, 5000, 86400, 700000, 3_000_000]) expect(rulerMax(v)).toBeGreaterThan(v)
  })
})
