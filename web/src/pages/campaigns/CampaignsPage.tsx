import { useQuery } from '@tanstack/react-query'
import { Link } from '@tanstack/react-router'
import { Megaphone, Plus } from 'lucide-react'

import { buttonClass, Card, EmptyState, ErrorNotice, PageHeader, Skeleton, Table, TBody, Td, Th, THead, Tr } from '../../components/ui'
import { type Campaign, campaignsQuery } from '../../lib/campaigns'
import { errorMessage } from '../../lib/errors'
import { formatDateTime } from '../../lib/format'
import { CampaignStatusBadge, ProgressBar, progressText } from './shared'

const newLink = buttonClass({ variant: 'primary' })

function when(c: Campaign): string {
  if (c.status === 'scheduled') return `Gepland ${formatDateTime(c.scheduled_at)}`
  if (c.started_at) return `Gestart ${formatDateTime(c.started_at)}`
  return `Aangemaakt ${formatDateTime(c.created_at)}`
}

export function CampaignsPage() {
  const list = useQuery(campaignsQuery)
  return (
    <div className="relative h-full overflow-y-auto px-4 py-8 md:px-8">
      <div className="mx-auto flex w-full max-w-5xl flex-col gap-6">
        <PageHeader
          breadcrumb="Klanten"
          title="Campagnes"
          description="Eenmalige e-mails naar een segment van je contacten. Afgemelde contacten worden altijd overgeslagen."
          actions={
            <Link to="/campagnes/nieuw" search={{ stap: 'mailbox' }} className={newLink}>
              <Plus size={16} aria-hidden />
              Nieuwe campagne
            </Link>
          }
        />
        {list.isPending ? (
          <Skeleton className="h-48" />
        ) : list.isError ? (
          <ErrorNotice>{errorMessage(list.error)}</ErrorNotice>
        ) : list.data.campaigns.length === 0 ? (
          <Card>
            <EmptyState
              icon={<Megaphone size={20} />}
              title="Nog geen campagnes"
              description="Kies een segment van contacten en verstuur ze een e-mail."
              action={
                <Link to="/campagnes/nieuw" search={{ stap: 'mailbox' }} className={newLink}>
                  Maak een campagne
                </Link>
              }
            />
          </Card>
        ) : (
          <Card flush>
            <Table>
              <THead>
                <Th>Campagne</Th>
                <Th>Status</Th>
                <Th className="w-56">Voortgang</Th>
                <Th className="hidden md:table-cell">Mailbox</Th>
                <Th className="hidden sm:table-cell">Wanneer</Th>
              </THead>
              <TBody>
                {list.data.campaigns.map((c) => (
                  <Tr key={c.id}>
                    <Td>
                      <Link
                        to={c.status === 'draft' ? '/campagnes/$id/bewerken' : '/campagnes/$id'}
                        params={{ id: c.id }}
                        {...(c.status === 'draft' ? { search: { stap: 'mailbox' as const } } : {})}
                        className="text-ink hover:underline"
                      >
                        {c.name}
                      </Link>
                      <div className="t-label">{c.segment?.name ?? 'Geen segment gekozen'}</div>
                    </Td>
                    <Td>
                      <CampaignStatusBadge status={c.status} />
                    </Td>
                    <Td>
                      <div className="flex flex-col gap-1">
                        <ProgressBar counts={c.counts} label={`Voortgang van ${c.name}`} />
                        <span className="t-label tabular-nums">{progressText(c)}</span>
                      </div>
                    </Td>
                    <Td className="hidden text-muted md:table-cell">{c.mailbox.name}</Td>
                    <Td className="hidden text-muted sm:table-cell">{when(c)}</Td>
                  </Tr>
                ))}
              </TBody>
            </Table>
          </Card>
        )}
      </div>
    </div>
  )
}
