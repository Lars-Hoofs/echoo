import { useInfiniteQuery } from '@tanstack/react-query'
import { Link } from '@tanstack/react-router'
import { MessageSquare, StickyNote } from 'lucide-react'

import { Button, ErrorNotice, Skeleton } from '../../components/ui'
import { describeEvent, type TimelineEvent } from '../../lib/actions'
import { api } from '../../lib/api'
import type { Ref } from '../../lib/inbox'
import { errorMessage } from '../../lib/errors'
import { formatDateTime } from '../../lib/format'

interface TimelineItem {
  id: string
  kind: 'event' | 'note'
  type: string
  at: string
  actor: Ref | null
  user: Ref | null
  conversation: { id: string; number: number; subject: string } | null
  text: string
  data: Record<string, unknown>
}

interface TimelinePage {
  items: TimelineItem[]
  next_cursor: string | null
}

function describe(item: TimelineItem): string {
  if (item.kind === 'note') return `${item.actor?.name ?? 'Iemand'} plaatste een notitie`
  if (item.type === 'contact_merged') return `${item.actor?.name ?? 'Echoo'} voegde een contact samen met dit contact`
  const ev: TimelineEvent = {
    id: item.id,
    type: item.type,
    created_at: item.at,
    actor: item.actor,
    user: item.user,
    data: item.data,
  }
  return describeEvent(ev)
}

export function TimelineTab({ contactId }: { contactId: string }) {
  const list = useInfiniteQuery({
    queryKey: ['timeline', contactId],
    initialPageParam: '',
    queryFn: ({ pageParam }) =>
      api<TimelinePage>('GET', `/contacts/${contactId}/timeline${pageParam ? `?cursor=${encodeURIComponent(pageParam)}` : ''}`),
    getNextPageParam: (last) => last.next_cursor ?? undefined,
  })
  if (list.isPending) return <Skeleton className="h-32" />
  if (list.isError) return <ErrorNotice>{errorMessage(list.error)}</ErrorNotice>
  const items = list.data.pages.flatMap((p) => p.items)
  if (items.length === 0) return <p className="text-base text-muted">Nog geen activiteit.</p>
  return (
    <div className="flex flex-col gap-3">
      <ol className="flex flex-col gap-3">
        {items.map((item) => (
          <li key={`${item.kind}-${item.id}`} className="flex items-start gap-3">
            <span aria-hidden className="mt-0.5 flex size-6 shrink-0 items-center justify-center rounded-full bg-subtle text-muted">
              {item.kind === 'note' ? <StickyNote size={14} /> : <MessageSquare size={14} />}
            </span>
            <div className="min-w-0">
              <p className="text-base text-ink">{describe(item)}</p>
              {item.kind === 'note' && <p className="text-base wrap-anywhere whitespace-pre-wrap text-muted">{item.text}</p>}
              <p className="t-label">
                {formatDateTime(item.at)}
                {item.conversation && (
                  <>
                    {' · '}
                    <Link
                      to="/inbox/$view/$conversationId"
                      params={{
                        view: 'alle',
                        conversationId: item.conversation.id,
                      }}
                      className="hover:underline"
                    >
                      #{item.conversation.number} {item.conversation.subject}
                    </Link>
                  </>
                )}
              </p>
            </div>
          </li>
        ))}
      </ol>
      {list.hasNextPage && (
        <div className="flex justify-center">
          <Button busy={list.isFetchingNextPage} onClick={() => void list.fetchNextPage()}>
            Meer laden
          </Button>
        </div>
      )}
    </div>
  )
}
