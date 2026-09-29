import { Num } from './Num'

export interface Kpi {
  label: string
  value: number | string
}

// A short row of figures under a contact or organization header. Only figures the API returns.
export function KpiStrip({ items }: { items: Kpi[] }) {
  return (
    <dl className="card card-line grid grid-cols-2 gap-x-4 gap-y-5 sm:grid-cols-[repeat(auto-fit,minmax(160px,1fr))]">
      {items.map((k) => (
        <div key={k.label} className="flex flex-col-reverse justify-end gap-1">
          <dt className="t-label">{k.label}</dt>
          <dd>
            <Num value={k.value} size="s" />
          </dd>
        </div>
      ))}
    </dl>
  )
}
