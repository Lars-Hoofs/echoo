import { useId } from 'react'

import { HatchPattern, SrTable, useWidth } from './common'
import { type FlowLink, type FlowNode, layoutFlow } from './flow'

const nodeWidth = 3
const labelHeight = 42
// Room to the right of the last column for its labels.
const labelRoom = 116

// Columns of nodes joined by soft bands; a node is a thin rule with its name and count above it.
// The accent marks only the nodes and bands flagged accent.
export function FlowDiagram({
  title,
  description,
  nodes,
  links,
  format,
  height = 300,
}: {
  title: string
  description: string
  nodes: FlowNode[]
  links: FlowLink[]
  format: (n: number) => string
  height?: number
}) {
  const id = useId()
  const [ref, width] = useWidth()
  const columns = Math.max(1, ...nodes.map((n) => n.column + 1))
  // The last column keeps room for its labels; earlier ones are spread over the rest.
  const span = width - labelRoom - nodeWidth
  const columnX = Array.from({ length: columns }, (_, c) => (columns === 1 ? 0 : (span * c) / (columns - 1)))
  const layout = layoutFlow(nodes, links, { columnX, nodeWidth, labelHeight, gap: 10, height })
  const labelOf = new Map(nodes.map((n) => [n.id, n.label]))

  return (
    <figure className="m-0">
      <div ref={ref}>
        <svg width={width} height={Math.ceil(layout.height) + 4} role="img" aria-labelledby={`${id}-t ${id}-d`} className="block overflow-visible">
          <title id={`${id}-t`}>{title}</title>
          <desc id={`${id}-d`}>{description}</desc>
          <defs>
            <HatchPattern id={`${id}-hatch`} />
          </defs>
          {layout.links.map((l) => (
            <path
              key={`${l.from}-${l.to}`}
              d={l.path}
              fill={l.accent ? undefined : `url(#${id}-hatch)`}
              className={l.accent ? 'fill-accent stroke-ink' : 'stroke-chart-neutral'}
              strokeWidth={1}
              vectorEffect="non-scaling-stroke"
            />
          ))}
          {layout.nodes.map((n) => (
            <g key={n.id}>
              <rect x={n.x} y={n.y} width={nodeWidth} height={n.height} className={n.accent ? 'fill-accent' : 'fill-ink'} />
              <text x={n.x} y={n.y - 28} className="fill-muted text-xs">
                {n.label}
              </text>
              <text x={n.x} y={n.y - 8} className="fill-ink text-xl font-light tabular-nums">
                {format(n.value)}
              </text>
            </g>
          ))}
        </svg>
      </div>
      <SrTable caption={title} headers={['Stap', 'Aantal']} rows={layout.nodes.map((n) => [labelOf.get(n.id) ?? n.id, format(n.value)])} />
    </figure>
  )
}
