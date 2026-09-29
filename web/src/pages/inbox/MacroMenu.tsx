import * as Menu from '@radix-ui/react-dropdown-menu'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Link } from '@tanstack/react-router'
import { ChevronDown, Zap } from 'lucide-react'

import { useToast } from '../../components/Toast'
import { Button } from '../../components/ui'
import { api } from '../../lib/api'
import { type Macro, macroToast, macrosQuery } from '../../lib/automation'
import { errorMessage } from '../../lib/errors'
import type { ConversationListItem } from '../../lib/inbox'
import { menuItem, menuPanel } from '../shell/menu'

interface MacroResponse {
  results: { id: string; ok: boolean; code?: string }[]
}

// The Macro's menu of the conversation header and the bulk bar.
export function MacroMenu({
  conversations,
  size = 'md',
  onDone,
  iconOnlyOnPhone = false,
}: {
  conversations: ConversationListItem[]
  size?: 'sm' | 'md'
  onDone?: () => void
  // Shows a round icon button below the md breakpoint; the name stays for screen readers.
  iconOnlyOnPhone?: boolean
}) {
  const macros = useQuery(macrosQuery)
  const queryClient = useQueryClient()
  const toast = useToast()
  const run = useMutation({
    mutationFn: (m: Macro) => api<MacroResponse>('POST', `/macros/${m.id}/run`, { conversation_ids: conversations.map((c) => c.id) }),
    onSuccess: async (r, m) => {
      const failed = r.results.filter((x) => !x.ok).length
      toast(macroToast(m.name, r.results.length, failed), failed > 0 ? { tone: 'error' } : {})
      await queryClient.invalidateQueries({ queryKey: ['inbox'] })
      onDone?.()
    },
    onError: (err) => toast(errorMessage(err), { tone: 'error' }),
  })
  const personal = macros.data?.filter((m) => m.scope === 'personal') ?? []
  const shared = macros.data?.filter((m) => m.scope === 'global') ?? []

  return (
    <Menu.Root>
      <Menu.Trigger asChild>
        <Button size={size} busy={run.isPending} className={iconOnlyOnPhone ? 'max-md:size-11 max-md:px-0' : ''}>
          <Zap size={16} aria-hidden />
          <span className={iconOnlyOnPhone ? 'max-md:sr-only' : ''}>Macro&apos;s</span>
          <ChevronDown size={16} aria-hidden className={iconOnlyOnPhone ? 'max-md:hidden' : ''} />
        </Button>
      </Menu.Trigger>
      <Menu.Portal>
        <Menu.Content align="end" sideOffset={4} className={`${menuPanel} max-h-80 overflow-y-auto`}>
          {macros.isPending && <div className="px-4 py-2 text-base text-muted">Laden…</div>}
          {macros.isError && <div className="px-4 py-2 text-base text-danger-text">{errorMessage(macros.error)}</div>}
          {macros.data?.length === 0 && <div className="px-4 py-2 text-base text-muted">Nog geen macro&apos;s.</div>}
          {[
            { title: 'Werkruimte', list: shared },
            { title: 'Persoonlijk', list: personal },
          ]
            .filter((g) => g.list.length > 0)
            .map((g) => (
              <Menu.Group key={g.title}>
                <Menu.Label className="t-label px-4 pt-2 pb-1">{g.title}</Menu.Label>
                {g.list.map((m) => (
                  <Menu.Item key={m.id} className={menuItem} onSelect={() => run.mutate(m)}>
                    {m.name}
                  </Menu.Item>
                ))}
              </Menu.Group>
            ))}
          <Menu.Separator className="my-1 h-px bg-line" />
          <Menu.Item asChild className={menuItem}>
            <Link to="/instellingen/macros">Macro&apos;s beheren</Link>
          </Menu.Item>
        </Menu.Content>
      </Menu.Portal>
    </Menu.Root>
  )
}
