import { queryOptions } from '@tanstack/react-query'

import { api } from './api'
import { pollWhenDisconnected } from './realtime'

export type Period = 'today' | '7d' | '30d' | '90d' | 'custom'
export const periods: { value: Period; label: string }[] = [
  { value: 'today', label: 'Vandaag' },
  { value: '7d', label: '7 d' },
  { value: '30d', label: '30 d' },
  { value: '90d', label: '90 d' },
  { value: 'custom', label: 'Aangepast' },
]

export type ReportTab = 'overzicht' | 'agenten' | 'teams' | 'mailboxen' | 'labels' | 'tevredenheid' | 'live'
export const reportTabs: { slug: ReportTab; label: string }[] = [
  { slug: 'overzicht', label: 'Overzicht' },
  { slug: 'agenten', label: 'Agenten' },
  { slug: 'teams', label: 'Teams' },
  { slug: 'mailboxen', label: 'Mailboxen' },
  { slug: 'labels', label: 'Labels' },
  { slug: 'tevredenheid', label: 'Tevredenheid' },
  { slug: 'live', label: 'Live' },
]

export const tabFromSlug = (slug: string): ReportTab | undefined => reportTabs.find((t) => t.slug === slug)?.slug

// Filter and period state as it lives in the URL.
export interface ReportSearch {
  periode?: Period
  van?: string
  tot?: string
  mailbox?: string
  team?: string
  agent?: string
  label?: string
}

// Applies a change to a search: a key set to null is removed, so no undefined ever reaches the URL.
export function patchSearch(prev: ReportSearch, patch: { [K in keyof ReportSearch]?: ReportSearch[K] | null }): ReportSearch {
  const next: Record<string, string> = {}
  for (const [k, v] of Object.entries({ ...prev, ...patch })) {
    if (typeof v === 'string' && v) next[k] = v
  }
  return parseReportSearch(next)
}

const isoDate = /^\d{4}-\d{2}-\d{2}$/

export function parseReportSearch(search: Record<string, unknown>): ReportSearch {
  const out: ReportSearch = {}
  if (typeof search.periode === 'string' && periods.some((p) => p.value === search.periode)) out.periode = search.periode as Period
  for (const key of ['van', 'tot'] as const) {
    const v = search[key]
    if (typeof v === 'string' && isoDate.test(v)) out[key] = v
  }
  for (const key of ['mailbox', 'team', 'agent', 'label'] as const) {
    const v = search[key]
    if (typeof v === 'string' && v) out[key] = v
  }
  return out
}

// The API query string for a search. Custom periods without both dates fall back to 7 d.
export function reportParams(search: ReportSearch): URLSearchParams {
  const q = new URLSearchParams()
  const custom = search.periode === 'custom' && search.van && search.tot
  q.set('period', custom ? 'custom' : search.periode && search.periode !== 'custom' ? search.periode : '7d')
  if (custom) {
    q.set('from', search.van ?? '')
    q.set('to', search.tot ?? '')
  }
  for (const [param, value] of [['mailbox', search.mailbox], ['team', search.team], ['agent', search.agent], ['label', search.label]] as const) {
    if (value) q.set(param, value)
  }
  return q
}

export type ReportKind = 'overview' | 'agents' | 'teams' | 'mailboxes' | 'labels' | 'csat' | 'live'

export function exportUrl(kind: ReportKind, search: ReportSearch, extra?: Record<string, string>): string {
  const q = reportParams(search)
  for (const [k, v] of Object.entries(extra ?? {})) q.set(k, v)
  return `/api/v1/reports/${kind}/export?${q.toString()}`
}

export interface ReportPeriod {
  preset: string
  from: string
  to: string
  timezone: string
  group: 'day' | 'week'
  days: number
}

export interface Duration {
  count: number
  median_seconds: number | null
  p90_seconds: number | null
}

export interface Rate {
  met: number
  total: number
}

export interface Metrics {
  new_conversations: number
  customer_messages: number
  replies: number
  resolved: number
  reopened: number
  first_response: Duration
  resolution: Duration
  sla_first_response: Rate
  sla_resolution: Rate
}

export type TimeBasis = 'wall_clock' | 'business_hours' | 'mixed'

export interface CsatSummary {
  sent: number
  responses: number
  average: number | null
  response_rate: number | null
  distribution: number[]
}

export interface StatusCounts {
  open: number
  waiting: number
  closed: number
}

// Where the conversations created in the period stand now. answered + unanswered is
// current.new_conversations; arrived adds the spam (docs/reports.md).
export interface Lifecycle {
  arrived: number
  spam: number
  answered: StatusCounts
  unanswered: StatusCounts
}

export interface Overview {
  period: ReportPeriod
  time_basis: TimeBasis
  current: Metrics
  previous: Metrics
  // unanswered: conversations created in the bucket that are open without a first reply from us.
  series: (Metrics & { date: string; unanswered: number })[]
  open_now: number
  csat: CsatSummary
  lifecycle: Lifecycle
  first_response_target: { count: number; seconds: number | null }
}

