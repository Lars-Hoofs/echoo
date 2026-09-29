import { useId, useState } from 'react'

import { SrTable, Tooltip, axisText, margin, useWidth } from './common'
import { labelIndexes, linearScale, niceTicksInUnit } from './scale'

export interface LinePoint {
  label: string
  // Longer description for the tooltip and the data table; defaults to label.
  full?: string
  // Null when nothing was measured in that bucket; the line breaks there.
  value: number | null
}

// A single line. unit is the tick spacing base (60 for minutes, 3600 for hours, ...), so axis
// ticks land on round durations.
export function LineChart({
  title,
  description,
  seriesLabel,
  points,
  format,
  unit = 1,
  height = 180,
}: {
  title: string
  description: string
  seriesLabel: string
  points: LinePoint[]
  format: (n: number) => string
  unit?: number
  height?: number
}) {
  const id = useId()
  const [ref, width] = useWidth()
  const [hover, setHover] = useState<number | null>(null)
  const max = Math.max(0, ...points.map((p) => p.value ?? 0))
  const { ticks, max: top } = niceTicksInUnit(max, unit)
  // Duration labels such as "1 u 30 min" are wider than the default margin.
  const left = Math.max(margin.left, Math.ceil(Math.max(...ticks.map((t) => format(t).length)) * 6.4) + 10)
  const plot = { left, right: width - margin.right, top: margin.top, bottom: height - margin.bottom }
  const y = linearScale([0, top], [plot.bottom, plot.top])
  const n = points.length
  const x = (i: number) => (n <= 1 ? (plot.left + plot.right) / 2 : plot.left + 20 + (i / (n - 1)) * (plot.right - plot.left - 40))
  const name = (p: LinePoint) => p.full ?? p.label

  let path = ''
  let pen = false
  points.forEach((p, i) => {
    if (p.value === null) {
      pen = false
      return
    }
    path += `${pen ? 'L' : 'M'}${x(i).toFixed(1)} ${y(p.value).toFixed(1)}`
    pen = true
  })
  const labels = labelIndexes(n, Math.max(2, Math.floor((plot.right - plot.left) / 64)))
  const hovered = hover === null ? undefined : points[hover]

  return (
    <figure className="m-0">
      <div ref={ref} className="relative">
        <svg width={width} height={height} role="img" aria-labelledby={`${id}-t ${id}-d`} className="block">
          <title id={`${id}-t`}>{title}</title>
          <desc id={`${id}-d`}>{description}</desc>
          {ticks.map((t) => (
            <g key={t}>
              <line x1={plot.left} x2={plot.right} y1={y(t)} y2={y(t)} className="stroke-chart-grid" strokeWidth={1} />
              <text x={plot.left - 6} y={y(t)} textAnchor="end" dominantBaseline="middle" className={axisText}>
                {format(t)}
              </text>
            </g>
          ))}
          {path && <path d={path} className="c-line" strokeLinejoin="round" strokeLinecap="round" />}
          {points.map((p, i) =>
            p.value === null ? null : (
              <g key={`${p.label}-${i}`}>
                <circle cx={x(i)} cy={y(p.value)} r={12} fill="transparent" onMouseEnter={() => setHover(i)} onMouseLeave={() => setHover(null)} />
                <circle
                  cx={x(i)}
                  cy={y(p.value)}
                  r={hover === i ? 5 : n > 40 ? 2 : 3.5}
                  tabIndex={0}
                  aria-label={`${name(p)}: ${format(p.value)}`}
                  className={`outline-none ${hover === i ? 'c-dot-solid' : 'c-dot'}`}
                  onFocus={() => setHover(i)}
                  onBlur={() => setHover(null)}
                />
              </g>
            ),
          )}
          {labels.map((i) => (
            <text key={i} x={x(i)} y={height - 6} textAnchor="middle" className={axisText}>
              {points[i]?.label}
            </text>
          ))}
        </svg>
        {hover !== null && hovered && hovered.value !== null && (
          <Tooltip x={x(hover)} y={y(hovered.value) - 8} width={width}>
            <span className="block text-faint">{name(hovered)}</span>
            <span className="font-medium tabular-nums">{format(hovered.value)}</span>
          </Tooltip>
        )}
      </div>
      <SrTable caption={title} headers={['Periode', seriesLabel]} rows={points.map((p) => [name(p), p.value === null ? '—' : format(p.value)])} />
    </figure>
  )
}
