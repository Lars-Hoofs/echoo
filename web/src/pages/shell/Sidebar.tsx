import * as Menu from '@radix-ui/react-dropdown-menu'
import { useMutation, useQuery, useQueryClient, useSuspenseQuery } from '@tanstack/react-query'
import { Link, useLocation, useNavigate } from '@tanstack/react-router'
import {
  Archive,
  BookOpen,
  Building2,
  Check,
  ChevronDown,
  ChevronLeft,
  ChartColumn,
  ChevronRight,
  Contact,
  Inbox,
  Key,
  Keyboard,
  KeyRound,
  Layers,
  ListChecks,
  LogIn,
  ListFilter,
  Megaphone,
  LogOut,
  MoreHorizontal,
  Mail,
  MessageSquareText,
  Monitor,
  Moon,
  Palette,
  ScrollText,
  Settings,
  ShieldCheck,
  ShieldBan,
  SlidersHorizontal,
  SquarePen,
  Tag,
  Sun,
  UserCog,
  Timer,
  Trash2,
  UserCheck,
  Workflow,
  Zap,
  UserRound,
  Users,
  UsersRound,
  Webhook,
} from 'lucide-react'
import { type CSSProperties, type ReactNode, useState } from 'react'

import { Avatar } from '../../components/Avatar'
import { LabelDot } from '../../components/LabelChip'
import { Num } from '../../components/Num'
import { IconButton } from '../../components/ui'
import { api } from '../../lib/api'
import { labelsQuery } from '../../lib/actions'
import { type Availability, availabilityLabel } from '../../lib/automation'
import { filterKeys } from '../../lib/filters'
import { type InboxSearch, summaryQuery } from '../../lib/inbox'
import { savedViewsQuery } from '../../lib/savedViews'
import { usePersistedFlag } from '../../lib/storage'
import { hasWorkspaceSettings, type settingsPages } from '../../lib/nav'
import type { Permission } from '../../lib/permissions'
import { applyTheme, hasPermission, type Me, meQuery, type Theme, type User } from '../../lib/session'
import { NewConversationDialog } from '../inbox/composer/NewConversationDialog'
import { menuItem, menuPanel } from './menu'
import { navItemClass } from './navStyles'
import { useCommandBar } from './CommandBar'
import { NotificationsBell } from './NotificationsBell'
import { SearchField } from './SearchField'
import { ViewsNav } from './ViewsNav'

function Count({ n }: { n: number | undefined }) {
  if (!n) return null
  return <span className="tag tabular-nums">{n}</span>
}

// The accent means "the customer waits on us": unread conversations assigned to me.
function UnreadCount({ n }: { n: number | undefined }) {
  if (!n) return null
  return (
    <span className="tag tag-accent tabular-nums">
      {n}
      <span className="sr-only"> ongelezen</span>
    </span>
  )
}

function Group({ id, title, children }: { id: string; title: string; children: ReactNode }) {
  const [open, toggle] = usePersistedFlag(`echoo.nav.${id}`, true)
  const Chevron = open ? ChevronDown : ChevronRight
  return (
    <div>
      <button
        type="button"
        onClick={toggle}
        aria-expanded={open}
        className="nav-label flex w-full cursor-pointer items-center gap-1 text-left hover:text-ink"
      >
        <Chevron size={14} aria-hidden />
        {title}
      </button>
      {open && <div className="nav">{children}</div>}
    </div>
  )
}

function SearchRow({ onNavigate }: { onNavigate: () => void }) {
  const me = useQuery(meQuery)
  const [newOpen, setNewOpen] = useState(false)
  return (
    <div className="flex gap-2">
      <SearchField onNavigate={onNavigate} />
      {hasPermission(me.data, 'conversations.write') && (
        <IconButton label="Nieuw gesprek" onClick={() => setNewOpen(true)} className="shrink-0">
          <SquarePen aria-hidden />
        </IconButton>
      )}
      <NewConversationDialog open={newOpen} onOpenChange={setNewOpen} />
    </div>
  )
}

