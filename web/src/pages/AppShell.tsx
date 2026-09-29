import * as Dialog from '@radix-ui/react-dialog'
import { Outlet, useLocation } from '@tanstack/react-router'
import { Menu, X } from 'lucide-react'
import { useState } from 'react'

import { Logo } from '../components/Logo'
import { ToastProvider } from '../components/Toast'
import { IconButton } from '../components/ui'
import { useRealtime } from '../lib/realtime'
import { PickerProvider } from './inbox/ActionPickers'
import { CommandProvider } from './shell/CommandBar'
import { SidebarBody } from './shell/Sidebar'

const floating = 'mx-2 mb-2 rounded-lg min-[1000px]:m-2 min-[1000px]:ml-0'
const flat = 'max-[899px]:border-0 min-[900px]:mx-2 min-[900px]:mb-2 min-[900px]:rounded-lg min-[1000px]:m-2 min-[1000px]:ml-0'

// Below 1000px the sidebar becomes a sheet behind a top bar. The pages inside switch layout at
// 900px, so on a conversation the top bar stays visible in between (899px and below it is the
// conversation's own header with a back button).
export function AppShell() {
  useRealtime()
  const [sheetOpen, setSheetOpen] = useState(false)
  const pathname = useLocation({ select: (l) => l.pathname })
  // On phones an open conversation is a full-screen page with its own back button.
  const conversationOpen = /^\/inbox\/[^/]+\/[^/]+/.test(pathname)

  return (
    <ToastProvider>
      <PickerProvider>
        <CommandProvider>
          <div className="flex h-full bg-app">
            <aside className="hidden w-68 shrink-0 min-[1000px]:block">
              <SidebarBody onNavigate={() => undefined} />
            </aside>

            <div className="flex min-w-0 flex-1 flex-col">
              <header
                className={`flex h-14 shrink-0 items-center gap-2 px-3 min-[1000px]:hidden ${conversationOpen ? 'max-[899px]:hidden' : ''}`}
              >
                <IconButton label="Menu openen" size="sm" onClick={() => setSheetOpen(true)}>
                  <Menu size={16} aria-hidden />
                </IconButton>
                <Logo size="sm" />
              </header>
              <main className={`relative min-h-0 flex-1 overflow-hidden border border-line bg-surface ${conversationOpen ? flat : floating}`}>
                <Outlet />
              </main>
            </div>

            <Dialog.Root open={sheetOpen} onOpenChange={setSheetOpen}>
              <Dialog.Portal>
                <Dialog.Overlay className="fixed inset-0 z-40 bg-scrim backdrop-blur-sm data-[state=open]:animate-[echoo-fade-in_var(--dur-move)_var(--ease-enter)] min-[1000px]:hidden" />
                <Dialog.Content
                  aria-describedby={undefined}
                  className="fixed inset-y-0 left-0 z-50 w-[320px] max-w-[88vw] border-r border-line bg-app min-[1000px]:hidden"
                >
                  <Dialog.Title className="sr-only">Navigatie</Dialog.Title>
                  <Dialog.Close asChild>
                    <IconButton label="Menu sluiten" size="sm" className="absolute top-6 right-4">
                      <X size={16} aria-hidden />
                    </IconButton>
                  </Dialog.Close>
                  <SidebarBody inSheet onNavigate={() => setSheetOpen(false)} />
                </Dialog.Content>
              </Dialog.Portal>
            </Dialog.Root>
          </div>
        </CommandProvider>
      </PickerProvider>
    </ToastProvider>
  )
}
