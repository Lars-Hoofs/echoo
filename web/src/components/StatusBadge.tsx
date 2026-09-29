import { Ban, CircleCheck, Clock } from 'lucide-react'

import { type ConversationStatus, type ListStatus, statusLabel } from '../lib/inbox'

const tone: Record<ConversationStatus, string> = {
  open: 'bg-st-open-bg text-st-open',
  waiting: 'bg-st-waiting-bg text-st-waiting',
  closed: 'bg-st-closed-bg text-st-closed',
  spam: 'bg-st-spam-bg text-st-spam',
}

// A different glyph per status: colour is never the only signal. Only open has the accent: it is
// the state in which the customer waits on us.
export function StatusGlyph({ status, size = 14 }: { status: ListStatus; size?: number }) {
  const common = { width: size, height: size, 'aria-hidden': true, className: 'shrink-0' } as const
  if (status === 'snoozed') return <Clock {...common} />
  if (status === 'closed') return <CircleCheck {...common} />
  if (status === 'spam') return <Ban {...common} />
  return (
    <svg {...common} viewBox="0 0 16 16" fill="none" stroke="currentColor" strokeWidth={1.5}>
      <circle cx="8" cy="8" r="6.25" />
      {status === 'waiting' && <path d="M8 3.5a4.5 4.5 0 0 1 0 9z" fill="currentColor" stroke="none" />}
    </svg>
  )
}

export function StatusBadge({ status, className = '' }: { status: ConversationStatus; className?: string }) {
  return (
    <span className={`tag gap-1 ${tone[status]} ${className}`}>
      <StatusGlyph status={status} size={12} />
      {statusLabel[status]}
    </span>
  )
}
