import { focusManager, QueryClient, QueryClientProvider, useQueryClient } from '@tanstack/react-query'
import * as Notifications from 'expo-notifications'
import { router, Stack } from 'expo-router'
import * as SplashScreen from 'expo-splash-screen'
import { StatusBar } from 'expo-status-bar'
import { useCallback, useEffect, useRef } from 'react'
import { AppState } from 'react-native'

import { ApiError } from '../lib/api'
import { resyncPush } from '../lib/push'
import { pushData, routeForPush } from '../lib/routes'
import { SessionProvider, useSession } from '../lib/session'
import { useColors } from '../ui/theme'
import { ToastProvider } from '../ui/toast'

void SplashScreen.preventAutoHideAsync()

// Queries refetch when the app comes back to the foreground, like a browser tab regaining focus.
focusManager.setEventListener((setFocused) => {
  const sub = AppState.addEventListener('change', (s) => setFocused(s === 'active'))
  return () => sub.remove()
})

const queryClient = new QueryClient({
  defaultOptions: {
    // Client errors (not found, forbidden) do not get better by asking again.
    queries: { retry: (count, err) => !(err instanceof ApiError && err.status >= 400 && err.status < 500) && count < 2 },
  },
})

export default function RootLayout() {
  return (
    <QueryClientProvider client={queryClient}>
      <SessionProvider>
        <ToastProvider>
          <Navigator />
        </ToastProvider>
      </SessionProvider>
    </QueryClientProvider>
  )
}

function Navigator() {
  const { state } = useSession()
  const c = useColors()
  const signedIn = state.status === 'signed-in'

  useEffect(() => {
    if (state.status !== 'loading') void SplashScreen.hideAsync()
  }, [state.status])

  useEffect(() => {
    if (signedIn) resyncPush().catch((err: unknown) => console.warn('push registration not refreshed', err))
  }, [signedIn])

  usePushNotifications(signedIn)

  if (state.status === 'loading') return null
  return (
    <>
      <StatusBar style="auto" />
      <Stack screenOptions={{ headerShown: false, contentStyle: { backgroundColor: c.bg } }}>
        <Stack.Protected guard={signedIn}>
          <Stack.Screen name="(tabs)" />
          <Stack.Screen name="gesprek/[id]" />
        </Stack.Protected>
        <Stack.Protected guard={state.status === 'no-server'}>
          <Stack.Screen name="verbinden" />
        </Stack.Protected>
        <Stack.Protected guard={state.status === 'signed-out'}>
          <Stack.Screen name="inloggen" />
        </Stack.Protected>
        <Stack.Protected guard={state.status === 'unfinished'}>
          <Stack.Screen name="afronden" />
        </Stack.Protected>
      </Stack>
    </>
  )
}

// A push that arrives refreshes the lists; tapping one opens its conversation, also when the
// tap started the app or the user had to sign in first.
function usePushNotifications(signedIn: boolean) {
  const qc = useQueryClient()
  const launch = Notifications.useLastNotificationResponse()
  const handled = useRef(new Set<string>())
  const pending = useRef<ReturnType<typeof routeForPush>>(null)

  const open = useCallback(
    (response: Notifications.NotificationResponse) => {
      const id = response.notification.request.identifier
      if (handled.current.has(id)) return
      handled.current.add(id)
      const route = routeForPush(pushData(response.notification.request))
      if (!route) return
      if (signedIn) router.push(route)
      else pending.current = route
    },
    [signedIn],
  )

  useEffect(() => {
    const received = Notifications.addNotificationReceivedListener(() => {
      void qc.invalidateQueries({ queryKey: ['inbox'] })
      void qc.invalidateQueries({ queryKey: ['notifications'] })
    })
    const tapped = Notifications.addNotificationResponseReceivedListener(open)
    return () => {
      received.remove()
      tapped.remove()
    }
  }, [qc, open])

  // The tap that launched the app arrives before any listener exists.
  useEffect(() => {
    if (launch) open(launch)
  }, [launch, open])

  useEffect(() => {
    if (signedIn && pending.current) {
      router.push(pending.current)
      pending.current = null
    }
  }, [signedIn])
}
