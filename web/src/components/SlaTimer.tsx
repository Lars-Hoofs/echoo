import { Clock, TriangleAlert } from 'lucide-react'

import type { ConversationListItem } from '../lib/inbox'
import { slaTimer, useNow } from '../lib/sla'

// The SLA deadline of a conversation. Text and glyph carry the meaning: neutral with a clock while
// the deadline is close, alert once it has passed. Nothing is shown while the conversation is
// comfortably inside its deadlines, unless compact is false (the header).
export function SlaTimer({ item, compact = true }: { item: Pick<ConversationListItem, 'sla' | 'status'>; compact?: boolean }) {
  const timer = slaTimer(item.sla, item.status, useNow())
  if (!timer) return null
  const urgent = timer.breached || timer.atRisk
  if (compact && !urgent) return null
  const tone = timer.breached ? 'tag-alert' : timer.atRisk ? 'text-ink' : ''
  return (
    <span className={`tag shrink-0 gap-1 tabular-nums ${tone}`}>
      {timer.breached ? <TriangleAlert size={16} aria-hidden /> : <Clock size={16} aria-hidden />}
      <span className="sr-only">{timer.long}</span>
      <span aria-hidden>{compact ? timer.short : timer.long}</span>
    </span>
  )
}
