import { type CSSProperties, useId } from 'react'

import { SrTable } from './common'

export interface RulerMark {
  value: number
  // Shown next to the mark, with the value: "Mediaan", "SLA-doel".
  label: string
  // The mark that carries the message is drawn larger and its label sits above the ruler.
  primary?: boolean
  tone?: 'ink' | 'muted' | 'alert'
}

const tones = { ink: '[--tick:var(--ink)]', muted: '[--tick:var(--ink-2)]', alert: '[--tick:var(--alert)]' } as const

// Where a label sits so it stays inside the ruler: flush left near the start, flush right near
// the end, centred in between.
function anchor(at: number): string {
  if (at < 12) return 'translate-x-0 text-left'
  if (at > 88) return '-translate-x-full text-right'
  return '-translate-x-1/2 text-center'
}

// A ruler of hairlines from 0 to max with marks at real values; the highlighted mark is the one
// the sentence beside it is about.
export function TickRuler({
  title,
  description,
  max,
  marks,
  format,
}: {
  title: string
  description: string
  max: number
  marks: RulerMark[]
  format: (n: number) => string
}) {
  const id = useId()
  const at = (v: number) => Math.min(100, Math.max(0, (v / max) * 100))
  const primary = marks.filter((m) => m.primary)
  const secondary = marks.filter((m) => !m.primary)

  return (
    <figure className="m-0">
      <div role="img" aria-labelledby={`${id}-t ${id}-d`} className="pt-1">
        <span id={`${id}-t`} className="sr-only">
          {title}
        </span>
        <span id={`${id}-d`} className="sr-only">
          {description}
        </span>
        <div aria-hidden className="relative h-12">
          {primary.map((m) => (
            <div key={m.label} style={{ left: `${at(m.value)}%` }} className={`absolute bottom-0 flex w-max flex-col gap-1 ${anchor(at(m.value))}`}>
              <span className="t-label">{m.label}</span>
              <span className="text-xl font-light text-ink tabular-nums">{format(m.value)}</span>
            </div>
          ))}
        </div>
        <div aria-hidden className="relative pt-2">
          <div className="ticks">
            {marks.map((m) => (
              <i
                key={m.label}
                style={{ '--at': `${at(m.value)}%` } as CSSProperties}
                className={`${tones[m.tone ?? 'ink']} ${m.primary ? '' : '-top-px! -bottom-px! w-px!'}`}
              />
            ))}
          </div>
          <div className="relative mt-3 h-10">
            {secondary.map((m) => (
              <div key={m.label} style={{ left: `${at(m.value)}%` }} className={`absolute top-0 flex w-max flex-col gap-1 ${anchor(at(m.value))}`}>
                <span className="t-label">{m.label}</span>
                <span className="text-base text-ink tabular-nums">{format(m.value)}</span>
              </div>
            ))}
          </div>
          <div className="t-label flex justify-between tabular-nums">
            <span>0</span>
            <span>{format(max)}</span>
          </div>
        </div>
      </div>
      <SrTable caption={title} headers={['Meting', 'Waarde']} rows={marks.map((m) => [m.label, format(m.value)])} />
    </figure>
  )
}