export type ReportRow = Metrics & { id: string | null; name: string }

export interface ReportTable {
  period: ReportPeriod
  time_basis: TimeBasis
  rows: ReportRow[]
}

export interface CsatReport {
  period: ReportPeriod
  by: 'agent' | 'team' | 'mailbox' | 'label'
  summary: CsatSummary
  series: (CsatSummary & { date: string })[]
  rows: (CsatSummary & { id: string | null; name: string })[]
  comments: { conversation_id: string; number: number; subject: string; rating: number; comment: string; at: string; assignee: string }[]
}

export interface LiveReport {
  open: number
  unassigned: number
  waiting: number
  sla_at_risk: number
  sla_breached: number
  agents: { id: string; name: string; open: number; waiting: number; sla_risk: number; online: boolean; availability: 'online' | 'busy' | 'offline' }[]
  attention: Attention
  at: string
}

// Open conversations without a first reply from us. Breached ones are past their first-response
// due time, due_soon ones reach it within window_minutes.
export interface Attention {
  unanswered: number
  breached: number
  due_soon: number
  next_due_seconds: number
  oldest_seconds: number
  unassigned: number
  holders: { id: string; name: string; count: number }[]
  window_minutes: number
}

const get = <T>(kind: string, search: ReportSearch, extra?: Record<string, string>) => {
  const q = reportParams(search)
  for (const [k, v] of Object.entries(extra ?? {})) q.set(k, v)
  return api<T>('GET', `/reports/${kind}?${q.toString()}`)
}

const key = (kind: string, search: ReportSearch, extra = '') => ['reports', kind, reportParams(search).toString(), extra]

export const overviewQuery = (s: ReportSearch) => queryOptions({ queryKey: key('overview', s), queryFn: () => get<Overview>('overview', s) })

export const tableQuery = (kind: 'agents' | 'teams' | 'mailboxes' | 'labels', s: ReportSearch) =>
  queryOptions({ queryKey: key(kind, s), queryFn: () => get<ReportTable>(kind, s) })

export const csatQuery = (s: ReportSearch, by: string) =>
  queryOptions({ queryKey: key('csat', s, by), queryFn: () => get<CsatReport>('csat', s, { by }) })

// The live view ignores the period; only the filters count.
function filtersOnly(s: ReportSearch): ReportSearch {
  const out: ReportSearch = {}
  if (s.mailbox) out.mailbox = s.mailbox
  if (s.team) out.team = s.team
  if (s.agent) out.agent = s.agent
  if (s.label) out.label = s.label
  return out
}

export const liveQuery = (s: ReportSearch) => {
  const filters = filtersOnly(s)
  return queryOptions({
    queryKey: key('live', filters),
    queryFn: () => {
      const q = new URLSearchParams(reportParams(filters))
      q.delete('period')
      return api<LiveReport>('GET', `/reports/live?${q.toString()}`)
    },
    refetchInterval: pollWhenDisconnected(30_000),
  })
}

export interface ReportSettings {
  timezone: string
}

export const reportSettingsQuery = queryOptions({
  queryKey: ['settings', 'reports'],
  queryFn: () => api<ReportSettings>('GET', '/settings/reports'),
})

export interface CsatSettings {
  enabled: boolean
  delay_hours: number
}

export const csatSettingsQuery = (mailboxId: string) =>
  queryOptions({
    queryKey: ['mailboxes', mailboxId, 'csat'],
    queryFn: () => api<CsatSettings>('GET', `/mailboxes/${encodeURIComponent(mailboxId)}/csat`),
  })

// ---- Formatting (Dutch) ----

const integer = new Intl.NumberFormat('nl-NL', { maximumFractionDigits: 0 })
const oneDecimal = new Intl.NumberFormat('nl-NL', { minimumFractionDigits: 1, maximumFractionDigits: 1 })

export const formatNumber = (n: number) => integer.format(n)

// "42 min", "6 u 10 min", "2 d 3 u", "< 1 min"; an em dash when nothing was measured.
export function formatDuration(seconds: number | null): string {
  if (seconds === null) return '—'
  const minutes = Math.round(seconds / 60)
  if (minutes < 1) return '< 1 min'
  if (minutes < 60) return `${minutes} min`
  const hours = Math.floor(minutes / 60)
  const restMinutes = minutes % 60
  if (hours < 24) return restMinutes === 0 ? `${hours} u` : `${hours} u ${restMinutes} min`
  const days = Math.floor(hours / 24)
  const restHours = hours % 24
  return restHours === 0 ? `${days} d` : `${days} d ${restHours} u`
}

export interface NumPart {
  value: string
  // Rendered grey, with its decimal separator: ",1".
  decimals?: string | undefined
  unit?: string | undefined
}

