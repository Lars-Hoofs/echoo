import { useInfiniteQuery, useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Link, useNavigate } from '@tanstack/react-router'
import { Download, ListFilter, Plus, Search, Upload, Users } from 'lucide-react'
import { type SubmitEvent, useEffect, useMemo, useState } from 'react'

import { Avatar } from '../../components/Avatar'
import { Dialog, DialogFooter } from '../../components/Dialog'
import {
  Badge,
  Button,
  buttonClass,
  Card,
  EmptyState,
  ErrorNotice,
  Field,
  Input,
  PageHeader,
  Select,
  Skeleton,
  Switch,
  Table,
  TBody,
  Td,
  Th,
  THead,
  Tr,
} from '../../components/ui'
import { api } from '../../lib/api'
import {
  attributeDefsQuery,
  type ContactFilter,
  type ContactItem,
  contactDisplayName,
  contactQueryString,
  type ContactQuery,
  emptyFilter,
  filterIsActive,
  cleanFilter,
  type Segment,
  segmentsQuery,
} from '../../lib/contacts'
import { errorMessage, fieldError } from '../../lib/errors'
import { formatRelative } from '../../lib/format'
import { hasPermission, meQuery } from '../../lib/session'
import { ContactDialog } from './ContactDialog'
import { FilterBuilder } from './FilterBuilder'
import { ImportDialog } from './ImportDialog'

interface ContactPage {
  contacts: ContactItem[]
  next_cursor: string | null
}

const sortOptions: {
  value: string
  label: string
  sort: ContactQuery['sort']
  dir: ContactQuery['dir']
}[] = [
  {
    value: 'last_activity:desc',
    label: 'Laatste activiteit',
    sort: 'last_activity',
    dir: 'desc',
  },
  { value: 'name:asc', label: 'Naam A-Z', sort: 'name', dir: 'asc' },
  { value: 'name:desc', label: 'Naam Z-A', sort: 'name', dir: 'desc' },
  {
    value: 'created:desc',
    label: 'Nieuwste eerst',
    sort: 'created',
    dir: 'desc',
  },
  { value: 'created:asc', label: 'Oudste eerst', sort: 'created', dir: 'asc' },
]

const sameFilter = (a: ContactFilter, b: ContactFilter) => JSON.stringify(cleanFilter(a)) === JSON.stringify(cleanFilter(b))

