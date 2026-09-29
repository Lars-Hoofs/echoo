import * as RadixDialog from '@radix-ui/react-dialog'
import { useQuery } from '@tanstack/react-query'
import { useNavigate, useParams } from '@tanstack/react-router'
import { Command } from 'cmdk'
import {
  BookOpen,
  Bookmark,
  ChartColumn,
  ClockArrowUp,
  Flag,
  History,
  Inbox,
  Layers,
  ListFilter,
  Mail,
  Megaphone,
  MessageSquare,
  MessageSquareReply,
  NotebookPen,
  Search,
  Settings,
  Tag,
  UserRound,
  Users,
  UsersRound,
} from 'lucide-react'
import { createContext, type ReactNode, useCallback, useContext, useEffect, useMemo, useRef, useState } from 'react'

import { StatusGlyph } from '../../components/StatusBadge'
import { Kbd } from '../../components/ui'
import { type ComposerMode, requestComposer } from '../../lib/composer'
import { savedToSearch } from '../../lib/filters'
import { conversationQuery, statusLabel, summaryQuery } from '../../lib/inbox'
import { canOpenSettingsPage, customerPages, sectionPages, settingsPages, visibleSectionPages } from '../../lib/nav'
import { isSearchable, matchesQuery, readRecent, type RecentConversation, rememberConversation, searchPreviewQuery } from '../../lib/search'
import { savedViewsQuery } from '../../lib/savedViews'
import { hasPermission, meQuery } from '../../lib/session'
import { usePickers } from '../inbox/ActionPickers'
import { useConversationActions } from '../inbox/useConversationActions'
import { ShortcutsDialog } from './ShortcutsDialog'
import { useGlobalShortcuts } from './useGlobalShortcuts'

interface CommandApi {
  openPalette: () => void
  openShortcuts: () => void
}

const CommandContext = createContext<CommandApi | null>(null)

export function useCommandBar(): CommandApi {
  const ctx = useContext(CommandContext)
  if (!ctx) throw new Error('useCommandBar needs a CommandProvider')
  return ctx
}

const sectionIcons: Record<(typeof sectionPages)[number]['to'], ReactNode> = {
  '/campagnes': <Megaphone size={16} aria-hidden />,
  '/rapportage': <ChartColumn size={16} aria-hidden />,
  '/kennisbank': <BookOpen size={16} aria-hidden />,
  '/kennisbank/beheer': <BookOpen size={16} aria-hidden />,
}

interface Item {
  id: string
  label: string
  hint?: string
  icon: ReactNode
  shortcut?: string[]
  run: () => void
}

const itemClass = 'flex cursor-default items-center gap-3 rounded-full px-4 py-2 text-base text-ink outline-none data-[selected=true]:bg-selected max-md:py-3'
const groupClass = '[&_[cmdk-group-heading]]:px-4 [&_[cmdk-group-heading]]:pt-3 [&_[cmdk-group-heading]]:pb-1 [&_[cmdk-group-heading]]:text-xs [&_[cmdk-group-heading]]:text-muted'

function useDebounced<T>(value: T, ms: number): T {
  const [debounced, setDebounced] = useState(value)
  useEffect(() => {
    const t = setTimeout(() => {
      setDebounced(value)
    }, ms)
    return () => {
      clearTimeout(t)
    }
  }, [value, ms])
  return debounced
}

// The open conversation is remembered for the "Recent" section.
function useRememberOpenConversation() {
  const id = useParams({ strict: false }).conversationId
  const detail = useQuery({
    ...conversationQuery(id ?? ''),
    enabled: id !== undefined,
  })
  const number = detail.data?.conversation.number
  const subject = detail.data?.conversation.subject
  useEffect(() => {
    if (id !== undefined && number !== undefined && subject !== undefined) rememberConversation({ id, number, subject })
  }, [id, number, subject])
}

