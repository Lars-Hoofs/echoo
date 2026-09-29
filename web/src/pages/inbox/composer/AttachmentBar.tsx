import { Paperclip, TriangleAlert, X } from 'lucide-react'

import { formatBytes } from '../../../lib/format'
import type { AttachmentItem } from './useAttachments'

export function AttachmentBar({ items, onRemove }: { items: AttachmentItem[]; onRemove: (key: string) => void }) {
  if (items.length === 0) return null
  return (
    <ul aria-label="Bijlagen" className="mt-3 flex flex-wrap gap-2">
      {items.map((i) => (
        <li
          key={i.key}
          className={`relative flex h-8 max-w-full items-center gap-2 overflow-hidden rounded-full border bg-surface px-3 text-base ${i.error ? 'border-danger-text/50' : 'border-line-strong'}`}
        >
          {i.error ? <TriangleAlert size={16} aria-hidden className="shrink-0 text-danger-text" /> : <Paperclip size={16} aria-hidden className="shrink-0 text-muted" />}
          <span className="min-w-0 truncate text-ink">{i.name}</span>
          {i.error ? (
            <span className="shrink-0 text-danger-text">{i.error}</span>
          ) : i.upload ? (
            <span className="shrink-0 text-muted">{formatBytes(i.size)}</span>
          ) : (
            <span className="shrink-0 text-muted tabular-nums" role="status">
              {Math.round(i.progress * 100)}%
            </span>
          )}
          <button
            type="button"
            aria-label={`${i.name} verwijderen`}
            onClick={() => onRemove(i.key)}
            className="inline-flex size-6 shrink-0 items-center justify-center rounded-full text-muted hover:bg-subtle hover:text-ink"
          >
            <X size={16} aria-hidden />
          </button>
          {!i.upload && !i.error && (
            <span
              aria-hidden
              style={{ width: `${Math.round(i.progress * 100)}%` }}
              className="absolute bottom-0 left-0 h-0.5 bg-ink transition-[width]"
            />
          )}
        </li>
      ))}
    </ul>
  )
}
