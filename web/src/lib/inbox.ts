import { infiniteQueryOptions, queryOptions } from '@tanstack/react-query'

import { api } from './api'
import { advancedKey, appendFilterParams } from './filters'
import type { PhishingWarning } from './render'
import type { ConversationSla } from './sla'
import { pollWhenDisconnected } from './realtime'

export type ConversationStatus = 'open' | 'waiting' | 'closed' | 'spam'
// Snoozed is a list view over open and waiting conversations, not a status a conversation has.
export type ListStatus = ConversationStatus | 'snoozed'
export type Priority = 'none' | 'low' | 'normal' | 'high' | 'urgent'
export type InboxView = 'mine' | 'unassigned' | 'all'
export type ViewSlug = 'mine' | 'zonder-toewijzing' | 'alle'
export type OutboundStatus = 'queued' | 'sending' | 'retry' | 'sent' | 'failed' | 'uncertain' | 'bounced' | 'cancelled'

export const statusLabel: Record<ConversationStatus, string> = {
  open: 'Open',
  waiting: 'Wachtend',
  closed: 'Gesloten',
  spam: 'Spam',
}

export const statusOrder: ConversationStatus[] = ['open', 'waiting', 'closed', 'spam']
export const listStatusOrder: ListStatus[] = [...statusOrder, 'snoozed']
export const listStatusLabel: Record<ListStatus, string> = { ...statusLabel, snoozed: 'Uitgesteld' }

export const priorityLabel: Record<Priority, string> = {
  none: 'Geen',
  low: 'Laag',
  normal: 'Normaal',
  high: 'Hoog',
  urgent: 'Urgent',
}

const slugToView: Record<ViewSlug, InboxView> = { mine: 'mine', 'zonder-toewijzing': 'unassigned', alle: 'all' }
const viewToSlug: Record<InboxView, ViewSlug> = { mine: 'mine', unassigned: 'zonder-toewijzing', all: 'alle' }

export function viewFromSlug(slug: string): InboxView | undefined {
  return Object.hasOwn(slugToView, slug) ? slugToView[slug as ViewSlug] : undefined
}

export function slugFromView(view: InboxView): ViewSlug {
  return viewToSlug[view]
}

export const viewTitle: Record<InboxView, string> = {
  mine: 'Mijn',
  unassigned: 'Zonder toewijzing',
  all: 'Alle',
}

export interface InboxSearch {
  status?: ListStatus
  // mailbox, team, label and priority hold one or more comma separated values.
  mailbox?: string
  team?: string
  label?: string
  // "me", "none" or a user id.
  assignee?: string
  priority?: string
  attachment?: true
  // YYYY-MM-DD; before is exclusive, like the API's.
  after?: string
  before?: string
  contact?: string
  organization?: string
  // The saved view this list started from.
  weergave?: string
}

// Unknown or default values are dropped so URLs stay clean and bookmarkable.
export function parseInboxSearch(search: Record<string, unknown>): InboxSearch {
  const out: InboxSearch = {}
  if (typeof search.status === 'string' && search.status !== 'open' && (listStatusOrder as string[]).includes(search.status)) {
    out.status = search.status as ListStatus
  }
  if (typeof search.mailbox === 'string' && search.mailbox) out.mailbox = search.mailbox
  if (typeof search.team === 'string' && search.team) out.team = search.team
  if (typeof search.label === 'string' && search.label) out.label = search.label
  if (typeof search.assignee === 'string' && search.assignee) out.assignee = search.assignee
  if (typeof search.priority === 'string') {
    const priorities = search.priority.split(',').filter((p) => Object.hasOwn(priorityLabel, p))
    if (priorities.length > 0) out.priority = priorities.join(',')
  }
  if (search.attachment === true) out.attachment = true
  for (const key of ['after', 'before'] as const) {
    const v = search[key]
    if (typeof v === 'string' && /^\d{4}-\d{2}-\d{2}$/.test(v)) out[key] = v
  }
  if (typeof search.contact === 'string' && search.contact) out.contact = search.contact
  if (typeof search.organization === 'string' && search.organization) out.organization = search.organization
  if (typeof search.weergave === 'string' && search.weergave) out.weergave = search.weergave
  return out
}

