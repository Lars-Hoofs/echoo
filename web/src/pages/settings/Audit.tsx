import { useInfiniteQuery, useQuery } from '@tanstack/react-query'
import { Download, ScrollText } from 'lucide-react'
import { useState } from 'react'

import { Avatar } from '../../components/Avatar'
import { Button, Card, EmptyState, ErrorNotice, Field, Input, Page, PageHeader, Select, Skeleton, Table, TBody, Td, Th, THead, Tr } from '../../components/ui'
import { api } from '../../lib/api'
import { errorMessage } from '../../lib/errors'
import { formatDateTime } from '../../lib/format'
import { auditActionLabel, auditGroups, auditTarget } from '../../lib/platform'
import type { User } from '../../lib/session'

interface Entry {
  id: number
  at: string
  actor_id: string
  actor_name: string
  actor_email: string
  ip: string
  action: string
  target_type: string
  target_id: string
  metadata: Record<string, unknown>
}

interface Filters {
  action: string
  actor: string
  from: string
  to: string
}

function queryString(f: Filters, before?: number): string {
  const p = new URLSearchParams()
  if (f.action) p.set('action', f.action)
  if (f.actor) p.set('actor', f.actor)
  if (f.from) p.set('from', f.from)
  if (f.to) p.set('to', f.to)
  if (before) p.set('before', String(before))
  const s = p.toString()
  return s ? `?${s}` : ''
}

export function AuditPage() {
  const [filters, setFilters] = useState<Filters>({ action: '', actor: '', from: '', to: '' })
  const users = useQuery({ queryKey: ['users'], queryFn: () => api<{ users: User[] }>('GET', '/users') })
  const log = useInfiniteQuery({
    queryKey: ['admin', 'audit', filters],
    initialPageParam: 0,
    queryFn: ({ pageParam }) => api<{ entries: Entry[]; next_before: number | null }>('GET', `/audit${queryString(filters, pageParam || undefined)}`),
    getNextPageParam: (last) => last.next_before ?? undefined,
  })
  const entries = log.data?.pages.flatMap((p) => p.entries) ?? []
  const set = (patch: Partial<Filters>) => setFilters({ ...filters, ...patch })

  return (
    <Page>
      <PageHeader
        breadcrumb="Werkruimte"
        title="Auditlog"
        description="Wie heeft wat gedaan. Regels kunnen niet worden gewijzigd of verwijderd."
        actions={
          <Button onClick={() => window.location.assign(`/api/v1/audit/export${queryString(filters)}`)}>
            <Download size={16} aria-hidden />
            Exporteren als CSV
          </Button>
        }
      />
      <Card>
        <form className="grid gap-4 sm:grid-cols-2 lg:grid-cols-4" onSubmit={(e) => e.preventDefault()} aria-label="Filters">
          <Field label="Actie">
            {(p) => (
              <Select {...p} value={filters.action} onChange={(e) => set({ action: e.target.value })}>
                {auditGroups.map((g) => (
                  <option key={g.prefix} value={g.prefix}>
                    {g.label}
                  </option>
                ))}
              </Select>
            )}
          </Field>
          <Field label="Gebruiker">
            {(p) => (
              <Select {...p} value={filters.actor} onChange={(e) => set({ actor: e.target.value })}>
                <option value="">Iedereen</option>
                {(users.data?.users ?? []).map((u) => (
                  <option key={u.id} value={u.id}>
                    {u.name}
                  </option>
                ))}
              </Select>
            )}
          </Field>
          <Field label="Van">{(p) => <Input {...p} type="date" value={filters.from} max={filters.to || undefined} onChange={(e) => set({ from: e.target.value })} />}</Field>
          <Field label="Tot en met">{(p) => <Input {...p} type="date" value={filters.to} min={filters.from || undefined} onChange={(e) => set({ to: e.target.value })} />}</Field>
        </form>
      </Card>
      {log.isPending ? (
        <Skeleton className="h-64" />
      ) : log.isError ? (
        <ErrorNotice>{errorMessage(log.error)}</ErrorNotice>
      ) : entries.length === 0 ? (
        <Card>
          <EmptyState icon={<ScrollText size={20} />} title="Geen regels gevonden" description="Pas de filters aan om meer te zien." />
        </Card>
      ) : (
        <>
          <Card flush>
            <Table>
              <THead>
                <Th>Tijdstip</Th>
                <Th>Gebruiker</Th>
                <Th>Actie</Th>
                <Th className="hidden lg:table-cell">Onderwerp</Th>
                <Th className="hidden md:table-cell">IP-adres</Th>
              </THead>
              <TBody>
                {entries.map((e) => (
                  <Tr key={e.id}>
                    <Td className="whitespace-nowrap text-muted">{formatDateTime(e.at)}</Td>
                    <Td>
                      {e.actor_name ? (
                        <div className="flex items-center gap-2">
                          <Avatar name={e.actor_name} size={24} />
                          <span className="truncate text-ink">{e.actor_name}</span>
                        </div>
                      ) : (
                        <span className="text-faint">Systeem</span>
                      )}
                    </Td>
                    <Td>
                      <div className="text-ink">{auditActionLabel(e.action)}</div>
                      <div className="font-mono text-sm text-faint">{e.action}</div>
                    </Td>
                    <Td className="hidden text-muted lg:table-cell" title={e.target_id}>
                      {auditTarget(e.target_type, e.target_id, e.metadata)}
                    </Td>
                    <Td className="hidden font-mono text-sm text-muted md:table-cell">{e.ip || '—'}</Td>
                  </Tr>
                ))}
              </TBody>
            </Table>
          </Card>
          {log.hasNextPage && (
            <div>
              <Button busy={log.isFetchingNextPage} onClick={() => void log.fetchNextPage()}>
                Oudere regels laden
              </Button>
            </div>
          )}
        </>
      )}
    </Page>
  )
}
