import { useQuery } from '@tanstack/react-query'
import { Link } from '@tanstack/react-router'
import { ArrowUpRight, ChartColumn } from 'lucide-react'

import { Avatar } from '../../components/Avatar'
import { BarChart, FlowDiagram, TickRuler } from '../../components/charts'
import { rulerMax } from '../../components/charts/scale'
import { useWidth } from '../../components/charts/common'
import { buttonClass, EmptyState, ErrorNotice } from '../../components/ui'
import { errorMessage } from '../../lib/errors'
import {
  bucketFull,
  bucketLabel,
  durationParts,
  formatDuration,
  formatNumber,
  formatRating,
  liveQuery,
  type LiveReport,
  type Overview,
  overviewQuery,
  type ReportSearch,
} from '../../lib/reports'
import { attentionSummary } from './attention'
import { GroupTable } from './GroupTable'
import { overviewKpis } from './kpis'
import { BasisNote, ChartCard, KpiRow, numParts, ReportNum, ReportSkeleton } from './shared'
import { Reveal } from '../../components/Reveal'

function VolumeCard({ o }: { o: Overview }) {
  const group = o.period.group
  const per = group === 'week' ? 'week' : 'dag'
  const data = o.series.map((s) => ({ label: bucketLabel(s.date), full: bucketFull(s.date, group), value: s.new_conversations, accent: s.unanswered }))
  return (
    <ChartCard title={`Volume per ${per}`} remark={`${formatNumber(o.current.new_conversations)} nieuwe gesprekken`} i={4}>
      <BarChart
        title={`Nieuwe gesprekken per ${per}`}
        description={`Staafdiagram met het aantal nieuwe gesprekken per ${per} in de gekozen periode. Het effen deel van een staaf zijn de gesprekken van die ${per} die nog open zijn zonder eerste reactie van ons.`}
        seriesLabel="Nieuwe gesprekken"
        accentLabel="zonder eerste reactie"
        data={data}
        format={formatNumber}
        height={260}
      />
      <p className="t-label">
        Gearceerd: al beantwoord of afgehandeld. Effen: nog open zonder eerste reactie van ons, de klant wacht op ons.
      </p>
    </ChartCard>
  )
}

function InsightCard({ live, search }: { live: LiveReport; search: ReportSearch }) {
  const s = attentionSummary(live)
  const holders = live.attention.holders
  const shown = holders.slice(0, 4)
  return (
    <Reveal i={5} className="min-w-0">
      <article className="card aura flex h-full min-h-80 flex-col justify-between gap-6">
        <div className="flex items-center justify-between gap-3">
          <span className="t-label">Wat nu aandacht vraagt</span>
          {shown.length > 0 && (
            <div className="flex gap-1">
              {shown.map((h) => (
                <Avatar key={h.id} name={h.name} />
              ))}
              {holders.length > shown.length && (
                <span aria-hidden className="avatar">
                  +{holders.length - shown.length}
                </span>
              )}
            </div>
          )}
        </div>
        <div className="flex flex-col gap-3">
          <h2 className="t-h2">{s.headline}</h2>
          {s.body && <p className="t-body">{s.body}</p>}
        </div>
        <div className="flex items-center gap-3">
          {s.kind !== 'quiet' && (
            <>
              <Link
                to="/inbox/$view"
                params={{ view: s.view }}
                search={{ ...(search.mailbox && { mailbox: search.mailbox }), ...(search.team && { team: search.team }), ...(search.label && { label: search.label }) }}
                className={buttonClass({ variant: 'primary' })}
              >
                Open gesprekken
                <ArrowUpRight size={20} aria-hidden />
              </Link>
            </>
          )}
        </div>
      </article>
    </Reveal>
  )
}

const rulerFormat = (v: number) =>
  durationParts(v)
    .map((p) => `${p.value} ${p.unit ?? ''}`.trim())
    .join(' ')

// One ruler with its sentence: the median as the highlighted tick, the 90th percentile, and a
// target when there is one. A median past its target is drawn in the alert color, since that is
// a breached SLA.
function TimeRuler({
  heading,
  remark,
  what,
  median,
  p90,
  target,
  sentence,
}: {
  heading: string
  remark: string
  what: string
  median: number
  p90: number
  target: number | null
  sentence: string
}) {
  const late = target !== null && median > target
  const max = rulerMax(Math.max(median, p90, target ?? 0))
  return (
    <section className="flex flex-col gap-4">
      <div className="flex items-baseline justify-between gap-3">
        <h3 className="text-base text-ink">{heading}</h3>
        <span className="t-label">{remark}</span>
      </div>
      <TickRuler
        title={heading}
        description={`Liniaal van 0 tot ${rulerFormat(max)}. De mediaan van de ${what} is ${rulerFormat(median)}${target === null ? '' : `, het SLA-doel ${rulerFormat(target)}`}, en 9 op de 10 gesprekken zijn binnen ${rulerFormat(p90)}.`}
        max={max}
        marks={[
          { value: median, label: 'Mediaan', primary: true, tone: late ? 'alert' : 'ink' },
          ...(target === null ? [] : [{ value: target, label: 'SLA-doel', tone: 'muted' as const }]),
          { value: p90, label: '9 op 10 binnen', tone: 'muted' as const },
        ]}
        format={rulerFormat}
      />
      <p className="t-body">{sentence}</p>
    </section>
  )
}