function InboxNav({ onNavigate }: { onNavigate: () => void }) {
  const summary = useQuery(summaryQuery)
  const loc = useLocation()
  const path = loc.pathname
  const search = loc.search as InboxSearch
  const view = /^\/inbox\/([^/]+)/.exec(path)?.[1]
  const filtered = filterKeys.some((k) => k !== 'status' && search[k] !== undefined) || search.weergave !== undefined
  const isView = (slug: string) => view === slug && !filtered
  const labels = useQuery(labelsQuery)
  const views = useQuery(savedViewsQuery)
  const s = summary.data
  const me = useQuery(meQuery)

  return (
    <nav aria-label="Inbox" className="nav sidebar-nav">
      <Link
        to="/inbox/$view"
        params={{ view: 'mine' }}
        onClick={onNavigate}
        aria-current={isView('mine') ? 'page' : undefined}
        className={navItemClass(isView('mine'))}
      >
        <Inbox aria-hidden />
        <span>Mijn inbox</span>
        {s?.counts.unread_mine ? <UnreadCount n={s.counts.unread_mine} /> : <Count n={s?.counts.mine} />}
      </Link>

      <Group id="conversations" title="Gesprekken">
        <Link
          to="/inbox/$view"
          params={{ view: 'alle' }}
          onClick={onNavigate}
          aria-current={isView('alle') ? 'page' : undefined}
          className={navItemClass(isView('alle'))}
        >
          <Layers aria-hidden />
          <span>Alle gesprekken</span>
          <Count n={s?.counts.all} />
        </Link>
        <Link
          to="/inbox/$view"
          params={{ view: 'zonder-toewijzing' }}
          onClick={onNavigate}
          aria-current={isView('zonder-toewijzing') ? 'page' : undefined}
          className={navItemClass(isView('zonder-toewijzing'))}
        >
          <ListFilter aria-hidden />
          <span>Zonder toewijzing</span>
          <Count n={s?.counts.unassigned} />
        </Link>
        {hasPermission(me.data, 'conversations.delete') && (
          <Link
            to="/prullenbak"
            onClick={onNavigate}
            aria-current={path.startsWith('/prullenbak') ? 'page' : undefined}
            className={navItemClass(path.startsWith('/prullenbak'))}
          >
            <Trash2 aria-hidden />
            <span>Prullenbak</span>
          </Link>
        )}
      </Group>

      {views.data && views.data.length > 0 && (
        <Group id="views" title="Weergaven">
          <ViewsNav views={views.data} activeId={search.weergave} onNavigate={onNavigate} />
        </Group>
      )}

      {hasPermission(me.data, 'contacts.read') && (
        <Group id="contacts" title="Klanten">
          <Link
            to="/contacten"
            onClick={onNavigate}
            aria-current={path.startsWith('/contacten') ? 'page' : undefined}
            className={navItemClass(path.startsWith('/contacten'))}
          >
            <Contact aria-hidden />
            <span>Contacten</span>
          </Link>
          <Link
            to="/organisaties"
            onClick={onNavigate}
            aria-current={path.startsWith('/organisaties') ? 'page' : undefined}
            className={navItemClass(path.startsWith('/organisaties'))}
          >
            <Building2 aria-hidden />
            <span>Organisaties</span>
          </Link>
          {hasPermission(me.data, 'campaigns.manage') && (
            <Link
              to="/campagnes"
              onClick={onNavigate}
              aria-current={path.startsWith('/campagnes') ? 'page' : undefined}
              className={navItemClass(path.startsWith('/campagnes'))}
            >
              <Megaphone aria-hidden />
              <span>Campagnes</span>
            </Link>
          )}
        </Group>
      )}

      {hasPermission(me.data, 'reports.view') && (
        <div className="mt-2">
          <Link
            to="/rapportage"
            onClick={onNavigate}
            aria-current={path.startsWith('/rapportage') ? 'page' : undefined}
            className={navItemClass(path.startsWith('/rapportage'))}
          >
            <ChartColumn aria-hidden />
            <span>Rapportage</span>
          </Link>
        </div>
      )}

      <Group id="kb" title="Kennisbank">
        <Link
          to="/kennisbank"
          onClick={onNavigate}
          aria-current={path.startsWith('/kennisbank') ? 'page' : undefined}
          className={navItemClass(path.startsWith('/kennisbank'))}
        >
          <BookOpen aria-hidden />
          <span>Artikelen</span>
        </Link>
      </Group>

      {s && s.teams.length > 0 && (
        <Group id="teams" title="Teams">
          {s.teams.map((t) => {
            const active = search.team === t.id
            return (
              <Link
                key={t.id}
                to="/inbox/$view"
                params={{ view: 'alle' }}
                search={{ team: t.id }}
                onClick={onNavigate}
                aria-current={active ? 'page' : undefined}
                className={navItemClass(active)}
              >
                <UsersRound aria-hidden />
                <span className="truncate">{t.name}</span>
                <Count n={t.open_count} />
              </Link>
            )
          })}
        </Group>
      )}

      {labels.data && labels.data.length > 0 && (
        <Group id="labels" title="Labels">
          {labels.data.map((l) => {
            const active = search.label === l.id
            return (
              <Link
                key={l.id}
                to="/inbox/$view"
                params={{ view: 'alle' }}
                search={{ label: l.id }}
                onClick={onNavigate}
                aria-current={active ? 'page' : undefined}
                className={navItemClass(active)}
              >
                <LabelDot color={l.color_token} className="mx-1.5" />
                <span className="truncate">{l.name}</span>
              </Link>
            )
          })}
        </Group>
      )}

      {s && s.mailboxes.length > 0 && (
        <Group id="mailboxes" title="Mailboxen">
          {s.mailboxes.map((m) => {
            const active = search.mailbox === m.id
            return (
              <Link
                key={m.id}
                to="/inbox/$view"
                params={{ view: 'alle' }}
                search={{ mailbox: m.id }}
                onClick={onNavigate}
                aria-current={active ? 'page' : undefined}
                className={navItemClass(active)}
              >
                <Mail aria-hidden />
                <span className="truncate">{m.name}</span>
                <Count n={m.open_count} />
              </Link>
            )
          })}
        </Group>
      )}
    </nav>
  )
}

