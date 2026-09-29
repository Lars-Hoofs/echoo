import { queryOptions } from '@tanstack/react-query'
import { useSyncExternalStore } from 'react'

import { api } from './api'
import type { Address, ConversationStatus } from './inbox'
import { pollWhenDisconnected } from './realtime'

const ADDRESS = /^[^\s@<>"',;()[\]\\]+@[^\s@<>"',;()[\]\\]+\.[^\s@<>"',;()[\]\\]{2,}$/

export interface ParsedRecipients {
  valid: Address[]
  invalid: string[]
}

// Splits on commas, semicolons and line breaks, but not inside "quotes" or <angle brackets>, so
// `"Vries, Jan" <jan@example.com>` stays one recipient.
function splitRecipients(input: string): string[] {
  const parts: string[] = []
  let current = ''
  let quoted = false
  let angled = false
  for (const ch of input) {
    if (ch === '"') quoted = !quoted
    else if (!quoted && ch === '<') angled = true
    else if (!quoted && ch === '>') angled = false
    if (!quoted && !angled && (ch === ',' || ch === ';' || ch === '\n' || ch === '\r')) {
      parts.push(current)
      current = ''
    } else {
      current += ch
    }
  }
  parts.push(current)
  return parts.map((p) => p.trim()).filter(Boolean)
}

function parseOne(token: string): Address | null {
  const angle = /^(.*)<([^<>]*)>$/.exec(token)
  const rawAddress = (angle ? (angle[2] ?? '') : token).trim().toLowerCase()
  if (!ADDRESS.test(rawAddress)) return null
  const name = angle ? (angle[1] ?? '').trim().replace(/^"(.*)"$/, '$1').trim() : ''
  if (/[<>]/.test(name)) return null
  return { name, address: rawAddress }
}

// Parses what a person typed or pasted into recipient chips. Bare addresses separated by
// spaces are accepted too. Anything that is not an address is returned in invalid.
export function parseRecipients(input: string): ParsedRecipients {
  const valid: Address[] = []
  const invalid: string[] = []
  for (const token of splitRecipients(input)) {
    const pieces = token.includes('<') ? [token] : token.split(/\s+/)
    const candidates = pieces.length > 1 && pieces.every((p) => p.includes('@')) ? pieces : [token]
    for (const candidate of candidates) {
      const parsed = parseOne(candidate)
      if (parsed) valid.push(parsed)
      else invalid.push(candidate)
    }
  }
  return { valid, invalid }
}

// Adds addresses to a list without duplicates; the first spelling of a name wins.
export function mergeAddresses(list: Address[], extra: Address[]): Address[] {
  const seen = new Set(list.map((a) => a.address))
  const out = [...list]
  for (const a of extra) {
    if (!seen.has(a.address)) {
      seen.add(a.address)
      out.push(a)
    }
  }
  return out
}

export const formatAddress = (a: Address): string => (a.name ? `${a.name} <${a.address}>` : a.address)

export interface TemplateVariable {
  name: string
  label: string
}

// Must match the allowlist in internal/compose/variables.go.
export const templateVariables: TemplateVariable[] = [
  { name: 'contact.name', label: 'Naam contactpersoon' },
  { name: 'contact.first_name', label: 'Voornaam contactpersoon' },
  { name: 'contact.email', label: 'E-mailadres contactpersoon' },
  { name: 'agent.name', label: 'Jouw naam' },
  { name: 'agent.first_name', label: 'Jouw voornaam' },
  { name: 'conversation.number', label: 'Gespreksnummer' },
  { name: 'mailbox.name', label: 'Naam mailbox' },
]

export interface VariableMatch {
  from: number
  to: number
  name: string
  known: boolean
}

const VARIABLE = /\{\{\s*([a-z_]+\.[a-z_]+)\s*\}\}/g

