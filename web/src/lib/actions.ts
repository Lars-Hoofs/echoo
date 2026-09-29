import type { InfiniteData } from '@tanstack/react-query'
import { queryOptions } from '@tanstack/react-query'

import { api } from './api'
import { splitList } from './filters'
import { formatDateTime } from './format'
import {
  type ConversationListItem,
  type ConversationPage,
  type ConversationStatus,
  type InboxView,
  type LabelColor,
  type LabelRef,
  type ListStatus,
  type Message,
  type Priority,
  priorityLabel,
  type Ref,
  statusLabel,
} from './inbox'

export interface Label extends LabelRef {
  description: string
  created_at: string
}

export interface Assignees {
  users: Ref[]
  teams: Ref[]
}

export interface TimelineEvent {
  id: string
  type: string
  created_at: string
  actor: Ref | null
  user: Ref | null
  data: Record<string, unknown>
}

export const labelsQuery = queryOptions({
  queryKey: ['labels'],
  queryFn: () => api<{ labels: Label[] }>('GET', '/labels').then((r) => r.labels),
  staleTime: 60_000,
})

export const assigneesQuery = (mailboxId: string) =>
  queryOptions({
    queryKey: ['assignees', mailboxId],
    queryFn: () => api<Assignees>('GET', `/assignees?mailbox_id=${encodeURIComponent(mailboxId)}`),
    staleTime: 60_000,
  })

// Under the conversation key, so anything that refreshes the conversation refreshes its timeline.
export const eventsQuery = (id: string) =>
  queryOptions({
    queryKey: ['inbox', 'conversation', id, 'events'],
    queryFn: () => api<{ events: TimelineEvent[] }>('GET', `/conversations/${encodeURIComponent(id)}/events`).then((r) => r.events),
  })

// What a user asked for. Fields that are absent stay as they are; null clears.
export interface ChangeSet {
  status?: ConversationStatus
  priority?: Priority
  assignee?: Ref | null
  team?: Ref | null
  snoozedUntil?: string | null
  addLabels?: LabelRef[]
  removeLabelIds?: string[]
}

// Predicts the server's result, so the list can update before the response arrives.
export function applyChange<T extends ConversationListItem>(item: T, c: ChangeSet): T {
  const next = { ...item, version: item.version + 1 }
  if (c.status !== undefined) next.status = c.status
  if (c.priority !== undefined) next.priority = c.priority
  if (c.assignee !== undefined) next.assignee = c.assignee
  if (c.team !== undefined) next.team = c.team
  if (c.snoozedUntil !== undefined) next.snoozed_until = c.snoozedUntil
  if (next.status === 'closed' || next.status === 'spam') next.snoozed_until = null
  if (c.addLabels || c.removeLabelIds) {
    const removed = new Set(c.removeLabelIds)
    const kept = item.labels.filter((l) => !removed.has(l.id) && !c.addLabels?.some((a) => a.id === l.id))
    next.labels = [...kept, ...(c.addLabels ?? [])].sort((a, b) => a.name.localeCompare(b.name, 'nl'))
  }
  return next
}

export function isSnoozed(item: Pick<ConversationListItem, 'snoozed_until'>, now: Date): boolean {
  return item.snoozed_until !== null && new Date(item.snoozed_until).getTime() > now.getTime()
}

export interface ListMatch {
  view: InboxView
  status: ListStatus
  mailbox?: string | undefined
  team?: string | undefined
  label?: string | undefined
}

// Mirrors the server's list filters, so a conversation that no longer belongs to the list it is
// shown in leaves it immediately.
export function matchesList(item: ConversationListItem, f: ListMatch, meId: string, now: Date): boolean {
  if (f.view === 'mine' && item.assignee?.id !== meId) return false
  if (f.view === 'unassigned' && item.assignee !== null) return false
  const mailboxes = splitList(f.mailbox)
  if (mailboxes.length > 0 && !mailboxes.includes(item.mailbox.id)) return false
  const teams = splitList(f.team)
  if (teams.length > 0 && !(item.team && teams.includes(item.team.id))) return false
  const labels = splitList(f.label)
  if (labels.length > 0 && !item.labels.some((l) => labels.includes(l.id))) return false
  const snoozed = isSnoozed(item, now)
  if (f.status === 'snoozed') return snoozed && (item.status === 'open' || item.status === 'waiting')
  return item.status === f.status && !snoozed
}

// The list filter a cached list was fetched with, read back from its query key.
export function listMatchFromKey(key: readonly unknown[]): ListMatch | null {
  const [, , view, status, mailbox, team, label] = key
  if (typeof view !== 'string' || typeof status !== 'string') return null
  return {
    view: view as InboxView,
    status: status as ListStatus,
    mailbox: typeof mailbox === 'string' && mailbox ? mailbox : undefined,
    team: typeof team === 'string' && team ? team : undefined,
    label: typeof label === 'string' && label ? label : undefined,
  }
}

