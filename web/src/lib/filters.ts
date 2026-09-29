import type { InboxSearch, ListStatus, Priority } from './inbox'
import { listStatusLabel, priorityLabel } from './inbox'

// The filters of the conversation list. In the URL, several ids of one kind are comma separated
// ("mailbox=a,b"); the API takes the parameter repeated and saved views store arrays.
export type FilterKey = 'status' | 'mailbox' | 'team' | 'label' | 'assignee' | 'priority' | 'attachment' | 'after' | 'before' | 'contact' | 'organization'

export const filterKeys: FilterKey[] = ['status', 'mailbox', 'team', 'label', 'assignee', 'priority', 'attachment', 'after', 'before', 'contact', 'organization']

export function splitList(value: string | undefined): string[] {
  return value ? value.split(',').filter((v) => v !== '') : []
}

export function toggleInList(value: string | undefined, id: string): string | undefined {
  const list = splitList(value)
  const next = list.includes(id) ? list.filter((v) => v !== id) : [...list, id]
  return next.length > 0 ? next.join(',') : undefined
}

// The fields a list request adds to view and status.
export type FilterFields = Omit<InboxSearch, 'weergave'>

export function appendFilterParams(q: URLSearchParams, f: FilterFields) {
  for (const id of splitList(f.mailbox)) q.append('mailbox_id', id)
  for (const id of splitList(f.team)) q.append('team_id', id)
  for (const id of splitList(f.label)) q.append('label_id', id)
  if (f.assignee) q.set('assignee', f.assignee)
  for (const p of splitList(f.priority)) q.append('priority', p)
  if (f.attachment) q.set('has_attachment', 'true')
  if (f.after) q.set('after', f.after)
  if (f.before) q.set('before', f.before)
  if (f.contact) q.set('contact_id', f.contact)
  if (f.organization) q.set('organization_id', f.organization)
}

// The part of a filter that the original list key does not carry, so lists with different
// advanced filters never share a cache entry.
export function advancedKey(f: FilterFields): string {
  return JSON.stringify([f.assignee ?? '', f.priority ?? '', f.attachment ?? false, f.after ?? '', f.before ?? '', f.contact ?? '', f.organization ?? ''])
}

export interface SavedFilters {
  status?: ListStatus
  mailbox_ids?: string[]
  label_ids?: string[]
  team_ids?: string[]
  assignee?: string
  priorities?: Priority[]
  has_attachment?: boolean
  after?: string
  before?: string
  contact_id?: string
  organization_id?: string
}

// Open is the default status, so it never counts as a filter, even when a URL spells it out.
function statusFilter(s: InboxSearch): ListStatus | undefined {
  return s.status === 'open' ? undefined : s.status
}

export function searchToSaved(s: InboxSearch): SavedFilters {
  const out: SavedFilters = {}
  const status = statusFilter(s)
  if (status) out.status = status
  if (s.mailbox) out.mailbox_ids = splitList(s.mailbox)
  if (s.label) out.label_ids = splitList(s.label)
  if (s.team) out.team_ids = splitList(s.team)
  if (s.assignee) out.assignee = s.assignee
  if (s.priority) out.priorities = splitList(s.priority) as Priority[]
  if (s.attachment) out.has_attachment = true
  if (s.after) out.after = s.after
  if (s.before) out.before = s.before
  if (s.contact) out.contact_id = s.contact
  if (s.organization) out.organization_id = s.organization
  return out
}

function joined(list: string[] | undefined): string | undefined {
  return list && list.length > 0 ? list.join(',') : undefined
}

export function savedToSearch(f: SavedFilters): InboxSearch {
  const out: InboxSearch = {}
  if (f.status && f.status !== 'open') out.status = f.status
  const mailbox = joined(f.mailbox_ids)
  if (mailbox) out.mailbox = mailbox
  const label = joined(f.label_ids)
  if (label) out.label = label
  const team = joined(f.team_ids)
  if (team) out.team = team
  if (f.assignee) out.assignee = f.assignee
  const priority = joined(f.priorities)
  if (priority) out.priority = priority
  if (f.has_attachment) out.attachment = true
  if (f.after) out.after = f.after
  if (f.before) out.before = f.before
  if (f.contact_id) out.contact = f.contact_id
  if (f.organization_id) out.organization = f.organization_id
  return out
}

