import * as Menu from '@radix-ui/react-dropdown-menu'
import { MoreHorizontal } from 'lucide-react'
import { Fragment, type ReactNode } from 'react'

import { IconButton } from './ui'

export interface ActionMenuItem {
  label: string
  icon?: ReactNode
  onSelect: () => void
  danger?: boolean
  // Draws a divider above this item.
  separated?: boolean
}

export function ActionMenu({ label, items }: { label: string; items: ActionMenuItem[] }) {
  return (
    <Menu.Root>
      <Menu.Trigger asChild>
        <IconButton label={label} size="sm">
          <MoreHorizontal size={16} aria-hidden />
        </IconButton>
      </Menu.Trigger>
      <Menu.Portal>
        <Menu.Content
          align="end"
          sideOffset={4}
          className="z-50 min-w-52 rounded-lg border border-line bg-float p-2 shadow-float data-[state=open]:animate-[echoo-fade-in_var(--dur-press)_var(--ease-enter)]"
        >
          {items.map((item) => (
            <Fragment key={item.label}>
              {item.separated && <Menu.Separator className="my-2 h-px bg-line" />}
              <Menu.Item
                onSelect={item.onSelect}
                className={`flex cursor-default items-center gap-3 rounded-full px-4 py-2 text-base outline-none data-highlighted:bg-selected max-md:py-3 ${item.danger ? 'text-danger-text' : 'text-ink'}`}
              >
                {item.icon && (
                  <span aria-hidden className={`inline-flex shrink-0 ${item.danger ? '' : 'text-muted'}`}>
                    {item.icon}
                  </span>
                )}
                {item.label}
              </Menu.Item>
            </Fragment>
          ))}
        </Menu.Content>
      </Menu.Portal>
    </Menu.Root>
  )
}
