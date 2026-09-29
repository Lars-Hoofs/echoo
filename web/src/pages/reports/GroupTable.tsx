import { useQuery } from '@tanstack/react-query'
import { Users } from 'lucide-react'
import type { CSSProperties } from 'react'

import { Avatar } from '../../components/Avatar'
import { Card, EmptyState, ErrorNotice, Table, TBody, Td, Th, THead, Tr } from '../../components/ui'
import { errorMessage } from '../../lib/errors'
import {
  formatDuration,
  formatNumber,
  formatPercent,
  overviewQuery,
  ratePercent,
  type ReportRow,
  type ReportSearch,
  tableQuery,
} from '../../lib/reports'
import { hasPermission, meQuery } from '../../lib/session'
import { overviewKpis } from './kpis'
import { BasisNote, ChartCard, KpiRow, ReportSkeleton } from './shared'

export type GroupKind = 'agents' | 'teams' | 'mailboxes' | 'labels'

const copy: Record<GroupKind, { column: string; none: string; empty: string; chart: string; note?: string }> = {
  agents: {
    column: 'Agent',
    none: 'Niet toegewezen',
    empty: 'Nog geen gesprekken of antwoorden van agents in deze periode.',
    chart: 'Nieuwe gesprekken per agent',
  },
  teams: { column: 'Team', none: 'Geen team', empty: 'Nog geen gesprekken voor teams in deze periode.', chart: 'Nieuwe gesprekken per team' },
  mailboxes: {
    column: 'Mailbox',
    none: 'Onbekend',
    empty: 'Nog geen gesprekken in deze mailboxen in deze periode.',
    chart: 'Nieuwe gesprekken per mailbox',
  },
  labels: {
    column: 'Label',
    none: 'Zonder label',
    empty: 'Nog geen gesprekken met of zonder label in deze periode.',
    chart: 'Nieuwe gesprekken per label',
    note: 'Een gesprek telt mee onder elk van zijn labels.',
  },
}

const rankLimit = 8

const width = (fraction: number) => ({ '--w': `${Math.round(fraction * 100)}%` }) as CSSProperties

function RowName({ row, none, avatar }: { row: ReportRow; none: string; avatar: boolean }) {
  return (
    <span className="flex items-center gap-3">
      {avatar && (row.id ? <Avatar name={row.name} /> : <span aria-hidden className="avatar">–</span>)}
      <span className={row.id ? 'text-ink' : 'text-muted'}>{row.id ? row.name : none}</span>
    </span>
  )
}

// The conversations per row as hatched bars with the name and the count written on the chart.
function RankBars({ title, rows, none, note }: { title: string; rows: ReportRow[]; none: string; note?: string | undefined }) {
  const shown = rows.filter((r) => r.new_conversations > 0).sort((a, b) => b.new_conversations - a.new_conversations).slice(0, rankLimit)
  const max = Math.max(1, ...shown.map((r) => r.new_conversations))
  const remark = [rows.length > rankLimit ? `Top ${rankLimit} van ${rows.length}` : '', note].filter(Boolean).join(' · ')
  return (
    <ChartCard title={title} remark={remark || undefined} i={4}>
      {shown.length === 0 ? (
        <p className="t-body">In deze periode zijn geen nieuwe gesprekken binnengekomen.</p>
      ) : (
        <ul className="flex flex-col gap-4" aria-label={title}>
          {shown.map((r) => (
            <li key={r.id ?? 'none'} className="grid grid-cols-[minmax(0,9rem)_1fr_3rem] items-center gap-4 sm:grid-cols-[minmax(0,12rem)_1fr_3rem]">
              <span className={`truncate text-base ${r.id ? 'text-ink' : 'text-muted'}`}>{r.id ? r.name : none}</span>
              <span aria-hidden className="block h-6">
                <span className="hatch block h-full border-t-0 border-r border-r-ink-3" style={{ width: `${(r.new_conversations / max) * 100}%` }} />
              </span>
              <span className="text-right text-lg tabular-nums text-ink">{formatNumber(r.new_conversations)}</span>
            </li>
          ))}
        </ul>
      )}
    </ChartCard>
  )
}