// Finds {{variables}} in text. Unknown ones are reported so the editor can flag them: the
// server leaves them visible in the message when a template is used.
export function findVariables(text: string): VariableMatch[] {
  const known = new Set(templateVariables.map((v) => v.name))
  return Array.from(text.matchAll(VARIABLE), (m) => {
    const name = m[1] ?? ''
    return { from: m.index, to: m.index + m[0].length, name, known: known.has(name) }
  })
}

export interface Upload {
  id: string
  filename: string
  size: number
  content_type: string
  content_id: string
  expires_at: string
}

export interface Draft {
  to: Address[]
  cc: Address[]
  bcc: Address[]
  subject: string
  html: string
  attachments: Upload[]
  updated_at: string
}

export interface ReplyDefaults {
  to: Address[]
  cc: Address[]
  // Senders who wrote into the thread without being a known participant. Never in to or cc by default.
  suggested_cc: Address[]
  subject: string
  from: Address
  send_delay_seconds: number
}

// The suggestions that are not yet among the recipients.
export function pendingSuggestions(suggested: Address[], ...fields: Address[][]): Address[] {
  const taken = new Set(fields.flat().map((a) => a.address.toLowerCase()))
  return suggested.filter((a) => !taken.has(a.address.toLowerCase()))
}

export interface SentMessage {
  message: { id: string }
  undo_until: string | null
  conversation_id?: string
}

export interface SendPayload {
  idempotency_key: string
  to: Address[]
  cc: Address[]
  bcc: Address[]
  subject: string
  html: string
  attachment_ids: string[]
  status_after: Exclude<ConversationStatus, 'open' | 'spam'> | null
}

// The composer copies these into its own state when it opens, so they are read fresh each time
// (gcTime 0) and never refetched behind the person's back.
export const replyDefaultsQuery = (conversationId: string) =>
  queryOptions({
    queryKey: ['composer', 'defaults', conversationId],
    queryFn: () => api<ReplyDefaults>('GET', `/conversations/${encodeURIComponent(conversationId)}/reply-defaults`),
    staleTime: Infinity,
    gcTime: 0,
    retry: false,
  })

export const draftQuery = (conversationId: string) =>
  queryOptions({
    queryKey: ['composer', 'draft', conversationId],
    queryFn: () => api<{ draft: Draft | null }>('GET', `/conversations/${encodeURIComponent(conversationId)}/draft`),
    staleTime: Infinity,
    gcTime: 0,
    retry: false,
  })

const composerVersions = new Map<string, number>()
const composerListeners = new Set<() => void>()

// Makes the composer of a conversation start over from the current defaults and draft, after a
// send or an undo. Autosaving a draft never does this, so typing is not interrupted.
export function resetComposer(conversationId: string) {
  composerVersions.set(conversationId, (composerVersions.get(conversationId) ?? 0) + 1)
  composerListeners.forEach((l) => {
    l()
  })
}

export function useComposerVersion(conversationId: string): number {
  return useSyncExternalStore(
    (l) => {
      composerListeners.add(l)
      return () => composerListeners.delete(l)
    },
    () => composerVersions.get(conversationId) ?? 0,
  )
}

export type ComposerMode = 'reply' | 'note'

export function composerModeForKey(key: string): ComposerMode | undefined {
  if (key === 'r') return 'reply'
  if (key === 'n') return 'note'
  return undefined
}

const COMPOSER_REQUEST = 'echoo:composer-request'

// Asks the composer of the open conversation to switch to a mode and take focus. The shortcuts
// and the command bar sit far from the composer in the tree, so this goes through the window.
export function requestComposer(mode: ComposerMode): void {
  window.dispatchEvent(new CustomEvent<ComposerMode>(COMPOSER_REQUEST, { detail: mode }))
}

export function onComposerRequest(handler: (mode: ComposerMode) => void): () => void {
  const listener = (e: Event) => {
    if (e instanceof CustomEvent) handler(e.detail as ComposerMode)
  }
  window.addEventListener(COMPOSER_REQUEST, listener)
  return () => {
    window.removeEventListener(COMPOSER_REQUEST, listener)
  }
}

export interface Mentionable {
  id: string
  name: string
  email: string
}

