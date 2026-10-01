import { infiniteQueryOptions, queryOptions } from '@tanstack/react-query'

import { api } from './api'
import type {
  AppNotification,
  ConversationDetail,
  ConversationPage,
  InboxSummary,
  InboxView,
  ListStatus,
  PushDevice,
  PushPrefs,
  ReplyDefaults,
  SearchHit,
  Template,
  TimelineEvent,
} from './types'

// The app has no event stream: lists refresh on focus, on pull, when a push arrives and on
// this interval while on screen.
export const LIST_REFRESH_MS = 30_000

export const summaryQuery = queryOptions({
  queryKey: ['inbox', 'summary'],
  queryFn: () => api<InboxSummary>('GET', '/inbox/summary'),
  refetchInterval: LIST_REFRESH_MS,
})

export const conversationsQuery = (view: InboxView, status: ListStatus) =>
  infiniteQueryOptions({
    queryKey: ['inbox', 'conversations', view, status],
    queryFn: ({ pageParam }) => {
      const q = new URLSearchParams({ view, status, limit: '30' })
      if (pageParam) q.set('cursor', pageParam)
      return api<ConversationPage>('GET', `/conversations?${q.toString()}`)
    },
    initialPageParam: '',
    getNextPageParam: (last) => last.next_cursor ?? undefined,
    refetchInterval: LIST_REFRESH_MS,
  })

export const conversationQuery = (id: string) =>
  queryOptions({
    queryKey: ['inbox', 'conversation', id],
    queryFn: () => api<ConversationDetail>('GET', `/conversations/${encodeURIComponent(id)}`),
    refetchInterval: LIST_REFRESH_MS,
  })

export const eventsQuery = (id: string) =>
  queryOptions({
    queryKey: ['inbox', 'conversation', id, 'events'],
    queryFn: () => api<{ events: TimelineEvent[] }>('GET', `/conversations/${encodeURIComponent(id)}/events`).then((r) => r.events),
  })

export const replyDefaultsQuery = (id: string) =>
  queryOptions({
    queryKey: ['inbox', 'conversation', id, 'reply-defaults'],
    queryFn: () => api<ReplyDefaults>('GET', `/conversations/${encodeURIComponent(id)}/reply-defaults`),
    staleTime: 60_000,
  })

export const notificationsQuery = queryOptions({
  queryKey: ['notifications'],
  queryFn: () => api<{ notifications: AppNotification[]; unread_count: number }>('GET', '/notifications'),
  refetchInterval: LIST_REFRESH_MS,
})

export const templatesQuery = queryOptions({
  queryKey: ['templates'],
  queryFn: () => api<{ templates: Template[] }>('GET', '/templates').then((r) => r.templates),
  staleTime: 5 * 60_000,
})

export const searchQuery = (q: string) =>
  queryOptions({
    queryKey: ['search', q],
    queryFn: () => api<{ results: SearchHit[] }>('GET', `/search?q=${encodeURIComponent(q)}&limit=30`).then((r) => r.results),
    enabled: q.trim().length >= 2,
    staleTime: 30_000,
  })

export const pushDevicesQuery = queryOptions({
  queryKey: ['push', 'devices'],
  queryFn: () => api<{ devices: PushDevice[] }>('GET', '/push/devices').then((r) => r.devices),
})

export const notificationSettingsQuery = queryOptions({
  queryKey: ['me', 'notification-settings'],
  queryFn: () => api<{ email: { mentions: boolean; assignments: boolean; replies: boolean }; push: PushPrefs }>('GET', '/me/notification-settings'),
})

// A change to one conversation. expected_version makes a stale screen fail loudly instead of
// overwriting what a colleague just did.
export function patchConversation(id: string, version: number, change: Record<string, unknown>) {
  return api<{ version: number }>('PATCH', `/conversations/${encodeURIComponent(id)}`, { ...change, expected_version: version })
}

export const pushConfigQuery = queryOptions({
  queryKey: ['push', 'config'],
  queryFn: () => api<{ web_push_public_key: string | null; apns: boolean; fcm: boolean }>('GET', '/push/config'),
  staleTime: 5 * 60_000,
})
