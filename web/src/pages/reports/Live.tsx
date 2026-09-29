import { useQuery } from '@tanstack/react-query'
import { Users } from 'lucide-react'
import type { CSSProperties } from 'react'

import { Avatar } from '../../components/Avatar'
import { Reveal } from '../../components/Reveal'
import { Card, EmptyState, ErrorNotice, Table, TBody, Td, Th, THead, Tr } from '../../components/ui'
import { availabilityLabel } from '../../lib/automation'
import { errorMessage } from '../../lib/errors'
import { formatNumber, type LiveReport, liveQuery, type ReportSearch } from '../../lib/reports'
import { numParts, ReportNum, ReportSkeleton } from './shared'

interface Figure {
  label: string
  value: number
  // The customer is waiting for us: the one thing the accent marks.
  waitsOnUs?: boolean
  // Breached SLAs are the one thing besides errors that gets the alert color.
  breached?: number
}

function Figures({ d }: { d: LiveReport }) {
  const items: Figure[] = [
    { label: 'Open', value: d.open, waitsOnUs: true },
    { label: 'Zonder toewijzing', value: d.unassigned },
    { label: 'Wachtend op klant', value: d.waiting },
    { label: 'SLA-risico', value: d.sla_at_risk + d.sla_breached, breached: d.sla_breached },
  ]
  return (
    <dl aria-label="Nu" className="m-0 grid grid-cols-2 gap-x-5 gap-y-10 py-10 lg:grid-cols-4 lg:gap-x-8 lg:py-14">
      {items.map((k, i) => (
        <Reveal key={k.label} i={i} className="grid min-w-0 grid-cols-[auto_1fr] content-start items-center gap-x-3 gap-y-3">
          <dt className="t-label row-start-2 flex items-center gap-2">
            {k.waitsOnUs && <i aria-hidden className="size-1.5 rounded-full bg-accent" />}
            {k.label}
          </dt>
          <dd className="col-span-2 row-start-1 m-0">
            <ReportNum parts={numParts(k.value)} size="l" />
          </dd>
          {k.breached !== undefined && k.breached > 0 && (
            <dd className="row-start-2 m-0">
              <span className="tag tag-alert">{formatNumber(k.breached)} overschreden</span>
            </dd>
          )}
        </Reveal>
      ))}
    </dl>
  )
}

const share = (part: number, whole: number) => ({ '--w': `${whole === 0 ? 0 : (part / whole) * 100}%` }) as CSSProperties

// Open conversations are solid accent (the customer waits on us), waiting ones hatched; both are
// drawn to the same scale, the busiest agent's total.
function Split({ open, waiting, max }: { open: number; waiting: number; max: number }) {
  return (
    <span aria-hidden className="flex h-5 w-full min-w-24 items-end">
      <span className="solid-accent block h-full w-(--w)" style={share(open, max)} />
      <span className="hatch block h-full w-(--w)" style={share(waiting, max)} />
    </span>
  )
}

export function LiveTab({ search }: { search: ReportSearch }) {
  const q = useQuery(liveQuery(search))
  if (q.isPending) return <ReportSkeleton />
  if (q.isError) return <ErrorNotice>{errorMessage(q.error)}</ErrorNotice>
  const d = q.data
  const max = Math.max(1, ...d.agents.map((a) => a.open + a.waiting))
  return (
    <div className="flex flex-col">
      <Figures d={d} />
      <Reveal i={4}>
        <Card flush title="Per agent" description="Open gesprekken zijn effen, gesprekken die op de klant wachten gearceerd.">
          {d.agents.length === 0 ? (
            <EmptyState
              icon={<Users size={20} />}
              title="Niemand is online en niemand heeft open gesprekken"
              description="Zodra iemand Echoo opent of een gesprek toegewezen krijgt, verschijnt die persoon hier."
            />
          ) : (
            <Table>
              <THead>
                <Th>Agent</Th>
                <Th className="max-md:hidden">Status</Th>
                <Th className="max-sm:hidden">Verdeling</Th>
                <Th numeric>Open</Th>
                <Th numeric>Wachtend</Th>
                <Th numeric>SLA-risico</Th>
              </THead>
              <TBody>
                {d.agents.map((a) => (
                  <Tr key={a.id}>
                    <Td className="min-w-40">
                      <span className="flex items-center gap-3">
                        <Avatar name={a.name} />
                        <span className="text-ink">{a.name}</span>
                      </span>
                    </Td>
                    <Td className="max-md:hidden">
                      <span className="inline-flex items-center gap-2 text-muted">
                        <span aria-hidden className={`size-2 rounded-full border ${a.online ? 'border-ink bg-ink' : 'border-ink-3'}`} />
                        {a.online ? 'In Echoo' : 'Niet in Echoo'}
                        <span className="text-faint">· staat op {availabilityLabel[a.availability].toLowerCase()}</span>
                      </span>
                    </Td>
                    <Td className="w-48 max-sm:hidden">
                      <Split open={a.open} waiting={a.waiting} max={max} />
                    </Td>
                    <Td numeric>{formatNumber(a.open)}</Td>
                    <Td numeric>{formatNumber(a.waiting)}</Td>
                    <Td numeric>{formatNumber(a.sla_risk)}</Td>
                  </Tr>
                ))}
              </TBody>
            </Table>
          )}
        </Card>
      </Reveal>
    </div>
  )
}
