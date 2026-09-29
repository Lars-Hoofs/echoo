import * as Menu from '@radix-ui/react-dropdown-menu'
import { useQueryClient } from '@tanstack/react-query'
import { ChevronDown, Clock, Ellipsis } from 'lucide-react'

import { useToast } from '../../components/Toast'
import { Button, IconButton } from '../../components/ui'
import { snoozePresets } from '../../lib/actions'
import { errorMessage } from '../../lib/errors'
import { type ConversationListItem, type ConversationStatus, statusLabel } from '../../lib/inbox'
import { markUnread } from '../../lib/unread'
import { menuItem, menuPanel } from '../shell/menu'
import { usePickers } from './ActionPickers'
import { MacroMenu } from './MacroMenu'
import { useConversationActions } from './useConversationActions'

const otherStatusLabel: Record<ConversationStatus, string> = {
  open: 'Heropenen',
  waiting: statusLabel.waiting,
  closed: statusLabel.closed,
  spam: statusLabel.spam,
}

// onMarkedUnread runs after the conversation was marked unread; the pane leaves the conversation
// then, because an open conversation is marked read again by itself.
export function HeaderActions({ conversation: c, onMarkedUnread }: { conversation: ConversationListItem; onMarkedUnread: () => void }) {
  const apply = useConversationActions()
  const qc = useQueryClient()
  const toast = useToast()
  const { openPicker } = usePickers()
  const done = c.status === 'closed' || c.status === 'spam'
  const primary: ConversationStatus = done ? 'open' : 'closed'
  const others = (['waiting', 'spam', 'open'] as const).filter((s) => s !== c.status && s !== primary)

  return (
    <div className="flex flex-wrap items-center gap-2 min-[900px]:ml-auto">
      <MacroMenu conversations={[c]} size="sm" iconOnlyOnPhone />
      <Menu.Root>
        <Menu.Trigger asChild>
          <IconButton label="Meer acties" size="sm">
            <Ellipsis aria-hidden />
          </IconButton>
        </Menu.Trigger>
        <Menu.Portal>
          <Menu.Content align="end" sideOffset={4} className={menuPanel}>
            <Menu.Item
              className={menuItem}
              onSelect={() => {
                markUnread(qc, c.id).then(onMarkedUnread, (e: unknown) => toast(errorMessage(e), { tone: 'error' }))
              }}
            >
              Markeren als ongelezen
            </Menu.Item>
          </Menu.Content>
        </Menu.Portal>
      </Menu.Root>
      {!done && (
        <Menu.Root>
          <Menu.Trigger asChild>
            <Button size="sm" className="max-md:size-11 max-md:px-0">
              <Clock size={16} aria-hidden />
              <span className="max-md:sr-only">Uitstellen</span>
            </Button>
          </Menu.Trigger>
          <Menu.Portal>
            <Menu.Content align="end" sideOffset={4} className={menuPanel}>
              {snoozePresets(new Date()).map((p) => (
                <Menu.Item key={p.key} className={menuItem} onSelect={() => void apply([c], { snoozedUntil: p.at.toISOString() })}>
                  {p.label}
                </Menu.Item>
              ))}
              <Menu.Item className={menuItem} onSelect={() => openPicker('snooze', [c])}>
                Kies datum en tijd
              </Menu.Item>
              {c.snoozed_until && (
                <>
                  <Menu.Separator className="my-1 h-px bg-line" />
                  <Menu.Item className={menuItem} onSelect={() => void apply([c], { snoozedUntil: null })}>
                    Uitstel opheffen
                  </Menu.Item>
                </>
              )}
            </Menu.Content>
          </Menu.Portal>
        </Menu.Root>
      )}
      <div className="flex">
        <Button variant="primary" size="sm" className="rounded-r-none" onClick={() => void apply([c], { status: primary })}>
          {done ? 'Heropenen' : 'Sluiten'}
        </Button>
        <Menu.Root>
          <Menu.Trigger asChild>
            <Button variant="primary" size="sm" className="rounded-l-none border-l border-on-ink/30 px-2" aria-label="Meer statussen">
              <ChevronDown size={16} aria-hidden />
            </Button>
          </Menu.Trigger>
          <Menu.Portal>
            <Menu.Content align="end" sideOffset={4} className={menuPanel}>
              {others.map((s) => (
                <Menu.Item key={s} className={menuItem} onSelect={() => void apply([c], { status: s })}>
                  {s === 'open' && c.status === 'waiting' ? 'Open' : otherStatusLabel[s]}
                </Menu.Item>
              ))}
            </Menu.Content>
          </Menu.Portal>
        </Menu.Root>
      </div>
    </div>
  )
}
