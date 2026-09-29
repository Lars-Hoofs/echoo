import { queryOptions } from '@tanstack/react-query'

import { api } from './api'
import type { SavedFilters } from './filters'
import { type Ref } from './inbox'
import { pollWhenDisconnected } from './realtime'

export type ViewScope = 'personal' | 'everyone' | 'team'

export interface SavedView {
  id: string
  name: string
  scope: ViewScope
  team: Ref | null
  filters: SavedFilters
  sort: 'last_message_desc'
  position: number
  editable: boolean
  // Capped at 1000; null when the view does not list open conversations.
  open_count: number | null
}

export interface SavedViewInput {
  name: string
  filters: SavedFilters
  scope: ViewScope
  team_id?: string
}

export const savedViewsQuery = queryOptions({
  // Under "inbox" so that changing a conversation refreshes the counts too.
  queryKey: ['inbox', 'saved-views'],
  queryFn: () => api<{ views: SavedView[] }>('GET', '/saved-views').then((r) => r.views),
  refetchInterval: pollWhenDisconnected(30_000),
})

export const createSavedView = (input: SavedViewInput) => api<{ view: SavedView }>('POST', '/saved-views', input).then((r) => r.view)

export const updateSavedView = (id: string, patch: Partial<SavedViewInput>) =>
  api<{ view: SavedView }>('PATCH', `/saved-views/${encodeURIComponent(id)}`, patch).then((r) => r.view)

export const deleteSavedView = (id: string) => api<undefined>('DELETE', `/saved-views/${encodeURIComponent(id)}`)

export const scopeLabel: Record<ViewScope, string> = { personal: 'Persoonlijk', everyone: 'Iedereen', team: 'Team' }

// Counts stop at 1000 on the server; from there the UI says 999+.
export function formatViewCount(n: number): string {
  return n > 999 ? '999+' : String(n)
}
