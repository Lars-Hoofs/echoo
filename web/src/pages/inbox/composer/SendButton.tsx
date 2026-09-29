import * as Menu from '@radix-ui/react-dropdown-menu'
import { ChevronDown } from 'lucide-react'

import { Button } from '../../../components/ui'
import type { SendPayload } from '../../../lib/composer'
import { menuItem, menuPanel } from '../../shell/menu'

export type StatusAfter = SendPayload['status_after']

// Split button: the main part sends, the menu sends and sets the status of the conversation.
export function SendButton({ onSend, disabled, busy }: { onSend: (status: StatusAfter) => void; disabled: boolean; busy: boolean }) {
  return (
    <div className="inline-flex">
      <Button
        variant="accent"
        size="sm"
        disabled={disabled}
        busy={busy}
        onClick={() => onSend(null)}
        className="rounded-r-none"
      >
        Versturen
      </Button>
      <Menu.Root>
        <Menu.Trigger asChild>
          <Button variant="accent" size="sm" disabled={disabled || busy} aria-label="Meer opties voor versturen" className="rounded-l-none border-l border-on-accent/30 px-2">
            <ChevronDown size={16} aria-hidden />
          </Button>
        </Menu.Trigger>
        <Menu.Portal>
          <Menu.Content side="top" align="end" sideOffset={6} className={`${menuPanel} min-w-56`}>
            <Menu.Item className={menuItem} onSelect={() => onSend('waiting')}>
              Versturen en op wachtend zetten
            </Menu.Item>
            <Menu.Item className={menuItem} onSelect={() => onSend('closed')}>
              Versturen en sluiten
            </Menu.Item>
          </Menu.Content>
        </Menu.Portal>
      </Menu.Root>
    </div>
  )
}