function SettingsLink({
  to,
  icon,
  children,
  onNavigate,
}: {
  to: (typeof settingsPages)[number]['to']
  icon: ReactNode
  children: string
  onNavigate: () => void
}) {
  return (
    <Link
      to={to}
      onClick={onNavigate}
      className={navItemClass(false)}
      activeProps={{ className: navItemClass(true), 'aria-current': 'page' }}
      inactiveProps={{ className: navItemClass(false) }}
    >
      {icon}
      <span>{children}</span>
    </Link>
  )
}

function SettingsNav({ me, onNavigate }: { me: Me; onNavigate: () => void }) {
  const can = (permission: Permission) => hasPermission(me, permission)
  return (
    <nav aria-label="Instellingen" className="nav sidebar-nav">
      <Link to="/inbox/$view" params={{ view: 'alle' }} onClick={onNavigate} className={navItemClass(false)}>
        <ChevronLeft aria-hidden />
        <span>Terug naar inbox</span>
      </Link>
      <span className="nav-label">Persoonlijk</span>
      <div className="nav">
        <SettingsLink to="/instellingen/profiel" icon={<UserRound aria-hidden />} onNavigate={onNavigate}>
          Profiel
        </SettingsLink>
        <SettingsLink to="/instellingen/beveiliging" icon={<KeyRound aria-hidden />} onNavigate={onNavigate}>
          Beveiliging
        </SettingsLink>
        <SettingsLink to="/instellingen/standaardantwoorden" icon={<MessageSquareText aria-hidden />} onNavigate={onNavigate}>
          Standaardantwoorden
        </SettingsLink>
        <SettingsLink to="/instellingen/macros" icon={<Zap aria-hidden />} onNavigate={onNavigate}>
          Macro&apos;s
        </SettingsLink>
        <SettingsLink to="/instellingen/api-tokens" icon={<Key aria-hidden />} onNavigate={onNavigate}>
          API-tokens
        </SettingsLink>
      </div>
      {hasWorkspaceSettings(me) && (
        <>
          <span className="nav-label">Werkruimte</span>
          <div className="nav">
            {can('users.manage') && (
              <SettingsLink to="/instellingen/gebruikers" icon={<Users aria-hidden />} onNavigate={onNavigate}>
                Gebruikers
              </SettingsLink>
            )}
            {can('users.manage') && (
              <SettingsLink to="/instellingen/rollen" icon={<UserCog aria-hidden />} onNavigate={onNavigate}>
                Rollen
              </SettingsLink>
            )}
            {can('teams.manage') && (
              <SettingsLink to="/instellingen/teams" icon={<UsersRound aria-hidden />} onNavigate={onNavigate}>
                Teams
              </SettingsLink>
            )}
            {can('mailboxes.manage') && (
              <SettingsLink to="/instellingen/mailboxen" icon={<Mail aria-hidden />} onNavigate={onNavigate}>
                Mailboxen
              </SettingsLink>
            )}
            {can('labels.manage') && (
              <SettingsLink to="/instellingen/labels" icon={<Tag aria-hidden />} onNavigate={onNavigate}>
                Labels
              </SettingsLink>
            )}
            {can('conversations.delete') && (
              <SettingsLink to="/instellingen/geblokkeerde-afzenders" icon={<ShieldBan aria-hidden />} onNavigate={onNavigate}>
                Geblokkeerde afzenders
              </SettingsLink>
            )}
            {can('settings.manage') && (
              <SettingsLink to="/instellingen/velden" icon={<SlidersHorizontal aria-hidden />} onNavigate={onNavigate}>
                Aangepaste velden
              </SettingsLink>
            )}
            {can('automation.manage') && (
              <SettingsLink to="/instellingen/regels" icon={<Workflow aria-hidden />} onNavigate={onNavigate}>
                Regels
              </SettingsLink>
            )}
            {can('sla.manage') && (
              <SettingsLink to="/instellingen/sla" icon={<Timer aria-hidden />} onNavigate={onNavigate}>
                SLA
              </SettingsLink>
            )}
            {can('automation.manage') && (
              <SettingsLink to="/instellingen/toewijzing" icon={<UserCheck aria-hidden />} onNavigate={onNavigate}>
                Toewijzing
              </SettingsLink>
            )}
            {can('settings.manage') && (
              <SettingsLink to="/instellingen/werkruimte" icon={<ShieldCheck aria-hidden />} onNavigate={onNavigate}>
                Beveiliging werkruimte
              </SettingsLink>
            )}
            {me.user.role === 'owner' && (
              <SettingsLink to="/instellingen/inloggen" icon={<LogIn aria-hidden />} onNavigate={onNavigate}>
                Inloggen met SSO
              </SettingsLink>
            )}
            {can('api_tokens.manage_all') && (
              <SettingsLink to="/instellingen/tokens" icon={<Key aria-hidden />} onNavigate={onNavigate}>
                Alle API-tokens
              </SettingsLink>
            )}
            {can('webhooks.manage') && (
              <SettingsLink to="/instellingen/webhooks" icon={<Webhook aria-hidden />} onNavigate={onNavigate}>
                Webhooks
              </SettingsLink>
            )}
            {can('settings.manage') && (
              <SettingsLink to="/instellingen/taken" icon={<ListChecks aria-hidden />} onNavigate={onNavigate}>
                Taken
              </SettingsLink>
            )}
            {can('settings.manage') && (
              <SettingsLink to="/instellingen/privacy" icon={<Archive aria-hidden />} onNavigate={onNavigate}>
                Privacy en retentie
              </SettingsLink>
            )}
            {can('audit.view') && (
              <SettingsLink to="/instellingen/auditlog" icon={<ScrollText aria-hidden />} onNavigate={onNavigate}>
                Auditlog
              </SettingsLink>
            )}
          </div>
        </>
      )}
    </nav>
  )
}

