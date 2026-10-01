import { api } from './api'

// Web Push on this browser or installed web app, through the service worker. The native iOS
// and Android apps (mobile/) register their APNs or FCM token themselves.

export interface PushConfig {
  web_push_public_key: string | null
  apns: boolean
  fcm: boolean
}

export interface PushDevice {
  id: string
  kind: 'webpush' | 'apns' | 'fcm'
  label: string
  created_at: string
  last_push_at: string | null
  current: boolean
}

export interface PushPrefs {
  mentions: boolean
  assignments: boolean
  replies: boolean
  sla: boolean
}

// How this browser can receive pushes: 'install' means iOS Safari, which only allows Web Push
// once the app has been added to the home screen.
export type PushSupport = 'webpush' | 'install' | 'unsupported'

export function isStandalone(): boolean {
  return window.matchMedia('(display-mode: standalone)').matches || (navigator as { standalone?: boolean }).standalone === true
}

export function isIOS(ua = navigator.userAgent, touchPoints = navigator.maxTouchPoints): boolean {
  // iPadOS reports a desktop Mac user agent; touch support gives it away.
  return /iPhone|iPad|iPod/.test(ua) || (/Macintosh/.test(ua) && touchPoints > 1)
}

export function pushSupport(): PushSupport {
  const web = 'serviceWorker' in navigator && 'PushManager' in window && 'Notification' in window
  if (web) return 'webpush'
  if (isIOS() && !isStandalone()) return 'install'
  return 'unsupported'
}

// A short name for the device list: "iPhone · Safari", "Android · Chrome".
export function deviceLabel(ua = navigator.userAgent): string {
  const os = /iPhone/.test(ua)
    ? 'iPhone'
    : /iPad/.test(ua)
      ? 'iPad'
      : /Android/.test(ua)
        ? 'Android'
        : /Windows/.test(ua)
          ? 'Windows'
          : /Macintosh/.test(ua)
            ? 'Mac'
            : /Linux/.test(ua)
              ? 'Linux'
              : 'Onbekend apparaat'
  const browser = /Edg\//.test(ua)
    ? 'Edge'
    : /Firefox\//.test(ua)
      ? 'Firefox'
      : /SamsungBrowser\//.test(ua)
        ? 'Samsung Internet'
        : /Chrome\//.test(ua)
          ? 'Chrome'
          : /Safari\//.test(ua)
            ? 'Safari'
            : 'browser'
  return `${os} · ${browser}${isStandalone() ? ' (app)' : ''}`
}

export function urlBase64ToBytes(value: string): Uint8Array<ArrayBuffer> {
  const padded = value.replace(/-/g, '+').replace(/_/g, '/') + '='.repeat((4 - (value.length % 4)) % 4)
  const raw = atob(padded)
  const out = new Uint8Array(new ArrayBuffer(raw.length))
  for (let i = 0; i < raw.length; i++) out[i] = raw.charCodeAt(i)
  return out
}

function bytesToUrlBase64(buf: ArrayBuffer | null): string {
  if (!buf) return ''
  let s = ''
  for (const b of new Uint8Array(buf)) s += String.fromCharCode(b)
  return btoa(s).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '')
}

export function registerServiceWorker(): void {
  if (!('serviceWorker' in navigator)) return
  navigator.serviceWorker.register('/sw.js', { scope: '/' }).catch((err: unknown) => {
    console.warn('service worker registration failed', err)
  })
}

async function sendSubscription(sub: PushSubscription): Promise<PushDevice> {
  return api<PushDevice>('POST', '/push/devices', {
    kind: 'webpush',
    endpoint: sub.endpoint,
    keys: { p256dh: bytesToUrlBase64(sub.getKey('p256dh')), auth: bytesToUrlBase64(sub.getKey('auth')) },
    label: deviceLabel(),
  })
}

export class PushDeniedError extends Error {
  constructor() {
    super('notifications are blocked for this site')
  }
}

// Asks for permission and registers this device. Must run from a user gesture: iOS refuses the
// permission prompt otherwise.
export async function enablePush(config: PushConfig): Promise<PushDevice> {
  if (!config.web_push_public_key) throw new Error('web push is not configured on the server')
  const permission = await Notification.requestPermission()
  if (permission !== 'granted') throw new PushDeniedError()
  const reg = await navigator.serviceWorker.ready
  const existing = await reg.pushManager.getSubscription()
  const key = urlBase64ToBytes(config.web_push_public_key)
  // A subscription made with another key (the server's VAPID key changed) cannot be reused.
  if (existing && bytesToUrlBase64(existing.options.applicationServerKey) !== config.web_push_public_key) {
    await existing.unsubscribe()
  }
  const sub = (await reg.pushManager.getSubscription()) ?? (await reg.pushManager.subscribe({ userVisibleOnly: true, applicationServerKey: key }))
  return sendSubscription(sub)
}

export async function disablePush(device: PushDevice | undefined): Promise<void> {
  if ('serviceWorker' in navigator) {
    const reg = await navigator.serviceWorker.getRegistration('/')
    const sub = await reg?.pushManager.getSubscription()
    await sub?.unsubscribe()
  }
  if (device) await api('DELETE', `/push/devices/${encodeURIComponent(device.id)}`)
}

// Keeps an existing registration tied to the signed-in user and session, for example after
// signing in again on a phone that already allowed notifications. It never prompts.
export async function resyncPush(): Promise<void> {
  if (pushSupport() !== 'webpush' || Notification.permission !== 'granted') return
  const reg = await navigator.serviceWorker.getRegistration('/')
  const sub = await reg?.pushManager.getSubscription()
  if (sub) await sendSubscription(sub)
}

// Opens the conversation a notification is about: the service worker posts its URL to the open
// app.
export function onNotificationOpen(navigate: (path: string) => void): () => void {
  const toPath = (url: unknown): string | null => {
    if (typeof url !== 'string') return null
    const u = new URL(url, window.location.origin)
    return u.origin === window.location.origin ? u.pathname + u.search : null
  }
  const cleanups: (() => void)[] = []
  if ('serviceWorker' in navigator) {
    const onMessage = (e: MessageEvent) => {
      const data = e.data as { type?: string; url?: unknown } | null
      if (data?.type !== 'echoo:navigate') return
      const path = toPath(data.url)
      if (path) navigate(path)
    }
    navigator.serviceWorker.addEventListener('message', onMessage)
    cleanups.push(() => navigator.serviceWorker.removeEventListener('message', onMessage))
  }
  return () => cleanups.forEach((c) => c())
}
