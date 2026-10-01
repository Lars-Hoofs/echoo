// Echoo's service worker. It only handles push notifications: pages and API responses are
// never cached, so nothing from a conversation is stored on the device by it.

self.addEventListener('install', () => self.skipWaiting())
self.addEventListener('activate', (event) => event.waitUntil(self.clients.claim()))

self.addEventListener('push', (event) => {
  let data
  try {
    data = event.data ? event.data.json() : {}
  } catch {
    data = {}
  }
  const title = data.title || 'Echoo'
  event.waitUntil(
    self.registration.showNotification(title, {
      body: data.body || '',
      tag: data.tag || undefined,
      // A newer push about the same conversation replaces the older one but still alerts.
      renotify: Boolean(data.tag),
      icon: '/icons/icon-192.png',
      badge: '/icons/badge-96.png',
      data: { url: typeof data.url === 'string' && data.url.startsWith('/') ? data.url : '/inbox/alle' },
    }),
  )
})

self.addEventListener('notificationclick', (event) => {
  event.notification.close()
  const url = new URL(event.notification.data?.url || '/inbox/alle', self.location.origin).href
  event.waitUntil(
    (async () => {
      const windows = await self.clients.matchAll({ type: 'window', includeUncontrolled: true })
      const open = windows.find((c) => new URL(c.url).origin === self.location.origin)
      if (open) {
        await open.focus()
        // The app routes without a reload; navigate() is the fallback for an older page.
        open.postMessage({ type: 'echoo:navigate', url })
        return
      }
      await self.clients.openWindow(url)
    })(),
  )
})
