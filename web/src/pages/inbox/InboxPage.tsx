import * as Menu from '@radix-ui/react-dropdown-menu'
import { useInfiniteQuery, useQuery } from '@tanstack/react-query'
import { getRouteApi, Link, Outlet, useNavigate, useParams } from '@tanstack/react-router'
import { Check, ChevronDown, Inbox, MessageSquare, SlidersHorizontal } from 'lucide-react'
import { useEffect, useRef, useState } from 'react'

import { Reveal } from '../../components/Reveal'
import { StatusGlyph } from '../../components/StatusBadge'
import { Button, EmptyState as Empty, ErrorNotice } from '../../components/ui'
import { labelsQuery } from '../../lib/actions'
import { errorMessage } from '../../lib/errors'
import { isTypingTarget } from '../../lib/keys'
import {
  conversationsQuery,
  type InboxSearch,
  type InboxSummary,
  type InboxView,
  type ListStatus,
  listStatusLabel,
  listStatusOrder,
  summaryQuery,
  withStatus,
  slugFromView,
  viewFromSlug,
  type ViewSlug,
} from '../../lib/inbox'
import { advancedKey, splitList } from '../../lib/filters'
import { savedViewsQuery } from '../../lib/savedViews'
import { hasPermission, meQuery } from '../../lib/session'
import { menuItem, menuPanel } from '../shell/menu'
import { BulkBar } from './BulkBar'
import { FilterBar } from './FilterBar'
import { FilterMenu } from './FilterMenu'
import { ConversationRow, SkeletonRows } from './ConversationRow'
import { useHeldNewConversations } from './liveList'
import { NewConversationsPill } from './NewConversationsPill'
import { useFilterLookups } from './useFilterLookups'
import { useInboxShortcuts } from './useInboxShortcuts'

const route = getRouteApi('/auth/ready/inbox/$view')

const tabs: { slug: ViewSlug; view: InboxView; label: string }[] = [
  { slug: 'mine', view: 'mine', label: 'Mijn' },
  { slug: 'zonder-toewijzing', view: 'unassigned', label: 'Zonder toewijzing' },
  { slug: 'alle', view: 'all', label: 'Alle' },
]

function StatusFilter({ slug, search }: { slug: ViewSlug; search: InboxSearch }) {
  const navigate = useNavigate()
  const status = search.status ?? 'open'
  return (
    <Menu.Root>
      <Menu.Trigger className="btn btn-s gap-2 max-md:h-11 max-md:px-4">
        <StatusGlyph status={status} size={16} />
        {listStatusLabel[status]}
        <ChevronDown size={16} aria-hidden />
        <span className="sr-only">, status filter</span>
      </Menu.Trigger>
      <Menu.Portal>
        <Menu.Content align="end" sideOffset={4} className={menuPanel}>
          <Menu.RadioGroup
            value={status}
            onValueChange={(v) => {
              void navigate({ to: '/inbox/$view', params: { view: slug }, search: withStatus(search, v as ListStatus) })
            }}
          >
            {listStatusOrder.map((s) => (
              <Menu.RadioItem key={s} value={s} className={menuItem}>
                <StatusGlyph status={s} size={16} />
                {listStatusLabel[s]}
                <Menu.ItemIndicator className="ml-auto">
                  <Check aria-hidden />
                </Menu.ItemIndicator>
              </Menu.RadioItem>
            ))}
          </Menu.RadioGroup>
        </Menu.Content>
      </Menu.Portal>
    </Menu.Root>
  )
}

