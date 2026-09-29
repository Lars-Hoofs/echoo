import { useQuery } from '@tanstack/react-query'
import { Link, useParams } from '@tanstack/react-router'
import { Pencil } from 'lucide-react'
import { useState } from 'react'

import { Avatar } from '../../components/Avatar'
import { Reveal } from '../../components/Reveal'
import { Button, Card, ErrorNotice, Skeleton } from '../../components/ui'
import { api, ApiError } from '../../lib/api'
import { contactDisplayName, type OrganizationFull } from '../../lib/contacts'
import { errorMessage } from '../../lib/errors'
import { formatRelative } from '../../lib/format'
import { hasPermission, meQuery } from '../../lib/session'
import { AttributesTab } from './AttributesTab'
import { ConversationsTab, useContactConversations } from './ConversationsTab'
import { type Kpi, KpiStrip } from '../../components/KpiStrip'
import { NotesPanel } from './NotesPanel'
import { OrganizationDialog } from './OrganizationDialog'
import { Tabs } from './Tabs'

type TabId = 'contacts' | 'conversations' | 'notes' | 'attributes'

export function OrganizationPage() {
  const { id } = useParams({ from: '/auth/ready/organisaties/$id' })
  const me = useQuery(meQuery)
  const org = useQuery({
    queryKey: ['organization', id],
    queryFn: async () => (await api<{ organization: OrganizationFull }>('GET', `/organizations/${id}`)).organization,
    retry: false,
  })
  const conversations = useContactConversations('organizations', id)
  const [tab, setTab] = useState<TabId>('contacts')
  const [editing, setEditing] = useState(false)
  const editable = hasPermission(me.data, 'contacts.write')
  const shell = (children: React.ReactNode) => (
    <div className="relative h-full overflow-y-auto px-4 py-8 md:px-8">
      <div className="mx-auto flex w-full max-w-4xl flex-col gap-6">
        <Link to="/organisaties" className="t-label w-fit hover:text-ink">
          Terug naar organisaties
        </Link>
        {children}
      </div>
    </div>
  )
  if (org.isPending) return shell(<Skeleton className="h-40" />)
  if (org.isError) {
    const gone = org.error instanceof ApiError && org.error.status === 404
    return shell(
      <ErrorNotice>{gone ? 'Deze organisatie bestaat niet (meer) of je hebt er geen toegang toe.' : errorMessage(org.error)}</ErrorNotice>,
    )
  }
  const o = org.data
  const tabs: { id: TabId; label: string }[] = [
    { id: 'contacts', label: `Contacten (${o.contact_count})` },
    { id: 'conversations', label: 'Gesprekken' },
    { id: 'notes', label: 'Notities' },
    { id: 'attributes', label: 'Attributen' },
  ]
  const loaded = conversations.data?.pages.flatMap((p) => p.conversations)
  const kpis: Kpi[] = [{ label: 'Contacten', value: o.contact_count }]
  // Conversation figures are only exact when every page has been read.
  if (loaded && !conversations.hasNextPage) {
    kpis.push({ label: 'Gesprekken', value: loaded.length })
    kpis.push({ label: 'Open gesprekken', value: loaded.filter((x) => x.status === 'open').length })
  }
  const lastActivity = o.contacts.map((c) => c.last_activity_at).sort().at(-1)
  if (lastActivity) kpis.push({ label: 'Laatste activiteit', value: formatRelative(lastActivity) })
  return shell(
    <>
      <Reveal>
        <header className="flex flex-wrap items-start justify-between gap-4">
          <div className="flex min-w-0 items-start gap-5">
            <Avatar name={o.name} size={64} />
            <div className="min-w-0">
              <h1 className="t-h3 wrap-anywhere text-ink">{o.name}</h1>
              <p className="mt-2 text-base text-muted">{o.domains.length > 0 ? o.domains.join(', ') : 'Geen domeinen'}</p>
            </div>
          </div>
          {editable && (
            <Button onClick={() => setEditing(true)}>
              <Pencil size={16} aria-hidden />
              Bewerken
            </Button>
          )}
        </header>
      </Reveal>
      <Reveal i={1}>
        <KpiStrip items={kpis} />
      </Reveal>
      <Tabs tabs={tabs} value={tab} onChange={setTab}>
        {tab === 'contacts' &&
          (o.contacts.length === 0 ? (
            <p className="text-base text-muted">Geen contacten die jij kunt zien.</p>
          ) : (
            <Card flush>
              <ul className="divide-y divide-line">
                {o.contacts.map((c) => (
                  <li key={c.id}>
                    <Link
                      to="/contacten/$id"
                      params={{ id: c.id }}
                      className="flex items-center gap-3 px-4 py-3 hover:bg-subtle/60 sm:px-6"
                    >
                      <Avatar name={contactDisplayName(c)} />
                      <span className="min-w-0 flex-1">
                        <span className="block truncate text-base text-ink">{contactDisplayName(c)}</span>
                        {c.name && <span className="block truncate text-sm text-muted">{c.email}</span>}
                      </span>
                      <span className="t-label">{formatRelative(c.last_activity_at)}</span>
                    </Link>
                  </li>
                ))}
              </ul>
            </Card>
          ))}
        {tab === 'conversations' && <ConversationsTab scope="organizations" id={id} />}
        {tab === 'notes' && <NotesPanel scope="organizations" id={id} />}
        {tab === 'attributes' && (
          <AttributesTab
            entity="organization"
            patchPath={`/organizations/${id}`}
            current={o.custom_attributes}
            editable={editable}
            invalidateKey={['organization', id]}
          />
        )}
      </Tabs>
      {editing && <OrganizationDialog organization={o} onClose={() => setEditing(false)} />}
    </>,
  )
}
