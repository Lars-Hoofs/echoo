import { useQuery } from '@tanstack/react-query'
import { Link } from '@tanstack/react-router'
import { SmilePlus } from 'lucide-react'

import { BarChart, LineChart } from '../../components/charts'
import { Card, EmptyState, ErrorNotice, Select, Table, TBody, Td, Th, THead, Tr } from '../../components/ui'
import { errorMessage } from '../../lib/errors'
import { formatDateTime } from '../../lib/format'
import { bucketFull, bucketLabel, csatQuery, formatNumber, formatPercent, formatRating, type ReportSearch } from '../../lib/reports'
import { ChartCard, KpiRow, numParts, ReportSkeleton } from './shared'

const dimensions = [
  { value: 'agent', label: 'Per agent', column: 'Agent', none: 'Niet toegewezen' },
  { value: 'team', label: 'Per team', column: 'Team', none: 'Geen team' },
  { value: 'mailbox', label: 'Per mailbox', column: 'Mailbox', none: 'Onbekend' },
  { value: 'label', label: 'Per label', column: 'Label', none: 'Zonder label' },
] as const

export type CsatDimension = (typeof dimensions)[number]['value']

export function CsatTab({ search, by, onBy }: { search: ReportSearch; by: CsatDimension; onBy: (by: CsatDimension) => void }) {
  const q = useQuery(csatQuery(search, by))
  if (q.isPending) return <ReportSkeleton />
  if (q.isError) return <ErrorNotice>{errorMessage(q.error)}</ErrorNotice>
  const d = q.data
  const dim = dimensions.find((x) => x.value === by) ?? dimensions[0]
  const s = d.summary
  const group = d.period.group
  const points = d.series.map((p) => ({ label: bucketLabel(p.date), full: bucketFull(p.date, group), value: p.responses > 0 ? p.average : null }))
  const per = group === 'week' ? 'week' : 'dag'

  return (
    <div className="flex flex-col">
      <KpiRow
        label="Tevredenheid"
        items={[
          {
            label: 'Gemiddelde beoordeling',
            parts: s.responses > 0 && s.average !== null ? [...numParts(s.average, 1).map((p) => ({ ...p, unit: 'van 5' }))] : [{ value: '—' }],
            note: s.responses > 0 ? undefined : 'Nog geen beoordelingen',
          },
          { label: 'Beoordelingen', parts: numParts(s.responses) },
          { label: 'Onderzoeken verstuurd', parts: numParts(s.sent) },
          {
            label: 'Responspercentage',
            parts: s.response_rate === null ? [{ value: '—' }] : numParts(s.response_rate, 1, '%'),
          },
        ]}
      />
      {s.sent === 0 ? (
        <EmptyState
          icon={<SmilePlus size={20} />}
          title="Nog geen tevredenheidsonderzoeken verstuurd in deze periode"
          description="Zet het onderzoek aan per mailbox onder Instellingen, Mailboxen. Het wordt eenmalig verstuurd na het oplossen van een gesprek."
        />
      ) : (
        <div className="flex flex-col gap-4">
          <section className="grid grid-cols-1 gap-4 xl:grid-cols-[1.55fr_1fr]">
            <ChartCard title="Verdeling van de beoordelingen" remark={`${formatNumber(s.responses)} beoordelingen`} i={4}>
              <BarChart
                title="Verdeling van de beoordelingen"
                description="Staafdiagram met het aantal beoordelingen van 1 tot en met 5."
                seriesLabel="Aantal beoordelingen"
                data={s.distribution.map((value, i) => ({ label: `${i + 1}`, full: `${i + 1} van 5`, value }))}
                format={formatNumber}
                axis={false}
                height={220}
              />
              <p className="t-label">Beoordeling van 1 (ontevreden) tot 5 (tevreden).</p>
            </ChartCard>
            <ChartCard title={`Gemiddelde beoordeling per ${per}`} remark="Op de dag dat het onderzoek is verstuurd" i={5}>
              <LineChart
                title="Gemiddelde beoordeling"
                description={`Lijndiagram met de gemiddelde beoordeling per ${per}, gerekend vanaf de dag waarop het onderzoek is verstuurd.`}
                seriesLabel="Gemiddelde beoordeling"
                points={points}
                format={formatRating}
                height={220}
              />
            </ChartCard>
          </section>
          <ChartCard
            title="Uitsplitsing"
            i={6}
            flush
            remark={
              <Select aria-label="Uitsplitsen naar" value={by} onChange={(e) => onBy(dimensions.find((x) => x.value === e.target.value)?.value ?? 'agent')}>
                {dimensions.map((x) => (
                  <option key={x.value} value={x.value}>
                    {x.label}
                  </option>
                ))}
              </Select>
            }
          >
            <Table>
              <THead>
                <Th>{dim.column}</Th>
                <Th numeric>Verstuurd</Th>
                <Th numeric>Beantwoord</Th>
                <Th numeric>Respons</Th>
                <Th numeric>Gemiddelde</Th>
              </THead>
              <TBody>
                {d.rows.map((r) => (
                  <Tr key={r.id ?? 'none'}>
                    <Td className={r.id ? 'text-ink' : 'text-muted'}>{r.id ? r.name : dim.none}</Td>
                    <Td numeric>{formatNumber(r.sent)}</Td>
                    <Td numeric>{formatNumber(r.responses)}</Td>
                    <Td numeric>{formatPercent(r.response_rate)}</Td>
                    <Td numeric>{formatRating(r.average)}</Td>
                  </Tr>
                ))}
              </TBody>
            </Table>
          </ChartCard>
          <Card title="Opmerkingen" flush>
            {d.comments.length === 0 ? (
              <p className="t-body px-4 py-5 sm:px-6">Klanten hebben in deze periode geen opmerkingen achtergelaten.</p>
            ) : (
              <ul className="list m-0 p-0">
                {d.comments.map((c) => (
                  <li key={c.conversation_id} className="flex flex-col gap-1 px-4 py-4 sm:px-6">
                    <div className="flex flex-wrap items-baseline gap-x-3 t-label">
                      <span className="text-ink tabular-nums">{c.rating} van 5</span>
                      <Link to="/inbox/$view/$conversationId" params={{ view: 'alle', conversationId: c.conversation_id }} className="text-ink underline-offset-2 hover:underline">
                        #{c.number} {c.subject}
                      </Link>
                      <span>{formatDateTime(c.at)}</span>
                      {c.assignee && <span>{c.assignee}</span>}
                    </div>
                    <p className="m-0 text-read whitespace-pre-wrap text-ink">{c.comment}</p>
                  </li>
                ))}
              </ul>
            )}
          </Card>
        </div>
      )}
    </div>
  )
}
