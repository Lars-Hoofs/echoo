// Pure scale and tick helpers shared by the SVG charts.

// Rounds a step up to 1, 2, 5 or 10 times a power of ten.
function niceStep(rough: number): number {
  const exp = Math.floor(Math.log10(rough))
  const base = 10 ** exp
  const f = rough / base
  const nice = f <= 1 ? 1 : f <= 2 ? 2 : f <= 5 ? 5 : 10
  return nice * base
}

export interface Ticks {
  ticks: number[]
  max: number
}

// Ticks from 0 up to a rounded maximum that covers max. A chart with no data still gets 0..1.
export function niceTicks(max: number, count = 4): Ticks {
  if (!(max > 0)) return { ticks: [0, 1], max: 1 }
  const step = niceStep(max / count)
  const top = Math.ceil(max / step - 1e-9) * step
  const ticks: number[] = []
  for (let v = 0; v <= top + step / 2; v += step) ticks.push(Math.round(v / step) * step)
  return { ticks, max: top }
}

// Like niceTicks for a quantity that reads best in multiples of a unit (60 for minutes, 3600 for
// hours, 86400 for days).
export function niceTicksInUnit(max: number, unit: number, count = 4): Ticks {
  const t = niceTicks(max / unit, count)
  return { ticks: t.ticks.map((v) => v * unit), max: t.max * unit }
}

// The unit in which a duration axis reads best, given the largest value in seconds.
export function durationUnit(maxSeconds: number): number {
  if (maxSeconds >= 2 * 86400) return 86400
  if (maxSeconds >= 3600) return 3600
  return 60
}

export type LinearScale = (v: number) => number

export function linearScale(domain: [number, number], range: [number, number]): LinearScale {
  const [d0, d1] = domain
  const [r0, r1] = range
  const span = d1 - d0
  return (v) => (span === 0 ? r0 : r0 + ((v - d0) / span) * (r1 - r0))
}

export interface Band {
  step: number
  width: number
  // Left edge of band i.
  x: (i: number) => number
  // Centre of band i.
  center: (i: number) => number
}

// n equal bands over range; padding is the fraction of a step left empty between bands.
export function bandScale(n: number, range: [number, number], padding = 0.2): Band {
  const step = n > 0 ? (range[1] - range[0]) / n : 0
  const width = step * (1 - padding)
  const inset = (step - width) / 2
  return { step, width, x: (i) => range[0] + i * step + inset, center: (i) => range[0] + i * step + step / 2 }
}

export interface BarRect {
  x: number
  y: number
  width: number
  height: number
}

// Rectangles of a bar chart whose baseline is at y = bottom. A zero value keeps a hairline of
// 0 height so the bar is still focusable.
export function barRects(values: number[], band: Band, y: LinearScale, bottom: number): BarRect[] {
  return values.map((v, i) => {
    const top = y(v)
    return { x: band.x(i), y: Math.min(top, bottom), width: band.width, height: Math.max(bottom - top, 0) }
  })
}

// Indexes of the x labels to show so that at most maxLabels appear, evenly spread, always
// including the first.
export function labelIndexes(n: number, maxLabels: number): number[] {
  if (n <= 0) return []
  const every = Math.max(1, Math.ceil(n / Math.max(1, maxLabels)))
  const out: number[] = []
  for (let i = 0; i < n; i += every) out.push(i)
  return out
}

// Indexes of the bars that get a value label: all of them when they fit, else only the highest
// and the latest, so labels never collide.
export function valueLabelIndexes(values: number[], fitsAll: boolean): number[] {
  if (values.length === 0) return []
  if (fitsAll) return values.map((_, i) => i)
  const peak = values.reduce((best, v, i) => (v > (values[best] ?? -Infinity) ? i : best), 0)
  const last = values.length - 1
  return peak === last ? [peak] : [peak, last].sort((a, b) => a - b)
}

const rulerSteps = [900, 1800, 3600, 5400, 7200, 10800, 14400, 21600, 28800, 43200, 86400, 172800, 345600, 604800]

// The end of a duration ruler: the first round duration that covers value with some room.
export function rulerMax(seconds: number): number {
  const wanted = seconds * 1.15
  return rulerSteps.find((s) => s >= wanted) ?? Math.ceil(wanted / 604800) * 604800
}