// The dot is only a hint next to the text label.
const availabilityDot: Record<Availability, string> = {
  online: 'bg-ink',
  busy: 'border border-ink bg-transparent',
  offline: 'bg-line-strong',
}

const themes: { value: Theme; label: string; icon: ReactNode }[] = [
  { value: 'system', label: 'Systeem', icon: <Monitor aria-hidden /> },
  { value: 'light', label: 'Licht', icon: <Sun aria-hidden /> },
  { value: 'dark', label: 'Donker', icon: <Moon aria-hidden /> },
]

function UserMenu({ user, onNavigate }: { user: User; onNavigate: () => void }) {
  const me = useQuery(meQuery)
  const { openShortcuts } = useCommandBar()
  const queryClient = useQueryClient()
  const navigate = useNavigate()
  const setTheme = useMutation({
    mutationFn: (theme: Theme) => api<{ user: User }>('PATCH', '/me', { theme }),
    onMutate: (theme) => {
      applyTheme(theme)
    },
    onSuccess: (r) => {
      queryClient.setQueryData<Me>(['me'], (old) => (old ? { ...old, user: r.user } : old))
    },
    onError: () => {
      applyTheme(user.theme)
    },
  })
  const setAvailability = useMutation({
    mutationFn: (availability: Availability) => api<{ availability: Availability }>('PUT', '/me/availability', { availability }),
    onSuccess: (r) => {
      queryClient.setQueryData<Me>(['me'], (old) => (old ? { ...old, user: { ...old.user, availability: r.availability } } : old))
    },
  })
  const logout = useMutation({
    mutationFn: () => api('POST', '/auth/logout'),
    onSuccess: async () => {
      queryClient.clear()
      await navigate({ to: '/inloggen' })
    },
  })

  return (
    <Menu.Root>
      <Menu.Trigger
        className="flex w-full cursor-pointer items-center gap-3 rounded-full p-2 text-left transition-colors hover:bg-selected data-[state=open]:bg-selected"
        aria-label={`Gebruikersmenu voor ${user.name}`}
      >
        <Avatar name={user.name} size={32} />
        <span className="min-w-0 flex-1">
          <span className="block truncate text-base leading-5 text-ink">{user.name}</span>
          <span className="t-label block truncate">{user.email}</span>
        </span>
        <MoreHorizontal aria-hidden className="shrink-0 text-muted" />
      </Menu.Trigger>
      <Menu.Portal>
        <Menu.Content side="top" align="start" sideOffset={6} className={`${menuPanel} min-w-56`}>
          <Menu.Item asChild className={menuItem}>
            <Link to="/instellingen/profiel" onClick={onNavigate}>
              <UserRound aria-hidden /> Profiel
            </Link>
          </Menu.Item>
          <Menu.Item asChild className={menuItem}>
            <Link to="/instellingen/beveiliging" onClick={onNavigate}>
              <KeyRound aria-hidden /> Beveiliging
            </Link>
          </Menu.Item>
          {hasPermission(me.data, 'conversations.write') && (
            <Menu.Sub>
              <Menu.SubTrigger className={`${menuItem} data-[state=open]:bg-subtle`}>
                <span aria-hidden className={`size-2 rounded-full ${availabilityDot[user.availability]}`} /> Beschikbaarheid:{' '}
                {availabilityLabel[user.availability].toLowerCase()}
                <ChevronRight aria-hidden className="ml-auto text-muted" />
              </Menu.SubTrigger>
              <Menu.Portal>
                <Menu.SubContent sideOffset={4} className={menuPanel}>
                  <Menu.RadioGroup value={user.availability} onValueChange={(v) => setAvailability.mutate(v as Availability)}>
                    {(Object.keys(availabilityLabel) as Availability[]).map((a) => (
                      <Menu.RadioItem key={a} value={a} className={menuItem}>
                        <span aria-hidden className={`size-2 rounded-full ${availabilityDot[a]}`} /> {availabilityLabel[a]}
                        <Menu.ItemIndicator className="ml-auto">
                          <Check aria-hidden />
                        </Menu.ItemIndicator>
                      </Menu.RadioItem>
                    ))}
                  </Menu.RadioGroup>
                </Menu.SubContent>
              </Menu.Portal>
            </Menu.Sub>
          )}
          <Menu.Item className={menuItem} onSelect={openShortcuts}>
            <Keyboard aria-hidden /> Sneltoetsen
          </Menu.Item>
          <Menu.Sub>
            <Menu.SubTrigger className={`${menuItem} data-[state=open]:bg-subtle`}>
              <Palette aria-hidden /> Thema
              <ChevronRight aria-hidden className="ml-auto text-muted" />
            </Menu.SubTrigger>
            <Menu.Portal>
              <Menu.SubContent sideOffset={4} className={menuPanel}>
                <Menu.RadioGroup value={user.theme} onValueChange={(v) => setTheme.mutate(v as Theme)}>
                  {themes.map((t) => (
                    <Menu.RadioItem key={t.value} value={t.value} className={menuItem}>
                      {t.icon} {t.label}
                      <Menu.ItemIndicator className="ml-auto">
                        <Check aria-hidden />
                      </Menu.ItemIndicator>
                    </Menu.RadioItem>
                  ))}
                </Menu.RadioGroup>
              </Menu.SubContent>
            </Menu.Portal>
          </Menu.Sub>
          <Menu.Separator className="my-2 h-px bg-line" />
          <Menu.Item className={menuItem} onSelect={() => logout.mutate()}>
            <LogOut aria-hidden /> Uitloggen
          </Menu.Item>
        </Menu.Content>
      </Menu.Portal>
    </Menu.Root>
  )
}

