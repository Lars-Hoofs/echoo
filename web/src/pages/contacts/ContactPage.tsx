import { useQuery } from '@tanstack/react-query'
import { Link, useParams } from '@tanstack/react-router'
import { Building2, Download, GitMerge, Pencil, Phone, Trash2 } from 'lucide-react'
import { useState } from 'react'

import { ActionMenu, type ActionMenuItem } from '../../components/ActionMenu'
import { Avatar } from '../../components/Avatar'
import { Badge, Button, ErrorNotice, Skeleton } from '../../components/ui'
import { Reveal } from '../../components/Reveal'
import { api, ApiError } from '../../lib/api'
import { type ContactFull, contactDisplayName } from '../../lib/contacts'
import { errorMessage } from '../../lib/errors'
import { formatRelative } from '../../lib/format'
import { hasPermission, meQuery } from '../../lib/session'
import { AttributesTab } from './AttributesTab'
import { ConsentStatus } from './ConsentStatus'
import { ContactDialog } from './ContactDialog'
import { DataExportDialog, EraseDialog, MergeDialog } from './ContactActions'
import { ConversationsTab, useContactConversations } from './ConversationsTab'
import { type Kpi, KpiStrip } from '../../components/KpiStrip'
import { NotesPanel } from './NotesPanel'
import { Tabs } from './Tabs'
import { TimelineTab } from './TimelineTab'

type TabId = 'conversations' | 'notes' | 'attributes' | 'activity'
const tabs: { id: TabId; label: string }[] = [
  { id: 'conversations', label: 'Gesprekken' },
  { id: 'notes', label: 'Notities' },
  { id: 'attributes', label: 'Attributen' },
  { id: 'activity', label: 'Activiteit' },
]

export function ContactPage() {
  const { id } = useParams({ from: '/auth/ready/contacten/$id' })
  const me = useQuery(meQuery)
  const contact = useQuery({
    queryKey: ['contact', id],
    queryFn: async () => (await api<{ contact: ContactFull }>('GET', `/contacts/${id}`)).contact,
    retry: false,
  })
  const conversations = useContactConversations('contacts', id)
  const [tab, setTab] = useState<TabId>('conversations')
  const [dialog, setDialog] = useState<'edit' | 'merge' | 'export' | 'erase' | null>(null)
  const editable = hasPermission(me.data, 'contacts.write')

  const shell = (children: React.ReactNode) => (
    <div className="relative h-full overflow-y-auto px-4 py-8 md:px-8">
      <div className="mx-auto flex w-full max-w-4xl flex-col gap-6">
        <Link to="/contacten" className="t-label w-fit hover:text-ink">
          Terug naar contacten
        </Link>
        {children}
      </div>
    </div>
  )

  if (contact.isPending) return shell(<Skeleton className="h-40" />)
  if (contact.isError) {
    const gone = contact.error instanceof ApiError && contact.error.status === 404
    return shell(
      <ErrorNotice>{gone ? 'Dit contact bestaat niet (meer) of je hebt er geen toegang toe.' : errorMessage(contact.error)}</ErrorNotice>,
    )
  }
  const c = contact.data
  const name = contactDisplayName(c)
  const items: ActionMenuItem[] = []
  if (editable)
    items.push({
      label: 'Samenvoegen met…',
      icon: <GitMerge size={16} />,
      onSelect: () => setDialog('merge'),
    })
  if (hasPermission(me.data, 'contacts.erase')) {
    items.push({
      label: 'Gegevens exporteren',
      icon: <Download size={16} />,
      separated: items.length > 0,
      onSelect: () => setDialog('export'),
    })
    items.push({
      label: 'Contact wissen',
      icon: <Trash2 size={16} />,
      danger: true,
      onSelect: () => setDialog('erase'),
    })
  }

  const loaded = conversations.data?.pages.flatMap((p) => p.conversations)
  const kpis: Kpi[] = [{ label: 'Gesprekken', value: c.conversation_count }]
  // The open count is only exact when every page of conversations has been read.
  if (loaded && !conversations.hasNextPage)
    kpis.push({
      label: 'Open gesprekken',
      value: loaded.filter((x) => x.status === 'open').length,
    })
  kpis.push({
    label: 'Laatste contact',
    value: c.last_activity_at ? formatRelative(c.last_activity_at) : '—',
  })

  return shell(
    <>
      <Reveal>
        <header className="flex flex-wrap items-start justify-between gap-4">
          <div className="flex min-w-0 items-start gap-5">
            <Avatar name={name} size={64} />
            <div className="min-w-0">
              <h1 className="t-h3 wrap-anywhere text-ink">{name}</h1>
              <ul className="mt-2 flex flex-col gap-1">
                {c.emails.map((e) => (
                  <li key={e.email} className="flex items-center gap-2 text-base text-muted">
                    <span className="min-w-0 truncate">{e.email}</span>
                    {e.primary && c.emails.length > 1 && <Badge>Primair</Badge>}
                    {e.bounced_at && <Badge tone="danger">Onbereikbaar</Badge>}
                  </li>
                ))}
              </ul>
              <div className="mt-2 flex flex-wrap items-center gap-x-4 gap-y-1 text-base text-muted">
                {c.phone && (
                  <span className="flex items-center gap-2">
                    <Phone size={14} aria-hidden />
                    {c.phone}
                  </span>
                )}
                {c.organization && (
                  <Link
                    to="/organisaties/$id"
                    params={{ id: c.organization.id }}
                    className="flex items-center gap-2 text-ink hover:underline"
                  >
                    <Building2 size={14} aria-hidden />
                    {c.organization.name}
                  </Link>
                )}
              </div>
            </div>
          </div>
          <div className="flex items-center gap-2">
            {editable && (
              <Button onClick={() => setDialog('edit')}>
                <Pencil size={16} aria-hidden />
                Bewerken
              </Button>
            )}
            {items.length > 0 && <ActionMenu label="Meer acties" items={items} />}
          </div>
        </header>
      </Reveal>
      <Reveal i={1}>
        <KpiStrip items={kpis} />
      </Reveal>
      <ConsentStatus contact={c} canUndo={hasPermission(me.data, 'campaigns.manage')} />
      <Tabs tabs={tabs} value={tab} onChange={setTab}>
        {tab === 'conversations' && <ConversationsTab scope="contacts" id={id} />}
        {tab === 'notes' && <NotesPanel scope="contacts" id={id} />}
        {tab === 'attributes' && (
          <AttributesTab
            entity="contact"
            patchPath={`/contacts/${id}`}
            current={c.custom_attributes}
            editable={editable}
            invalidateKey={['contact', id]}
          />
        )}
        {tab === 'activity' && <TimelineTab contactId={id} />}
      </Tabs>
      {dialog === 'edit' && <ContactDialog contact={c} onClose={() => setDialog(null)} />}
      {dialog === 'merge' && <MergeDialog primary={c} onClose={() => setDialog(null)} />}
      {dialog === 'export' && <DataExportDialog contact={c} onClose={() => setDialog(null)} />}
      {dialog === 'erase' && <EraseDialog contact={c} onClose={() => setDialog(null)} />}
    </>,
  )
}