function TimesCard({ o }: { o: Overview }) {
  const first = o.current.first_response
  const resolution = o.current.resolution
  const target = o.first_response_target.seconds

  const firstSentence = (median: number) =>
    target === null
      ? `De helft van de gesprekken kreeg binnen ${formatDuration(median)} een eerste reactie. Voor deze gesprekken is geen SLA-doel voor de eerste reactie ingesteld.`
      : median > target
        ? `De helft van de gesprekken kreeg binnen ${formatDuration(median)} een eerste reactie. Dat is langer dan het doel van ${formatDuration(target)}.`
        : `De helft van de gesprekken kreeg binnen ${formatDuration(median)} een eerste reactie, binnen het doel van ${formatDuration(target)}.`

  return (
    <ChartCard title="Reactie- en oplostijd" i={7}>
      {first.median_seconds === null || first.p90_seconds === null ? (
        <p className="t-body">In deze periode is nog geen eerste reactie verstuurd, dus er valt niets te meten.</p>
      ) : (
        <TimeRuler
          heading="Eerste reactie"
          remark={`${formatNumber(first.count)} beantwoord`}
          what="eerste reactie"
          median={first.median_seconds}
          p90={first.p90_seconds}
          target={target}
          sentence={firstSentence(first.median_seconds)}
        />
      )}
      {resolution.median_seconds === null || resolution.p90_seconds === null ? (
        <p className="t-body">In deze periode is nog geen gesprek opgelost.</p>
      ) : (
        <TimeRuler
          heading="Oplostijd"
          remark={`${formatNumber(resolution.count)} opgelost`}
          what="oplostijd"
          median={resolution.median_seconds}
          p90={resolution.p90_seconds}
          target={null}
          sentence={`De helft van de gesprekken was binnen ${formatDuration(resolution.median_seconds)} opgelost, 9 op de 10 binnen ${formatDuration(resolution.p90_seconds)}.`}
        />
      )}
    </ChartCard>
  )
}

// Binnengekomen, beantwoord, opgelost and the branches, from the lifecycle counts. On a narrow
// card the diagram would not fit three columns, so it is drawn as two.
function LifecycleCard({ o }: { o: Overview }) {
  const [ref, width] = useWidth(700)
  const l = o.lifecycle
  const answered = l.answered.open + l.answered.waiting + l.answered.closed
  const noReply = l.unanswered.waiting + l.unanswered.closed
  const formatFlow = formatNumber

  const arrivals = [
    { id: 'answered', label: 'Beantwoord', value: answered, column: 1 },
    { id: 'waiting-us', label: 'Wacht op ons', value: l.unanswered.open, column: 1, accent: true },
    { id: 'no-reply', label: 'Zonder reactie', value: noReply, column: 1 },
    { id: 'spam', label: 'Spam', value: l.spam, column: 1 },
  ]
  const outcomes = [
    { id: 'closed', label: 'Opgelost', value: l.answered.closed },
    { id: 'waiting', label: 'Wachtend op klant', value: l.answered.waiting },
    { id: 'open', label: 'Nog open', value: l.answered.open },
  ]
  const arrivalLinks = arrivals.map((n) => ({ from: 'arrived', to: n.id, value: n.value, accent: n.accent }))
  const description =
    `Van de ${formatNumber(l.arrived)} binnengekomen gesprekken zijn er ${formatNumber(answered)} beantwoord, ${formatNumber(l.unanswered.open)} wachten nog op een eerste reactie van ons, ` +
    `${formatNumber(noReply)} zijn zonder reactie afgehandeld of op wachtend gezet en ${formatNumber(l.spam)} zijn spam. ` +
    `Van de beantwoorde gesprekken zijn er ${formatNumber(l.answered.closed)} opgelost, ${formatNumber(l.answered.waiting)} wachten op de klant en ${formatNumber(l.answered.open)} zijn nog open.`

  return (
    <ChartCard title="Levensloop van de gesprekken" remark={`${formatNumber(l.arrived)} binnengekomen`} i={6}>
      <div ref={ref} className="flex flex-col gap-8">
        {width >= 500 ? (
          <FlowDiagram
            title="Levensloop van de binnengekomen gesprekken"
            description={description}
            format={formatFlow}
            height={400}
            nodes={[
              { id: 'arrived', label: 'Binnengekomen', value: l.arrived, column: 0 },
              ...arrivals,
              ...outcomes.map((n, i) => ({ ...n, column: 2, alignWith: i === 0 ? 'answered' : undefined })),
            ]}
            links={[...arrivalLinks, ...outcomes.map((n) => ({ from: 'answered', to: n.id, value: n.value }))]}
          />
        ) : (
          <>
            <FlowDiagram
              title="Binnengekomen gesprekken en hun eerste reactie"
              description={description}
              format={formatFlow}
              height={220}
              nodes={[{ id: 'arrived', label: 'Binnengekomen', value: l.arrived, column: 0 }, ...arrivals]}
              links={arrivalLinks}
            />
            <FlowDiagram
              title="Beantwoorde gesprekken en hun status nu"
              description={`Van de ${formatNumber(answered)} beantwoorde gesprekken zijn er ${formatNumber(l.answered.closed)} opgelost, ${formatNumber(l.answered.waiting)} wachten op de klant en ${formatNumber(l.answered.open)} zijn nog open.`}
              format={formatFlow}
              height={200}
              nodes={[{ id: 'answered', label: 'Beantwoord', value: answered, column: 0 }, ...outcomes.map((n) => ({ ...n, column: 1 }))]}
              links={outcomes.map((n) => ({ from: 'answered', to: n.id, value: n.value }))}
            />
          </>
        )}
      </div>
      <p className="t-label">
        De gesprekken van deze periode, op hun status nu. Beantwoord: wij hebben een eerste reactie verstuurd. Zonder reactie: afgehandeld of op wachtend gezet zonder antwoord.
      </p>
    </ChartCard>
  )
}