function useConversationCommands(close: () => void, compose: (mode: ComposerMode) => void): Item[] {
  const id = useParams({ strict: false }).conversationId
  const detail = useQuery({
    ...conversationQuery(id ?? ''),
    enabled: id !== undefined,
  })
  const me = useQuery(meQuery)
  const apply = useConversationActions()
  const { openPicker } = usePickers()
  const c = detail.data?.conversation
  if (!c || !hasPermission(me.data, 'conversations.write')) return []

  const done = c.status === 'closed' || c.status === 'spam'
  const items: Item[] = []
  if (c.can_write) {
    items.push(
      {
        id: 'reply',
        label: 'Antwoorden',
        icon: <MessageSquareReply size={16} aria-hidden />,
        shortcut: ['r'],
        run: () => {
          compose('reply')
        },
      },
      {
        id: 'note',
        label: 'Notitie schrijven',
        icon: <NotebookPen size={16} aria-hidden />,
        shortcut: ['n'],
        run: () => {
          compose('note')
        },
      },
    )
  }
  items.push({
    id: 'close',
    label: done ? 'Heropenen' : 'Sluiten',
    icon: <StatusGlyph status={done ? 'open' : 'closed'} size={16} />,
    shortcut: ['e'],
    run: () => {
      close()
      void apply([c], { status: done ? 'open' : 'closed' })
    },
  })
  if (c.status !== 'waiting') {
    items.push({
      id: 'waiting',
      label: statusLabel.waiting,
      icon: <StatusGlyph status="waiting" size={16} />,
      shortcut: ['w'],
      run: () => {
        close()
        void apply([c], { status: 'waiting' })
      },
    })
  }
  const pickers = [
    { id: 'assign', label: 'Toewijzen aan…', icon: <UserRound size={16} aria-hidden />, key: 'a', kind: 'assign' },
    { id: 'label', label: 'Label…', icon: <Tag size={16} aria-hidden />, key: 'l', kind: 'label' },
    { id: 'snooze', label: 'Uitstellen…', icon: <ClockArrowUp size={16} aria-hidden />, key: 's', kind: 'snooze' },
    { id: 'priority', label: 'Prioriteit…', icon: <Flag size={16} aria-hidden />, key: 'p', kind: 'priority' },
  ] as const
  for (const p of pickers) {
    items.push({
      id: p.id,
      label: p.label,
      icon: p.icon,
      shortcut: [p.key],
      run: () => {
        close()
        openPicker(p.kind, [c])
      },
    })
  }
  return items
}

function Row({ item }: { item: Item }) {
  return (
    <Command.Item value={item.id} onSelect={item.run} className={itemClass}>
      <span className="shrink-0 text-muted">{item.icon}</span>
      <span className="min-w-0 flex-1 truncate">
        {item.label}
        {item.hint && <span className="text-muted"> — {item.hint}</span>}
      </span>
      {item.shortcut && (
        <span className="flex shrink-0 items-center gap-1">
          {item.shortcut.map((k, i) => (
            <Kbd key={`${k}-${i}`}>{k}</Kbd>
          ))}
        </span>
      )}
    </Command.Item>
  )
}

function Section({ heading, items }: { heading: string; items: Item[] }) {
  if (items.length === 0) return null
  return (
    <Command.Group heading={heading} className={groupClass}>
      {items.map((item) => (
        <Row key={item.id} item={item} />
      ))}
    </Command.Group>
  )
}