export function patchPages(
  data: InfiniteData<ConversationPage, string>,
  ids: ReadonlySet<string>,
  change: ChangeSet,
  keep: (item: ConversationListItem) => boolean,
): InfiniteData<ConversationPage, string> {
  return {
    ...data,
    pages: data.pages.map((page) => ({
      ...page,
      conversations: page.conversations.flatMap((item) => {
        if (!ids.has(item.id)) return [item]
        const next = applyChange(item, change)
        return keep(next) ? [next] : []
      }),
    })),
  }
}

// The PATCH body for one conversation. Labels have their own endpoint.
export function patchBody(c: ChangeSet): Record<string, unknown> {
  const body: Record<string, unknown> = {}
  if (c.status !== undefined) body.status = c.status
  if (c.priority !== undefined) body.priority = c.priority
  if (c.assignee !== undefined) body.assignee_user_id = c.assignee?.id ?? null
  if (c.team !== undefined) body.assignee_team_id = c.team?.id ?? null
  if (c.snoozedUntil !== undefined) body.snoozed_until = c.snoozedUntil
  return body
}

export function bulkAction(c: ChangeSet): Record<string, unknown> {
  const { snoozed_until, ...rest } = patchBody(c)
  const action: Record<string, unknown> = rest
  if (c.snoozedUntil !== undefined) action.snooze_until = snoozed_until
  if (c.addLabels?.length) action.add_label_ids = c.addLabels.map((l) => l.id)
  if (c.removeLabelIds?.length) action.remove_label_ids = c.removeLabelIds
  return action
}

export interface BulkResult {
  id: string
  ok: boolean
  code?: string
  version?: number
}

export function splitBulk(results: BulkResult[]): { ok: string[]; failed: BulkResult[] } {
  return { ok: results.filter((r) => r.ok).map((r) => r.id), failed: results.filter((r) => !r.ok) }
}

// The change that puts one conversation back as it was. Labels are restored per conversation.
export function inverseChange(item: ConversationListItem, c: ChangeSet): ChangeSet {
  const undo: ChangeSet = {}
  if (c.status !== undefined) undo.status = item.status
  if (c.priority !== undefined) undo.priority = item.priority
  if (c.assignee !== undefined) undo.assignee = item.assignee
  if (c.team !== undefined) undo.team = item.team
  if (c.snoozedUntil !== undefined) undo.snoozedUntil = item.snoozed_until
  if (c.addLabels) undo.removeLabelIds = c.addLabels.filter((l) => !item.labels.some((x) => x.id === l.id)).map((l) => l.id)
  if (c.removeLabelIds) undo.addLabels = item.labels.filter((l) => c.removeLabelIds?.includes(l.id))
  return undo
}

export interface SnoozePreset {
  key: 'tomorrow' | 'monday' | 'week'
  label: string
  at: Date
}

function atNine(base: Date, addDays: number): Date {
  const d = new Date(base)
  d.setDate(d.getDate() + addDays)
  d.setHours(9, 0, 0, 0)
  return d
}

export function snoozePresets(now: Date): SnoozePreset[] {
  // getDay(): 0 is Sunday, 1 is Monday. Today being Monday means next Monday, never today.
  const untilMonday = (8 - now.getDay()) % 7 || 7
  const week = new Date(now)
  week.setDate(week.getDate() + 7)
  return [
    { key: 'tomorrow', label: 'Morgen 09:00', at: atNine(now, 1) },
    { key: 'monday', label: 'Maandag 09:00', at: atNine(now, untilMonday) },
    { key: 'week', label: 'Over een week', at: week },
  ]
}

const plural = (n: number, one: string, many: string) => (n === 1 ? one : `${n} ${many}`)

// The toast text after a change; count is how many conversations it applied to.
export function describeChange(c: ChangeSet, count: number, now = new Date()): string {
  const subject = plural(count, 'Gesprek', 'gesprekken')
  if (c.status === 'closed') return `${subject} gesloten`
  if (c.status === 'open') return `${subject} heropend`
  if (c.status === 'waiting') return `${subject} op wachtend gezet`
  if (c.status === 'spam') return `${subject} als spam gemarkeerd`
  if (c.assignee) return `${subject} toegewezen aan ${c.assignee.name}`
  if (c.assignee === null) return `Toewijzing verwijderd van ${plural(count, 'gesprek', 'gesprekken')}`
  if (c.team) return `${subject} toegewezen aan team ${c.team.name}`
  if (c.team === null) return `Team verwijderd van ${plural(count, 'gesprek', 'gesprekken')}`
  if (c.priority) return `Prioriteit ${priorityLabel[c.priority].toLowerCase()} gezet voor ${plural(count, 'gesprek', 'gesprekken')}`
  if (c.snoozedUntil) return `${subject} uitgesteld tot ${formatDateTime(c.snoozedUntil, now)}`
  if (c.snoozedUntil === null) return `Uitstel opgeheven voor ${plural(count, 'gesprek', 'gesprekken')}`
  if (c.addLabels?.length) return `Label toegevoegd aan ${plural(count, 'gesprek', 'gesprekken')}`
  if (c.removeLabelIds?.length) return `Label verwijderd van ${plural(count, 'gesprek', 'gesprekken')}`
  return `${subject} bijgewerkt`
}

