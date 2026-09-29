import * as Dialog from '@radix-ui/react-dialog'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { getRouteApi, Link, useNavigate } from '@tanstack/react-router'
import { ChevronLeft, Info, X } from 'lucide-react'
import { useEffect, useRef, useState } from 'react'

import { Avatar } from '../../components/Avatar'
import { useToast } from '../../components/Toast'
import { SlaTimer } from '../../components/SlaTimer'
import { StatusBadge } from '../../components/StatusBadge'
import { Button, ErrorNotice, IconButton, Skeleton as Bar } from '../../components/ui'
import { errorMessage } from '../../lib/errors'
import { eventsQuery } from '../../lib/actions'
import { formatDateTime } from '../../lib/format'
import { conversationQuery, type InboxSearch } from '../../lib/inbox'
import { markRead, READ_DELAY_MS } from '../../lib/unread'
import { typingLabel, useConversationPresence, viewersLabel } from '../../lib/presence'
import { hasPermission, meQuery } from '../../lib/session'
import { Composer } from './Composer'
import { ContactPanel } from './ContactPanel'
import { HeaderActions } from './HeaderActions'
import { MessageThread } from './MessageThread'

const route = getRouteApi('/auth/ready/inbox/$view/$conversationId')

function Skeleton() {
  return (
    <div className="flex min-w-0 flex-1 flex-col" aria-hidden>
      <div className="flex shrink-0 flex-col gap-4 border-b border-line px-4 pt-4 pb-4 motion-safe:animate-pulse min-[900px]:px-6">
        <div className="flex flex-col gap-2">
          <div className="h-3 w-32 rounded-full bg-subtle" />
          <div className="h-6 w-2/3 rounded-full bg-subtle" />
        </div>
        <div className="flex items-center gap-2">
          <div className="h-8 w-28 rounded-full bg-subtle" />
          <div className="h-8 w-28 rounded-full bg-subtle" />
          <div className="ml-auto h-8 w-28 rounded-full bg-subtle" />
        </div>
      </div>
      <div className="flex flex-1 flex-col gap-4 p-6 motion-safe:animate-pulse">
        <Bar className="h-24 w-2/3 rounded-lg" />
        <Bar className="ml-auto h-16 w-1/2 rounded-lg" />
        <Bar className="h-32 w-3/5 rounded-lg" />
      </div>
    </div>
  )
}

