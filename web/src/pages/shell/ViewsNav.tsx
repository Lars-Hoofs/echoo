import { Link } from '@tanstack/react-router'

import { savedToSearch } from '../../lib/filters'
import { formatViewCount, type SavedView } from '../../lib/savedViews'
import { navItemClass } from './navStyles'

export function ViewsNav({ views, activeId, onNavigate }: { views: SavedView[]; activeId: string | undefined; onNavigate: () => void }) {
  return (
    <>
      {views.map((v) => {
        const active = activeId === v.id
        return (
          <Link
            key={v.id}
            to="/inbox/$view"
            params={{ view: 'alle' }}
            search={{ ...savedToSearch(v.filters), weergave: v.id }}
            onClick={onNavigate}
            aria-current={active ? 'page' : undefined}
            className={navItemClass(active)}
          >
            <i aria-hidden className="swatch" />
            <span className="truncate">{v.name}</span>
            {v.open_count ? <span className="tag tabular-nums">{formatViewCount(v.open_count)}</span> : null}
          </Link>
        )
      })}
    </>
  )
}
