import { Ban, Check, CircleHelp, Clock, TriangleAlert } from 'lucide-react'
import { type ReactNode, useState } from 'react'

import { Badge } from '../../components/ui'
import { describeEvent, mergeTimeline, type TimelineEvent } from '../../lib/actions'
import { formatDateTime } from '../../lib/format'
import type { Message, OutboundStatus } from '../../lib/inbox'
import { splitQuotes } from '../../lib/quotes'
import { ForwardButton } from './composer/ForwardButton'
import { MailBody, PhishingNotice } from './MailBody'
import { MessageAttachments } from './MessageAttachments'

const uncertainText =
  'Onbekend of dit bericht is verzonden. Controleer de map Verzonden voordat je het opnieuw verstuurt.'

interface Delivery {
  label: string
  icon: ReactNode
}

const deliveries: Record<OutboundStatus, Delivery> = {
  sent: { label: 'Verzonden', icon: <Check size={16} aria-hidden /> },
  queued: { label: 'Wordt verzonden', icon: <Clock size={16} aria-hidden /> },
  sending: { label: 'Wordt verzonden', icon: <Clock size={16} aria-hidden /> },
  retry: { label: 'Nieuwe poging volgt', icon: <Clock size={16} aria-hidden /> },
  failed: { label: 'Niet verzonden', icon: <TriangleAlert size={16} aria-hidden /> },
  bounced: { label: 'Niet bezorgd', icon: <TriangleAlert size={16} aria-hidden /> },
  uncertain: { label: 'Onzeker', icon: <CircleHelp size={16} aria-hidden /> },
  cancelled: { label: 'Geannuleerd', icon: <Ban size={16} aria-hidden /> },
}

// Delivery is quiet while it goes well. A send that failed, bounced or may not have left is an
// error and gets the alert tag with its label.
const deliveryErrors: OutboundStatus[] = ['failed', 'bounced', 'uncertain']

function DeliveryMark({ status }: { status: OutboundStatus }) {
  const d = deliveries[status]
  if (deliveryErrors.includes(status)) {
    return (
      <span title={status === 'uncertain' ? uncertainText : d.label}>
        <Badge tone="danger" icon={d.icon}>
          {d.label}
        </Badge>
      </span>
    )
  }
  return (
    <span title={d.label} className="inline-flex items-center">
      {d.icon}
      <span className="sr-only">{d.label}</span>
    </span>
  )
}

function DeliveryNotice({ message }: { message: Message }) {
  const error = message.outbound_error ? ` ${message.outbound_error}` : ''
  const text =
    message.outbound_status === 'failed'
      ? `Niet verzonden.${message.outbound_error ? ` De mailserver antwoordde:${error}` : ''}`
      : message.outbound_status === 'bounced'
        ? `Niet bezorgd.${message.outbound_error ? ` De mailserver van de ontvanger antwoordde:${error}` : ''}`
        : message.outbound_status === 'retry'
          ? 'Verzenden lukt nog niet. Er volgt een nieuwe poging.'
          : message.outbound_status === 'uncertain'
            ? uncertainText
            : undefined
  if (!text) return null
  const bad = message.outbound_status !== null && deliveryErrors.includes(message.outbound_status)
  return (
    <p className={`flex max-w-[85%] items-start gap-2 text-base min-[900px]:max-w-[75%] ${bad ? 'text-danger-text' : 'text-muted'}`}>
      <span className="shrink-0">{bad ? <TriangleAlert size={16} aria-hidden /> : <Clock size={16} aria-hidden />}</span>
      <span>{text}</span>
    </p>
  )
}

function Body({ text }: { text: string }) {
  const [open, setOpen] = useState(false)
  const segments = splitQuotes(text)
  return (
    <div className="text-read wrap-anywhere whitespace-pre-wrap">
      {segments.map((seg, i) =>
        seg.kind === 'text' ? (
          <p key={i} className={i > 0 ? 'mt-2' : ''}>
            {seg.text}
          </p>
        ) : (
          <div key={i} className={i > 0 ? 'mt-2' : ''}>
            <button
              type="button"
              aria-expanded={open}
              onClick={() => setOpen(!open)}
              className="t-label text-ink underline underline-offset-2 whitespace-normal"
            >
              {open ? 'Geciteerde tekst verbergen' : 'Geciteerde tekst tonen'}
            </button>
            {open && <blockquote className="mt-2 border-l border-line-strong pl-3 text-muted">{seg.text}</blockquote>}
          </div>
        ),
      )}
    </div>
  )
}