function ExtraFigures({ o }: { o: Overview }) {
  const { current: c, previous: p } = o
  const items = [
    { label: 'Klantberichten', parts: numParts(c.customer_messages), before: p.customer_messages, now: c.customer_messages },
    { label: 'Antwoorden verstuurd', parts: numParts(c.replies), before: p.replies, now: c.replies },
    { label: 'Opgelost', parts: numParts(c.resolved), before: p.resolved, now: c.resolved },
    { label: 'Heropend', parts: numParts(c.reopened), before: p.reopened, now: c.reopened },
    {
      label: o.csat.responses > 0 ? `Tevredenheid, ${formatNumber(o.csat.responses)} reacties` : 'Tevredenheid',
      parts: o.csat.responses > 0 ? [{ value: formatRating(o.csat.average), unit: 'van 5' }] : [{ value: '—' }],
      before: null,
      now: null,
    },
  ]
  return (
    <Reveal i={9}>
      <dl aria-label="Aanvullende cijfers" className="m-0 grid grid-cols-2 gap-x-5 gap-y-8 border-t border-line pt-8 sm:grid-cols-3 lg:grid-cols-5">
        {items.map((k) => (
          <div key={k.label} className="flex min-w-0 flex-col gap-2">
            <dd className="m-0">
              <ReportNum parts={k.parts} size="s" />
            </dd>
            <dt className="t-label">{k.label}</dt>
          </div>
        ))}
      </dl>
    </Reveal>
  )
}

export function OverviewTab({ search }: { search: ReportSearch }) {
  const q = useQuery(overviewQuery(search))
  const live = useQuery(liveQuery(search))
  if (q.isPending) return <ReportSkeleton />
  if (q.isError) return <ErrorNotice>{errorMessage(q.error)}</ErrorNotice>
  const o = q.data
  const c = o.current
  const empty = c.new_conversations + c.customer_messages + c.replies + c.resolved === 0 && o.open_now === 0

  return (
    <div className="flex flex-col">
      <KpiRow label="Kerncijfers" items={overviewKpis(o)} />
      <div className="mb-4 -mt-4 empty:hidden">
        <BasisNote basis={o.time_basis} />
      </div>
      {empty ? (
        <EmptyState
          icon={<ChartColumn size={20} />}
          title="Nog geen gesprekken in deze periode"
          description="Kies een langere periode of een andere mailbox."
        />
      ) : (
        <div className="flex flex-col gap-4">
          <section className="grid grid-cols-1 gap-4 xl:grid-cols-[1.55fr_1fr]">
            <VolumeCard o={o} />
            {live.data ? (
              <InsightCard live={live.data} search={search} />
            ) : (
              <Reveal i={5} className="min-w-0">
                <article className="card aura flex h-full min-h-80 flex-col justify-between gap-6" aria-busy={live.isPending}>
                  <span className="t-label">Wat nu aandacht vraagt</span>
                  <p className="t-body">{live.isError ? errorMessage(live.error) : 'De actuele stand wordt geladen.'}</p>
                </article>
              </Reveal>
            )}
            <LifecycleCard o={o} />
            <TimesCard o={o} />
          </section>
          <ChartCard title="Per agent" i={8} flush>
            <GroupTable kind="agents" search={search} embedded />
          </ChartCard>
          <ExtraFigures o={o} />
        </div>
      )}
    </div>
  )
}
