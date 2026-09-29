import { useInfiniteQuery } from '@tanstack/react-query'
import { Link } from '@tanstack/react-router'

import { StatusBadge } from '../../components/StatusBadge'
import { Button, Card, ErrorNotice, Skeleton } from '../../components/ui'
import { api } from '../../lib/api'
import type { ConversationPageResult } from '../../lib/contacts'
import { errorMessage } from '../../lib/errors'
import { formatRelative } from '../../lib/format'

export function useContactConversations(scope: 'contacts' | 'organizations', id: string) {
  return useInfiniteQuery({
    queryKey: ['contact-conversations', scope, id],
    initialPageParam: '',
    queryFn: ({ pageParam }) =>
      api<ConversationPageResult>('GET', `/${scope}/${id}/conversations${pageParam ? `?cursor=${encodeURIComponent(pageParam)}` : ''}`),
    getNextPageParam: (last) => last.next_cursor ?? undefined,
  })
}

// Conversations of a contact or an organization, open ones first; the server orders them.
export function ConversationsTab({ scope, id }: { scope: 'contacts' | 'organizations'; id: string }) {
  const list = useContactConversations(scope, id)
  if (list.isPending) return <Skeleton className="h-32" />
  if (list.isError) return <ErrorNotice>{errorMessage(list.error)}</ErrorNotice>
  const rows = list.data.pages.flatMap((p) => p.conversations)
  if (rows.length === 0) return <p className="text-base text-muted">Geen gesprekken die jij kunt zien.</p>
  return (
    <div className="flex flex-col gap-3">
      <Card flush>
        <ul className="divide-y divide-line">
          {rows.map((c) => (
            <li key={c.id}>
              <Link
                to="/inbox/$view/$conversationId"
                params={{ view: 'alle', conversationId: c.id }}
                className="flex flex-col gap-1 px-4 py-4 hover:bg-subtle/60 sm:px-6"
              >
                <span className="flex flex-wrap items-center gap-2">
                  <span className="min-w-0 truncate text-base text-ink">{c.subject || '(geen onderwerp)'}</span>
                  <StatusBadge status={c.status} />
                </span>
                <span className="truncate text-sm text-muted">{c.preview}</span>
                <span className="t-label">
                  #{c.number} · {c.mailbox.name} · {formatRelative(c.last_message_at)}
                </span>
              </Link>
            </li>
          ))}
        </ul>
      </Card>
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
