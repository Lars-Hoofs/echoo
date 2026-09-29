import { useQuery } from '@tanstack/react-query'
import { Link } from '@tanstack/react-router'
import { BookOpen, ExternalLink, FolderTree, Plus, Search } from 'lucide-react'
import { useEffect, useState } from 'react'

import { KpiStrip } from '../../components/KpiStrip'
import { Badge, buttonClass, Card, EmptyState, ErrorNotice, Input, PageHeader, Segmented, Select, Skeleton, Table, TBody, Td, Th, THead, Tr } from '../../components/ui'
import { errorMessage } from '../../lib/errors'
import { formatRelative } from '../../lib/format'
import { type ArticleStatus, categoryChoices, kbArticlesQuery, kbCategoriesQuery, kbPortalQuery, statusLabel } from '../../lib/kb'
import { hasPermission, meQuery } from '../../lib/session'

type StatusFilter = ArticleStatus | 'all'

export function KbListPage() {
  const me = useQuery(meQuery)
  const portal = useQuery(kbPortalQuery)
  const categories = useQuery(kbCategoriesQuery)
  const [status, setStatus] = useState<StatusFilter>('all')
  const [categoryId, setCategoryId] = useState('')
  const [q, setQ] = useState('')
  const [debounced, setDebounced] = useState('')
  useEffect(() => {
    const t = setTimeout(() => setDebounced(q.trim()), 300)
    return () => clearTimeout(t)
  }, [q])

  const editor = hasPermission(me.data, 'kb.write')
  const list = useQuery(kbArticlesQuery({ status: status === 'all' ? '' : status, categoryId, q: debounced }))
  const filtered = status !== 'all' || categoryId !== '' || debounced !== ''

  const statusOptions: { value: StatusFilter; label: string }[] = [
    { value: 'all', label: 'Alle' },
    ...(editor ? [{ value: 'draft' as const, label: statusLabel.draft }] : []),
    { value: 'published', label: statusLabel.published },
    ...(editor ? [{ value: 'archived' as const, label: statusLabel.archived }] : []),
  ]

  return (
    <div className="relative h-full overflow-y-auto px-4 py-8 md:px-8">
      <div className="mx-auto flex w-full max-w-5xl flex-col gap-6">
        <PageHeader
          breadcrumb="Helpcenter"
          title="Kennisbank"
          description="Artikelen voor je klanten, openbaar te lezen zonder inloggen. Je kunt ze ook in een antwoord invoegen."
          actions={
            <>
              {portal.data && (
                <a href={portal.data.public_url} target="_blank" rel="noreferrer" className={buttonClass()}>
                  <ExternalLink size={16} aria-hidden />
                  Openbare site
                </a>
              )}
              {hasPermission(me.data, 'kb.manage') && (
                <Link to="/kennisbank/beheer" className={buttonClass()}>
                  <FolderTree size={16} aria-hidden />
                  Beheer
                </Link>
              )}
              {editor && (
                <Link to="/kennisbank/nieuw" className={buttonClass({ variant: 'primary' })}>
                  <Plus size={16} aria-hidden />
                  Nieuw artikel
                </Link>
              )}
            </>
          }
        />

        <div className="flex flex-wrap items-center gap-2">
          <div className="relative min-w-56 flex-1">
            <Search size={16} aria-hidden className="pointer-events-none absolute top-1/2 left-4 -translate-y-1/2 text-faint" />
            <Input aria-label="Zoek in artikelen" placeholder="Zoek op titel of tekst" value={q} onChange={(e) => setQ(e.target.value)} className="pl-10" />
          </div>
          <Select aria-label="Categorie" value={categoryId} onChange={(e) => setCategoryId(e.target.value)} className="w-52">
            <option value="">Alle categorieën</option>
            {categoryChoices(categories.data ?? []).map((c) => (
              <option key={c.id} value={c.id}>
                {c.label}
              </option>
            ))}
          </Select>
          <Segmented label="Status" value={status} options={statusOptions} onChange={setStatus} />
        </div>

        {list.data && !filtered && list.data.length > 0 && list.data.length < 100 && (
          <KpiStrip
            items={[
              { label: 'Artikelen', value: list.data.length },
              { label: 'Gepubliceerd', value: list.data.filter((a) => a.status === 'published').length },
              { label: 'Keer gelezen', value: list.data.reduce((n, a) => n + a.view_count, 0) },
              { label: 'Keer nuttig gevonden', value: list.data.reduce((n, a) => n + a.helpful_yes, 0) },
            ]}
          />
        )}
        {list.isPending ? (
          <Skeleton className="h-48" />
        ) : list.isError ? (
          <ErrorNotice>{errorMessage(list.error)}</ErrorNotice>
        ) : list.data.length === 0 ? (
          <Card>
            <EmptyState
              icon={<BookOpen size={20} />}
              title={filtered ? 'Geen artikelen gevonden' : 'Nog geen artikelen'}
              description={filtered ? 'Pas de zoekopdracht of de filters aan.' : 'Schrijf het eerste artikel voor je klanten.'}
              action={
                !filtered && editor ? (
                  <Link to="/kennisbank/nieuw" className={buttonClass({ variant: 'primary' })}>
                    Schrijf een artikel
                  </Link>
                ) : undefined
              }
            />
          </Card>
        ) : (
          <Card flush>
            <Table>
              <THead>
                <Th>Artikel</Th>
                <Th className="hidden md:table-cell">Categorie</Th>
                <Th>Status</Th>
                <Th className="hidden lg:table-cell">Gelezen</Th>
                <Th className="hidden lg:table-cell">Nuttig</Th>
                <Th className="hidden sm:table-cell">Bijgewerkt</Th>
              </THead>
              <TBody>
                {list.data.map((a) => (
                  <Tr key={a.id}>
                    <Td>
                      <Link to="/kennisbank/$id" params={{ id: a.id }} className="text-ink hover:underline">
                        {a.title}
                      </Link>
                      <div className="t-label">/hulp/a/{a.slug}</div>
                    </Td>
                    <Td className="hidden text-muted md:table-cell">{a.category_name ?? '—'}</Td>
                    <Td>
                      <Badge dot>{statusLabel[a.status]}</Badge>
                    </Td>
                    <Td numeric className="hidden lg:table-cell">
                      {a.view_count}
                    </Td>
                    <Td className="hidden text-muted lg:table-cell">
                      {a.helpful_yes + a.helpful_no === 0 ? '—' : `${a.helpful_yes} ja · ${a.helpful_no} nee`}
                    </Td>
                    <Td className="hidden text-muted sm:table-cell">
                      {formatRelative(a.updated_at)}
                      {a.updated_by_name && <div className="t-label">{a.updated_by_name}</div>}
                    </Td>
                  </Tr>
                ))}
              </TBody>
            </Table>
          </Card>
        )}
        {list.data && list.data.length >= 100 && <p className="t-label">De eerste 100 artikelen worden getoond. Zoek of filter om de rest te vinden.</p>}
      </div>
    </div>
  )
}
