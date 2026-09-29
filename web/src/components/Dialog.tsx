import * as RadixDialog from '@radix-ui/react-dialog'
import { X } from 'lucide-react'
import type { ReactNode } from 'react'

import { IconButton } from './ui'

const widths = { sm: 'max-w-sm', md: 'max-w-md', lg: 'max-w-xl', xl: 'max-w-4xl' }

export function Dialog({
  open,
  onOpenChange,
  title,
  description,
  size = 'md',
  children,
}: {
  open: boolean
  onOpenChange: (open: boolean) => void
  title: string
  description?: string | undefined
  size?: keyof typeof widths
  children: ReactNode
}) {
  return (
    <RadixDialog.Root open={open} onOpenChange={onOpenChange}>
      <RadixDialog.Portal>
        <RadixDialog.Overlay className="fixed inset-0 bg-scrim backdrop-blur-sm data-[state=open]:animate-[echoo-fade-in_var(--dur-move)_var(--ease-enter)]" />
        <RadixDialog.Content
          className={`fixed top-[10vh] left-1/2 flex max-h-[80vh] w-[calc(100vw-32px)] -translate-x-1/2 flex-col overflow-hidden rounded-lg border border-line bg-float shadow-float data-[state=open]:animate-[echoo-fade-in_var(--dur-move)_var(--ease-enter)] ${widths[size]}`}
          {...(description ? {} : { 'aria-describedby': undefined })}
        >
          <div className="flex items-start justify-between gap-4 border-b border-line px-6 py-5">
            <div className="min-w-0">
              <RadixDialog.Title className="t-title text-ink">{title}</RadixDialog.Title>
              {description && <RadixDialog.Description className="t-body mt-1">{description}</RadixDialog.Description>}
            </div>
            <RadixDialog.Close asChild>
              <IconButton label="Sluiten" size="sm" className="-mt-1 -mr-2">
                <X size={16} aria-hidden />
              </IconButton>
            </RadixDialog.Close>
          </div>
          <div className="min-h-0 overflow-y-auto px-6 pt-5 pb-6">{children}</div>
        </RadixDialog.Content>
      </RadixDialog.Portal>
    </RadixDialog.Root>
  )
}

// Buttons at the bottom of a dialog body, set off by a rule. Place it last inside the
// dialog children; the negative margins cancel the body padding. It sticks to the bottom of the
// dialog, so a long form never scrolls its buttons out of view.
export function DialogFooter({ children }: { children: ReactNode }) {
  return (
    <div className="sticky -bottom-6 z-10 -mx-6 mt-2 -mb-6 flex flex-wrap items-center justify-end gap-2 border-t border-line bg-float px-6 py-4">
      {children}
    </div>
  )
}