// The rows with a bar for the share of conversations and one for the share within SLA.
export function ReportRows({ rows, kind, detailed = true }: { rows: ReportRow[]; kind: GroupKind; detailed?: boolean }) {
  const c = copy[kind]
  const max = Math.max(1, ...rows.map((r) => r.new_conversations))
  return (
    <Table>
      <THead>
        <Th>{c.column}</Th>
        <Th numeric>Gesprekken</Th>
        {detailed && <Th numeric className="max-lg:hidden">Antwoorden</Th>}
        {detailed && <Th numeric className="max-lg:hidden">Opgelost</Th>}
        <Th numeric>Eerste reactie</Th>
        <Th numeric className="max-md:hidden">Oplostijd</Th>
        <Th numeric>Binnen SLA</Th>
      </THead>
      <TBody>
        {rows.map((r) => {
          const sla = ratePercent(r.sla_first_response) ?? ratePercent(r.sla_resolution)
          return (
            <Tr key={r.id ?? 'none'}>
              <Td className="min-w-40">
                <RowName row={r} none={c.none} avatar={kind === 'agents'} />
              </Td>
              <Td numeric>
                <span className="inline-flex items-center justify-end gap-3">
                  <span aria-hidden className="progress progress-ink w-12 max-sm:hidden">
                    <i style={width(r.new_conversations / max)} />
                  </span>
                  <span className="w-10 text-right">{formatNumber(r.new_conversations)}</span>
                </span>
              </Td>
              {detailed && <Td numeric className="max-lg:hidden">{formatNumber(r.replies)}</Td>}
              {detailed && <Td numeric className="max-lg:hidden">{formatNumber(r.resolved)}</Td>}
              <Td numeric>{formatDuration(r.first_response.median_seconds)}</Td>
              <Td numeric className="max-md:hidden">{formatDuration(r.resolution.median_seconds)}</Td>
              <Td numeric>
                <span className="inline-flex items-center justify-end gap-3">
                  <span aria-hidden className="progress progress-ink w-12 max-sm:hidden">
                    <i style={width((sla ?? 0) / 100)} />
                  </span>
                  <span className="w-16 text-right">{formatPercent(sla)}</span>
                </span>
              </Td>
            </Tr>
          )
        })}
      </TBody>
    </Table>
  )
}

// One tab per dimension: the four figures of the overview, the conversations per row as a chart,
// and the table with the response and resolution times.
export function GroupTable({ kind, search, embedded = false }: { kind: GroupKind; search: ReportSearch; embedded?: boolean }) {
  const q = useQuery(tableQuery(kind, search))
  const overview = useQuery({ ...overviewQuery(search), enabled: !embedded })
  const me = useQuery(meQuery)
  const c = copy[kind]
  if (q.isPending || (!embedded && overview.isPending)) return <ReportSkeleton />
  if (q.isError) return <ErrorNotice>{errorMessage(q.error)}</ErrorNotice>
  if (overview.isError) return <ErrorNotice>{errorMessage(overview.error)}</ErrorNotice>
  const rows = q.data.rows
  const ownOnly = kind === 'agents' && me.data && !hasPermission(me.data, 'reports.view_all_agents')

  const table =
    rows.length === 0 ? (
      <EmptyState icon={<Users size={20} />} title={c.empty} description="Kies een langere periode of een andere mailbox." />
    ) : (
      <ReportRows rows={rows} kind={kind} detailed={!embedded} />
    )
  if (embedded) return table

  return (
    <div className="flex flex-col">
      {overview.data && <KpiRow label="Kerncijfers" items={overviewKpis(overview.data)} />}
      <div className="flex flex-col gap-4">
        <BasisNote basis={q.data.time_basis} />
        {ownOnly && <p className="t-label">Je ziet alleen je eigen cijfers. Beheerders zien alle agents.</p>}
        {rows.length > 0 && <RankBars title={c.chart} rows={rows} none={c.none} note={c.note} />}
        <Card flush>{table}</Card>
      </div>
    </div>
  )
}
