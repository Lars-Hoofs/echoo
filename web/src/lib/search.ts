import { infiniteQueryOptions, queryOptions } from '@tanstack/react-query'

import { api } from './api'
import type { ConversationListItem } from './inbox'
import { readStored, writeStored } from './storage'

export interface SearchResult {
  conversation: ConversationListItem
  // Plain text with matches between U+27E6 and U+27E7; empty when only the subject or contact matched.
  snippet: string
}

export interface SearchPage {
  results: SearchResult[]
  next_cursor: string | null
}

export const MIN_QUERY_LENGTH = 2

const OPEN = '⟦'
const CLOSE = '⟧'

export interface SnippetSegment {
  text: string
  match: boolean
}

// Turns the server's marked plain text into segments to render as text nodes. The text is never
// treated as HTML, so mail content cannot inject markup through a snippet.
export function splitSnippet(snippet: string): SnippetSegment[] {
  const segments: SnippetSegment[] = []
  let match = false
  let current = ''
  const flush = () => {
    if (current !== '') segments.push({ text: current, match })
    current = ''
  }
  for (const ch of snippet.replace(/\s+/g, ' ').trim()) {
    if (ch === OPEN) {
      flush()
      match = true
    } else if (ch === CLOSE) {
      flush()
      match = false
    } else {
      current += ch
    }
  }
  flush()
  return segments
}

export function searchParams(q: string, cursor?: string, limit?: number): string {
  const params = new URLSearchParams({ q })
  if (limit) params.set('limit', String(limit))
  if (cursor) params.set('cursor', cursor)
  return params.toString()
}

export function isSearchable(q: string): boolean {
  return q.trim().length >= MIN_QUERY_LENGTH
}

export const searchQuery = (q: string) =>
  infiniteQueryOptions({
    queryKey: ['search', q],
    queryFn: ({ pageParam }) => api<SearchPage>('GET', `/search?${searchParams(q, pageParam)}`),
    initialPageParam: '',
    getNextPageParam: (last) => last.next_cursor ?? undefined,
    enabled: isSearchable(q),
    staleTime: 15_000,
  })

const PREVIEW_LIMIT = 6

// The few results the command bar shows while typing.
export const searchPreviewQuery = (q: string) =>
  queryOptions({
    queryKey: ['search', 'preview', q],
    queryFn: () => api<SearchPage>('GET', `/search?${searchParams(q, undefined, PREVIEW_LIMIT)}`),
    enabled: isSearchable(q),
    staleTime: 15_000,
  })

export interface RecentConversation {
  id: string
  number: number
  subject: string
}

const RECENT_KEY = 'echoo.recent'
const RECENT_MAX = 5

function isRecent(v: unknown): v is RecentConversation {
  if (typeof v !== 'object' || v === null) return false
  const r = v as Record<string, unknown>
  return typeof r.id === 'string' && typeof r.number === 'number' && typeof r.subject === 'string'
}

export function parseRecent(raw: string | null): RecentConversation[] {
  if (raw === null) return []
  try {
    const data: unknown = JSON.parse(raw)
    return Array.isArray(data) ? data.filter(isRecent).slice(0, RECENT_MAX) : []
  } catch {
    return []
  }
}

export function addRecent(list: RecentConversation[], item: RecentConversation): RecentConversation[] {
  return [item, ...list.filter((r) => r.id !== item.id)].slice(0, RECENT_MAX)
}

export function readRecent(): RecentConversation[] {
  return parseRecent(readStored(RECENT_KEY))
}

export function rememberConversation(item: RecentConversation) {
  writeStored(RECENT_KEY, JSON.stringify(addRecent(readRecent(), item)))
}

function normalize(s: string): string {
  return s.toLowerCase().normalize('NFD').replace(/\p{M}/gu, '')
}

// True when every word of the query occurs in the text, ignoring case and accents.
export function matchesQuery(text: string, query: string): boolean {
  const t = normalize(text)
  return normalize(query)
    .split(/\s+/)
    .filter((w) => w !== '')
    .every((w) => t.includes(w))
}