function Palette({ onClose, onCompose }: { onClose: () => void; onCompose: (mode: ComposerMode) => void }) {
  const navigate = useNavigate()
  const [query, setQuery] = useState('')
  const debounced = useDebounced(query.trim(), 200)
  const [recent] = useState<RecentConversation[]>(readRecent)
  const summary = useQuery(summaryQuery)
  const views = useQuery(savedViewsQuery)
  const me = useQuery(meQuery)
  const results = useQuery(searchPreviewQuery(debounced))
  const actions = useConversationCommands(onClose, onCompose)

  const go = useCallback(
    (fn: () => Promise<void>) => () => {
      onClose()
      void fn()
    },
    [onClose],
  )
  const openConversation = useCallback(
    (id: string) =>
      go(() =>
        navigate({
          to: '/inbox/$view/$conversationId',
          params: { view: 'alle', conversationId: id },
        }),
      ),
    [go, navigate],
  )

  const navigation = useMemo<Item[]>(() => {
    const items: Item[] = [
      {
        id: 'nav-mine',
        label: 'Mijn inbox',
        icon: <Inbox size={16} aria-hidden />,
        shortcut: ['g', 'i'],
        run: go(() => navigate({ to: '/inbox/$view', params: { view: 'mine' } })),
      },
      {
        id: 'nav-all',
        label: 'Alle gesprekken',
        icon: <Layers size={16} aria-hidden />,
        shortcut: ['g', 'a'],
        run: go(() => navigate({ to: '/inbox/$view', params: { view: 'alle' } })),
      },
      {
        id: 'nav-unassigned',
        label: 'Zonder toewijzing',
        icon: <ListFilter size={16} aria-hidden />,
        run: go(() =>
          navigate({
            to: '/inbox/$view',
            params: { view: 'zonder-toewijzing' },
          }),
        ),
      },
    ]
    for (const v of views.data ?? []) {
      items.push({
        id: `view-${v.id}`,
        label: v.name,
        hint: 'Weergave',
        icon: <Bookmark size={16} aria-hidden />,
        run: go(() =>
          navigate({
            to: '/inbox/$view',
            params: { view: 'alle' },
            search: { ...savedToSearch(v.filters), weergave: v.id },
          }),
        ),
      })
    }
    for (const m of summary.data?.mailboxes ?? []) {
      items.push({
        id: `mailbox-${m.id}`,
        label: m.name,
        hint: 'Mailbox',
        icon: <Mail size={16} aria-hidden />,
        run: go(() =>
          navigate({
            to: '/inbox/$view',
            params: { view: 'alle' },
            search: { mailbox: m.id },
          }),
        ),
      })
    }
    for (const t of summary.data?.teams ?? []) {
      items.push({
        id: `team-${t.id}`,
        label: t.name,
        hint: 'Team',
        icon: <UsersRound size={16} aria-hidden />,
        run: go(() =>
          navigate({
            to: '/inbox/$view',
            params: { view: 'alle' },
            search: { team: t.id },
          }),
        ),
      })
    }
    for (const page of customerPages) {
      items.push({
        id: `customers-${page.to}`,
        label: page.label,
        hint: 'Klanten',
        icon: <Users size={16} aria-hidden />,
        run: go(() => navigate({ to: page.to })),
      })
    }
    for (const page of visibleSectionPages(me.data)) {
      items.push({
        id: `section-${page.to}`,
        label: page.label,
        ...('hint' in page ? { hint: page.hint } : {}),
        icon: sectionIcons[page.to],
        run: go(() => navigate({ to: page.to })),
      })
    }
    for (const page of settingsPages) {
      if (!canOpenSettingsPage(me.data, page.access)) continue
      items.push({
        id: `settings-${page.to}`,
        label: page.label,
        hint: 'Instellingen',
        icon: <Settings size={16} aria-hidden />,
        ...(page.to === '/instellingen/profiel' ? { shortcut: ['g', 's'] } : {}),
        run: go(() => navigate({ to: page.to })),
      })
    }
    return items
  }, [views.data, summary.data, me.data, go, navigate])

  const searching = query.trim() !== ''
  const shown = (items: Item[]) => (searching ? items.filter((i) => matchesQuery(`${i.label} ${i.hint ?? ''}`, query)) : items)
  const shownActions = shown(actions)
  const shownNavigation = shown(navigation)
  const shownRecent = recent.filter((r) => !searching || matchesQuery(`${r.number} ${r.subject}`, query))

  const found: Item[] =
    isSearchable(debounced) && debounced === query.trim()
      ? (results.data?.results ?? []).map((r) => ({
          id: `conversation-${r.conversation.id}`,
          label: r.conversation.subject || 'Zonder onderwerp',
          hint: `#${r.conversation.number}`,
          icon: <MessageSquare size={16} aria-hidden />,
          run: openConversation(r.conversation.id),
        }))
      : []
  if (found.length > 0) {
    found.push({
      id: 'search-all',
      label: `Alle resultaten voor “${debounced}”`,
      icon: <Search size={16} aria-hidden />,
      run: go(() => navigate({ to: '/zoeken', search: { q: debounced } })),
    })
  }
  const recentItems: Item[] = shownRecent.map((r) => ({
    id: `recent-${r.id}`,
    label: r.subject || 'Zonder onderwerp',
    hint: `#${r.number}`,
    icon: <History size={16} aria-hidden />,
    run: openConversation(r.id),
  }))
  const total = shownActions.length + shownNavigation.length + found.length + recentItems.length
  const pending = isSearchable(query.trim()) && (results.isFetching || debounced !== query.trim())

  return (
    <Command label="Opdrachtenbalk" shouldFilter={false} loop className="flex max-h-[70vh] flex-col">
      <Command.Input
        autoFocus
        value={query}
        onValueChange={setQuery}
        placeholder="Zoek een actie, pagina of gesprek"
        aria-label="Zoek een actie, pagina of gesprek"
        className="h-14 w-full shrink-0 border-b border-line bg-transparent px-6 text-lg text-ink outline-none placeholder:text-faint"
      />
      <Command.List className="min-h-0 flex-1 overflow-y-auto p-3">
        {total === 0 && !pending && <Command.Empty className="px-2 py-6 text-center text-base text-muted">Niets gevonden.</Command.Empty>}
        {total === 0 && pending && <div className="px-2 py-6 text-center text-base text-muted">Zoeken</div>}
        <Section heading="Acties" items={shownActions} />
        <Section heading="Gesprekken" items={found} />
        <Section heading="Recent" items={recentItems} />
        <Section heading="Navigatie" items={shownNavigation} />
      </Command.List>
      <div className="flex shrink-0 items-center justify-between gap-3 border-t border-line px-6 py-3 text-sm text-muted max-md:hidden">
        <span>
          <Kbd>↵</Kbd> openen · <Kbd>↑</Kbd>
          <Kbd>↓</Kbd> navigeren · <Kbd>Esc</Kbd> sluiten
        </span>
        <span aria-live="polite">{total === 1 ? '1 resultaat' : `${total} resultaten`}</span>
      </div>
    </Command>
  )
}

