// The app route a push opens. The server sends web paths (/inbox/alle/<id>), because the same
// notification also goes to browsers.
export function routeForPush(data: Record<string, unknown> | undefined): '/ik' | `/gesprek/${string}` | null {
  const url = data?.url
  if (typeof url !== 'string') return null
  const conversation = /^\/inbox\/[a-z-]+\/([0-9a-f-]{36})$/i.exec(url)
  if (conversation?.[1]) return `/gesprek/${conversation[1]}`
  if (url.startsWith('/instellingen/meldingen')) return '/ik'
  return null
}

interface PushRequest {
  content: { data?: Record<string, unknown> | null }
  trigger: unknown
}

// Where a push keeps the custom fields the server sends: expo-notifications puts the full APNs
// payload in trigger.payload (content.data only holds a "body" dictionary there) and the FCM data
// in trigger.remoteMessage.data.
export function pushData(request: PushRequest): Record<string, unknown> | undefined {
  const t = request.trigger as { payload?: Record<string, unknown>; remoteMessage?: { data?: Record<string, unknown> } } | null
  return t?.payload ?? t?.remoteMessage?.data ?? request.content.data ?? undefined
}
