import * as Menu from '@radix-ui/react-dropdown-menu'
import { ChevronDown, X } from 'lucide-react'

import { Button, IconButton } from '../../components/ui'
import { type ConversationListItem, type ConversationStatus, statusLabel } from '../../lib/inbox'
import { menuItem, menuPanel } from '../shell/menu'
import { usePickers } from './ActionPickers'
import { MacroMenu } from './MacroMenu'
import { useConversationActions } from './useConversationActions'

const menuStatuses: { status: ConversationStatus; label: string }[] = [
  { status: 'waiting', label: statusLabel.waiting },
  { status: 'spam', label: statusLabel.spam },
  { status: 'open', label: 'Heropenen' },
]

// Replaces the list header while conversations are selected.
export function BulkBar({
  selected,
  total,
  onClear,
  onSelectAll,
}: {
  selected: ConversationListItem[]
  total: number
  onClear: () => void
  onSelectAll: () => void
}) {
  const apply = useConversationActions()
  const { openPicker } = usePickers()
  const run = (status: ConversationStatus) => {
    void apply(selected, { status })
    onClear()
  }
  return (
    <div role="toolbar" aria-label="Acties voor selectie" className="flex shrink-0 flex-col gap-3 px-4 pt-6 pb-4">
      <div className="flex items-center gap-3">
        <IconButton label="Selectie wissen" size="sm" onClick={onClear}>
          <X aria-hidden />
        </IconButton>
        <span role="status" className="t-title min-w-0 flex-1 truncate">
          {selected.length === 1 ? '1 geselecteerd' : `${selected.length} geselecteerd`}
        </span>
        {selected.length < total && (
          <Button size="sm" variant="ghost" onClick={onSelectAll}>
            Alles selecteren
          </Button>
        )}
      </div>
      <div className="flex flex-wrap items-center gap-2">
        <Button size="sm" variant="primary" onClick={() => run('closed')}>
          Sluiten
        </Button>
        <Menu.Root>
          <Menu.Trigger asChild>
            <Button size="sm">
              Status
              <ChevronDown size={16} aria-hidden />
            </Button>
          </Menu.Trigger>
          <Menu.Portal>
            <Menu.Content align="start" sideOffset={4} className={menuPanel}>
              {menuStatuses.map((s) => (
                <Menu.Item key={s.status} className={menuItem} onSelect={() => run(s.status)}>
                  {s.label}
                </Menu.Item>
              ))}
            </Menu.Content>
          </Menu.Portal>
        </Menu.Root>
        <Button size="sm" onClick={() => openPicker('assign', selected)}>
          Toewijzen
        </Button>
        <Button size="sm" onClick={() => openPicker('label', selected)}>
          Labels
        </Button>
        <Button size="sm" onClick={() => openPicker('snooze', selected)}>
          Uitstellen
        </Button>
        <MacroMenu conversations={selected} size="sm" onDone={onClear} />
      </div>
    </div>
  )
}