function recipients(label: string, list: Message['to']): string | undefined {
  if (list.length === 0) return undefined
  return `${label}: ${list.map((a) => a.name || a.address).join(', ')}`
}

// Inbound mail is an outlined card on the page, our own replies a filled one on the right.
// Neither is accent: the accent means the customer waits on us, and a sent reply answers that.
function EmailMessage({ conversationId, message, readOnly }: { conversationId: string; message: Message; readOnly: boolean }) {
  const out = message.direction === 'out'
  const sender = out ? (message.author?.name ?? message.from.name) : message.from.name
  const time = message.received_at ?? message.sent_at
  // Customer mail with an HTML part renders in the sandboxed frame; our own replies stay text.
  const html = message.has_html && !out
  const meta = [recipients('Aan', message.to), recipients('Cc', message.cc)].filter(Boolean).join(' · ')
  return (
    <li className={`flex flex-col gap-2 ${out ? 'items-end' : 'items-start'}`}>
      <div className={`t-label flex max-w-[85%] flex-col min-[900px]:max-w-[75%] ${out ? 'items-end' : 'items-start'}`}>
        <span className="max-w-full truncate text-ink">{sender || message.from.address}</span>
        {meta && <span className="max-w-full truncate">{meta}</span>}
      </div>
      <div
        className={`p-4 ${html ? 'w-full min-[900px]:max-w-[90%]' : 'max-w-[85%] min-[900px]:max-w-[75%]'} ${out ? 'rounded-lg rounded-br-md bg-subtle text-ink' : 'rounded-lg rounded-bl-md border border-line-strong text-ink'}`}
      >
        {html ? (
          <MailBody conversationId={conversationId} message={message} readOnly={readOnly} />
        ) : (
          <>
            {!out && message.phishing_warnings.length > 0 && (
              <div className="mb-3">
                <PhishingNotice warnings={message.phishing_warnings} />
              </div>
            )}
            <Body text={message.body_text} />
          </>
        )}
        <MessageAttachments attachments={message.attachments} hideInline={html} />
        <div className="t-label mt-3 flex items-center justify-end gap-2">
          {time && <time dateTime={time}>{formatDateTime(time)}</time>}
          {out && message.outbound_status && <DeliveryMark status={message.outbound_status} />}
          {!readOnly && message.outbound_status !== 'cancelled' && <ForwardButton messageId={message.id} />}
        </div>
      </div>
      {out && <DeliveryNotice message={message} />}
    </li>
  )
}

function NoteMessage({ message }: { message: Message }) {
  const time = message.received_at ?? message.sent_at
  return (
    <li className="rounded-r-lg border-l-2 border-ink bg-note px-4 py-3">
      <div className="t-label flex items-baseline justify-between gap-2">
        <span className="text-ink">Interne notitie{message.author ? ` · ${message.author.name}` : ''}</span>
        {time && <time dateTime={time}>{formatDateTime(time)}</time>}
      </div>
      <div className="text-read mt-2 wrap-anywhere whitespace-pre-wrap text-ink">{message.body_text}</div>
    </li>
  )
}

function TimelineLine({ event }: { event: TimelineEvent }) {
  return (
    <li className="t-label flex items-center justify-center gap-2">
      <span className="min-w-0 truncate">{describeEvent(event)}</span>
      <time dateTime={event.created_at} className="shrink-0 tabular-nums">
        {formatDateTime(event.created_at)}
      </time>
    </li>
  )
}

// readOnly shows a conversation in the trash: nothing in it can be forwarded or changed.
export function MessageThread({
  conversationId,
  messages,
  events = [],
  readOnly = false,
}: {
  conversationId: string
  messages: Message[]
  events?: TimelineEvent[]
  readOnly?: boolean
}) {
  return (
    <ol className="flex flex-col gap-6">
      {mergeTimeline(messages, events).map((entry) => {
        if (entry.kind === 'event') return <TimelineLine key={entry.event.id} event={entry.event} />
        const m = entry.message
        return m.kind === 'note' ? (
          <NoteMessage key={m.id} message={m} />
        ) : m.kind === 'system' ? (
          <li key={m.id} className="t-label text-center">
            {m.body_text}
          </li>
        ) : (
          <EmailMessage key={m.id} conversationId={conversationId} message={m} readOnly={readOnly} />
        )
      })}
    </ol>
  )
}