export function ContactsPage() {
  const me = useQuery(meQuery)
  const navigate = useNavigate()
  const [q, setQ] = useState('')
  const [debounced, setDebounced] = useState('')
  const [sortValue, setSortValue] = useState('last_activity:desc')
  const [filter, setFilter] = useState<ContactFilter>(emptyFilter)
  const [showFilters, setShowFilters] = useState(false)
  const [segmentId, setSegmentId] = useState('')
  const [dialog, setDialog] = useState<'new' | 'import' | 'save-segment' | 'edit-segment' | 'delete-segment' | null>(null)
  const defs = useQuery(attributeDefsQuery())
  const segments = useQuery(segmentsQuery)
  const segment = segments.data?.find((s) => s.id === segmentId)

  useEffect(() => {
    const t = setTimeout(() => setDebounced(q), 300)
    return () => clearTimeout(t)
  }, [q])

  const sort = sortOptions.find((o) => o.value === sortValue) ?? sortOptions[0]
  const query: ContactQuery = {
    q: debounced,
    sort: sort?.sort ?? 'last_activity',
    dir: sort?.dir ?? 'desc',
    segmentId: '',
    filter,
  }
  const qs = contactQueryString(query)
  const list = useInfiniteQuery({
    queryKey: ['contacts', qs],
    initialPageParam: '',
    queryFn: ({ pageParam }) => api<ContactPage>('GET', `/contacts?${qs}${pageParam ? `&cursor=${encodeURIComponent(pageParam)}` : ''}`),
    getNextPageParam: (last) => last.next_cursor ?? undefined,
  })
  const rows = useMemo(() => list.data?.pages.flatMap((p) => p.contacts) ?? [], [list.data])
  const active = filterIsActive(filter)
  const dirty = segment ? !sameFilter(segment.filter, filter) : false
  const queryClient = useQueryClient()
  const updateSegment = useMutation({
    mutationFn: () =>
      api('PATCH', `/contact-segments/${segmentId}`, {
        filter: cleanFilter(filter),
      }),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ['contact-segments'] }),
  })

  const pickSegment = (id: string) => {
    setSegmentId(id)
    const s = segments.data?.find((x) => x.id === id)
    setFilter(s ? s.filter : emptyFilter)
    if (s) setShowFilters(true)
  }

  return (
    <div className="relative h-full overflow-y-auto px-4 py-8 md:px-8">
      <div className="mx-auto flex w-full max-w-5xl flex-col gap-6">
        <PageHeader
          breadcrumb="Klanten"
          title="Contacten"
          description="Iedereen met wie je mailt, met hun gesprekken, notities en velden."
          actions={
            <>
              {hasPermission(me.data, 'contacts.import') && (
                <Button onClick={() => setDialog('import')}>
                  <Upload size={16} aria-hidden />
                  Importeren
                </Button>
              )}
              <a href={`/api/v1/contacts/export?${qs}`} className={buttonClass()}>
                <Download size={16} aria-hidden />
                Exporteren
              </a>
              {hasPermission(me.data, 'contacts.write') && (
                <Button variant="primary" onClick={() => setDialog('new')}>
                  <Plus size={16} aria-hidden />
                  Contact toevoegen
                </Button>
              )}
            </>
          }
        />
        <div className="flex flex-wrap items-center gap-2">
          <div className="relative min-w-56 flex-1">
            <Search size={16} aria-hidden className="pointer-events-none absolute top-1/2 left-4 -translate-y-1/2 text-faint" />
            <Input
              aria-label="Zoeken in contacten"
              placeholder="Zoek op naam, e-mailadres of organisatie"
              className="pl-10"
              value={q}
              onChange={(e) => setQ(e.target.value)}
            />
          </div>
          <Select className="w-52" aria-label="Sorteren" value={sortValue} onChange={(e) => setSortValue(e.target.value)}>
            {sortOptions.map((o) => (
              <option key={o.value} value={o.value}>
                {o.label}
              </option>
            ))}
          </Select>
          <Select className="w-52" aria-label="Segment" value={segmentId} onChange={(e) => pickSegment(e.target.value)}>
            <option value="">Alle contacten</option>
            {segments.data?.map((s) => (
              <option key={s.id} value={s.id}>
                {s.name}
                {s.shared ? ' (gedeeld)' : ''}
              </option>
            ))}
          </Select>
          <Button aria-expanded={showFilters} onClick={() => setShowFilters((v) => !v)}>
            <ListFilter size={16} aria-hidden />
            Filters
            {active && <Badge>{cleanFilter(filter).conditions.length}</Badge>}
          </Button>
        </div>
        {showFilters && (
          <Card>
            <div className="flex flex-col gap-4">
              <FilterBuilder filter={filter} defs={defs.data ?? []} onChange={setFilter} />
              <div className="flex flex-wrap items-center gap-2">
                {segment ? (
                  <>
                    {segment.can_edit && (
                      <Button
                        size="sm"
                        variant="primary"
                        disabled={!dirty}
                        busy={updateSegment.isPending}
                        onClick={() => updateSegment.mutate()}
                      >
                        Segment bijwerken
                      </Button>
                    )}
                    {segment.can_edit && (
                      <Button size="sm" onClick={() => setDialog('edit-segment')}>
                        Segment bewerken
                      </Button>
                    )}
                    {segment.can_edit && (
                      <Button size="sm" onClick={() => setDialog('delete-segment')}>
                        Segment verwijderen
                      </Button>
                    )}
                  </>
                ) : (
                  active &&
                  hasPermission(me.data, 'contacts.write') && (
                    <Button size="sm" onClick={() => setDialog('save-segment')}>
                      Segment opslaan
                    </Button>
                  )
                )}
                {active && (
                  <Button
                    size="sm"
                    variant="ghost"
                    onClick={() => {
                      setFilter(emptyFilter)
                      setSegmentId('')
                    }}
                  >
                    Filters wissen
                  </Button>
                )}
              </div>
              {updateSegment.isError && <ErrorNotice>{errorMessage(updateSegment.error)}</ErrorNotice>}
            </div>
          </Card>
        )}
        {list.isPending ? (
          <Skeleton className="h-64" />
        ) : list.isError ? (
          <ErrorNotice>{errorMessage(list.error)}</ErrorNotice>
        ) : rows.length === 0 ? (
          <Card>
            <EmptyState
              icon={<Users size={20} />}
              title={debounced || active ? 'Geen contacten gevonden' : 'Nog geen contacten'}
              description={
                debounced || active
                  ? 'Pas je zoekopdracht of filters aan.'
                  : 'Contacten verschijnen zodra er mail binnenkomt, of voeg er zelf een toe.'
              }
            />
          </Card>
        ) : (
          <>
            <Card flush>
              <Table>
                <THead>
                  <Th>Naam</Th>
                  <Th className="hidden md:table-cell">E-mail</Th>
                  <Th className="hidden lg:table-cell">Organisatie</Th>
                  <Th className="hidden sm:table-cell">Laatste activiteit</Th>
                  <Th numeric>Gesprekken</Th>
                </THead>
                <TBody>
                  {rows.map((c) => (
                    <Tr key={c.id}>
                      <Td>
                        <Link
                          to="/contacten/$id"
                          params={{ id: c.id }}
                          className="flex min-w-0 items-center gap-3 text-ink hover:underline"
                        >
                          <Avatar name={contactDisplayName(c)} />
                          <span className="truncate">{contactDisplayName(c)}</span>
                        </Link>
                      </Td>
                      <Td className="hidden text-muted md:table-cell">{c.name ? c.email : ''}</Td>
                      <Td className="hidden text-muted lg:table-cell">{c.organization?.name ?? '—'}</Td>
                      <Td className="hidden text-muted sm:table-cell">{formatRelative(c.last_activity_at)}</Td>
                      <Td numeric>{c.conversation_count}</Td>
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
        {dialog === 'new' && (
          <ContactDialog onClose={() => setDialog(null)} onSaved={(id) => void navigate({ to: '/contacten/$id', params: { id } })} />
        )}
        {dialog === 'import' && <ImportDialog onClose={() => setDialog(null)} />}
        {(dialog === 'save-segment' || dialog === 'edit-segment') && (
          <SegmentDialog
            segment={dialog === 'edit-segment' ? segment : undefined}
            filter={filter}
            onClose={() => setDialog(null)}
            onSaved={(s) => setSegmentId(s.id)}
          />
        )}
        {dialog === 'delete-segment' && segment && (
          <DeleteSegmentDialog
            segment={segment}
            onClose={() => setDialog(null)}
            onDeleted={() => {
              setSegmentId('')
              setFilter(emptyFilter)
            }}
          />
        )}
      </div>
    </div>
  )
}

function SegmentDialog({
  segment,
  filter,
  onClose,
  onSaved,
}: {
  segment?: Segment | undefined
  filter: ContactFilter
  onClose: () => void
  onSaved: (s: Segment) => void
}) {
  const [name, setName] = useState(segment?.name ?? '')
  const [shared, setShared] = useState(segment?.shared ?? false)
  const queryClient = useQueryClient()
  const save = useMutation({
    mutationFn: () =>
      segment
        ? api<{ segment: Segment }>('PATCH', `/contact-segments/${segment.id}`, { name, shared })
        : api<{ segment: Segment }>('POST', '/contact-segments', {
            name,
            shared,
            filter: cleanFilter(filter),
          }),
    onSuccess: async (r) => {
      await queryClient.invalidateQueries({ queryKey: ['contact-segments'] })
      onSaved(r.segment)
      onClose()
    },
  })
  const submit = (e: SubmitEvent) => {
    e.preventDefault()
    save.mutate()
  }
  const nameError = fieldError(save.error, 'name')
  return (
    <Dialog open onOpenChange={(o) => !o && onClose()} title={segment ? 'Segment bewerken' : 'Segment opslaan'}>
      <form onSubmit={submit} className="flex flex-col gap-4" noValidate>
        {save.isError && !nameError && <ErrorNotice>{errorMessage(save.error)}</ErrorNotice>}
        <Field label="Naam" error={nameError}>
          {(p) => <Input {...p} autoFocus maxLength={80} value={name} onChange={(e) => setName(e.target.value)} />}
        </Field>
        <div className="flex items-center justify-between gap-4">
          <span id="shared-label" className="text-base text-ink">
            Delen met iedereen
            <span className="block text-sm text-muted">Anderen kunnen het segment gebruiken, alleen jij en beheerders wijzigen het.</span>
          </span>
          <Switch checked={shared} onChange={setShared} label="Delen met iedereen" />
        </div>
        <DialogFooter>
          <Button onClick={onClose}>Annuleren</Button>
          <Button type="submit" variant="primary" busy={save.isPending} disabled={!name.trim()}>
            Opslaan
          </Button>
        </DialogFooter>
      </form>
    </Dialog>
  )
}

function DeleteSegmentDialog({ segment, onClose, onDeleted }: { segment: Segment; onClose: () => void; onDeleted: () => void }) {
  const queryClient = useQueryClient()
  const del = useMutation({
    mutationFn: () => api('DELETE', `/contact-segments/${segment.id}`),
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: ['contact-segments'] })
      onDeleted()
      onClose()
    },
  })
  return (
    <Dialog open onOpenChange={(o) => !o && onClose()} title="Segment verwijderen">
      <div className="flex flex-col gap-4">
        <p className="text-base text-muted">Het segment {segment.name} wordt verwijderd. De contacten zelf blijven bestaan.</p>
        {del.isError && <ErrorNotice>{errorMessage(del.error)}</ErrorNotice>}
        <DialogFooter>
          <Button onClick={onClose}>Annuleren</Button>
          <Button variant="danger" busy={del.isPending} onClick={() => del.mutate()}>
            Verwijderen
          </Button>
        </DialogFooter>
      </div>
    </Dialog>
  )
}
