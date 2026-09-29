import { Link } from '@tanstack/react-router'
import { ArrowUp, Clock, CornerUpLeft, Paperclip } from 'lucide-react'

import { Avatar } from '../../components/Avatar'
import { LabelChip } from '../../components/LabelChip'
import { SlaTimer } from '../../components/SlaTimer'
import { StatusGlyph } from '../../components/StatusBadge'
import { Badge } from '../../components/ui'
import { formatRelative } from '../../lib/format'
import { type ConversationListItem, priorityLabel, slugFromView, type InboxView } from '../../lib/inbox'

export const ROW_HEIGHT = 'h-23'

// The one state the accent stands for: an open conversation whose last word is the customer's.
// An unread one already shows the dot next to the name, so the status glyph only adds the accent
// once the message has been read and is still unanswered.
const waitsOnUs = (item: ConversationListItem) => item.status === 'open' && item.last_direction === 'in' && !item.unread

// The status glyph of a conversation: neutral, except for the ring that marks a read conversation
// still waiting for our reply.
export function StatusMark({ item }: { item: ConversationListItem }) {
  if (!waitsOnUs(item)) return <StatusGlyph status={item.status} size={16} />
  return (
    <span className="inline-grid size-5 shrink-0 place-items-center rounded-full bg-accent text-on-accent">
      <StatusGlyph status={item.status} size={12} />
      <span className="sr-only">Wacht op antwoord. </span>
    </span>
  )
}

export function ConversationRow({
  item,
  view,
  active,
  tabbable,
  onFocus,
  selected,
  selecting,
  onToggleSelect,
}: {
  item: ConversationListItem
  view: InboxView
  active: boolean
  tabbable: boolean
  onFocus: () => void
  selected: boolean
  // True while any row is selected: every row then shows its checkbox.
  selecting: boolean
  onToggleSelect: (() => void) | undefined
}) {
  const name = item.contact?.name || item.contact?.email || 'Onbekende afzender'
  const meta = item.assignee ? `${item.mailbox.name} · ${item.assignee.name}` : item.mailbox.name
  const highPriority = item.priority === 'high' || item.priority === 'urgent'
  const showBox = onToggleSelect !== undefined
  const boxVisible = selecting ? 'opacity-100' : 'opacity-0 group-hover:opacity-100 focus-visible:opacity-100'
  return (
    <li className="group relative">
      {showBox ? (
        <input
          type="checkbox"
          checked={selected}
          onChange={onToggleSelect}
          tabIndex={-1}
          aria-label={`Selecteer gesprek van ${name}`}
          className={`absolute top-5 left-6 z-10 size-4 accent-(--ink) ${boxVisible}`}
        />
      ) : null}
      <Link
        to="/inbox/$view/$conversationId"
        params={{ view: slugFromView(view), conversationId: item.id }}
        search={(prev) => prev}
        data-row={item.id}
        data-unread={item.unread}
        tabIndex={tabbable ? 0 : -1}
        onFocus={onFocus}
        aria-current={active ? 'page' : undefined}
        className={`flex ${ROW_HEIGHT} gap-3 border-b border-line px-4 py-3 -outline-offset-2 ${active ? 'bg-selected' : 'hover:bg-subtle'}`}
      >
        <span className={`shrink-0 ${!showBox ? '' : selecting ? 'invisible' : 'group-hover:invisible'}`}>
          <Avatar name={name} />
        </span>
        <span className="flex min-w-0 flex-1 flex-col">
          <span className="flex h-6 items-center gap-2">
            {item.unread && (
              <>
                <span aria-hidden className="size-2 shrink-0 rounded-full bg-accent" />
                <span className="sr-only">Ongelezen: </span>
              </>
            )}
            <span className={`min-w-0 truncate text-base text-ink ${item.unread ? 'font-bold' : ''}`}>{name}</span>
            {highPriority && (
              <span className="shrink-0">
                <Badge icon={<ArrowUp size={16} />}>{priorityLabel[item.priority]}</Badge>
              </span>
            )}
            <span className="flex-1" />
            {item.has_attachments && (
              <>
                <Paperclip size={16} aria-hidden className="shrink-0 text-muted" />
                <span className="sr-only">Met bijlage</span>
              </>
            )}
            <time dateTime={item.last_message_at} className="t-label shrink-0 tabular-nums">
              {formatRelative(item.last_message_at)}
            </time>
          </span>
          <span className="flex h-5 items-center gap-2 text-base text-muted">
            {item.last_direction === 'out' && (
              <>
                <CornerUpLeft size={16} aria-hidden className="shrink-0" />
                <span className="sr-only">Jij: </span>
              </>
            )}
            <span className={`min-w-0 flex-1 truncate ${item.unread ? 'text-ink' : ''}`}>{item.preview || item.subject}</span>
          </span>
          <span className="t-label flex h-6 items-center gap-2">
            <StatusMark item={item} />
            <span className="min-w-0 flex-1 truncate">{meta}</span>
            {item.snoozed_until && (
              <>
                <Clock size={16} aria-hidden className="shrink-0" />
                <span className="sr-only">Uitgesteld</span>
              </>
            )}
            <SlaTimer item={item} />
            {item.labels.slice(0, 1).map((l) => (
              <span key={l.id} className="max-w-24 shrink-0">
                <LabelChip label={l} />
              </span>
            ))}
            {item.labels.length > 1 && <span className="shrink-0">+{item.labels.length - 1}</span>}
          </span>
        </span>
      </Link>
    </li>
  )
}

export function SkeletonRows({ count = 8 }: { count?: number }) {
  return (
    <ul aria-hidden>
      {Array.from({ length: count }, (_, i) => (
        <li key={i} className={`flex ${ROW_HEIGHT} gap-3 border-b border-line px-4 py-3 motion-safe:animate-pulse`}>
          <div className="size-8 shrink-0 rounded-full bg-subtle" />
          <div className="flex min-w-0 flex-1 flex-col">
            <div className="flex h-6 items-center justify-between">
              <div className="h-3 w-28 rounded-full bg-subtle" />
              <div className="h-3 w-8 rounded-full bg-subtle" />
            </div>
            <div className="flex h-5 items-center">
              <div className="h-3 w-56 max-w-full rounded-full bg-subtle" />
            </div>
            <div className="flex h-6 items-center">
              <div className="h-3 w-32 rounded-full bg-subtle" />
            </div>
          </div>
        </li>
      ))}
    </ul>
  )
}