// Two searches filter alike when they select the same rows, whatever order the ids were picked in.
export function sameFilters(a: InboxSearch, b: InboxSearch): boolean {
  const canon = (s: InboxSearch) =>
    JSON.stringify(
      filterKeys.map((k) => {
        const v = s[k]
        if (k === 'mailbox' || k === 'team' || k === 'label' || k === 'priority') return splitList(v as string | undefined).toSorted()
        if (k === 'status') return statusFilter(s) ?? null
        return v ?? null
      }),
    )
  return canon(a) === canon(b)
}

export function hasFilters(s: InboxSearch): boolean {
  return filterKeys.some((k) => (k === 'status' ? statusFilter(s) : s[k]) !== undefined)
}

// Clearing one filter kind. Status returns to its default, open.
export function withoutFilter(s: InboxSearch, key: FilterKey): InboxSearch {
  return Object.fromEntries(Object.entries(s).filter(([k]) => k !== key))
}

export function withFilter<K extends FilterKey>(s: InboxSearch, key: K, value: InboxSearch[K] | undefined): InboxSearch {
  if (value === undefined) return withoutFilter(s, key)
  return { ...s, [key]: value }
}

const DAY_MS = 24 * 60 * 60 * 1000

// The API's `before` is exclusive; the UI speaks of "tot en met", so its date is a day earlier.
export function inclusiveEnd(before: string): string {
  return new Date(Date.parse(`${before}T00:00:00Z`) - DAY_MS).toISOString().slice(0, 10)
}

export function exclusiveEnd(inclusive: string): string {
  return new Date(Date.parse(`${inclusive}T00:00:00Z`) + DAY_MS).toISOString().slice(0, 10)
}

const displayDate = new Intl.DateTimeFormat('nl-NL', { day: 'numeric', month: 'short', year: 'numeric', timeZone: 'UTC' })

export function formatFilterDate(date: string): string {
  const t = Date.parse(`${date}T00:00:00Z`)
  return Number.isNaN(t) ? date : displayDate.format(t)
}

export interface Lookups {
  mailboxes: ReadonlyMap<string, string>
  teams: ReadonlyMap<string, string>
  labels: ReadonlyMap<string, string>
  users: ReadonlyMap<string, string>
}

export const filterNames: Record<FilterKey, string> = {
  status: 'Status',
  mailbox: 'Mailbox',
  team: 'Team',
  label: 'Label',
  assignee: 'Toegewezen aan',
  priority: 'Prioriteit',
  attachment: 'Bijlage',
  after: 'Vanaf',
  before: 'Tot en met',
  contact: 'Contact',
  organization: 'Organisatie',
}

export interface Chip {
  key: FilterKey
  text: string
}

function names(ids: string[], map: ReadonlyMap<string, string>): string {
  return ids.map((id) => map.get(id) ?? 'onbekend').join(' of ')
}

// One removable chip per filter, worded as a sentence: "Status is Wachtend".
export function chipsFor(s: InboxSearch, lookups: Lookups): Chip[] {
  const chips: Chip[] = []
  const status = statusFilter(s)
  if (status) chips.push({ key: 'status', text: `Status is ${listStatusLabel[status]}` })
  if (s.mailbox) chips.push({ key: 'mailbox', text: `Mailbox is ${names(splitList(s.mailbox), lookups.mailboxes)}` })
  if (s.team) chips.push({ key: 'team', text: `Team is ${names(splitList(s.team), lookups.teams)}` })
  if (s.label) chips.push({ key: 'label', text: `Label is ${names(splitList(s.label), lookups.labels)}` })
  if (s.assignee) {
    const who = s.assignee === 'me' ? 'mij' : s.assignee === 'none' ? 'niemand' : (lookups.users.get(s.assignee) ?? 'onbekend')
    chips.push({ key: 'assignee', text: `Toegewezen aan is ${who}` })
  }
  if (s.priority) {
    const text = splitList(s.priority)
      .map((p) => priorityLabel[p as Priority])
      .join(' of ')
    chips.push({ key: 'priority', text: `Prioriteit is ${text}` })
  }
  if (s.attachment) chips.push({ key: 'attachment', text: 'Heeft bijlage' })
  if (s.after) chips.push({ key: 'after', text: `Vanaf ${formatFilterDate(s.after)}` })
  if (s.before) chips.push({ key: 'before', text: `Tot en met ${formatFilterDate(inclusiveEnd(s.before))}` })
  if (s.contact) chips.push({ key: 'contact', text: 'Contact is gekozen' })
  if (s.organization) chips.push({ key: 'organization', text: 'Organisatie is gekozen' })
  return chips
}
