import { useEffect, useRef, useSyncExternalStore } from 'react'

import { api } from './api'

export interface Viewer {
  id: string
  name: string
  typing: boolean
}

interface Entry {
  name: string
  viewingEnd: number
  typingEnd: number
}

// Mirrors the server's TTLs: a heartbeat every 15 s keeps a viewer alive for 30 s, typing lapses after 8 s.
const VIEWING_TTL_MS = 30_000
const TYPING_TTL_MS = 8_000
const HEARTBEAT_MS = 15_000
const TYPING_REPORT_MS = 4_000

const entries = new Map<string, Map<string, Entry>>()
const snapshots = new Map<string, readonly Viewer[]>()
const listeners = new Set<() => void>()
const NONE: readonly Viewer[] = []
let now = () => Date.now()
let expiryTimer: ReturnType<typeof setTimeout> | undefined

export function setPresenceClock(clock: () => number) {
  now = clock
}

export function resetPresence() {
  entries.clear()
  snapshots.clear()
  clearTimeout(expiryTimer)
  expiryTimer = undefined
  now = () => Date.now()
}

function rebuild() {
  const t = now()
  let nextExpiry = Infinity
  for (const [conversationId, users] of entries) {
    const viewers: Viewer[] = []
    for (const [id, e] of users) {
      if (e.viewingEnd <= t) {
        users.delete(id)
        continue
      }
      viewers.push({ id, name: e.name, typing: e.typingEnd > t })
      nextExpiry = Math.min(nextExpiry, e.viewingEnd, e.typingEnd > t ? e.typingEnd : Infinity)
    }
    if (users.size === 0) {
      entries.delete(conversationId)
      snapshots.delete(conversationId)
      continue
    }
    viewers.sort((a, b) => a.name.localeCompare(b.name) || a.id.localeCompare(b.id))
    const prev = snapshots.get(conversationId)
    const same =
      prev?.length === viewers.length &&
      prev.every((p, i) => p.id === viewers[i]?.id && p.typing === viewers[i].typing && p.name === viewers[i].name)
    if (!same) snapshots.set(conversationId, viewers)
  }
  clearTimeout(expiryTimer)
  expiryTimer = Number.isFinite(nextExpiry) ? setTimeout(publish, Math.max(nextExpiry - t, 0) + 5) : undefined
}

function publish() {
  rebuild()
  listeners.forEach((l) => {
    l()
  })
}

export function applyPresence(ev: { conversation_id?: string; user_id?: string; state?: string; name?: string }) {
  if (!ev.conversation_id || !ev.user_id) return
  const users = entries.get(ev.conversation_id) ?? new Map<string, Entry>()
  if (ev.state === 'left') {
    users.delete(ev.user_id)
  } else if (ev.state === 'viewing' || ev.state === 'typing') {
    const t = now()
    const prev = users.get(ev.user_id)
    users.set(ev.user_id, {
      name: ev.name ?? prev?.name ?? '',
      viewingEnd: t + VIEWING_TTL_MS,
      typingEnd: ev.state === 'typing' ? t + TYPING_TTL_MS : (prev?.typingEnd ?? 0),
    })
  } else {
    return
  }
  entries.set(ev.conversation_id, users)
  publish()
}

interface ViewersResponse {
  viewers: { user_id: string; name: string; typing: boolean }[]
}

export function seedPresence(conversationId: string, viewers: ViewersResponse['viewers']) {
  const t = now()
  const users = new Map<string, Entry>()
  for (const v of viewers) {
    users.set(v.user_id, { name: v.name, viewingEnd: t + VIEWING_TTL_MS, typingEnd: v.typing ? t + TYPING_TTL_MS : 0 })
  }
  entries.set(conversationId, users)
  publish()
}

function subscribe(l: () => void) {
  listeners.add(l)
  return () => listeners.delete(l)
}

export function getViewers(conversationId: string): readonly Viewer[] {
  return snapshots.get(conversationId) ?? NONE
}

const presencePath = (conversationId: string) => `/conversations/${encodeURIComponent(conversationId)}/presence`

// Presence is advisory: a failed beat is retried by the next one, and surfacing it would only
// bother the agent while they read.
function report(conversationId: string, state: 'viewing' | 'typing' | 'left') {
  api('POST', presencePath(conversationId), { state }).catch(() => undefined)
}

// Announces that the current user has the conversation open and returns the other viewers.
export function useConversationPresence(conversationId: string, selfId: string | undefined): readonly Viewer[] {
  useEffect(() => {
    let cancelled = false
    // Best effort like the beats: live events fill the gap if this read fails.
    api<ViewersResponse>('GET', presencePath(conversationId))
      .then((r) => {
        if (!cancelled) seedPresence(conversationId, r.viewers)
      })
      .catch(() => undefined)

    const beat = () => {
      if (!document.hidden) report(conversationId, 'viewing')
    }
    beat()
    const timer = setInterval(beat, HEARTBEAT_MS)
    document.addEventListener('visibilitychange', beat)
    return () => {
      cancelled = true
      clearInterval(timer)
      document.removeEventListener('visibilitychange', beat)
      report(conversationId, 'left')
    }
  }, [conversationId])

  const viewers = useSyncExternalStore(subscribe, () => getViewers(conversationId))
  return viewers.filter((v) => v.id !== selfId)
}

// Returns a function to call on every editor change; it reports at most once per few seconds.
export function useReportTyping(conversationId: string): () => void {
  const last = useRef(0)
  return () => {
    const t = Date.now()
    if (t - last.current < TYPING_REPORT_MS) return
    last.current = t
    report(conversationId, 'typing')
  }
}

export function viewersLabel(viewers: readonly Viewer[]): string {
  const names = viewers.map((v) => v.name || 'Een collega')
  if (names.length <= 2) return names.join(' en ')
  return `${names[0]}, ${names[1]} en ${names.length - 2} ${names.length === 3 ? 'andere' : 'anderen'}`
}

export function typingLabel(viewers: readonly Viewer[]): string {
  const typers = viewers.filter((v) => v.typing).map((v) => v.name || 'Een collega')
  if (typers.length === 0) return ''
  if (typers.length === 1) return `${typers[0]} typt een antwoord.`
  if (typers.length === 2) return `${typers[0]} en ${typers[1]} typen een antwoord.`
  return 'Meerdere collega’s typen een antwoord.'
}
