import { useQuery } from '@tanstack/react-query'
import { getRouteApi, Link } from '@tanstack/react-router'
import { useState } from 'react'

import { Reveal } from '../../components/Reveal'
import { Input, Segmented, Select } from '../../components/ui'
import { labelsQuery } from '../../lib/actions'
import { api } from '../../lib/api'
import { summaryQuery } from '../../lib/inbox'
import {
  exportUrl,
  liveQuery,
  overviewQuery,
  type Period,
  patchSearch,
  periods,
  type ReportKind,
  type ReportSearch,
  reportTabs,
  tabFromSlug,
} from '../../lib/reports'
import { hasPermission, meQuery, type User } from '../../lib/session'
import { CsatTab, type CsatDimension } from './Csat'
import { GroupTable } from './GroupTable'
import { LiveTab } from './Live'
import { OverviewTab } from './Overview'
import { ExportButton, PeriodLine } from './shared'

const route = getRouteApi('/auth/ready/rapportage/$tab')

type FilterKey = 'mailbox' | 'team' | 'agent' | 'label'

const timeFormat = new Intl.DateTimeFormat('nl-NL', { hour: '2-digit', minute: '2-digit', second: '2-digit' })

const exportKind: Record<string, ReportKind> = {
  overzicht: 'overview',
  agenten: 'agents',
  teams: 'teams',
  mailboxen: 'mailboxes',
  labels: 'labels',
  tevredenheid: 'csat',
  live: 'live',
}

function FilterSelect({
  label,
  all,
  value,
  options,
  onChange,
}: {
  label: string
  all: string
  value: string | undefined
  options: { id: string; name: string }[]
  onChange: (id: string) => void
}) {
  return (
    <Select aria-label={label} value={value ?? ''} onChange={(e) => onChange(e.target.value)} className="min-w-36">
      <option value="">{all}</option>
      {options.map((o) => (
        <option key={o.id} value={o.id}>
          {o.name}
        </option>
      ))}
    </Select>
  )
}

function Filters({ search, admin }: { search: ReportSearch; admin: boolean }) {
  const navigate = route.useNavigate()
  const summary = useQuery(summaryQuery)
  const labels = useQuery(labelsQuery)
  const users = useQuery({ queryKey: ['users'], queryFn: () => api<{ users: User[] }>('GET', '/users'), enabled: admin })
  const set = (patch: Parameters<typeof patchSearch>[1]) => {
    void navigate({ search: (prev) => patchSearch(prev, patch), replace: true })
  }
  const setFilter = (key: FilterKey) => (id: string) => {
    set({ [key]: id })
  }
  return (
    <>
      <FilterSelect label="Mailbox" all="Alle mailboxen" value={search.mailbox} options={summary.data?.mailboxes ?? []} onChange={setFilter('mailbox')} />
      <FilterSelect label="Team" all="Alle teams" value={search.team} options={summary.data?.teams ?? []} onChange={setFilter('team')} />
      <FilterSelect label="Label" all="Alle labels" value={search.label} options={labels.data ?? []} onChange={setFilter('label')} />
      {admin && (
        <FilterSelect
          label="Agent"
          all="Alle agents"
          value={search.agent}
          options={users.data?.users.filter((u) => u.can_write_conversations && !u.deactivated) ?? []}
          onChange={setFilter('agent')}
        />
      )}
    </>
  )
}

