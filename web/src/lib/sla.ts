import { useSyncExternalStore } from 'react'

import type { ConversationStatus } from './inbox'

export type SlaState = 'none' | 'ok' | 'at_risk' | 'breached'

export interface ConversationSla {
  state: SlaState
  first_response_due_at: string | null
  first_response_met_at: string | null
  resolution_due_at: string | null
}

export type SlaTarget = 'first_response' | 'resolution'

export interface SlaTimer {
  target: SlaTarget
  // Whole milliseconds until the deadline; negative once it has passed.
  remainingMs: number
  breached: boolean
  atRisk: boolean
  // "Nog 25 min" or "12 min te laat".
  short: string
  // "Eerste reactie over 25 min" or "Eerste reactie 12 min te laat".
  long: string
}

const MINUTE = 60_000

// Compact duration for timers: "< 1 min", "25 min", "3 u 5 min", "2 d 4 u". Never negative.
export function formatDuration(ms: number): string {
  const minutes = Math.floor(Math.abs(ms) / MINUTE)
  if (minutes < 1) return '< 1 min'
  if (minutes < 60) return `${minutes} min`
  const hours = Math.floor(minutes / 60)
  if (hours < 24) return minutes % 60 === 0 ? `${hours} u` : `${hours} u ${minutes % 60} min`
  const days = Math.floor(hours / 24)
  return hours % 24 === 0 ? `${days} d` : `${days} d ${hours % 24} u`
}

const targetName: Record<SlaTarget, string> = { first_response: 'Eerste reactie', resolution: 'Oplossing' }

// The deadline that matters now. A first response that has not been given comes first; the
// resolution deadline only runs while the conversation is open, because waiting stops its clock.
// Returns null when nothing is pending.
export function slaTimer(sla: ConversationSla | null, status: ConversationStatus, now: number): SlaTimer | null {
  if (!sla || status === 'closed' || status === 'spam') return null
  let target: SlaTarget
  let due: string
  if (sla.first_response_due_at && !sla.first_response_met_at) {
    target = 'first_response'
    due = sla.first_response_due_at
  } else if (sla.resolution_due_at && status === 'open') {
    target = 'resolution'
    due = sla.resolution_due_at
  } else {
    return null
  }
  const remainingMs = new Date(due).getTime() - now
  const breached = remainingMs < 0
  const duration = formatDuration(remainingMs)
  return {
    target,
    remainingMs,
    breached,
    atRisk: !breached && sla.state === 'at_risk',
    short: breached ? `${duration} te laat` : `Nog ${duration}`,
    long: `${targetName[target]} ${breached ? `${duration} te laat` : `over ${duration}`}`,
  }
}

const TICK_MS = 30_000
const listeners = new Set<() => void>()
let current = Date.now()
let timer: ReturnType<typeof setInterval> | undefined

function subscribe(listener: () => void) {
  listeners.add(listener)
  if (listeners.size === 1) {
    current = Date.now()
    timer = setInterval(() => {
      current = Date.now()
      listeners.forEach((l) => {
        l()
      })
    }, TICK_MS)
  }
  return () => {
    listeners.delete(listener)
    if (listeners.size === 0) clearInterval(timer)
  }
}

// The current time, updated every 30 seconds. All timers on screen share one interval.
export function useNow(): number {
  return useSyncExternalStore(subscribe, () => current)
}
