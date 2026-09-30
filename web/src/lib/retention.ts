import { queryOptions } from '@tanstack/react-query'

import { api } from './api'

export interface Periods {
  closed_conversation_months: number | null
  attachment_months: number | null
  spam_days: number | null
  trash_days: number | null
}

export interface RetentionInput {
  global: Periods
  audit_months: number | null
  mailboxes: (Periods & { mailbox_id: string })[]
}

export interface LastRun {
  at: string
  closed_conversations: number
  spam_conversations: number
  trash_conversations: number
  attachments: number
  audit_entries: number
}

export type RetentionSettings = RetentionInput & { last_run: LastRun | null }

export interface Impact {
  conversations: number
  messages: number
  attachments: number
  attachment_bytes: number
}

export interface RetentionPreview {
  closed_conversations: Impact
  spam_conversations: Impact
  trash_conversations: Impact
  attachments: Impact
  audit_entries: number
}

export interface KeyStatus {
  keys: { id: string; active: boolean; values: number; columns: Record<string, number> }[]
  unknown: { id: string; values: number }[]
}

export const retentionQuery = queryOptions({
  queryKey: ['settings', 'retention'],
  queryFn: () => api<RetentionSettings>('GET', '/settings/retention'),
})

export const keyStatusQuery = queryOptions({
  queryKey: ['admin', 'keys'],
  queryFn: () => api<KeyStatus>('GET', '/admin/keys'),
})

// The text of a period field: empty keeps the data forever.
export type PeriodText = { closed: string; attachments: string; spam: string; trash: string }

export function toText(p: Periods): PeriodText {
  const text = (n: number | null) => (n === null ? '' : String(n))
  return {
    closed: text(p.closed_conversation_months),
    attachments: text(p.attachment_months),
    spam: text(p.spam_days),
    trash: text(p.trash_days),
  }
}

// Returns null while the text is not a whole number of at least 1 (or empty, which keeps forever).
function parsePeriod(text: string): number | null | undefined {
  const t = text.trim()
  if (t === '') return null
  if (!/^\d+$/.test(t)) return undefined
  const n = Number(t)
  return n >= 1 ? n : undefined
}

export function fromText(t: PeriodText): Periods | undefined {
  const closed = parsePeriod(t.closed)
  const attachments = parsePeriod(t.attachments)
  const spam = parsePeriod(t.spam)
  const trash = parsePeriod(t.trash)
  if (closed === undefined || attachments === undefined || spam === undefined || trash === undefined) return undefined
  return { closed_conversation_months: closed, attachment_months: attachments, spam_days: spam, trash_days: trash }
}

export function auditFromText(text: string): number | null | undefined {
  const n = parsePeriod(text)
  return n === undefined || (n !== null && n < 3) ? undefined : n
}

export function hasAnyPeriod(p: Periods): boolean {
  return p.closed_conversation_months !== null || p.attachment_months !== null || p.spam_days !== null || p.trash_days !== null
}
