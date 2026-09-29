import { useNavigate } from '@tanstack/react-router'
import { Search } from 'lucide-react'
import { type SubmitEvent, useState } from 'react'

import { Kbd } from '../../components/ui'
import { isSearchable } from '../../lib/search'

// Enter opens the search page; "/" focuses this field from anywhere.
export function SearchField({ onNavigate }: { onNavigate: () => void }) {
  const navigate = useNavigate()
  const [q, setQ] = useState('')

  const submit = (e: SubmitEvent<HTMLFormElement>) => {
    e.preventDefault()
    const query = q.trim()
    if (!isSearchable(query)) return
    onNavigate()
    void navigate({ to: '/zoeken', search: { q: query } })
  }

  return (
    <form role="search" onSubmit={submit} className="field min-w-0 flex-1">
      <Search aria-hidden className="shrink-0" />
      <input
        type="search"
        data-search-input
        value={q}
        onChange={(e) => setQ(e.target.value)}
        onKeyDown={(e) => {
          if (e.key === 'Escape') e.currentTarget.blur()
        }}
        placeholder="Zoeken"
        aria-label="Zoeken"
        className="max-md:text-lg [&::-webkit-search-cancel-button]:hidden"
      />
      {q === '' && <Kbd>/</Kbd>}
    </form>
  )
}
