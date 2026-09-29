import * as Menu from '@radix-ui/react-dropdown-menu'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useNavigate } from '@tanstack/react-router'
import { Bell } from 'lucide-react'

import { IconButton } from '../../components/ui'
import { api } from '../../lib/api'
import { type AppNotification, notificationsQuery, notificationText } from '../../lib/composer'
import { formatRelative } from '../../lib/format'
import { menuItem, menuPanel } from './menu'

export function NotificationsBell() {
  const qc = useQueryClient()
  const navigate = useNavigate()
  const list = useQuery(notificationsQuery)
  const unread = list.data?.unread_count ?? 0
  const markRead = useMutation({
    mutationFn: (body: { ids: string[] } | { all: true }) => api('POST', '/notifications/read', body),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['notifications'] }),
  })

  function open(n: AppNotification) {
    if (!n.read_at) markRead.mutate({ ids: [n.id] })
    void navigate({ to: '/inbox/$view/$conversationId', params: { view: 'alle', conversationId: n.conversation_id } })
  }

  return (
    <Menu.Root>
      <Menu.Trigger asChild>
        <IconButton label={unread > 0 ? `Meldingen, ${unread} ongelezen` : 'Meldingen'} size="sm" className="relative">
          <Bell size={16} aria-hidden />
          {unread > 0 && (
            <span
              aria-hidden
              className="absolute -top-1 -right-1 flex h-4 min-w-4 items-center justify-center rounded-full bg-accent px-1 text-xs font-medium text-on-accent tabular-nums"
            >
              {unread > 9 ? '9+' : unread}
            </span>
          )}
        </IconButton>
      </Menu.Trigger>
      <Menu.Portal>
        <Menu.Content align="start" sideOffset={6} className={`${menuPanel} w-80 max-w-[calc(100vw-16px)] p-0`}>
          <div className="flex items-center justify-between border-b border-line px-4 py-3">
            <span className="t-title text-ink">Meldingen</span>
            <Menu.Item
              disabled={unread === 0}
              onSelect={() => markRead.mutate({ all: true })}
              className="cursor-default rounded-full px-3 py-1 text-sm text-ink underline outline-none data-disabled:opacity-50 data-highlighted:bg-selected"
            >
              Alles gelezen
            </Menu.Item>
          </div>
          {list.isError ? (
            <p className="px-4 py-4 text-base text-muted">Meldingen laden lukt niet.</p>
          ) : (list.data?.notifications.length ?? 0) === 0 ? (
            <p className="px-4 py-4 text-base text-muted">Geen meldingen.</p>
          ) : (
            <ul className="max-h-96 overflow-y-auto p-2">
              {list.data?.notifications.map((n) => (
                <li key={n.id}>
                  <Menu.Item className={`${menuItem} items-start rounded-md`} onSelect={() => open(n)}>
                    <span aria-hidden className={`mt-2 size-2 shrink-0 rounded-full ${n.read_at ? 'bg-transparent' : 'bg-accent'}`} />
                    <span className="min-w-0 flex-1">
                      <span className={`block ${n.read_at ? 'text-muted' : 'font-medium text-ink'}`}>{notificationText(n)}</span>
                      <span className="block truncate text-sm text-muted">
                        #{n.conversation_number} {n.conversation_subject || '(geen onderwerp)'}
                      </span>
                      <span className="block text-sm text-faint">{formatRelative(n.created_at)}</span>
                    </span>
                  </Menu.Item>
                </li>
              ))}
            </ul>
          )}
        </Menu.Content>
      </Menu.Portal>
    </Menu.Root>
  )
}
