import type { CSSProperties } from 'react'

import { Badge } from '../../components/ui'
import { type Campaign, type CampaignCounts, type CampaignStatus, progress, statusLabel } from '../../lib/campaigns'

export function CampaignStatusBadge({ status }: { status: CampaignStatus }) {
  // Campaign status is not "the customer waits on us", so every state is a neutral tag.
  return <Badge dot>{statusLabel[status]}</Badge>
}

// The text next to the bar carries the figures; the bar is only the shape of it.
export function ProgressBar({ counts, label }: { counts: CampaignCounts; label: string }) {
  const p = progress(counts)
  return (
    <div
      role="progressbar"
      aria-label={label}
      aria-valuemin={0}
      aria-valuemax={100}
      aria-valuenow={p.percent}
      aria-valuetext={`${p.done} van ${p.total}`}
      className="progress progress-ink"
    >
      <i style={{ '--w': `${p.percent}%` } as CSSProperties} />
    </div>
  )
}

export function progressText(c: Pick<Campaign, 'counts' | 'status'>): string {
  if (c.status === 'draft' || c.status === 'scheduled') return '—'
  const p = progress(c.counts)
  return `${p.done} van ${p.total}`
}
