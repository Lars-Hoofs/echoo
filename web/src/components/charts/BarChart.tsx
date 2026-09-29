import { useId, useState } from 'react'

import { HatchPattern, SrTable, Tooltip, axisText, margin, useWidth } from './common'
import { bandScale, barRects, labelIndexes, linearScale, niceTicks, valueLabelIndexes } from './scale'

export interface BarDatum {
  label: string
  // Longer description for the tooltip and the data table; defaults to label.
  full?: string
  // The whole bar.
  value: number
  // The part of the bar drawn solid accent, on top of the hatched rest. Only for a part that
  // carries the accent's meaning ("the customer waits on us").
  accent?: number
}

// Hatched bars as in ron. A bar can carry a solid accent part; the bar under the pointer or focus
// turns solid ink. Values are written on the chart: on every bar when they fit, else on the
// highest and the latest.
export function BarChart({
  title,
  description,
  seriesLabel,
  accentLabel,
  data,
  format,
  height = 200,
  axis = true,
}: {
  title: string
  description: string
  seriesLabel: string
  // Names the accent part in the tooltip and the data table.
  accentLabel?: string
  data: BarDatum[]
  format: (n: number) => string
  height?: number
  // Value gridlines and their labels; off for a chart of a few bars that carry their own labels.
  axis?: boolean
}) {
  const id = useId()
  const [ref, width] = useWidth()
  const [hover, setHover] = useState<number | null>(null)
  const left = axis ? margin.left : 4
  const plot = { left, right: width - margin.right, top: 24, bottom: height - margin.bottom }
  const max = Math.max(0, ...data.map((d) => d.value))
  const { ticks, max: top } = niceTicks(max)
  const y = linearScale([0, top], [plot.bottom, plot.top])
  const band = bandScale(data.length, [plot.left, plot.right], data.length > 45 ? 0.1 : 0.25)
  const rects = barRects(
    data.map((d) => d.value),
    band,
    y,
    plot.bottom,
  )
  const name = (d: BarDatum) => d.full ?? d.label
  const labels = labelIndexes(data.length, Math.max(2, Math.floor((plot.right - plot.left) / 56)))
  const valueLabels = valueLabelIndexes(
    data.map((d) => d.value),
    band.step >= 30,
  )
  const hasAccent = accentLabel !== undefined

  return (
    <figure className="m-0">
      <div ref={ref} className="relative">
        <svg width={width} height={height} role="img" aria-labelledby={`${id}-t ${id}-d`} className="block">
          <title id={`${id}-t`}>{title}</title>
          <desc id={`${id}-d`}>{description}</desc>
          {axis &&
            ticks.map((t) => (
              <g key={t}>
                <line x1={plot.left} x2={plot.right} y1={y(t)} y2={y(t)} className="stroke-chart-grid" strokeWidth={1} />
                <text x={plot.left - 6} y={y(t)} textAnchor="end" dominantBaseline="middle" className={axisText}>
                  {format(t)}
                </text>
              </g>
            ))}
          {!axis && <line x1={plot.left} x2={plot.right} y1={plot.bottom} y2={plot.bottom} className="stroke-chart-neutral" strokeWidth={1} />}
          {data.map((d, i) => {
            const r = rects[i]
            if (!r) return null
            const active = hover === i
            const accent = Math.min(d.accent ?? 0, d.value)
            const accentHeight = d.value > 0 ? (r.height * accent) / d.value : 0
            const restHeight = r.height - accentHeight
            return (
              <g key={`${d.label}-${i}`}>
                <rect
                  x={r.x}
                  y={r.y + accentHeight}
                  width={r.width}
                  height={restHeight}
                  fill={`url(#${id}-hatch)`}
                  tabIndex={0}
                  aria-label={`${name(d)}: ${format(d.value)}${hasAccent ? `, waarvan ${format(accent)} ${accentLabel}` : ''}`}
                  className={`outline-none ${active ? 'fill-ink' : ''}`}
                  onFocus={() => setHover(i)}
                  onBlur={() => setHover(null)}
                  onMouseEnter={() => setHover(i)}
                  onMouseLeave={() => setHover(null)}
                />
                {accentHeight > 0 && <rect x={r.x} y={r.y} width={r.width} height={accentHeight} className="fill-accent" onMouseEnter={() => setHover(i)} onMouseLeave={() => setHover(null)} />}
                <line x1={r.x} x2={r.x + r.width} y1={r.y} y2={r.y} className={accentHeight > 0 || active ? 'stroke-ink' : 'stroke-chart-neutral'} strokeWidth={1} />
                {restHeight > 0 && accentHeight > 0 && <line x1={r.x} x2={r.x + r.width} y1={r.y + accentHeight} y2={r.y + accentHeight} className="stroke-chart-neutral" strokeWidth={1} />}
                <rect x={band.x(i) - (band.step - band.width) / 2} y={plot.top} width={band.step} height={plot.bottom - plot.top} fill="transparent" onMouseEnter={() => setHover(i)} onMouseLeave={() => setHover(null)} />
              </g>
            )
          })}
          {valueLabels.map((i) => {
            const d = data[i]
            const r = rects[i]
            if (!d || !r) return null
            return (
              <text key={i} x={band.center(i)} y={r.y - 6} textAnchor="middle" className="fill-ink text-xs tabular-nums">
                {format(d.value)}
              </text>
            )
          })}
          {labels.map((i) => (
            <text key={i} x={band.center(i)} y={height - 6} textAnchor="middle" className={axisText}>
              {data[i]?.label}
            </text>
          ))}
          {/* After the bars, so the first rect or path in the figure is a bar. */}
          <defs>
            <HatchPattern id={`${id}-hatch`} />
          </defs>
        </svg>
        {hover !== null && data[hover] && rects[hover] && (
          <Tooltip x={band.center(hover)} y={rects[hover].y} width={width}>
            <span className="block text-faint">{name(data[hover])}</span>
            <span className="font-medium tabular-nums">{format(data[hover].value)}</span>
            {hasAccent && (data[hover].accent ?? 0) > 0 && (
              <span className="block text-faint tabular-nums">
                {format(data[hover].accent ?? 0)} {accentLabel}
              </span>
            )}
          </Tooltip>
        )}
      </div>
      <SrTable
        caption={title}
        headers={hasAccent ? ['Periode', seriesLabel, accentLabel] : ['Periode', seriesLabel]}
        rows={data.map((d) => (hasAccent ? [name(d), format(d.value), format(d.accent ?? 0)] : [name(d), format(d.value)]))}
      />
    </figure>
  )
}