function emptyCopy(
  view: InboxView,
  status: ListStatus,
  filterName: string | undefined,
): { text: string; link: 'all' | 'open' | 'none' } {
  const adjective = { open: 'open', waiting: 'wachtende', closed: 'gesloten', spam: 'spam', snoozed: 'uitgestelde' }[status]
  const noun = status === 'spam' ? 'Geen spamgesprekken' : `Geen ${adjective} gesprekken`
  if (filterName) return { text: `${noun} in ${filterName}.`, link: status === 'open' ? 'all' : 'open' }
  if (view === 'mine') return { text: `${noun} aan jou toegewezen.`, link: status === 'open' ? 'all' : 'open' }
  if (view === 'unassigned') return { text: `${noun} zonder toewijzing.`, link: status === 'open' ? 'all' : 'open' }
  return { text: `${noun}.`, link: status === 'open' ? 'none' : 'open' }
}

function EmptyState({
  view,
  status,
  search,
  summary,
}: {
  view: InboxView
  status: ListStatus
  search: InboxSearch
  summary: InboxSummary | undefined
}) {
  const { data: me } = useQuery(meQuery)
  const filterName =
    summary?.mailboxes.find((m) => m.id === search.mailbox)?.name ?? summary?.teams.find((t) => t.id === search.team)?.name
  const noMailbox = summary !== undefined && summary.mailboxes.length === 0
  const copy = emptyCopy(view, status, filterName)
  const linkClass = 'btn btn-s'
  const icon = <Inbox />

  if (noMailbox) {
    const admin = hasPermission(me, 'mailboxes.manage')
    return (
      <Empty
        icon={icon}
        title="Nog geen mailbox gekoppeld."
        {...(admin ? {} : { description: 'Vraag een beheerder om een mailbox te koppelen.' })}
        action={
          admin ? (
            <Link to="/instellingen/mailboxen" className={linkClass}>
              Mailbox toevoegen
            </Link>
          ) : undefined
        }
      />
    )
  }
  const advanced = { ...search }
  delete advanced.status
  delete advanced.weergave
  if (Object.keys(advanced).some((k) => k !== 'mailbox' && k !== 'team' && k !== 'label') || splitList(search.mailbox).length + splitList(search.team).length + splitList(search.label).length > 1) {
    return (
      <Empty
        icon={icon}
        title="Geen gesprekken met deze filters."
        action={
          <Link to="/inbox/$view" params={{ view: slugFromView(view) }} search={withStatus({}, status)} className={linkClass}>
            Filters wissen
          </Link>
        }
      />
    )
  }
  return (
    <Empty
      icon={icon}
      title={copy.text}
      action={
        copy.link === 'all' ? (
          <Link to="/inbox/$view" params={{ view: 'alle' }} className={linkClass}>
            Alle gesprekken tonen
          </Link>
        ) : copy.link === 'open' ? (
          <Link to="/inbox/$view" params={{ view: slugFromView(view) }} search={withStatus(search, 'open')} className={linkClass}>
            Open gesprekken tonen
          </Link>
        ) : summary ? (
          <Link to="/inbox/$view" params={{ view: slugFromView(view) }} search={withStatus(search, 'closed')} className={linkClass}>
            Gesloten gesprekken tonen
          </Link>
        ) : undefined
      }
    />
  )
}

