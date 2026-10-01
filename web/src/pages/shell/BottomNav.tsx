import { useQuery } from '@tanstack/react-query'
import { Link, useLocation } from '@tanstack/react-router'
import { Inbox, Layers, Menu, Search } from 'lucide-react'
import type { ReactNode } from 'react'

import { summaryQuery } from '../../lib/inbox'

// The phone's tab bar: the two lists an agent on call needs, search, and the full navigation
// behind "Meer". Only the unread count of "Mijn" is accent: the customer waits on us there.
export function BottomNav({ onMore }: { onMore: () => void }) {
  const path = useLocation({ select: (l) => l.pathname })
  const summary = useQuery(summaryQuery)
  const counts = summary.data?.counts

  return (
    <nav aria-label="Snelmenu" className="shrink-0 px-3 pt-2 pb-[max(0.5rem,env(safe-area-inset-bottom))] min-[1000px]:hidden">
      <div className="mx-auto flex max-w-md items-stretch gap-1 rounded-full border border-line bg-surface p-1">
        <Tab to="/inbox/mine" active={path.startsWith('/inbox/mine')} icon={<Inbox aria-hidden />} label="Mijn">
          {counts && counts.unread_mine > 0 && (
            <span className="tag tag-accent absolute -top-1 right-2 h-5 min-w-5 px-1 text-xs tabular-nums">
              {counts.unread_mine}
              <span className="sr-only"> ongelezen</span>
            </span>
          )}
        </Tab>
        <Tab to="/inbox/alle" active={path.startsWith('/inbox/alle') || path.startsWith('/inbox/zonder-toewijzing')} icon={<Layers aria-hidden />} label="Alle" />
        <Tab to="/zoeken" active={path.startsWith('/zoeken')} icon={<Search aria-hidden />} label="Zoeken" />
        <button type="button" onClick={onMore} className={tabClass(false)}>
          <Menu aria-hidden />
          <span>Meer</span>
        </button>
      </div>
    </nav>
  )
}

function tabClass(active: boolean) {
  return `relative flex h-14 flex-1 flex-col items-center justify-center gap-1 rounded-full text-xs transition-colors duration-(--dur-press) ease-(--ease-move) [&>svg]:size-5 ${
    active ? 'bg-ink text-surface' : 'text-muted active:bg-subtle'
  }`
}

function Tab({ to, active, icon, label, children }: { to: string; active: boolean; icon: ReactNode; label: string; children?: ReactNode }) {
  return (
    <Link to={to} aria-current={active ? 'page' : undefined} className={tabClass(active)}>
      {icon}
      <span>{label}</span>
      {children}
    </Link>
  )
}
