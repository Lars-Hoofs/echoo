import * as Dialog from '@radix-ui/react-dialog'
import { Outlet, useLocation, useRouter } from '@tanstack/react-router'
import { X } from 'lucide-react'
import { useEffect, useState } from 'react'

import { Logo } from '../components/Logo'
import { ToastProvider } from '../components/Toast'
import { IconButton } from '../components/ui'
import { onNotificationOpen, resyncPush } from '../lib/push'
import { useRealtime } from '../lib/realtime'
import { PickerProvider } from './inbox/ActionPickers'
import { CommandProvider } from './shell/CommandBar'
import { BottomNav } from './shell/BottomNav'
import { NotificationsBell } from './shell/NotificationsBell'
import { SidebarBody } from './shell/Sidebar'

const floating = 'mx-2 mb-2 rounded-lg min-[1000px]:m-2 min-[1000px]:ml-0'
const flat = 'max-[899px]:border-0 min-[900px]:mx-2 min-[900px]:mb-2 min-[900px]:rounded-lg min-[1000px]:m-2 min-[1000px]:ml-0'

// Below 1000px the sidebar becomes a sheet behind a top bar. The pages inside switch layout at
// 900px, so on a conversation the top bar stays visible in between (899px and below it is the
// conversation's own header with a back button).
export function AppShell() {
  useRealtime()
  usePushNotifications()
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
                className={`flex shrink-0 items-center gap-2 px-3 pt-[env(safe-area-inset-top)] min-[1000px]:hidden ${conversationOpen ? 'max-[899px]:hidden' : ''}`}
              >
                <div className="flex h-14 flex-1 items-center gap-2">
                  <Logo size="sm" />
                  <span className="ml-auto">
                    <NotificationsBell />
                  </span>
                </div>
              </header>
              <main
                className={`relative min-h-0 flex-1 overflow-hidden border border-line bg-surface ${conversationOpen ? `${flat} max-[899px]:pt-[env(safe-area-inset-top)]` : floating}`}
              >
                <Outlet />
              </main>
              {/* On phones an open conversation keeps the whole screen for reading and replying. */}
              <div className={conversationOpen ? 'max-[899px]:hidden' : ''}>
                <BottomNav onMore={() => setSheetOpen(true)} />
              </div>
            </div>

            <Dialog.Root open={sheetOpen} onOpenChange={setSheetOpen}>
              <Dialog.Portal>
                <Dialog.Overlay className="fixed inset-0 z-40 bg-scrim backdrop-blur-sm data-[state=open]:animate-[echoo-fade-in_var(--dur-move)_var(--ease-enter)] min-[1000px]:hidden" />
                <Dialog.Content
                  aria-describedby={undefined}
                  className="fixed inset-y-0 left-0 z-50 w-[320px] max-w-[88vw] border-r border-line bg-app pt-[env(safe-area-inset-top)] pb-[env(safe-area-inset-bottom)] min-[1000px]:hidden"
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

// Tapping a notification opens its conversation; a device that already allows notifications is
// re-registered, so it follows the user who signed in last.
function usePushNotifications() {
  const router = useRouter()
  useEffect(() => onNotificationOpen((path) => void router.navigate({ to: path })), [router])
  useEffect(() => {
    resyncPush().catch((err: unknown) => console.warn('push registration not refreshed', err))
  }, [])
}
