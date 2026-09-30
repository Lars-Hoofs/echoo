import { type InfiniteData, infiniteQueryOptions, queryOptions } from '@tanstack/react-query'

import type { BulkResult, TimelineEvent } from './actions'
import { api } from './api'
import type { ConversationDetail, ConversationPage, ConversationStatus, Ref } from './inbox'

export interface TrashedConversation {
  id: string
  number: number
  subject: string
  status: ConversationStatus
  preview: string
  last_message_at: string
  message_count: number
  mailbox: Ref
  contact: { id: string; name: string; email: string } | null
  deleted_at: string
  deleted_by: Ref | null
}

export interface TrashPage {
  conversations: TrashedConversation[]
  next_cursor: string | null
}

const PAGE_SIZE = 50

// Under the inbox key, so moving a conversation to the trash or back refreshes both sides.
export const trashQuery = (mailboxId: string) =>
  infiniteQueryOptions({
    queryKey: ['inbox', 'trash', mailboxId],
    queryFn: ({ pageParam }) => {
      const q = new URLSearchParams({ limit: String(PAGE_SIZE) })
      if (mailboxId) q.set('mailbox_id', mailboxId)
      if (pageParam) q.set('cursor', pageParam)
      return api<TrashPage>('GET', `/trash?${q.toString()}`)
    },
    initialPageParam: '',
    getNextPageParam: (last) => last.next_cursor ?? undefined,
  })

// A conversation in the trash, read-only: can_write is always false.
export interface TrashedConversationDetail extends ConversationDetail {
  events: TimelineEvent[]
  deleted_at: string
  deleted_by: Ref | null
}

export const trashedConversationQuery = (id: string) =>
  queryOptions({
    queryKey: ['inbox', 'trash', 'conversation', id],
    queryFn: () => api<TrashedConversationDetail>('GET', `/trash/${encodeURIComponent(id)}`),
  })

export type TrashAction = 'trash' | 'restore' | 'purge'

const paths: Record<TrashAction, string> = { trash: '/trash', restore: '/trash/restore', purge: '/trash/purge' }

export function runTrashAction(action: TrashAction, ids: string[]): Promise<BulkResult[]> {
  return api<{ results: BulkResult[] }>('POST', paths[action], { ids }).then((r) => r.results)
}

export function emptyTrash(mailboxId: string): Promise<number> {
  return api<{ deleted: number }>('POST', '/trash/empty', mailboxId ? { mailbox_id: mailboxId } : {}).then((r) => r.deleted)
}

const done: Record<TrashAction, string> = {
  trash: 'naar de prullenbak verplaatst',
  restore: 'teruggezet',
  purge: 'definitief verwijderd',
}

// The toast text after an action on count conversations of which ok succeeded.
export function describeTrashAction(action: TrashAction, ok: number, count: number): string {
  if (ok < count) return `${ok} van ${count} gesprekken ${done[action]}.`
  return count === 1 ? `Gesprek ${done[action]}` : `${count} gesprekken ${done[action]}`
}

// Drops the conversations from cached list pages, so they disappear before the refetch lands.
export function withoutConversations(data: InfiniteData<ConversationPage, string>, ids: ReadonlySet<string>): InfiniteData<ConversationPage, string> {
  return {
    ...data,
    pages: data.pages.map((p) => ({ ...p, conversations: p.conversations.filter((c) => !ids.has(c.id)) })),
  }
}
