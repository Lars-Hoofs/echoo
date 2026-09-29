import type { InfiniteData, QueryClient } from '@tanstack/react-query'

import { api } from './api'
import type { ConversationDetail, ConversationPage } from './inbox'

// Delay between opening a conversation and marking it read, so skimming through the list with
// the keyboard does not mark every row on the way.
export const READ_DELAY_MS = 800

export function withUnread(data: InfiniteData<ConversationPage, string>, id: string, unread: boolean): InfiniteData<ConversationPage, string> {
  return {
    ...data,
    pages: data.pages.map((page) => ({
      ...page,
      conversations: page.conversations.map((c) => (c.id === id && c.unread !== unread ? { ...c, unread } : c)),
    })),
  }
}

// Reading is personal and emits no realtime event, so the caches are patched here.
export function setUnread(qc: QueryClient, id: string, unread: boolean): void {
  for (const [key, data] of qc.getQueriesData<InfiniteData<ConversationPage, string>>({ queryKey: ['inbox', 'conversations'] })) {
    if (data) qc.setQueryData(key, withUnread(data, id, unread))
  }
  qc.setQueryData<ConversationDetail>(['inbox', 'conversation', id], (d) =>
    d && d.conversation.unread !== unread ? { ...d, conversation: { ...d.conversation, unread } } : d,
  )
  void qc.invalidateQueries({ queryKey: ['inbox', 'summary'] })
}

export async function markRead(qc: QueryClient, id: string): Promise<void> {
  await api('POST', `/conversations/${encodeURIComponent(id)}/read`)
  setUnread(qc, id, false)
}

export async function markUnread(qc: QueryClient, id: string): Promise<void> {
  await api('POST', `/conversations/${encodeURIComponent(id)}/unread`)
  setUnread(qc, id, true)
}
