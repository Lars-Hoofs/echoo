import * as Device from 'expo-device'
import * as Notifications from 'expo-notifications'
import { Platform } from 'react-native'

import { api } from './api'
import type { PushDevice } from './types'

// The channel the server sends to (internal/push/fcm.go); HIGH shows a heads-up banner.
const channel = 'echoo_urgent'

// While the app is open a push still shows as a banner: the agent may be on another screen.
Notifications.setNotificationHandler({
  handleNotification: () => Promise.resolve({ shouldShowBanner: true, shouldShowList: true, shouldPlaySound: true, shouldSetBadge: false }),
})

async function ensureChannel() {
  if (Platform.OS !== 'android') return
  await Notifications.setNotificationChannelAsync(channel, {
    name: 'Gesprekken',
    description: 'Nieuwe antwoorden, toewijzingen, vermeldingen en SLA-waarschuwingen',
    importance: Notifications.AndroidImportance.HIGH,
  })
}

type PushPermission = 'granted' | 'denied' | 'undetermined'

async function pushPermission(): Promise<PushPermission> {
  const p = await Notifications.getPermissionsAsync()
  return p.granted ? 'granted' : p.canAskAgain ? 'undetermined' : 'denied'
}

async function register(): Promise<PushDevice> {
  // The raw APNs or FCM token: Echoo sends to Apple and Google itself, with no relay between.
  const token = await Notifications.getDevicePushTokenAsync()
  const label = `${Device.modelName ?? (Platform.OS === 'ios' ? 'iPhone' : 'Android')} · Echoo-app`
  return api<PushDevice>('POST', '/push/devices', { kind: Platform.OS === 'ios' ? 'apns' : 'fcm', endpoint: token.data, label })
}

// Asks for permission (the channel comes first: on Android 13 creating it triggers the prompt)
// and registers this phone. Resolves to null when the user said no.
export async function enablePush(): Promise<PushDevice | null> {
  await ensureChannel()
  let p = await Notifications.getPermissionsAsync()
  if (!p.granted && p.canAskAgain) p = await Notifications.requestPermissionsAsync()
  if (!p.granted) return null
  return register()
}

// After signing in: a phone that already allows notifications registers again, so pushes follow
// the user and session who signed in last. It never prompts.
export async function resyncPush(): Promise<void> {
  if ((await pushPermission()) !== 'granted') return
  await ensureChannel()
  await register()
}