function PeriodControls({ search }: { search: ReportSearch }) {
  const navigate = route.useNavigate()
  const period: Period = search.periode ?? '7d'
  const [van, setVan] = useState(search.van ?? '')
  const [tot, setTot] = useState(search.tot ?? '')
  const set = (patch: Parameters<typeof patchSearch>[1]) => {
    void navigate({ search: (prev) => patchSearch(prev, patch), replace: true })
  }
  const setDates = (nextVan: string, nextTot: string) => {
    setVan(nextVan)
    setTot(nextTot)
    if (nextVan && nextTot && nextVan <= nextTot) set({ periode: 'custom', van: nextVan, tot: nextTot })
  }
  return (
    <>
      <Segmented
        label="Periode"
        value={period}
        options={periods}
        onChange={(p) => {
          if (p === 'custom') set({ periode: 'custom', van, tot })
          else set({ periode: p, van: null, tot: null })
        }}
      />
      {period === 'custom' && (
        <>
          <Input type="date" aria-label="Van" value={van} max={tot || undefined} onChange={(e) => setDates(e.target.value, tot)} className="h-8 w-40 max-md:h-11" />
          <Input type="date" aria-label="Tot en met" value={tot} min={van || undefined} onChange={(e) => setDates(van, e.target.value)} className="h-8 w-40 max-md:h-11" />
        </>
      )}
    </>
  )
}

// The small grey line above the title: the period the numbers cover, or when the live numbers were read.
function HeaderLine({ tab, search }: { tab: string; search: ReportSearch }) {
  const live = useQuery({ ...liveQuery(search), enabled: tab === 'live' })
  const overview = useQuery({ ...overviewQuery(search), enabled: tab !== 'live' })
  if (tab === 'live') {
    return <p className="t-label min-h-4">{live.data ? `Bijgewerkt om ${timeFormat.format(new Date(live.data.at))} · ververst elke 30 seconden` : ''}</p>
  }
  return overview.data ? <PeriodLine period={overview.data.period} /> : <p className="t-label min-h-4" />
}

export function ReportsPage() {
  const { tab: slug } = route.useParams()
  const search = route.useSearch()
  const me = useQuery(meQuery)
  const tab = tabFromSlug(slug) ?? 'overzicht'
  const admin = hasPermission(me.data, 'reports.view_all_agents')
  const [csatBy, setCsatBy] = useState<CsatDimension>('agent')
  const kind = exportKind[tab] ?? 'overview'

  return (
    <div className="relative h-full overflow-y-auto">
      <div className="mx-auto flex w-full max-w-[1240px] flex-col px-4 pt-6 pb-16 sm:px-8 sm:pt-8">
        <Reveal>
          <header className="flex flex-wrap items-end justify-between gap-x-6 gap-y-4">
            <div className="flex flex-col gap-1">
              <HeaderLine tab={tab} search={search} />
              <h1 className="t-h3">Rapportage</h1>
            </div>
            <div className="flex flex-wrap items-center gap-2">
              {tab !== 'live' && <PeriodControls search={search} />}
              <ExportButton href={exportUrl(kind, search, kind === 'csat' ? { by: csatBy } : undefined)} />
            </div>
          </header>
          <div className="mt-5 flex flex-wrap items-center gap-2">
            <Filters search={search} admin={admin} />
          </div>
        </Reveal>
        <div role="tablist" aria-label="Rapporten" className="mt-8 flex gap-1 overflow-x-auto border-b border-line">
          {reportTabs.map((t) => (
            <Link
              key={t.slug}
              to="/rapportage/$tab"
              params={{ tab: t.slug }}
              search={search}
              role="tab"
              aria-selected={t.slug === tab}
              className={`-mb-px border-b px-4 py-3 text-base whitespace-nowrap ${t.slug === tab ? 'border-ink text-ink' : 'border-transparent text-muted hover:text-ink'}`}
            >
              {t.label}
            </Link>
          ))}
        </div>
        <div role="tabpanel" aria-label={reportTabs.find((t) => t.slug === tab)?.label}>
          {tab === 'overzicht' && <OverviewTab search={search} />}
          {tab === 'agenten' && <GroupTable kind="agents" search={search} />}
          {tab === 'teams' && <GroupTable kind="teams" search={search} />}
          {tab === 'mailboxen' && <GroupTable kind="mailboxes" search={search} />}
          {tab === 'labels' && <GroupTable kind="labels" search={search} />}
          {tab === 'tevredenheid' && <CsatTab search={search} by={csatBy} onBy={setCsatBy} />}
          {tab === 'live' && <LiveTab search={search} />}
        </div>
      </div>
    </div>
  )
}
