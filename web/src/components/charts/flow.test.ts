import { describe, expect, it } from 'vitest'

import { type FlowLink, type FlowNode, layoutFlow } from './flow'

const options = { columnX: [0, 200, 400], nodeWidth: 3, labelHeight: 30, gap: 10, height: 300 }

const nodes: FlowNode[] = [
  { id: 'in', label: 'Binnengekomen', value: 100, column: 0 },
  { id: 'answered', label: 'Beantwoord', value: 80, column: 1 },
  { id: 'waiting', label: 'Wacht op ons', value: 12, column: 1, accent: true },
  { id: 'spam', label: 'Spam', value: 8, column: 1 },
  { id: 'closed', label: 'Opgelost', value: 60, column: 2, alignWith: 'answered' },
  { id: 'open', label: 'Nog open', value: 20, column: 2 },
]
const links: FlowLink[] = [
  { from: 'in', to: 'answered', value: 80 },
  { from: 'in', to: 'waiting', value: 12, accent: true },
  { from: 'in', to: 'spam', value: 8 },
  { from: 'answered', to: 'closed', value: 60 },
  { from: 'answered', to: 'open', value: 20 },
]

describe('layoutFlow', () => {
  it('draws equal counts at equal heights in every column', () => {
    const l = layoutFlow(nodes, links, options)
    const byId = new Map(l.nodes.map((n) => [n.id, n]))
    const inHeight = byId.get('in')?.height ?? 0
    const columnTwo = ['answered', 'waiting', 'spam'].reduce((sum, id) => sum + (byId.get(id)?.height ?? 0), 0)
    expect(columnTwo).toBeCloseTo(inHeight, 5)
    const answered = byId.get('answered')?.height ?? 0
    expect((byId.get('closed')?.height ?? 0) + (byId.get('open')?.height ?? 0)).toBeCloseTo(answered, 5)
  })

  it('keeps every column within the height and leaves room for the labels', () => {
    const l = layoutFlow(nodes, links, options)
    expect(l.height).toBeLessThanOrEqual(options.height)
    const column = l.nodes.filter((n) => n.column === 1).sort((a, b) => a.y - b.y)
    for (let i = 1; i < column.length; i++) {
      const previous = column[i - 1]
      const current = column[i]
      expect(current && previous && current.y - (previous.y + previous.height)).toBeGreaterThanOrEqual(options.labelHeight + options.gap)
    }
  })

  it('starts an aligned column level with the node it follows', () => {
    const l = layoutFlow(nodes, links, options)
    const byId = new Map(l.nodes.map((n) => [n.id, n]))
    expect(byId.get('closed')?.y).toBe(byId.get('answered')?.y)
  })

  it('drops empty nodes and the bands that touch them', () => {
    const l = layoutFlow(
      nodes.map((n) => (n.id === 'spam' ? { ...n, value: 0 } : n)),
      links.map((b) => (b.to === 'spam' ? { ...b, value: 0 } : b)),
      options,
    )
    expect(l.nodes.map((n) => n.id)).not.toContain('spam')
    expect(l.links).toHaveLength(4)
  })

  it('never draws a filled node thinner than the minimum', () => {
    const l = layoutFlow([{ id: 'a', label: 'a', value: 1000, column: 0 }, { id: 'b', label: 'b', value: 1, column: 1 }], [{ from: 'a', to: 'b', value: 1 }], { ...options, minNodeHeight: 2 })
    expect(l.nodes.find((n) => n.id === 'b')?.height).toBe(2)
  })

  it('returns an empty layout without any count', () => {
    expect(layoutFlow([{ id: 'a', label: 'a', value: 0, column: 0 }], [], options)).toEqual({ nodes: [], links: [], height: 0 })
  })

  it('carries the accent of a band', () => {
    const l = layoutFlow(nodes, links, options)
    expect(l.links.filter((b) => b.accent).map((b) => b.to)).toEqual(['waiting'])
  })
})
