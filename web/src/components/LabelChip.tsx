import { X } from 'lucide-react'

import type { LabelColor, LabelRef } from '../lib/inbox'

// Labels are neutral. Each colour token maps to a grey of its own lightness (see --lb-* in
// styles.css); the name next to the swatch is what identifies the label. Full class names, so
// Tailwind sees every one of them.
const dot: Record<LabelColor, string> = {
  slate: 'bg-lb-slate',
  blue: 'bg-lb-blue',
  teal: 'bg-lb-teal',
  green: 'bg-lb-green',
  amber: 'bg-lb-amber',
  orange: 'bg-lb-orange',
  red: 'bg-lb-red',
  violet: 'bg-lb-violet',
}

export function LabelDot({ color, className = '' }: { color: LabelColor; className?: string }) {
  return <i aria-hidden className={`inline-block size-2 shrink-0 rounded-full border border-line-strong ${dot[color]} ${className}`} />
}

export function LabelChip({ label, onRemove }: { label: Pick<LabelRef, 'name' | 'color_token'>; onRemove?: () => void }) {
  return (
    <span className="tag max-w-full gap-2">
      <LabelDot color={label.color_token} />
      <span className="truncate">{label.name}</span>
      {onRemove && (
        <button
          type="button"
          onClick={onRemove}
          aria-label={`Label ${label.name} verwijderen`}
          className="-mr-3 inline-flex size-6 shrink-0 items-center justify-center rounded-full hover:bg-line-strong"
        >
          <X size={16} aria-hidden />
        </button>
      )}
    </span>
  )
}
