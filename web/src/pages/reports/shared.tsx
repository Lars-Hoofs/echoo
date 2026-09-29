import { Download } from 'lucide-react'
import { Fragment, type ReactNode } from 'react'

import { Reveal } from '../../components/Reveal'
import { buttonClass, Skeleton } from '../../components/ui'
import { splitNumber } from '../../lib/format'
import { formatDelta, type NumPart, type ReportPeriod, periodLabel, type TimeBasis } from '../../lib/reports'

export function ExportButton({ href }: { href: string }) {
  return (
    <a href={href} download className={buttonClass()}>
      <Download size={20} aria-hidden />
      Exporteren
    </a>
  )
}

// The small grey line above the title.
export function PeriodLine({ period }: { period: ReportPeriod }) {
  return (
    <p className="t-label">
      Periode {periodLabel(period.from, period.to)} · tijden in {period.timezone}
    </p>
  )
}

export function BasisNote({ basis }: { basis: TimeBasis }) {
  if (basis === 'wall_clock') return null
  return (
    <p className="t-label">
      {basis === 'business_hours'
        ? 'Reactie- en oplostijden zijn gemeten in kantooruren.'
        : 'Reactie- en oplostijden zijn gemeten in kantooruren voor gesprekken met een SLA-beleid met kantooruren, de rest in klokuren.'}
    </p>
  )
}

export function ReportSkeleton() {
  return (
    <div className="flex flex-col gap-4" role="status" aria-label="Rapport laden">
      <Skeleton className="h-28" />
      <Skeleton className="h-64" />
    </div>
  )
}

// A whole number or a decimal, with the decimals and the unit in grey.
export function numParts(value: number, fractionDigits = 0, unit?: string): NumPart[] {
  const { integer, decimals } = splitNumber(value, fractionDigits)
  return [{ value: integer, decimals: decimals || undefined, unit }]
}

// ron's hero numeral from parts. Parts are joined by real spaces (which the flex layout does not
// draw), so the text reads "6 u 10 min" to a screen reader and in a copied value.
export function ReportNum({ parts, size = 'l' }: { parts: NumPart[]; size?: 's' | 'm' | 'l' }) {
  return (
    <span className={`num num-${size}`}>
      {parts.map((p, i) => (
        <Fragment key={i}>
          {i > 0 && ' '}
          <span className={i > 0 ? 'ml-[0.2em]' : ''}>
            {p.value}
            {p.decimals && <span className="dec">{p.decimals}</span>}
          </span>
          {p.unit && (
            <>
              {' '}
              <span className="unit">{p.unit}</span>
            </>
          )}
        </Fragment>
      ))}
    </span>
  )
}

export interface Kpi {
  label: string
  parts: NumPart[]
  // A neutral tag against the previous period, already formatted ("+6 %", "+2,1 pt").
  delta?: string | undefined
  // Shown where there is no comparison.
  note?: string | undefined
}

// The change of a figure against the previous period; nothing when there is nothing to compare.
export function deltaOf(current: number | null, previous: number | null): string | undefined {
  const delta = formatDelta(current, previous)
  return delta === 'n.v.t.' ? undefined : delta
}

// Numbers are the design: the value large and light, the label small and grey beneath it, and a
// neutral tag with the change. The label is the term of each group, the value its description.
export function KpiRow({ items, label, size = 'l' }: { items: Kpi[]; label: string; size?: 's' | 'm' | 'l' }) {
  return (
    <dl aria-label={label} className="m-0 grid grid-cols-2 gap-x-5 gap-y-10 py-10 lg:grid-cols-4 lg:gap-x-8 lg:py-14">
      {items.map((k, i) => (
        <Reveal key={k.label} i={i} className="grid min-w-0 grid-cols-[auto_1fr] content-start items-center gap-x-3 gap-y-3 max-sm:grid-cols-1">
          <dt className="t-label row-start-2">{k.label}</dt>
          <dd className="col-span-2 row-start-1 m-0 max-sm:col-span-1">
            <ReportNum parts={k.parts} size={size} />
          </dd>
          {(k.delta || k.note) && (
            <dd className="row-start-2 m-0 min-w-0 max-sm:row-start-3 max-sm:justify-self-start">
              {k.delta ? (
                <span className="tag">
                  {k.delta}
                  <span className="sr-only"> ten opzichte van de vorige periode</span>
                </span>
              ) : (
                <span className="t-label">{k.note}</span>
              )}
            </dd>
          )}
        </Reveal>
      ))}
    </dl>
  )
}

// A bordered card with a title, and a short grey remark on the right.
export function ChartCard({
  title,
  remark,
  i = 0,
  className = '',
  flush = false,
  children,
}: {
  title: string
  remark?: ReactNode
  i?: number
  className?: string
  // For a table that brings its own cell padding.
  flush?: boolean
  children: ReactNode
}) {
  return (
    <Reveal i={i} className={`min-w-0 ${className}`}>
      <article className={`card card-line flex h-full flex-col gap-6 ${flush ? 'overflow-hidden p-0' : ''}`}>
        <div className={`flex flex-wrap items-baseline justify-between gap-x-4 gap-y-1 ${flush ? 'px-4 pt-5 sm:px-6 sm:pt-6' : ''}`}>
          <h2 className="t-title">{title}</h2>
          {remark && <span className="t-label">{remark}</span>}
        </div>
        {children}
      </article>
    </Reveal>
  )
}