export function withStatus(search: InboxSearch, status: ListStatus): InboxSearch {
  const next = { ...search }
  if (status === 'open') delete next.status
  else next.status = status
  return next
}

export interface Ref {
  id: string
  name: string
}

export type LabelColor = 'slate' | 'blue' | 'teal' | 'green' | 'amber' | 'orange' | 'red' | 'violet'

export interface LabelRef {
  id: string
  name: string
  color_token: LabelColor
}

export interface InboxSummary {
  mailboxes: { id: string; name: string; email_address: string; open_count: number }[]
  teams: { id: string; name: string; open_count: number }[]
  counts: { mine: number; unassigned: number; all: number; unread_mine: number }
}

export interface ConversationListItem {
  id: string
  number: number
  subject: string
  status: ConversationStatus
  priority: Priority
  version: number
  snoozed_until: string | null
  labels: LabelRef[]
  preview: string
  last_message_at: string
  message_count: number
  has_attachments: boolean
  last_direction: 'in' | 'out' | null
  mailbox: Ref
  contact: { id: string; name: string; email: string } | null
  assignee: Ref | null
  team: Ref | null
  // Null when no SLA policy applies.
  sla: ConversationSla | null
  // Personal: a customer message or someone else's note arrived since you last read it.
  unread: boolean
}

export interface ConversationPage {
  conversations: ConversationListItem[]
  next_cursor: string | null
}

export interface Address {
  name: string
  address: string
}

export interface Attachment {
  id: string
  filename: string
  size: number
  sniffed_type: string
  inline: boolean
  download_url: string
  dangerous: boolean
  scan_status: 'not_scanned' | 'clean' | 'infected' | 'error'
}

export interface Message {
  id: string
  kind: 'email' | 'note' | 'system'
  direction: 'in' | 'out' | null
  from: Address
  to: Address[]
  cc: Address[]
  subject: string
  body_text: string
  sent_at: string | null
  received_at: string | null
  author: Ref | null
  attachments: Attachment[]
  outbound_status: OutboundStatus | null
  outbound_error: string | null
  has_html: boolean
  render_url: string
  blocked_images: number
  phishing_warnings: PhishingWarning[]
}

export interface ContactDetail {
  id: string
  name: string
  email: string
  organization: Ref | null
  conversation_count: number
}

export interface ConversationDetail {
  conversation: ConversationListItem & { created_at: string; first_responded_at: string | null; resolved_at: string | null; can_write: boolean }
  messages: Message[]
  contact: ContactDetail | null
  // The customer's satisfaction rating, null until they answered.
  csat: { rating: number; comment: string; updated_at: string } | null
}

export interface ListFilter extends Omit<InboxSearch, 'status' | 'weergave'> {
  view: InboxView
  status: ListStatus
}

const PAGE_SIZE = 50
const POLL_MS = 30_000

export const summaryQuery = queryOptions({
  queryKey: ['inbox', 'summary'],
  queryFn: () => api<InboxSummary>('GET', '/inbox/summary'),
  refetchInterval: pollWhenDisconnected(POLL_MS),
})

export function conversationsQuery(f: ListFilter) {
  return infiniteQueryOptions({
    queryKey: ['inbox', 'conversations', f.view, f.status, f.mailbox ?? '', f.team ?? '', f.label ?? '', advancedKey(f)],
    queryFn: ({ pageParam }) => {
      const q = new URLSearchParams({ view: f.view, status: f.status, limit: String(PAGE_SIZE) })
      appendFilterParams(q, f)
      if (pageParam) q.set('cursor', pageParam)
      return api<ConversationPage>('GET', `/conversations?${q.toString()}`)
    },
    initialPageParam: '',
    getNextPageParam: (last) => last.next_cursor ?? undefined,
    refetchInterval: pollWhenDisconnected(POLL_MS),
  })
}

export const conversationQuery = (id: string) =>
  queryOptions({
    queryKey: ['inbox', 'conversation', id],
    queryFn: () => api<ConversationDetail>('GET', `/conversations/${encodeURIComponent(id)}`),
    refetchInterval: pollWhenDisconnected(POLL_MS),
  })
