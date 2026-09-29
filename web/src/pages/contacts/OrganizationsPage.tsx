import { useInfiniteQuery, useQuery } from '@tanstack/react-query'
import { Link, useNavigate } from '@tanstack/react-router'
import { Building2, Plus, Search } from 'lucide-react'
import { useEffect, useMemo, useState } from 'react'

import { Avatar } from '../../components/Avatar'
import { Button, Card, EmptyState, ErrorNotice, Input, PageHeader, Skeleton, Table, TBody, Td, Th, THead, Tr } from '../../components/ui'
import { api } from '../../lib/api'
import type { OrganizationItem } from '../../lib/contacts'
import { errorMessage } from '../../lib/errors'
import { hasPermission, meQuery } from '../../lib/session'
import { OrganizationDialog } from './OrganizationDialog'

interface OrganizationPage {
  organizations: OrganizationItem[]
  next_cursor: string | null
}

export function OrganizationsPage() {
  const me = useQuery(meQuery)
  const navigate = useNavigate()
  const [q, setQ] = useState('')
  const [debounced, setDebounced] = useState('')
  const [creating, setCreating] = useState(false)
  useEffect(() => {
    const t = setTimeout(() => setDebounced(q.trim()), 300)
    return () => clearTimeout(t)
  }, [q])
  const list = useInfiniteQuery({
    queryKey: ['organizations', 'list', debounced],
    initialPageParam: '',
    queryFn: ({ pageParam }) => {
      const s = new URLSearchParams()
      if (debounced) s.set('q', debounced)
      if (pageParam) s.set('cursor', pageParam)
      return api<OrganizationPage>('GET', `/organizations?${s.toString()}`)
    },
    getNextPageParam: (last) => last.next_cursor ?? undefined,
  })
  const rows = useMemo(() => list.data?.pages.flatMap((p) => p.organizations) ?? [], [list.data])
  return (
    <div className="relative h-full overflow-y-auto px-4 py-8 md:px-8">
      <div className="mx-auto flex w-full max-w-5xl flex-col gap-6">
        <PageHeader
          breadcrumb="Klanten"
          title="Organisaties"
          description="Bedrijven waar je contacten bij horen. Ze worden automatisch herkend aan het domein van het e-mailadres."
          actions={
            hasPermission(me.data, 'contacts.write') ? (
              <Button variant="primary" onClick={() => setCreating(true)}>
                <Plus size={16} aria-hidden />
                Organisatie toevoegen
              </Button>
            ) : undefined
          }
        />
        <div className="relative max-w-md">
          <Search size={16} aria-hidden className="pointer-events-none absolute top-1/2 left-4 -translate-y-1/2 text-faint" />
          <Input
            aria-label="Zoeken in organisaties"
            placeholder="Zoek op naam of domein"
            className="pl-10"
            value={q}
            onChange={(e) => setQ(e.target.value)}
          />
        </div>
        {list.isPending ? (
          <Skeleton className="h-48" />
        ) : list.isError ? (
          <ErrorNotice>{errorMessage(list.error)}</ErrorNotice>
        ) : rows.length === 0 ? (
          <Card>
            <EmptyState icon={<Building2 size={20} />} title={debounced ? 'Geen organisaties gevonden' : 'Nog geen organisaties'} />
          </Card>
        ) : (
          <>
            <Card flush>
              <Table>
                <THead>
                  <Th>Naam</Th>
                  <Th className="hidden md:table-cell">Domeinen</Th>
                  <Th numeric>Contacten</Th>
                </THead>
                <TBody>
                  {rows.map((o) => (
                    <Tr key={o.id}>
                      <Td>
                        <Link to="/organisaties/$id" params={{ id: o.id }} className="flex min-w-0 items-center gap-3 text-ink hover:underline">
                          <Avatar name={o.name} />
                          <span className="truncate">{o.name}</span>
                        </Link>
                      </Td>
                      <Td className="hidden text-muted md:table-cell">{o.domains.join(', ') || '—'}</Td>
                      <Td numeric>{o.contact_count}</Td>
                    </Tr>
                  ))}
                </TBody>
              </Table>
            </Card>
            {list.hasNextPage && (
              <div className="flex justify-center">
                <Button busy={list.isFetchingNextPage} onClick={() => void list.fetchNextPage()}>
                  Meer laden
                </Button>
              </div>
            )}
          </>
        )}
        {creating && (
          <OrganizationDialog
            onClose={() => setCreating(false)}
            onSaved={(id) => void navigate({ to: '/organisaties/$id', params: { id } })}
          />
        )}
      </div>
    </div>
  )
}