// The personal number that matters most: open conversations against the user's capacity
// (users.max_open). Progress of one's own load is not "the customer waits", so it is ink.
function CapacityCard() {
  const me = useQuery(meQuery)
  const summary = useQuery(summaryQuery)
  if (!hasPermission(me.data, 'conversations.write') || !summary.data) return null
  const open = summary.data.counts.mine
  const capacity = me.data?.user.max_open ?? null
  return (
    <div className="card card-s stack">
      <div className="row-between">
        <span className="t-label">Jouw open gesprekken</span>
        <span className="t-label">{capacity === null ? 'zonder limiet' : `van ${capacity}`}</span>
      </div>
      <Num value={open} size="m" />
      {capacity !== null && (
        <div
          className="progress progress-ink"
          role="progressbar"
          aria-label="Open gesprekken ten opzichte van je limiet"
          aria-valuemin={0}
          aria-valuemax={capacity}
          aria-valuenow={Math.min(open, capacity)}
        >
          <i style={{ '--w': `${Math.min(100, Math.round((open / capacity) * 100))}%` } as CSSProperties} />
        </div>
      )}
    </div>
  )
}

function mailboxSubtitle(mailboxes: { name: string }[] | undefined): string | null {
  if (!mailboxes || mailboxes.length === 0) return null
  const [only] = mailboxes
  return mailboxes.length === 1 && only ? only.name : `${mailboxes.length} mailboxen`
}

