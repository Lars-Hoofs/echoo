// Pure layout of a flow diagram: columns of nodes joined by bands, band and node heights
// proportional to the counts they carry.

export interface FlowNode {
  id: string
  label: string
  value: number
  column: number
  accent?: boolean | undefined
  // Start this node's column level with the top of another node's slot (its label included).
  alignWith?: string | undefined
}

export interface FlowLink {
  from: string
  to: string
  value: number
  accent?: boolean | undefined
}

export interface PlacedNode extends FlowNode {
  x: number
  // Top of the node; its label sits above, within labelHeight.
  y: number
  height: number
}

export interface PlacedLink {
  from: string
  to: string
  value: number
  accent: boolean
  path: string
}

export interface FlowLayout {
  nodes: PlacedNode[]
  links: PlacedLink[]
  height: number
}

export interface FlowOptions {
  // x of the left edge of each column's nodes.
  columnX: number[]
  nodeWidth: number
  // Room above every node for its label.
  labelHeight: number
  // Vertical space between one node and the next label.
  gap: number
  // Height the tallest column may take.
  height: number
  minNodeHeight?: number
}

// Nodes with nothing in them are dropped, together with their bands, so that a diagram without
// spam simply has no spam node.
export function layoutFlow(allNodes: FlowNode[], allLinks: FlowLink[], o: FlowOptions): FlowLayout {
  const nodes = allNodes.filter((n) => n.value > 0)
  const alive = new Set(nodes.map((n) => n.id))
  const links = allLinks.filter((l) => l.value > 0 && alive.has(l.from) && alive.has(l.to))
  const minHeight = o.minNodeHeight ?? 2
  const columns = Math.max(0, ...nodes.map((n) => n.column + 1))

  // One scale for every column, so equal counts have equal heights everywhere. The column that
  // needs the most room for labels and gaps decides.
  let scale = Infinity
  for (let c = 0; c < columns; c++) {
    const inColumn = nodes.filter((n) => n.column === c)
    const sum = inColumn.reduce((total, n) => total + n.value, 0)
    if (sum > 0) scale = Math.min(scale, (o.height - inColumn.length * (o.labelHeight + o.gap)) / sum)
  }
  if (!Number.isFinite(scale) || scale <= 0) return { nodes: [], links: [], height: 0 }

  const placed = new Map<string, PlacedNode>()
  let bottom = 0
  for (let c = 0; c < columns; c++) {
    const inColumn = nodes.filter((n) => n.column === c)
    const anchor = inColumn.find((n) => n.alignWith)?.alignWith
    const anchorNode = anchor ? placed.get(anchor) : undefined
    let cursor = anchorNode ? anchorNode.y - o.labelHeight : 0
    for (const n of inColumn) {
      const height = Math.max(n.value * scale, minHeight)
      const p: PlacedNode = { ...n, x: o.columnX[c] ?? 0, y: cursor + o.labelHeight, height }
      placed.set(n.id, p)
      cursor = p.y + height + o.gap
      bottom = Math.max(bottom, p.y + height)
    }
  }

  const used = new Map<string, { out: number; in: number }>()
  const cursorOf = (id: string) => {
    let u = used.get(id)
    if (!u) used.set(id, (u = { out: 0, in: 0 }))
    return u
  }
  const out: PlacedLink[] = []
  for (const l of links) {
    const a = placed.get(l.from)
    const b = placed.get(l.to)
    if (!a || !b) continue
    const h = l.value * scale
    const ua = cursorOf(l.from)
    const ub = cursorOf(l.to)
    const x1 = a.x + o.nodeWidth
    const x2 = b.x
    const mid = (x1 + x2) / 2
    const y1 = a.y + ua.out
    const y2 = b.y + ub.in
    ua.out += h
    ub.in += h
    const f = (v: number) => v.toFixed(1)
    out.push({
      from: l.from,
      to: l.to,
      value: l.value,
      accent: l.accent ?? false,
      path: `M${f(x1)} ${f(y1)}C${f(mid)} ${f(y1)} ${f(mid)} ${f(y2)} ${f(x2)} ${f(y2)}L${f(x2)} ${f(y2 + h)}C${f(mid)} ${f(y2 + h)} ${f(mid)} ${f(y1 + h)} ${f(x1)} ${f(y1 + h)}Z`,
    })
  }
  return { nodes: [...placed.values()], links: out, height: bottom }
}
