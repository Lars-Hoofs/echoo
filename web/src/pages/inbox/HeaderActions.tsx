import * as Menu from '@radix-ui/react-dropdown-menu'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { ChevronDown, Clock, Ellipsis } from 'lucide-react'

import { useToast } from '../../components/Toast'
import { Button, IconButton } from '../../components/ui'
import { snoozePresets } from '../../lib/actions'
import { blockSender } from '../../lib/blocklist'
import { errorMessage } from '../../lib/errors'
import { type ConversationListItem, type ConversationStatus, statusLabel } from '../../lib/inbox'
import { hasPermission, meQuery } from '../../lib/session'
import { markUnread } from '../../lib/unread'
import { menuItem, menuPanel } from '../shell/menu'
import { usePickers } from './ActionPickers'
import { MacroMenu } from './MacroMenu'
import { useConversationActions } from './useConversationActions'
import { useTrashActions } from './useTrashActions'

const otherStatusLabel: Record<ConversationStatus, string> = {
  open: 'Heropenen',
  waiting: statusLabel.waiting,
  closed: statusLabel.closed,
  spam: statusLabel.spam,
}

// onLeave runs after the conversation was marked unread or moved to the trash; the pane leaves
// the conversation then, because an open conversation is marked read again by itself and a
// deleted one cannot be shown.
export function HeaderActions({ conversation: c, onLeave }: { conversation: ConversationListItem; onLeave: () => void }) {
  const apply = useConversationActions()
  const trash = useTrashActions()
  const qc = useQueryClient()
  const toast = useToast()
  const me = useQuery(meQuery)
  const { openPicker } = usePickers()
  const canDelete = hasPermission(me.data, 'conversations.delete')
  const senderEmail = c.contact?.email ?? ''

  // Blocking first: when it fails the conversation keeps its status and the error says why.
  const spamAndBlock = async () => {
    try {
      await blockSender(c.mailbox.id, senderEmail)
    } catch (e) {
      toast(errorMessage(e), { tone: 'error' })
      return
    }
    void qc.invalidateQueries({ queryKey: ['blocked-senders'] })
    await apply([c], { status: 'spam' })
    toast(`Nieuwe mail van ${senderEmail} gaat voortaan naar spam`)
  }
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
                markUnread(qc, c.id).then(onLeave, (e: unknown) => toast(errorMessage(e), { tone: 'error' }))
              }}
            >
              Markeren als ongelezen
            </Menu.Item>
            {canDelete && (
              <>
                <Menu.Separator className="my-1 h-px bg-line" />
                <Menu.Item
                  className={menuItem}
                  onSelect={() => {
                    void trash('trash', [c.id]).then((ok) => ok.length > 0 && onLeave())
                  }}
                >
                  Verwijderen
                </Menu.Item>
              </>
            )}
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
              {canDelete && senderEmail && (
                <Menu.Item className={menuItem} onSelect={() => void spamAndBlock()}>
                  {c.status === 'spam' ? 'Afzender blokkeren' : 'Spam en afzender blokkeren'}
                </Menu.Item>
              )}
            </Menu.Content>
          </Menu.Portal>
        </Menu.Root>
      </div>
    </div>
  )
}