export function CommandProvider({ children }: { children: ReactNode }) {
  const [paletteOpen, setPaletteOpen] = useState(false)
  const [helpOpen, setHelpOpen] = useState(false)
  const togglePalette = useCallback(() => {
    setPaletteOpen((o) => !o)
  }, [])
  const openPalette = useCallback(() => {
    setPaletteOpen(true)
  }, [])
  const openShortcuts = useCallback(() => {
    setHelpOpen(true)
  }, [])
  const closePalette = useCallback(() => {
    setPaletteOpen(false)
  }, [])
  // The dialog hands focus back to what had it when it closes, which would undo the composer's
  // own focus, so the request waits for onCloseAutoFocus.
  const pendingCompose = useRef<ComposerMode | null>(null)
  const compose = useCallback((mode: ComposerMode) => {
    pendingCompose.current = mode
    setPaletteOpen(false)
  }, [])
  useGlobalShortcuts({ onPalette: togglePalette, onHelp: openShortcuts })
  useRememberOpenConversation()
  const api = useMemo(() => ({ openPalette, openShortcuts }), [openPalette, openShortcuts])

  return (
    <CommandContext.Provider value={api}>
      {children}
      <RadixDialog.Root open={paletteOpen} onOpenChange={setPaletteOpen}>
        <RadixDialog.Portal>
          <RadixDialog.Overlay className="fixed inset-0 z-40 bg-scrim backdrop-blur-sm data-[state=open]:animate-[echoo-fade-in_var(--dur-move)_var(--ease-enter)]" />
          <RadixDialog.Content
            aria-describedby={undefined}
            onCloseAutoFocus={(e) => {
              const mode = pendingCompose.current
              if (mode === null) return
              pendingCompose.current = null
              e.preventDefault()
              requestComposer(mode)
            }}
            className="fixed top-[12vh] left-1/2 z-50 w-[calc(100vw-32px)] max-w-xl -translate-x-1/2 overflow-hidden rounded-lg border border-line bg-float shadow-float data-[state=open]:animate-[echoo-fade-in_var(--dur-move)_var(--ease-enter)]"
          >
            <RadixDialog.Title className="sr-only">Opdrachtenbalk</RadixDialog.Title>
            <Palette onClose={closePalette} onCompose={compose} />
          </RadixDialog.Content>
        </RadixDialog.Portal>
      </RadixDialog.Root>
      <ShortcutsDialog open={helpOpen} onOpenChange={setHelpOpen} />
    </CommandContext.Provider>
  )
}