const str = (v: unknown): string => (typeof v === 'string' ? v : '')

// One compact line for the conversation timeline.
export function describeEvent(e: TimelineEvent, now = new Date()): string {
  const text = eventText(e, now)
  return e.data.source === 'macro' ? `${text} via een macro` : text
}

function eventText(e: TimelineEvent, now: Date): string {
  const d = e.data
  const who = e.actor?.name ?? (d.source === 'rule' ? 'Een regel' : 'Echoo')
  switch (e.type) {
    case 'resolved':
      return d.source === 'system' ? `${who} sloot het gesprek automatisch` : `${who} sloot het gesprek`
    case 'sla_at_risk':
      return d.target === 'first_response' ? 'Eerste reactie dreigt te laat te komen' : 'De oplostijd dreigt te worden overschreden'
    case 'sla_breached':
      return d.target === 'first_response' ? 'De eerste reactie is te laat' : 'De oplostijd is overschreden'
    case 'reopened':
      return `${who} heropende het gesprek`
    case 'status_changed': {
      const to = str(d.to)
      return `${who} zette de status op ${(Object.hasOwn(statusLabel, to) ? statusLabel[to as ConversationStatus] : to).toLowerCase()}`
    }
    case 'assigned':
      return e.actor && e.user && e.actor.id === e.user.id
        ? `${who} nam het gesprek op`
        : `${who} wees het gesprek toe aan ${e.user?.name ?? 'een gebruiker'}`
    case 'unassigned':
      return `${who} haalde de toewijzing weg`
    case 'team_assigned':
      return str(d.team) ? `${who} wees het gesprek toe aan team ${str(d.team)}` : `${who} haalde het team weg`
    case 'priority_changed': {
      const to = str(d.to)
      return `${who} zette de prioriteit op ${(Object.hasOwn(priorityLabel, to) ? priorityLabel[to as Priority] : to).toLowerCase()}`
    }
    case 'labeled':
      return `${who} voegde label ${str(d.label)} toe`
    case 'unlabeled':
      return `${who} verwijderde label ${str(d.label)}`
    case 'snoozed':
      return `${who} stelde het gesprek uit tot ${formatDateTime(str(d.until), now)}`
    case 'campaign':
      return `Verstuurd in campagne ${str(d.name)}`
    case 'bounced':
      return str(d.recipient) ? `Bezorging aan ${str(d.recipient)} mislukt` : 'Bezorging mislukt'
    case 'woke':
      return d.manual === true ? `${who} hief het uitstel op` : 'Uitstel verstreken, het gesprek staat weer in de lijst'
    default:
      return `${who} wijzigde het gesprek`
  }
}

export const labelColors: LabelColor[] = ['slate', 'blue', 'teal', 'green', 'amber', 'orange', 'red', 'violet']

export const labelColorName: Record<LabelColor, string> = {
  slate: 'Grijs',
  blue: 'Blauw',
  teal: 'Groenblauw',
  green: 'Groen',
  amber: 'Oranje-geel',
  orange: 'Oranje',
  red: 'Rood',
  violet: 'Paars',
}

type Timed = Pick<Message, 'received_at' | 'sent_at'>

export type TimelineEntry<M> = { kind: 'message'; message: M } | { kind: 'event'; event: TimelineEvent }

const messageTime = (m: Timed) => new Date(m.received_at ?? m.sent_at ?? 0).getTime()

// Events sit between the messages in time order; on a tie the message goes first. The `created`
// event only marks where the conversation began, which the first message already shows.
export function mergeTimeline<M extends Timed>(messages: M[], events: TimelineEvent[]): TimelineEntry<M>[] {
  const entries: (TimelineEntry<M> & { at: number })[] = [
    ...messages.map((message) => ({ kind: 'message' as const, message, at: messageTime(message) })),
    ...events.filter((event) => event.type !== 'created').map((event) => ({ kind: 'event' as const, event, at: new Date(event.created_at).getTime() })),
  ]
  return entries.sort((a, b) => a.at - b.at || (a.kind === b.kind ? 0 : a.kind === 'message' ? -1 : 1))
}