export const mentionableQuery = (conversationId: string) =>
  queryOptions({
    queryKey: ['composer', 'mentionable', conversationId],
    queryFn: () => api<{ users: Mentionable[] }>('GET', `/conversations/${encodeURIComponent(conversationId)}/mentionable`),
    staleTime: 60_000,
  })

export type TemplateScope = 'personal' | 'team' | 'mailbox' | 'global'

export interface Template {
  id: string
  name: string
  shortcode: string
  scope: TemplateScope
  team_id: string | null
  mailbox_id: string | null
  subject: string
  body_html: string
  editable: boolean
  updated_at: string
}

export const templatesQuery = (manage = false) =>
  queryOptions({
    queryKey: ['templates', manage ? 'manage' : 'visible'],
    queryFn: () => api<{ templates: Template[] }>('GET', manage ? '/templates?manage=true' : '/templates'),
    staleTime: 30_000,
  })

export interface RenderedTemplate {
  subject: string
  body_html: string
  unresolved: string[]
}

export const scopeLabel: Record<TemplateScope, string> = {
  personal: 'Persoonlijk',
  team: 'Team',
  mailbox: 'Mailbox',
  global: 'Iedereen',
}

// Matches a template to what was typed after the "/": shortcode first, then name.
export function filterTemplates(templates: Template[], query: string): Template[] {
  const q = query.trim().toLowerCase()
  if (!q) return templates
  const score = (t: Template): number => {
    if (t.shortcode && t.shortcode === q) return 0
    if (t.shortcode.startsWith(q)) return 1
    if (t.name.toLowerCase().startsWith(q)) return 2
    if (t.name.toLowerCase().includes(q)) return 3
    return -1
  }
  return templates
    .map((t) => ({ t, s: score(t) }))
    .filter((x) => x.s >= 0)
    .sort((a, b) => a.s - b.s)
    .map((x) => x.t)
}

export interface AppNotification {
  id: string
  kind: 'mention' | 'assigned' | 'reply' | 'sla' | 'csat'
  conversation_id: string
  conversation_number: number
  conversation_subject: string
  message_id: string | null
  actor: { id: string; name: string } | null
  created_at: string
  read_at: string | null
}

export interface NotificationList {
  notifications: AppNotification[]
  unread_count: number
}

// The realtime client invalidates the ['notifications'] prefix on a notification event.
export const notificationsQuery = queryOptions({
  queryKey: ['notifications', 'list'],
  queryFn: () => api<NotificationList>('GET', '/notifications'),
  refetchInterval: pollWhenDisconnected(30_000),
})

export function notificationText(n: AppNotification): string {
  const who = n.actor?.name ?? 'Iemand'
  switch (n.kind) {
    case 'mention':
      return `${who} heeft je genoemd`
    case 'assigned':
      return `${who} heeft een gesprek aan je toegewezen`
    case 'reply':
      return 'Nieuw antwoord van de klant'
    case 'sla':
      return 'De reactietermijn loopt af'
    case 'csat':
      return 'Klant gaf een lage beoordeling'
  }
}

export interface Signature {
  mailbox_id: string | null
  body_html: string
}

interface DocNode {
  type?: string
  attrs?: Record<string, unknown> | undefined
  content?: DocNode[] | undefined
}

// Ids of the people mentioned in a TipTap document, once each, in order of appearance.
export function collectMentions(doc: DocNode): string[] {
  const ids: string[] = []
  const walk = (node: DocNode) => {
    const id = node.attrs?.id
    if (node.type === 'mention' && typeof id === 'string' && !ids.includes(id)) ids.push(id)
    node.content?.forEach(walk)
  }
  walk(doc)
  return ids
}

// How long a just-sent message can still be taken back, in milliseconds; 0 when it cannot.
export function undoWindowMs(undoUntil: string | null, now: number = Date.now()): number {
  if (!undoUntil) return 0
  return Math.max(0, new Date(undoUntil).getTime() - now)
}
