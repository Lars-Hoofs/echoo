import type { QueryClient, QueryKey } from '@tanstack/react-query'
import { useQueryClient } from '@tanstack/react-query'
import { useEffect, useSyncExternalStore } from 'react'

import { applyPresence } from './presence'

export interface RealtimeEvent {
  type: string
  conversation_id?: string
  mailbox_id?: string
  user_id?: string
  state?: string
  name?: string
}

const DEBOUNCE_MS = 300
const HIDDEN_PAUSE_MS = 5 * 60_000
const BACKOFF_BASE_MS = 1000
const BACKOFF_MAX_MS = 30_000
const EVENTS_URL = '/api/v1/events'
const CLOSED = 2

// Events carry ids only, so each one maps to the queries that have to be refetched.
export function invalidationKeys(ev: RealtimeEvent): QueryKey[] {
  switch (ev.type) {
    case 'conversation.updated':
    case 'message.created':
    case 'message.updated': {
      const keys: QueryKey[] = [['inbox', 'conversations'], ['inbox', 'summary']]
      if (ev.conversation_id) keys.push(['inbox', 'conversation', ev.conversation_id])
      return keys
    }
    case 'notification':
      return [['notifications']]
    case 'resync':
      return [['inbox'], ['notifications']]
    default:
      return []
  }
}

export function parseEvent(data: string): RealtimeEvent | null {
  let v: unknown
  try {
    v = JSON.parse(data)
  } catch {
    return null
  }
  if (typeof v === 'object' && v !== null && typeof (v as { type?: unknown }).type === 'string') return v as RealtimeEvent
  return null
}

// Exponential backoff with jitter between 50% and 100% of the step, so tabs do not reconnect in lockstep.
export function backoffDelay(attempt: number, random: () => number = Math.random): number {
  const step = Math.min(BACKOFF_BASE_MS * 2 ** attempt, BACKOFF_MAX_MS)
  return Math.round(step * (0.5 + random() / 2))
}

let connected = false
const listeners = new Set<() => void>()

function setConnected(next: boolean) {
  if (connected === next) return
  connected = next
  listeners.forEach((l) => {
    l()
  })
}

export const isRealtimeConnected = () => connected

export function useRealtimeConnected(): boolean {
  return useSyncExternalStore(
    (l) => {
      listeners.add(l)
      return () => listeners.delete(l)
    },
    () => connected,
  )
}

// Polling is only the fallback while the event stream is down.
export const pollWhenDisconnected = (ms: number) => () => (connected ? false : ms)

export type SourceLike = Pick<EventSource, 'readyState' | 'onopen' | 'onmessage' | 'onerror' | 'close'>

export interface RealtimeDeps {
  queryClient: QueryClient
  createSource: (url: string) => SourceLike
  isHidden: () => boolean
  onVisibilityChange: (cb: () => void) => () => void
  onPresence?: (ev: RealtimeEvent) => void
}

export class RealtimeClient {
  private source: SourceLike | null = null
  private attempt = 0
  private reconnectTimer: ReturnType<typeof setTimeout> | undefined
  private hiddenTimer: ReturnType<typeof setTimeout> | undefined
  private flushTimer: ReturnType<typeof setTimeout> | undefined
  private pending = new Map<string, QueryKey>()
  private resyncOnOpen = false
  private paused = false
  private stopped = true
  private unwatch: (() => void) | undefined

  constructor(private readonly deps: RealtimeDeps) {}

  start() {
    this.stopped = false
    this.unwatch = this.deps.onVisibilityChange(() => {
      this.visibilityChanged()
    })
    if (this.deps.isHidden()) this.visibilityChanged()
    else this.connect()
  }

  stop() {
    this.stopped = true
    this.unwatch?.()
    clearTimeout(this.reconnectTimer)
    clearTimeout(this.hiddenTimer)
    clearTimeout(this.flushTimer)
    this.flushTimer = undefined
    this.pending.clear()
    this.disconnect()
  }

  private connect() {
    this.source = this.deps.createSource(EVENTS_URL)
    const source = this.source
    source.onopen = () => {
      this.attempt = 0
      setConnected(true)
      if (this.resyncOnOpen) {
        this.resyncOnOpen = false
        this.queue(invalidationKeys({ type: 'resync' }))
      }
    }
    source.onmessage = (e: MessageEvent<string>) => {
      const ev = parseEvent(e.data)
      if (!ev) {
        console.error('realtime: ignoring a malformed event')
        return
      }
      if (ev.type === 'presence') this.deps.onPresence?.(ev)
      else this.queue(invalidationKeys(ev))
    }
    source.onerror = () => {
      // Polling only restarts on the next fetch, so trigger one now.
      if (connected) this.queue([['inbox']])
      setConnected(false)
      // While the browser is still retrying, the server replays or asks for a resync itself.
      // A closed source will not retry (for example after a 429 or 503), so this takes over.
      if (source.readyState !== CLOSED) return
      this.disconnect()
      this.resyncOnOpen = true
      this.reconnectTimer = setTimeout(() => {
        if (!this.stopped && !this.paused) this.connect()
      }, backoffDelay(this.attempt++))
    }
  }

  private disconnect() {
    if (this.source) {
      this.source.onopen = null
      this.source.onmessage = null
      this.source.onerror = null
      this.source.close()
      this.source = null
    }
    setConnected(false)
  }

  private visibilityChanged() {
    if (this.deps.isHidden()) {
      this.hiddenTimer ??= setTimeout(() => {
        this.paused = true
        clearTimeout(this.reconnectTimer)
        this.disconnect()
      }, HIDDEN_PAUSE_MS)
      return
    }
    clearTimeout(this.hiddenTimer)
    this.hiddenTimer = undefined
    if (this.paused || this.source === null) {
      this.paused = false
      this.resyncOnOpen = true
      clearTimeout(this.reconnectTimer)
      this.connect()
    }
  }

  private queue(keys: QueryKey[]) {
    for (const key of keys) this.pending.set(JSON.stringify(key), key)
    if (this.pending.size === 0 || this.flushTimer !== undefined) return
    this.flushTimer = setTimeout(() => {
      this.flushTimer = undefined
      const keysToRefresh = [...this.pending.values()]
      this.pending.clear()
      for (const queryKey of keysToRefresh) void this.deps.queryClient.invalidateQueries({ queryKey })
    }, DEBOUNCE_MS)
  }
}

// One EventSource per tab, for as long as the authenticated layout is mounted.
export function useRealtime() {
  const queryClient = useQueryClient()
  useEffect(() => {
    const client = new RealtimeClient({
      queryClient,
      createSource: (url) => new EventSource(url, { withCredentials: true }),
      isHidden: () => document.hidden,
      onVisibilityChange: (cb) => {
        document.addEventListener('visibilitychange', cb)
        return () => {
          document.removeEventListener('visibilitychange', cb)
        }
      },
      onPresence: applyPresence,
    })
    client.start()
    return () => {
      client.stop()
    }
  }, [queryClient])
}
