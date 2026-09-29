import { type InfiniteData, type QueryClient, useQuery, useQueryClient } from '@tanstack/react-query'
import { useCallback } from 'react'

import { useToast } from '../../components/Toast'
import {
  applyChange,
  bulkAction,
  type BulkResult,
  type ChangeSet,
  describeChange,
  inverseChange,
  listMatchFromKey,
  matchesList,
  patchBody,
  patchPages,
  splitBulk,
} from '../../lib/actions'
import { api, ApiError } from '../../lib/api'
import { errorMessage } from '../../lib/errors'
import type { ConversationDetail, ConversationListItem, ConversationPage } from '../../lib/inbox'
import { meQuery } from '../../lib/session'

type Snapshot = [readonly unknown[], unknown][]

const LISTS = ['inbox', 'conversations'] as const

function optimistic(qc: QueryClient, ids: ReadonlySet<string>, change: ChangeSet, meId: string) {
  const now = new Date()
  for (const [key, data] of qc.getQueriesData<InfiniteData<ConversationPage, string>>({ queryKey: LISTS })) {
    const match = listMatchFromKey(key)
    if (!data || !match) continue
    qc.setQueryData(
      key,
      patchPages(data, ids, change, (item) => matchesList(item, match, meId, now)),
    )
  }
  for (const id of ids) {
    qc.setQueryData<ConversationDetail>(['inbox', 'conversation', id], (d) =>
      d ? { ...d, conversation: applyChange(d.conversation, change) } : d,
    )
  }
}

function snapshot(qc: QueryClient, ids: ReadonlySet<string>): Snapshot {
  const snap: Snapshot = qc.getQueriesData({ queryKey: LISTS })
  for (const id of ids) snap.push([['inbox', 'conversation', id], qc.getQueryData(['inbox', 'conversation', id])])
  return snap
}

async function send(targets: ConversationListItem[], change: ChangeSet): Promise<BulkResult[]> {
  const [only] = targets
  if (targets.length === 1 && only) {
    const body = patchBody(change)
    if (Object.keys(body).length > 0) await api('PATCH', `/conversations/${only.id}`, body)
    if (change.addLabels || change.removeLabelIds) {
      const label_ids = applyChange(only, change).labels.map((l) => l.id)
      await api('PUT', `/conversations/${only.id}/labels`, { label_ids })
    }
    return [{ id: only.id, ok: true }]
  }
  const res = await api<{ results: BulkResult[] }>('POST', '/conversations/bulk', {
    ids: targets.map((t) => t.id),
    action: bulkAction(change),
  })
  return res.results
}

// Applies a change to one or more conversations: the lists update at once, a failure puts them
// back and shows why, and a success offers to undo.
export function useConversationActions() {
  const qc = useQueryClient()
  const toast = useToast()
  const me = useQuery(meQuery)
  const meId = me.data?.user.id ?? ''

  const apply = useCallback(
    async function run(targets: ConversationListItem[], change: ChangeSet, isUndo = false): Promise<void> {
      if (targets.length === 0) return
      const ids = new Set(targets.map((t) => t.id))
      await qc.cancelQueries({ queryKey: ['inbox'] })
      const before = snapshot(qc, ids)
      optimistic(qc, ids, change, meId)

      let results: BulkResult[]
      try {
        results = await send(targets, change)
      } catch (err) {
        for (const [key, data] of before) qc.setQueryData(key, data)
        toast(errorMessage(err), { tone: 'error' })
        void qc.invalidateQueries({ queryKey: ['inbox'] })
        return
      }
      void qc.invalidateQueries({ queryKey: ['inbox'] })

      const { ok, failed } = splitBulk(results)
      if (failed.length > 0) {
        const first = failed[0]
        const reason = first ? errorMessage(new ApiError(422, first.code ?? 'internal')) : ''
        toast(`${ok.length} van ${targets.length} gesprekken bijgewerkt. ${reason}`, { tone: 'error' })
        return
      }
      if (isUndo) {
        toast('Ongedaan gemaakt')
        return
      }
      const undo = () => {
        // Group by the inverse, so the conversations that share an old value are restored in one call.
        const groups = new Map<string, { change: ChangeSet; items: ConversationListItem[] }>()
        for (const item of targets) {
          const inv = inverseChange(item, change)
          const key = JSON.stringify(inv)
          const group = groups.get(key) ?? { change: inv, items: [] }
          group.items.push(item)
          groups.set(key, group)
        }
        for (const g of groups.values()) void run(g.items, g.change, true)
      }
      toast(describeChange(change, targets.length), { action: { label: 'Ongedaan maken', onClick: undo } })
    },
    [qc, toast, meId],
  )

  return apply
}
