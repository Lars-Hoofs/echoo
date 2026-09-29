import { type ReactNode, type RefObject, useEffect, useRef, useState } from 'react'

// Width of the element behind ref, kept in step with layout. Charts are drawn at that width, so
// text stays at its real size instead of scaling with a viewBox.
export function useWidth(fallback = 600): [RefObject<HTMLDivElement | null>, number] {
  const ref = useRef<HTMLDivElement | null>(null)
  const [width, setWidth] = useState(fallback)
  useEffect(() => {
    const el = ref.current
    if (!el || typeof ResizeObserver === 'undefined') return
    const observer = new ResizeObserver(([entry]) => {
      if (entry) setWidth(Math.max(Math.floor(entry.contentRect.width), 120))
    })
    observer.observe(el)
    return () => {
      observer.disconnect()
    }
  }, [])
  return [ref, width]
}

// The data behind a chart as a table for screen readers.
export function SrTable({ caption, headers, rows }: { caption: string; headers: string[]; rows: string[][] }) {
  return (
    <table className="sr-only">
      <caption>{caption}</caption>
      <thead>
        <tr>
          {headers.map((h) => (
            <th key={h} scope="col">
              {h}
            </th>
          ))}
        </tr>
      </thead>
      <tbody>
        {rows.map((r) => (
          <tr key={r.join('|')}>
            {r.map((c, i) => (i === 0 ? <th key={i} scope="row">{c}</th> : <td key={i}>{c}</td>))}
          </tr>
        ))}
      </tbody>
    </table>
  )
}

// ron's hatched fill: hairlines every 6px. Reference it as fill={`url(#${id})`}.
export function HatchPattern({ id }: { id: string }) {
  return (
    <pattern id={id} width={6} height={6} patternUnits="userSpaceOnUse">
      <path d="M0.5 0V6" className="stroke-chart-neutral" strokeWidth={1} />
    </pattern>
  )
}

// A tooltip anchored above a point; x is clamped so it stays inside the chart.
export function Tooltip({ x, y, width, children }: { x: number; y: number; width: number; children: ReactNode }) {
  const half = 80
  const left = Math.min(Math.max(x, half), Math.max(width - half, half))
  return (
    <div
      aria-hidden
      style={{ left, top: y }}
      className="pointer-events-none absolute z-10 w-max max-w-40 -translate-x-1/2 -translate-y-full rounded-md border border-line bg-float px-3 py-2 text-sm text-ink shadow-float"
    >
      {children}
    </div>
  )
}

export const axisText = 'fill-muted text-xs tabular-nums'
export const margin = { top: 8, right: 8, bottom: 24, left: 44 }
