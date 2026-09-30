import { useQuery } from '@tanstack/react-query'
import { getRouteApi, Link, useNavigate } from '@tanstack/react-router'
import { ChevronLeft, RotateCcw, Trash2 } from 'lucide-react'
import { useState } from 'react'

import { Button, ErrorNotice, Skeleton } from '../../components/ui'
import { errorMessage } from '../../lib/errors'
import { formatDateTime } from '../../lib/format'
import { trashedConversationQuery } from '../../lib/trash'
import { MessageThread } from './MessageThread'
import { PurgeDialog } from './TrashPage'
import { useTrashActions } from './useTrashActions'

const route = getRouteApi('/auth/ready/prullenbak/$conversationId')

// A conversation in the trash, to read before restoring it or deleting it for good.
export function TrashConversationPage() {
  const { conversationId } = route.useParams()
  const query = useQuery(trashedConversationQuery(conversationId))
  const run = useTrashActions()
  const navigate = useNavigate()
  const [purging, setPurging] = useState(false)
  const [restoring, setRestoring] = useState(false)

  const back = (
    <Link to="/prullenbak" className="btn btn-s self-start">
      <ChevronLeft size={16} aria-hidden /> Terug naar de prullenbak
    </Link>
  )

  const restore = async () => {
    setRestoring(true)
    const ok = await run('restore', [conversationId])
    setRestoring(false)
    if (ok.length > 0) void navigate({ to: '/inbox/$view/$conversationId', params: { view: 'alle', conversationId } })
  }

  return (
    <div className="relative h-full overflow-y-auto px-4 py-8 md:px-8">
      <div className="mx-auto flex w-full max-w-3xl flex-col gap-6">
        {back}
        {query.isPending ? (
          <Skeleton className="h-64" />
        ) : query.isError ? (
          <ErrorNotice>{errorMessage(query.error)}</ErrorNotice>
        ) : (
          <>
            <header className="flex flex-col gap-4 border-b border-line pb-4">
              <div className="min-w-0">
                <div className="t-label flex items-center gap-2">
                  <h1 className="min-w-0 truncate font-normal">{query.data.contact?.name || query.data.contact?.email || 'Onbekende afzender'}</h1>
                  <span className="shrink-0">
                    · #{query.data.conversation.number} · {query.data.conversation.mailbox.name}
                  </span>
                </div>
                <h2 className="t-title mt-1 line-clamp-2 wrap-anywhere">{query.data.conversation.subject || '(geen onderwerp)'}</h2>
              </div>
              <p role="note" className="rounded-md bg-subtle px-4 py-3 text-base text-ink">
                In de prullenbak sinds {formatDateTime(query.data.deleted_at)}
                {query.data.deleted_by && <> door {query.data.deleted_by.name}</>}. Zet het gesprek terug om het te beantwoorden of te
                wijzigen; na de bewaartermijn van de prullenbak wordt het definitief verwijderd.
              </p>
              <div className="flex flex-wrap gap-2">
                <Button variant="primary" busy={restoring} onClick={() => void restore()}>
                  <RotateCcw size={16} aria-hidden />
                  Terugzetten
                </Button>
                <Button variant="danger" onClick={() => setPurging(true)}>
                  <Trash2 size={16} aria-hidden />
                  Definitief verwijderen
                </Button>
              </div>
            </header>
            <MessageThread conversationId={conversationId} messages={query.data.messages} events={query.data.events} readOnly />
          </>
        )}
        {purging && (
          <PurgeDialog
            ids={[conversationId]}
            onClose={() => setPurging(false)}
            onDone={() => void navigate({ to: '/prullenbak' })}
          />
        )}
      </div>
    </div>
  )
}
