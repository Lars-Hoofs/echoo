import { useInfiniteQuery } from '@tanstack/react-query'
import { getRouteApi, Link, useNavigate } from '@tanstack/react-router'
import { ArrowLeft, Paperclip, Search } from 'lucide-react'
import { useEffect, useState } from 'react'

import { LabelChip } from '../../components/LabelChip'
import { Button, ErrorNotice } from '../../components/ui'
import { errorMessage } from '../../lib/errors'
import { formatRelative } from '../../lib/format'
import { isSearchable, MIN_QUERY_LENGTH, type SearchResult, searchQuery, splitSnippet } from '../../lib/search'
import { SkeletonRows, StatusMark } from '../inbox/ConversationRow'

const route = getRouteApi('/auth/ready/zoeken')

const TYPING_DELAY_MS = 300

// Matches arrive as segments and are rendered as text nodes; a snippet is never parsed as HTML.
function Snippet({ text }: { text: string }) {
  return (
    <>
      {splitSnippet(text).map((seg, i) =>
        seg.match ? (
          <mark key={i} className="rounded-sm bg-active px-1 font-medium text-ink">
            {seg.text}
          </mark>
        ) : (
          <span key={i}>{seg.text}</span>
        ),
      )}
    </>
  )
}

function ResultRow({ result }: { result: SearchResult }) {
  const c = result.conversation
  const name = c.contact?.name || c.contact?.email || 'Onbekende afzender'
  return (
    <li>
      <Link
        to="/inbox/$view/$conversationId"
        params={{ view: 'alle', conversationId: c.id }}
        className="flex gap-3 border-b border-line px-6 py-4 hover:bg-subtle"
      >
        <span className="mt-1 shrink-0 text-muted">
          <StatusMark item={c} />
        </span>
        <span className="min-w-0 flex-1">
          <span className="t-label flex items-center gap-2">
            <span className="min-w-0 flex-1 truncate">
              #{c.number} · {c.mailbox.name}
              {c.assignee ? ` · ${c.assignee.name}` : ''}
            </span>
            {c.has_attachments && (
              <>
                <Paperclip size={16} aria-hidden className="shrink-0" />
                <span className="sr-only">Met bijlage</span>
              </>
            )}
            <time dateTime={c.last_message_at} className="shrink-0 tabular-nums">
              {formatRelative(c.last_message_at)}
            </time>
          </span>
          <span className="mt-1 block truncate text-ink">{c.subject || 'Zonder onderwerp'}</span>
          <span className="block truncate text-muted">{name}</span>
          <span className="mt-1 line-clamp-2 text-muted">{result.snippet ? <Snippet text={result.snippet} /> : c.preview}</span>
          {c.labels.length > 0 && (
            <span className="mt-2 flex flex-wrap gap-2">
              {c.labels.map((l) => (
                <span key={l.id} className="max-w-32">
                  <LabelChip label={l} />
                </span>
              ))}
            </span>
          )}
        </span>
      </Link>
    </li>
  )
}

export function SearchPage() {
  const { q = '' } = route.useSearch()
  const navigate = useNavigate()
  const [draft, setDraft] = useState(q)
  const [seenQ, setSeenQ] = useState(q)
  // The field follows the URL when it changes elsewhere (the sidebar field, the command bar).
  if (seenQ !== q) {
    setSeenQ(q)
    if (draft.trim() !== q) setDraft(q)
  }

  useEffect(() => {
    const next = draft.trim()
    if (next === q) return
    const t = setTimeout(() => {
      void navigate({ to: '/zoeken', search: next ? { q: next } : {}, replace: true })
    }, TYPING_DELAY_MS)
    return () => {
      clearTimeout(t)
    }
  }, [draft, q, navigate])

  const query = useInfiniteQuery(searchQuery(q))
  const results = query.data?.pages.flatMap((p) => p.results) ?? []
  const searchable = isSearchable(q)

  return (
    <div className="h-full overflow-y-auto bg-list">
      <div className="mx-auto flex max-w-3xl flex-col">
        <header className="flex flex-col gap-4 px-6 pt-6 pb-4">
          <div className="flex items-center gap-3">
            <Link
              to="/inbox/$view"
              params={{ view: 'alle' }}
              className="icon-btn icon-btn-s shrink-0 max-md:size-11"
              aria-label="Terug naar inbox"
              title="Terug naar inbox"
            >
              <ArrowLeft aria-hidden />
            </Link>
            <h1 className="t-h3">Zoeken</h1>
          </div>
          <form
            role="search"
            onSubmit={(e) => {
              e.preventDefault()
            }}
            className="field"
          >
            <Search aria-hidden className="shrink-0" />
            <input
              type="search"
              data-search-input
              autoFocus={q === ''}
              value={draft}
              onChange={(e) => setDraft(e.target.value)}
              onKeyDown={(e) => {
                if (e.key === 'Escape') e.currentTarget.blur()
              }}
              placeholder="Zoek in alle gesprekken"
              aria-label="Zoekopdracht"
              className="[&::-webkit-search-cancel-button]:hidden max-md:text-lg"
            />
          </form>
          <p className="t-label">
            Filters combineer je met tekst, bijvoorbeeld <code className="rounded-sm bg-subtle px-2 py-1 font-mono">status:open van:@bedrijf.nl label:factuur &quot;exacte zin&quot;</code>.
          </p>
        </header>

        <div role="status" aria-live="polite" className="sr-only">
          {query.isSuccess ? `${results.length}${query.hasNextPage ? ' of meer' : ''} resultaten` : ''}
        </div>

        {!searchable && q !== '' && <p className="t-body px-6 py-6">Typ minstens {MIN_QUERY_LENGTH} tekens.</p>}
        {searchable && query.isPending && <SkeletonRows count={5} />}
        {searchable && query.isError && (
          <div className="flex flex-col items-start gap-3 px-6 py-4">
            <ErrorNotice>{errorMessage(query.error)}</ErrorNotice>
            <Button onClick={() => void query.refetch()}>Opnieuw proberen</Button>
          </div>
        )}
        {searchable && query.isSuccess && results.length === 0 && <p className="t-body px-6 py-6">Geen gesprekken gevonden voor “{q}”.</p>}
        {results.length > 0 && (
          <ul aria-label="Zoekresultaten" className="border-t border-line">
            {results.map((r) => (
              <ResultRow key={r.conversation.id} result={r} />
            ))}
          </ul>
        )}
        {query.hasNextPage && (
          <div className="p-6">
            <Button onClick={() => void query.fetchNextPage()} busy={query.isFetchingNextPage}>
              Meer resultaten
            </Button>
          </div>
        )}
      </div>
    </div>
  )
}