export function ConversationPane() {
  const { conversationId, view } = route.useParams()
  const query = useQuery(conversationQuery(conversationId))
  const me = useQuery(meQuery)
  const events = useQuery(eventsQuery(conversationId))
  const viewers = useConversationPresence(conversationId, me.data?.user.id)
  const [infoOpen, setInfoOpen] = useState(false)
  const scrollRef = useRef<HTMLDivElement>(null)
  const lastId = query.data?.messages.at(-1)?.id
  const unread = query.data?.conversation.unread
  const qc = useQueryClient()
  const toast = useToast()
  const navigate = useNavigate()

  useEffect(() => {
    if (!unread) return
    const t = setTimeout(() => {
      markRead(qc, conversationId).catch((e: unknown) => toast(errorMessage(e), { tone: 'error' }))
    }, READ_DELAY_MS)
    return () => {
      clearTimeout(t)
    }
  }, [unread, conversationId, lastId, qc, toast])

  // The newest message is what you come for. The composer and the mail frames finish loading after
  // the first paint and change the height, so the view stays at the bottom until you scroll away.
  const pinned = useRef(true)
  const hasThread = query.data !== undefined
  useEffect(() => {
    const el = scrollRef.current
    if (!el) return
    pinned.current = true
    const toBottom = () => {
      if (pinned.current) el.scrollTop = el.scrollHeight
    }
    toBottom()
    const observer = new ResizeObserver(toBottom)
    observer.observe(el)
    if (el.firstElementChild) observer.observe(el.firstElementChild)
    return () => {
      observer.disconnect()
    }
  }, [conversationId, lastId, hasThread])

  if (query.isPending) return <Skeleton />
  if (query.isError) {
    return (
      <div className="flex min-w-0 flex-1 flex-col items-start gap-3 p-6">
        <Link
          to="/inbox/$view"
          params={{ view }}
          search={(prev: InboxSearch) => prev}
          className="btn btn-s"
        >
          <ChevronLeft size={16} aria-hidden /> Terug naar de lijst
        </Link>
        <ErrorNotice>{errorMessage(query.error)}</ErrorNotice>
        <Button onClick={() => void query.refetch()}>Opnieuw proberen</Button>
      </div>
    )
  }

  const data = query.data
  const { conversation: c, contact } = data
  const name = contact?.name || contact?.email || 'Onbekende afzender'

  return (
    <>
      <section aria-label={`Gesprek ${c.number}`} className="flex min-w-0 flex-1 flex-col">
        <header className="flex shrink-0 flex-col gap-4 border-b border-line px-4 pt-4 pb-4 min-[900px]:px-6">
          <div className="flex items-start gap-3">
            <Link
              to="/inbox/$view"
              params={{ view }}
              search={(prev: InboxSearch) => prev}
              aria-label="Terug naar de lijst"
              title="Terug naar de lijst"
              className="icon-btn icon-btn-s shrink-0 max-md:size-11 min-[900px]:hidden"
            >
              <ChevronLeft aria-hidden />
            </Link>
            <div className="min-w-0 flex-1">
              <div className="t-label flex items-center gap-2">
                <h1 className="min-w-0 truncate font-normal">{name}</h1>
                <span className="shrink-0">
                  · #{c.number} · {c.mailbox.name}
                </span>
              </div>
              <h2 className="t-title mt-1 line-clamp-2 wrap-anywhere">{c.subject || '(geen onderwerp)'}</h2>
              {c.snoozed_until && <p className="t-label mt-1">Uitgesteld tot {formatDateTime(c.snoozed_until)}</p>}
            </div>
            <IconButton label="Contactgegevens tonen" size="sm" onClick={() => setInfoOpen(true)} className="min-[1440px]:hidden">
              <Info aria-hidden />
            </IconButton>
          </div>
          <div className="flex flex-wrap items-center gap-x-4 gap-y-3">
            <SlaTimer item={c} compact={false} />
            <StatusBadge status={c.status} className="max-sm:hidden min-[1440px]:hidden" />
            {viewers.length > 0 && (
              <div className="flex min-w-0 shrink items-center gap-2">
                <span className="flex shrink-0 -space-x-2">
                  {viewers.slice(0, 3).map((v) => (
                    <span key={v.id} className="rounded-full ring-2 ring-surface">
                      <Avatar name={v.name} size={24} />
                    </span>
                  ))}
                </span>
                <span className="t-label truncate max-sm:sr-only">Ook geopend door {viewersLabel(viewers)}</span>
              </div>
            )}
            {c.can_write && hasPermission(me.data, 'conversations.write') && (
              <HeaderActions
                conversation={c}
                onMarkedUnread={() => void navigate({ to: '/inbox/$view', params: { view }, search: (prev: InboxSearch) => prev })}
              />
            )}
          </div>
        </header>

        <div
          ref={scrollRef}
          onScroll={(e) => {
            const el = e.currentTarget
            pinned.current = el.scrollHeight - el.scrollTop - el.clientHeight < 48
          }}
          className="min-h-0 flex-1 overflow-y-auto"
        >
          <div className="mx-auto max-w-3xl px-4 py-6 min-[900px]:px-6">
            {data.csat && (
              <p className="t-body mb-6">
                Tevredenheid: <span className="text-ink tabular-nums">{data.csat.rating} van 5</span>
                {data.csat.comment && (
                  <>
                    {' · '}
                    <span className="whitespace-pre-wrap wrap-anywhere">{data.csat.comment}</span>
                  </>
                )}
              </p>
            )}
            <MessageThread messages={data.messages} events={events.data ?? []} />
          </div>
        </div>
        <p role="status" aria-live="polite" className="t-label h-5 shrink-0 truncate px-4 min-[900px]:px-6">
          {typingLabel(viewers)}
        </p>
        <Composer conversationId={c.id} mailboxId={c.mailbox.id} canWrite={c.can_write} />
      </section>

      <aside aria-label="Contact" className="hidden w-[280px] shrink-0 border-l border-line bg-list min-[1440px]:block">
        <ContactPanel data={data} />
      </aside>

      <Dialog.Root open={infoOpen} onOpenChange={setInfoOpen}>
        <Dialog.Portal>
          <Dialog.Overlay className="fixed inset-0 z-40 bg-scrim backdrop-blur-sm data-[state=open]:animate-[echoo-fade-in_var(--dur-move)_var(--ease-enter)] min-[1440px]:hidden" />
          <Dialog.Content
            aria-describedby={undefined}
            className="fixed inset-y-0 right-0 z-50 w-[320px] max-w-[92vw] border-l border-line bg-list min-[1440px]:hidden"
          >
            <Dialog.Title className="sr-only">Contact</Dialog.Title>
            <Dialog.Close asChild>
              <IconButton label="Sluiten" size="sm" className="absolute top-4 right-4">
                <X aria-hidden />
              </IconButton>
            </Dialog.Close>
            <ContactPanel data={data} />
          </Dialog.Content>
        </Dialog.Portal>
      </Dialog.Root>
    </>
  )
}
