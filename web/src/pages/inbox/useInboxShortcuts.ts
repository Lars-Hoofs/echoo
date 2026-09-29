import { useQuery, useQueryClient } from '@tanstack/react-query'
import { useEffect, useRef } from 'react'

import { composerModeForKey, requestComposer } from '../../lib/composer'
import type { ConversationDetail, ConversationListItem } from '../../lib/inbox'
import { isTypingTarget } from '../../lib/keys'
import { hasPermission, meQuery } from '../../lib/session'
import { type PickerKind, usePickers } from './ActionPickers'
import { useConversationActions } from './useConversationActions'

const pickerKeys: Record<string, PickerKind> = { a: 'assign', l: 'label', p: 'priority', s: 'snooze' }

interface Args {
  items: ConversationListItem[]
  conversationId: string | undefined
  selected: ConversationListItem[]
  toggle: (id: string) => void
  clearSelection: () => void
}

// e closes (or reopens), w sets waiting, r and n focus the composer of the open conversation as
// reply or note, a/l/p/s open the pickers, x selects. With a selection
// the keys act on all selected conversations, otherwise on the open or focused one.
export function useInboxShortcuts(args: Args) {
  const apply = useConversationActions()
  const { openPicker } = usePickers()
  const qc = useQueryClient()
  const me = useQuery(meQuery)
  const allowed = hasPermission(me.data, 'conversations.write')
  const latest = useRef(args)
  useEffect(() => {
    latest.current = args
  })

  useEffect(() => {
    if (!allowed) return
    const onKey = (e: KeyboardEvent) => {
      if (e.metaKey || e.ctrlKey || e.altKey || e.shiftKey || isTypingTarget(e.target)) return
      const { items, conversationId, selected, toggle, clearSelection } = latest.current
      const focusedId = document.activeElement instanceof HTMLElement ? document.activeElement.dataset.row : undefined
      const detail = conversationId
        ? qc.getQueryData<ConversationDetail>(['inbox', 'conversation', conversationId])?.conversation
        : undefined
      const single = items.find((i) => i.id === (focusedId || conversationId)) ?? detail
      const targets = selected.length > 0 ? selected : single ? [single] : []

      if (e.key === 'x') {
        const id = focusedId || conversationId
        if (!id) return
        e.preventDefault()
        toggle(id)
        return
      }
      const mode = composerModeForKey(e.key)
      if (mode) {
        if (!conversationId) return
        e.preventDefault()
        requestComposer(mode)
        return
      }
      if (targets.length === 0) return
      const picker = pickerKeys[e.key]
      if (picker) {
        e.preventDefault()
        openPicker(picker, targets)
      } else if (e.key === 'e' || e.key === 'w') {
        e.preventDefault()
        const done = targets.every((t) => t.status === 'closed' || t.status === 'spam')
        void apply(targets, { status: e.key === 'w' ? 'waiting' : done ? 'open' : 'closed' })
        if (selected.length > 0) clearSelection()
      }
    }
    document.addEventListener('keydown', onKey)
    return () => {
      document.removeEventListener('keydown', onKey)
    }
  }, [allowed, apply, openPicker, qc])
}
