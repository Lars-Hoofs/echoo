import * as Menu from '@radix-ui/react-dropdown-menu'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useNavigate } from '@tanstack/react-router'
import { MoreHorizontal, Pencil, Trash2, X } from 'lucide-react'
import { useState } from 'react'

import { Dialog, DialogFooter } from '../../components/Dialog'
import { useToast } from '../../components/Toast'
import { Button, ErrorNotice } from '../../components/ui'
import { errorMessage } from '../../lib/errors'
import { chipsFor, hasFilters, sameFilters, savedToSearch, searchToSaved, withoutFilter } from '../../lib/filters'
import { type InboxSearch, type ViewSlug, summaryQuery } from '../../lib/inbox'
import { deleteSavedView, savedViewsQuery, type SavedView, updateSavedView } from '../../lib/savedViews'
import { hasPermission, meQuery } from '../../lib/session'
import { menuItem, menuPanel } from '../shell/menu'
import { SaveViewDialog } from './SaveViewDialog'
import { useFilterLookups } from './useFilterLookups'

type Dialogs = 'save' | 'rename' | 'delete' | null

// Filters without the saved view they started from: what actually selects rows.
function filtersOnly(search: InboxSearch): InboxSearch {
  const rest = { ...search }
  delete rest.weergave
  return rest
}

// The chips under the list header, and the ways to keep them: save as a view, or change the view
// the list started from.
export function FilterBar({ slug, search }: { slug: ViewSlug; search: InboxSearch }) {
  const navigate = useNavigate()
  const queryClient = useQueryClient()
  const toast = useToast()
  const { lookups } = useFilterLookups()
  const views = useQuery(savedViewsQuery)
  const summary = useQuery(summaryQuery)
  const me = useQuery(meQuery)
  const [dialog, setDialog] = useState<Dialogs>(null)

  const active: SavedView | undefined = views.data?.find((v) => v.id === search.weergave)
  const filters = filtersOnly(search)
  const chips = chipsFor(filters, lookups)
  const modified = active !== undefined && !sameFilters(filters, savedToSearch(active.filters))
  const go = (next: InboxSearch) => {
    void navigate({ to: '/inbox/$view', params: { view: slug }, search: next })
  }
  const refresh = () => queryClient.invalidateQueries({ queryKey: ['inbox', 'saved-views'] })

  const update = useMutation({
    mutationFn: (view: SavedView) => updateSavedView(view.id, { filters: searchToSaved(filters) }),
    onSuccess: async () => {
      await refresh()
      toast('Weergave opgeslagen')
    },
  })
  const remove = useMutation({
    mutationFn: (view: SavedView) => deleteSavedView(view.id),
    onSuccess: async () => {
      await refresh()
      setDialog(null)
      go({})
      toast('Weergave verwijderd')
    },
  })

  if (chips.length === 0 && !active) return null

  return (
    <div className="flex shrink-0 flex-wrap items-center gap-2 border-b border-line px-4 pb-4">
      {chips.map((chip) => (
        <span key={chip.key} className="inline-flex h-8 max-w-full items-center gap-1 rounded-full bg-subtle pr-1 pl-3 text-sm text-ink max-md:h-11">
          <span className="truncate">{chip.text}</span>
          <button
            type="button"
            aria-label={`Filter verwijderen: ${chip.text}`}
            onClick={() => go(withoutFilter(search, chip.key))}
            className="inline-flex size-6 shrink-0 items-center justify-center rounded-full text-muted hover:bg-active hover:text-ink max-md:size-9"
          >
            <X size={16} aria-hidden />
          </button>
        </span>
      ))}
      {modified && (
        <>
          <Button size="sm" onClick={() => go({ ...savedToSearch(active.filters), weergave: active.id })}>
            Wijzigingen verwerpen
          </Button>
          {active.editable && (
            <Button size="sm" variant="primary" busy={update.isPending} onClick={() => update.mutate(active)}>
              Opslaan
            </Button>
          )}
          <Button size="sm" onClick={() => setDialog('save')}>
            Opslaan als nieuwe weergave
          </Button>
        </>
      )}
      {!active && hasFilters(filters) && (
        <Button size="sm" onClick={() => setDialog('save')}>
          Opslaan als weergave
        </Button>
      )}
      {active?.editable && !modified && (
        <Menu.Root>
          <Menu.Trigger
            aria-label="Weergave beheren"
            title="Weergave beheren"
            className="icon-btn icon-btn-s max-md:size-11"
          >
            <MoreHorizontal aria-hidden />
          </Menu.Trigger>
          <Menu.Portal>
            <Menu.Content align="start" sideOffset={4} className={menuPanel}>
              <Menu.Item className={menuItem} onSelect={() => setDialog('rename')}>
                <Pencil aria-hidden /> Naam wijzigen
              </Menu.Item>
              <Menu.Item className={`${menuItem} text-danger-text`} onSelect={() => setDialog('delete')}>
                <Trash2 aria-hidden /> Weergave verwijderen
              </Menu.Item>
            </Menu.Content>
          </Menu.Portal>
        </Menu.Root>
      )}
      {update.isError && <ErrorNotice>{errorMessage(update.error)}</ErrorNotice>}

      {(dialog === 'save' || dialog === 'rename') && (
        <SaveViewDialog
          search={filters}
          rename={dialog === 'rename' ? (active ?? null) : null}
          canShare={hasPermission(me.data, 'templates.manage_shared')}
          teams={summary.data?.teams ?? []}
          onClose={() => setDialog(null)}
        />
      )}
      {dialog === 'delete' && active && (
        <Dialog open onOpenChange={(o) => !o && setDialog(null)} title="Weergave verwijderen" size="sm">
          <div className="flex flex-col gap-4">
            {remove.isError && <ErrorNotice>{errorMessage(remove.error)}</ErrorNotice>}
            <p className="text-base text-ink">
              Weergave “{active.name}” verwijderen? De gesprekken zelf blijven bestaan.
            </p>
            <DialogFooter>
              <Button onClick={() => setDialog(null)}>Annuleren</Button>
              <Button variant="danger" busy={remove.isPending} onClick={() => remove.mutate(active)}>
                Verwijderen
              </Button>
            </DialogFooter>
          </div>
        </Dialog>
      )}
    </div>
  )
}
