import { useQuery } from '@tanstack/react-query'
import { Search } from 'lucide-react'
import { useEffect, useState } from 'react'

import { Dialog } from '../../components/Dialog'
import { ErrorNotice, Input, Skeleton } from '../../components/ui'
import { errorMessage } from '../../lib/errors'
import { type KbArticle, kbArticlesQuery } from '../../lib/kb'

// Lets an agent find a published article and put a link to it in a reply.
export function ArticlePicker({ open, onOpenChange, onPick }: { open: boolean; onOpenChange: (open: boolean) => void; onPick: (article: KbArticle) => void }) {
  return (
    <Dialog open={open} onOpenChange={onOpenChange} title="Artikel invoegen" description="Zoek een gepubliceerd artikel. Er komt een link met de titel in je antwoord." size="lg">
      <PickerBody onPick={onPick} />
    </Dialog>
  )
}

function PickerBody({ onPick }: { onPick: (article: KbArticle) => void }) {
  const [q, setQ] = useState('')
  const [debounced, setDebounced] = useState('')
  useEffect(() => {
    const t = setTimeout(() => setDebounced(q.trim()), 250)
    return () => clearTimeout(t)
  }, [q])
  const list = useQuery(kbArticlesQuery({ status: 'published', categoryId: '', q: debounced }))

  return (
    <div className="flex flex-col gap-3">
      <div className="relative">
        <Search size={16} aria-hidden className="pointer-events-none absolute top-1/2 left-2.5 -translate-y-1/2 text-faint" />
        <Input aria-label="Zoek een artikel" placeholder="Zoek op titel of tekst" value={q} onChange={(e) => setQ(e.target.value)} className="pl-8" autoFocus />
      </div>
      {list.isPending ? (
        <Skeleton className="h-32" />
      ) : list.isError ? (
        <ErrorNotice>{errorMessage(list.error)}</ErrorNotice>
      ) : list.data.length === 0 ? (
        <p className="py-4 text-center text-base text-muted">{debounced ? 'Geen artikelen gevonden.' : 'Er zijn nog geen gepubliceerde artikelen.'}</p>
      ) : (
        <ul aria-label="Artikelen" className="-mx-2 max-h-72 overflow-y-auto">
          {list.data.map((a) => (
            <li key={a.id}>
              <button type="button" onClick={() => onPick(a)} className="flex w-full flex-col rounded-md px-2 py-2 text-left hover:bg-subtle focus-visible:bg-subtle">
                <span className="truncate text-base text-ink">{a.title}</span>
                <span className="truncate text-sm text-faint">{a.category_name}</span>
              </button>
            </li>
          ))}
        </ul>
      )}
    </div>
  )
}