// inSheet leaves room in the header for the sheet's close button.
export function SidebarBody({ onNavigate, inSheet = false }: { onNavigate: () => void; inSheet?: boolean }) {
  const { data: me } = useSuspenseQuery(meQuery)
  const summary = useQuery(summaryQuery)
  const inSettings = useLocation({ select: (l) => l.pathname.startsWith('/instellingen') })
  const subtitle = mailboxSubtitle(summary.data?.mailboxes)
  return (
    <div className="flex h-full flex-col gap-4 px-4 py-6">
      <div className={`flex items-center gap-3 pl-2 ${inSheet ? 'pr-12' : ''}`}>
        <span aria-hidden className="logo">
          e
        </span>
        <div className="stack s min-w-0 flex-1">
          <span className="t-title">Echoo</span>
          {subtitle && <span className="t-label truncate">{subtitle}</span>}
        </div>
        <NotificationsBell />
      </div>
      <SearchRow onNavigate={onNavigate} />
      {inSettings ? <SettingsNav me={me} onNavigate={onNavigate} /> : <InboxNav onNavigate={onNavigate} />}
      <div className="flex flex-col gap-3">
        {!inSettings && (
          <Link to="/instellingen/profiel" onClick={onNavigate} className={navItemClass(false)}>
            <Settings aria-hidden />
            <span>Instellingen</span>
          </Link>
        )}
        <CapacityCard />
        <UserMenu user={me.user} onNavigate={onNavigate} />
      </div>
    </div>
  )
}
