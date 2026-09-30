import { type InfiniteData, useQueryClient } from '@tanstack/react-query'
import { useCallback } from 'react'

import { useToast } from '../../components/Toast'
import { type BulkResult, splitBulk } from '../../lib/actions'
import { ApiError } from '../../lib/api'
import { errorMessage } from '../../lib/errors'
import type { ConversationPage } from '../../lib/inbox'
import { describeTrashAction, runTrashAction, type TrashAction, withoutConversations } from '../../lib/trash'

const undoOf: Partial<Record<TrashAction, TrashAction>> = { trash: 'restore', restore: 'trash' }

// Moves conversations to the trash, back, or out for good, and says how it went. Resolves to the
// ids that succeeded; moving to the trash and restoring offer to undo.
export function useTrashActions() {
  const qc = useQueryClient()
  const toast = useToast()

  return useCallback(
    async function run(action: TrashAction, ids: string[], isUndo = false): Promise<string[]> {
      if (ids.length === 0) return []
      let results: BulkResult[]
      try {
        results = await runTrashAction(action, ids)
      } catch (err) {
        toast(errorMessage(err), { tone: 'error' })
        return []
      }
      const { ok, failed } = splitBulk(results)
      if (action === 'trash' && ok.length > 0) {
        const gone = new Set(ok)
        for (const [key, data] of qc.getQueriesData<InfiniteData<ConversationPage, string>>({ queryKey: ['inbox', 'conversations'] })) {
          if (data) qc.setQueryData(key, withoutConversations(data, gone))
        }
      }
      void qc.invalidateQueries({ queryKey: ['inbox'] })

      const [first] = failed
      if (first) {
        toast(`${describeTrashAction(action, ok.length, ids.length)} ${errorMessage(new ApiError(422, first.code ?? 'internal'))}`, { tone: 'error' })
        return ok
      }
      if (isUndo) {
        toast('Ongedaan gemaakt')
        return ok
      }
      const inverse = undoOf[action]
      toast(
        describeTrashAction(action, ok.length, ids.length),
        inverse ? { action: { label: 'Ongedaan maken', onClick: () => void run(inverse, ok, true) } } : undefined,
      )
      return ok
    },
    [qc, toast],
  )
}