function InboxPageContent() {
  const { view: slug } = route.useParams()
  const search = route.useSearch()
  const view = viewFromSlug(slug) ?? 'all'
  const status = search.status ?? 'open'
  const conversationId = useParams({ strict: false }).conversationId

  const summary = useQuery(summaryQuery)
  const list = useInfiniteQuery(conversationsQuery({ ...search, view, status }))
  const allItems = list.data?.pages.flatMap((p) => p.conversations) ?? []
  const filterKey = `${view}|${status}|${search.mailbox ?? ''}|${search.team ?? ''}|${search.label ?? ''}|${advancedKey(search)}`
  const [scrolled, setScrolled] = useState(false)
  const live = useHeldNewConversations(
    allItems.map((i) => i.id),
    filterKey,
    list.isSuccess,
    scrolled,
  )
  const items = allItems.filter((i) => !live.held.has(i.id))

  const [selection, setSelection] = useState({ key: filterKey, ids: new Set<string>() })
  // A selection belongs to the list it was made in; another filter starts with none.
  const selectedIds = selection.key === filterKey ? selection.ids : new Set<string>()
  const selectedItems = items.filter((i) => selectedIds.has(i.id))
  const toggleSelect = (id: string) => {
    const next = new Set(selectedIds)
    if (next.has(id)) next.delete(id)
    else next.add(id)
    setSelection({ key: filterKey, ids: next })
  }
  const clearSelection = () => setSelection({ key: filterKey, ids: new Set() })
  const me = useQuery(meQuery)
  const canSelect = hasPermission(me.data, 'conversations.write')
  useInboxShortcuts({ items, conversationId, selected: selectedItems, toggle: toggleSelect, clearSelection })

  const scrollRef = useRef<HTMLDivElement>(null)
  const sentinelRef = useRef<HTMLDivElement>(null)
  const [cursor, setCursor] = useState<string | undefined>(undefined)
  const tabbableId = items.some((i) => i.id === cursor) ? cursor : (conversationId ?? items[0]?.id)

  const { hasNextPage, isFetchingNextPage, fetchNextPage } = list
  useEffect(() => {
    const el = sentinelRef.current
    if (!el || !hasNextPage) return
    const io = new IntersectionObserver(
      (entries) => {
        if (entries.some((e) => e.isIntersecting) && !isFetchingNextPage) void fetchNextPage()
      },
      { root: scrollRef.current, rootMargin: '0px 0px 400px 0px' },
    )
    io.observe(el)
    return () => {
      io.disconnect()
    }
  }, [hasNextPage, isFetchingNextPage, fetchNextPage, items.length])

  useEffect(() => {
    const rows = () => Array.from(scrollRef.current?.querySelectorAll<HTMLElement>('[data-row]') ?? [])
    const focusRow = (el: HTMLElement | undefined) => {
      el?.focus()
      el?.scrollIntoView({ block: 'nearest' })
    }
    const onKey = (e: KeyboardEvent) => {
      if (e.metaKey || e.ctrlKey || e.altKey || isTypingTarget(e.target)) return
      const all = rows()
      const current = document.activeElement instanceof HTMLElement ? all.indexOf(document.activeElement) : -1
      if (e.key === 'j' || e.key === 'k') {
        e.preventDefault()
        const base = current >= 0 ? current : all.findIndex((r) => r.getAttribute('aria-current') === 'page')
        const next = e.key === 'j' ? Math.min(base + 1, all.length - 1) : Math.max(base - 1, 0)
        focusRow(all[next])
      } else if (e.key === 'o' && current >= 0) {
        e.preventDefault()
        all[current]?.click()
      } else if (e.key === 'Escape') {
        focusRow(all.find((r) => r.getAttribute('aria-current') === 'page') ?? all[0])
      }
    }
    document.addEventListener('keydown', onKey)
    return () => {
      document.removeEventListener('keydown', onKey)
    }
  }, [])

  const navigate = useNavigate()
  const [filterOpen, setFilterOpen] = useState(false)
  const { users } = useFilterLookups()
  const savedViews = useQuery(savedViewsQuery)
  const savedView = savedViews.data?.find((v) => v.id === search.weergave)
  const showCounts = status === 'open'
  const counts = summary.data?.counts
  const labels = useQuery(labelsQuery)
  const filterName =
    summary.data?.mailboxes.find((m) => m.id === search.mailbox)?.name ??
    summary.data?.teams.find((t) => t.id === search.team)?.name ??
    labels.data?.find((l) => l.id === search.label)?.name

  return (
    <div className="flex h-full">
      <section
        aria-label="Gesprekken"
        className={`${conversationId ? 'hidden min-[900px]:flex' : 'flex'} w-full shrink-0 flex-col border-r border-line bg-list min-[900px]:w-[360px]`}
      >
        {selectedItems.length > 0 ? (
          <BulkBar
            selected={selectedItems}
            total={items.length}
            onClear={clearSelection}
            onSelectAll={() => setSelection({ key: filterKey, ids: new Set(items.map((i) => i.id)) })}
          />
        ) : (
          <header className="flex shrink-0 flex-col gap-4 px-4 pt-6 pb-4">
            <h1 className="t-h3 truncate">{savedView?.name ?? filterName ?? 'Gesprekken'}</h1>
            <div className="flex items-center gap-2">
              <Button size="sm" onClick={() => setFilterOpen(true)}>
                <SlidersHorizontal size={16} aria-hidden />
                Filter
              </Button>
              <StatusFilter slug={slug as ViewSlug} search={search} />
            </div>
          </header>
        )}
        <FilterBar slug={slug as ViewSlug} search={search} />
        <FilterMenu
          open={filterOpen}
          onOpenChange={setFilterOpen}
          search={search}
          users={users}
          onChange={(next) => {
            void navigate({ to: '/inbox/$view', params: { view: slug }, search: next })
          }}
        />
        <nav aria-label="Weergave" className="shrink-0 border-b border-line px-4 pb-4">
          <div className="tabs max-w-full overflow-x-auto">
            {tabs.map((t) => {
              const active = t.slug === slug
              return (
                <Link
                  key={t.slug}
                  to="/inbox/$view"
                  params={{ view: t.slug }}
                  search={search}
                  aria-current={active ? 'page' : undefined}
                  className={`tab inline-flex items-center gap-2 px-3 max-md:h-11 ${active ? 'is-active' : ''}`}
                >
                  {t.label}
                  {showCounts && counts && <span className="tabular-nums opacity-70">{counts[t.view]}</span>}
                </Link>
              )
            })}
          </div>
        </nav>

        <div
          ref={scrollRef}
          onScroll={(e) => {
            setScrolled(e.currentTarget.scrollTop > 80)
          }}
          className="min-h-0 flex-1 overflow-y-auto"
        >
          <NewConversationsPill
            count={live.held.size}
            announcement={live.announcement}
            onShow={() => {
              live.release()
              scrollRef.current?.scrollTo({ top: 0 })
            }}
          />
          {list.isPending && <SkeletonRows />}
          {list.isError && (
            <div className="flex flex-col items-start gap-3 p-4">
              <ErrorNotice>{errorMessage(list.error)}</ErrorNotice>
              <Button onClick={() => void list.refetch()}>Opnieuw proberen</Button>
            </div>
          )}
          {list.isSuccess && items.length === 0 && (
            <Reveal>
              <EmptyState view={view} status={status} search={search} summary={summary.data} />
            </Reveal>
          )}
          {items.length > 0 && (
            <ul>
              {items.map((item) => (
                <ConversationRow
                  key={item.id}
                  item={item}
                  view={view}
                  active={item.id === conversationId}
                  tabbable={item.id === tabbableId}
                  onFocus={() => setCursor(item.id)}
                  selected={selectedIds.has(item.id)}
                  selecting={selectedIds.size > 0}
                  onToggleSelect={canSelect ? () => toggleSelect(item.id) : undefined}
                />
              ))}
            </ul>
          )}
          {hasNextPage && (
            <div ref={sentinelRef} className="t-label p-4 text-center" role="status">
              {isFetchingNextPage ? 'Meer gesprekken laden' : ''}
            </div>
          )}
        </div>
      </section>

      <div className={`${conversationId ? 'flex' : 'hidden min-[900px]:flex'} min-w-0 flex-1 bg-surface`}>
        {conversationId ? (
          <Outlet />
        ) : (
          <div className="flex flex-1 items-center justify-center">
            <Reveal>
              <Empty icon={<MessageSquare />} title="Kies een gesprek uit de lijst." />
            </Reveal>
          </div>
        )}
      </div>
    </div>
  )
}

export function InboxPage() {
  return <InboxPageContent />
}