// formatDuration split where ron's numerals switch to a small grey unit: 6 u 10 min becomes
// [{6, u}, {10, min}]. Nothing measured is a single dash.
export function durationParts(seconds: number | null): NumPart[] {
  if (seconds === null) return [{ value: '—' }]
  const minutes = Math.round(seconds / 60)
  if (minutes < 1) return [{ value: '< 1', unit: 'min' }]
  if (minutes < 60) return [{ value: String(minutes), unit: 'min' }]
  const hours = Math.floor(minutes / 60)
  const restMinutes = minutes % 60
  if (hours < 24) return restMinutes === 0 ? [{ value: String(hours), unit: 'u' }] : [{ value: String(hours), unit: 'u' }, { value: String(restMinutes), unit: 'min' }]
  const days = Math.floor(hours / 24)
  const restHours = hours % 24
  return restHours === 0 ? [{ value: String(days), unit: 'd' }] : [{ value: String(days), unit: 'd' }, { value: String(restHours), unit: 'u' }]
}

export const formatPercent = (value: number | null) => (value === null ? '—' : `${oneDecimal.format(value)} %`)

// Percentage of a rate, or null when nothing could be judged.
export const ratePercent = (r: Rate): number | null => (r.total === 0 ? null : (100 * r.met) / r.total)

export const formatRating = (average: number | null) => (average === null ? '—' : oneDecimal.format(average))

// Change against the previous period: "+6 %", "−3 %", "0 %", or "n.v.t." without a base.
export function formatDelta(current: number | null, previous: number | null): string {
  if (current === null || previous === null || previous === 0) return 'n.v.t.'
  const pct = Math.round(((current - previous) / previous) * 100)
  if (pct === 0) return '0 %'
  return `${pct > 0 ? '+' : '−'}${Math.abs(pct)} %`
}

// Change of a percentage against the previous period, in points: "+2,1 pt"; nothing without both.
export function formatPointsDelta(current: number | null, previous: number | null): string {
  if (current === null || previous === null) return 'n.v.t.'
  const diff = Math.round((current - previous) * 10) / 10
  if (diff === 0) return '0 pt'
  return `${diff > 0 ? '+' : '−'}${oneDecimal.format(Math.abs(diff))} pt`
}

// The share of conversations within their SLA: the first-response rate, or the resolution rate
// when no first-response target applied.
export function slaPercent(m: Metrics): number | null {
  return ratePercent(m.sla_first_response) ?? ratePercent(m.sla_resolution)
}

const words = ['nul', 'één', 'twee', 'drie', 'vier', 'vijf', 'zes', 'zeven', 'acht', 'negen', 'tien', 'elf', 'twaalf']

// Small counts in words ("drie"), larger ones as digits, for sentences that state what is going on.
export function countWord(n: number): string {
  return Number.isInteger(n) && n >= 0 && n < words.length ? (words[n] ?? String(n)) : formatNumber(n)
}

export const capitalize = (text: string) => text.charAt(0).toUpperCase() + text.slice(1)

const months = ['januari', 'februari', 'maart', 'april', 'mei', 'juni', 'juli', 'augustus', 'september', 'oktober', 'november', 'december']
const shortMonths = ['jan', 'feb', 'mrt', 'apr', 'mei', 'jun', 'jul', 'aug', 'sep', 'okt', 'nov', 'dec']
const weekdays = ['zondag', 'maandag', 'dinsdag', 'woensdag', 'donderdag', 'vrijdag', 'zaterdag']

function parts(iso: string): { y: number; m: number; d: number; weekday: number } {
  const [y = 0, m = 1, d = 1] = iso.split('-').map(Number)
  return { y, m, d, weekday: new Date(Date.UTC(y, m - 1, d)).getUTCDay() }
}

// "1 t/m 28 september", "28 augustus t/m 3 september", "28 september"; the year only when the
// period crosses years.
export function periodLabel(from: string, to: string): string {
  const a = parts(from)
  const b = parts(to)
  if (from === to) return `${b.d} ${months[b.m - 1]}`
  const crossYear = a.y !== b.y
  const start = a.m === b.m && !crossYear ? `${a.d}` : `${a.d} ${months[a.m - 1]}${crossYear ? ` ${a.y}` : ''}`
  return `${start} t/m ${b.d} ${months[b.m - 1]}${crossYear ? ` ${b.y}` : ''}`
}

// Short axis label of a bucket: "28 sep".
export function bucketLabel(date: string): string {
  const p = parts(date)
  return `${p.d} ${shortMonths[p.m - 1]}`
}

// Full description of a bucket for tooltips and tables.
export function bucketFull(date: string, group: 'day' | 'week'): string {
  const p = parts(date)
  return group === 'week' ? `Week van ${p.d} ${months[p.m - 1]}` : `${weekdays[p.weekday]} ${p.d} ${months[p.m - 1]}`
}
