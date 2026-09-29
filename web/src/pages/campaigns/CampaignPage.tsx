import { useInfiniteQuery, useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { getRouteApi, Link, useNavigate } from '@tanstack/react-router'
import { Download, Pause, Pencil, Play, Trash2, X } from 'lucide-react'
import { useState } from 'react'

import { Dialog, DialogFooter } from '../../components/Dialog'
import { Num } from '../../components/Num'
import { Badge, Button, buttonClass, Card, ErrorNotice, PageHeader, Segmented, Skeleton, Table, TBody, Td, Th, THead, Tr } from '../../components/ui'
import { api } from '../../lib/api'
import {
  type Campaign,
  campaignQuery,
  type CampaignRecipient,
  progress,
  recipientsQuery,
  type RecipientState,
  skipReasonLabel,
  stateLabel,
  stopReason,
} from '../../lib/campaigns'
import { errorMessage } from '../../lib/errors'
import { formatDateTime } from '../../lib/format'
import { CampaignStatusBadge, ProgressBar } from './shared'

const route = getRouteApi('/auth/ready/campagnes/$id')
const numberFormat = new Intl.NumberFormat('nl-NL')

type Action = 'pause' | 'resume' | 'cancel' | 'delete'

export function CampaignPage() {
  const { id } = route.useParams()
  const campaign = useQuery(campaignQuery(id))
  return (
    <div className="relative h-full overflow-y-auto px-4 py-8 md:px-8">
      <div className="mx-auto flex w-full max-w-5xl flex-col gap-6">
        {campaign.isPending ? (
          <Skeleton className="h-64" />
        ) : campaign.isError ? (
          <ErrorNotice>{errorMessage(campaign.error)}</ErrorNotice>
        ) : (
          <Detail campaign={campaign.data} />
        )}
      </div>
    </div>
  )
}

function Detail({ campaign: c }: { campaign: Campaign }) {
  const queryClient = useQueryClient()
  const navigate = useNavigate()
  const [confirm, setConfirm] = useState<Action | null>(null)
  const act = useMutation({
    mutationFn: (action: Action) => (action === 'delete' ? api('DELETE', `/campaigns/${c.id}`) : api('POST', `/campaigns/${c.id}/${action}`, {})),
    onSuccess: async (_data, action) => {
      setConfirm(null)
      await queryClient.invalidateQueries({ queryKey: ['campaigns'] })
      if (action === 'delete') await navigate({ to: '/campagnes' })
    },
    onError: async () => {
      // The state changed under us; show the campaign as it is now.
      setConfirm(null)
      await queryClient.invalidateQueries({ queryKey: ['campaigns'] })
    },
  })
  const p = progress(c.counts)
  const canPause = c.status === 'sending'
  const canResume = c.status === 'paused'
  const canCancel = c.status === 'scheduled' || c.status === 'sending' || c.status === 'paused'
  const canDelete = c.status === 'draft' || c.status === 'done' || c.status === 'cancelled'

  return (
    <>
      <PageHeader
        title={c.name}
        description={c.subject}
        breadcrumb={
          <Link to="/campagnes" className="hover:text-ink">
            Campagnes
          </Link>
        }
        actions={
          <>
            {c.status === 'draft' && (
              <Link to="/campagnes/$id/bewerken" params={{ id: c.id }} search={{ stap: 'mailbox' }} className={buttonClass()}>
                <Pencil size={16} aria-hidden />
                Bewerken
              </Link>
            )}
            {canPause && (
              <Button onClick={() => act.mutate('pause')} busy={act.isPending && act.variables === 'pause'}>
                <Pause size={16} aria-hidden />
                Pauzeren
              </Button>
            )}
            {canResume && (
              <Button variant="primary" onClick={() => act.mutate('resume')} busy={act.isPending && act.variables === 'resume'}>
                <Play size={16} aria-hidden />
                Hervatten
              </Button>
            )}
            {canCancel && (
              <Button onClick={() => setConfirm('cancel')}>
                <X size={16} aria-hidden />
                Annuleren
              </Button>
            )}
            {canDelete && (
              <Button onClick={() => setConfirm('delete')}>
                <Trash2 size={16} aria-hidden />
                Verwijderen
              </Button>
            )}
          </>
        }
      />
      {c.error && <ErrorNotice>{stopReason[c.error] ?? 'De campagne is vanzelf gestopt.'}</ErrorNotice>}
      {act.isError && <ErrorNotice>{errorMessage(act.error)}</ErrorNotice>}

      <Card>
        <div className="flex flex-col gap-4">
          <div className="flex flex-wrap items-center gap-3">
            <CampaignStatusBadge status={c.status} />
            <span className="text-base text-muted tabular-nums">
              {c.status === 'draft' ? 'Nog niet gestart' : c.status === 'scheduled' ? `Start ${formatDateTime(c.scheduled_at)}` : `${numberFormat.format(p.done)} van ${numberFormat.format(p.total)} afgehandeld`}
            </span>
          </div>
          <ProgressBar counts={c.counts} label="Voortgang" />
          <Counts campaign={c} />
          <dl className="grid grid-cols-[max-content_1fr] items-baseline gap-x-6 gap-y-2 border-t border-line pt-4 text-base">
            <dt className="t-label">Mailbox</dt>
            <dd>
              {c.mailbox.name} ({c.mailbox.address})
            </dd>
            <dt className="t-label">Segment</dt>
            <dd>{c.segment?.name ?? '—'}</dd>
            <dt className="t-label">Snelheid</dt>
            <dd>{c.rate_per_minute} per minuut</dd>
            {c.started_at && (
              <>
                <dt className="t-label">Gestart</dt>
                <dd>{formatDateTime(c.started_at)}</dd>
              </>
            )}
            {c.finished_at && (
              <>
                <dt className="t-label">Afgerond</dt>
                <dd>{formatDateTime(c.finished_at)}</dd>
              </>
            )}
            {c.created_by && (
              <>
                <dt className="t-label">Gemaakt door</dt>
                <dd>{c.created_by.name}</dd>
              </>
            )}
          </dl>
        </div>
      </Card>

      {c.counts.total > 0 && <Report campaign={c} />}

      <Dialog
        open={confirm === 'cancel' || confirm === 'delete'}
        onOpenChange={(open) => !open && setConfirm(null)}
        title={confirm === 'delete' ? 'Campagne verwijderen?' : 'Campagne annuleren?'}
        description={
          confirm === 'delete'
            ? 'De campagne en het rapport per ontvanger worden verwijderd. De gesprekken die ze aanmaakte blijven staan.'
            : 'Ontvangers die nog niet aan de beurt waren en berichten die nog in de wachtrij staan worden overgeslagen. Een bericht dat op dit moment wordt verzonden gaat nog wel weg.'
        }
      >
        <DialogFooter>
          <Button onClick={() => setConfirm(null)}>Terug</Button>
          <Button variant="danger" busy={act.isPending} onClick={() => confirm && act.mutate(confirm)}>
            {confirm === 'delete' ? 'Verwijderen' : 'Campagne annuleren'}
          </Button>
        </DialogFooter>
      </Dialog>
    </>
  )
}

function Counts({ campaign: c }: { campaign: Campaign }) {
  const reasons = Object.entries(c.counts.skipped_reasons).filter(([, n]) => n > 0)
  const items: { label: string; value: number; size: 's' | 'm' }[] = [
    { label: 'Verzonden', value: c.counts.sent, size: 'm' },
    { label: 'Mislukt', value: c.counts.failed, size: 'm' },
    { label: 'Overgeslagen', value: c.counts.skipped, size: 'm' },
    { label: 'Ontvangers', value: c.counts.total, size: 's' },
    { label: 'In wachtrij', value: c.counts.queued, size: 's' },
    { label: 'Nog te gaan', value: c.counts.pending, size: 's' },
  ]
  return (
    <div>
      <dl className="grid grid-cols-3 gap-x-4 gap-y-6">
        {items.map((i) => (
          <div key={i.label} className="flex flex-col-reverse justify-end gap-1">
            <dt className="t-label">{i.label}</dt>
            <dd>
              <Num value={i.value} size={i.size} />
            </dd>
          </div>
        ))}
      </dl>
      {reasons.length > 0 && (
        <p className="t-label mt-4">
          Overgeslagen: {reasons.map(([reason, n]) => `${numberFormat.format(n)} ${(skipReasonLabel[reason] ?? reason).toLowerCase()}`).join(', ')}.
        </p>
      )}
    </div>
  )
}

function recipientDetail(r: CampaignRecipient): string {
  if (r.state === 'skipped') return skipReasonLabel[r.skip_reason] ?? r.skip_reason
  if (r.state === 'failed') return r.error
  if (r.state === 'queued' && r.delivery === 'retry') return 'Nieuwe poging volgt'
  return ''
}

const stateTone: Record<RecipientState, 'neutral' | 'danger'> = {
  pending: 'neutral',
  queued: 'neutral',
  sent: 'neutral',
  failed: 'danger',
  skipped: 'neutral',
}

function Report({ campaign: c }: { campaign: Campaign }) {
  const [state, setState] = useState<RecipientState | ''>('')
  const list = useInfiniteQuery({ ...recipientsQuery(c.id, state), refetchInterval: c.counts.queued > 0 || c.status === 'sending' ? 10_000 : false })
  const rows = list.data?.pages.flatMap((p) => p.recipients) ?? []
  return (
    <Card
      title="Rapport per ontvanger"
      flush
      actions={
        <a href={`/api/v1/campaigns/${c.id}/report`} download className={buttonClass({ size: 'sm' })}>
          <Download size={16} aria-hidden />
          Exporteren
        </a>
      }
    >
      <div className="border-b border-line px-4 py-4 sm:px-6">
        <Segmented
          label="Toon ontvangers"
          value={state}
          options={[
            { value: '', label: 'Alle' },
            { value: 'sent', label: stateLabel.sent },
            { value: 'queued', label: stateLabel.queued },
            { value: 'pending', label: stateLabel.pending },
            { value: 'failed', label: stateLabel.failed },
            { value: 'skipped', label: stateLabel.skipped },
          ]}
          onChange={setState}
        />
      </div>
      {list.isPending ? (
        <Skeleton className="h-32" />
      ) : list.isError ? (
        <div className="p-4">
          <ErrorNotice>{errorMessage(list.error)}</ErrorNotice>
        </div>
      ) : rows.length === 0 ? (
        <p className="px-6 py-6 text-base text-muted">Geen ontvangers met deze status.</p>
      ) : (
        <>
          <Table>
            <THead>
              <Th>Ontvanger</Th>
              <Th>Status</Th>
              <Th className="hidden sm:table-cell">Toelichting</Th>
              <Th className="hidden md:table-cell">Afgerond</Th>
            </THead>
            <TBody>
              {rows.map((r) => (
                <Tr key={r.id}>
                  <Td>
                    <div className="text-ink">{r.name || r.email || 'Gewist contact'}</div>
                    {r.name && r.email && <div className="t-label">{r.email}</div>}
                  </Td>
                  <Td>
                    <Badge tone={stateTone[r.state]} dot>{stateLabel[r.state]}</Badge>
                  </Td>
                  <Td className="hidden text-muted sm:table-cell">
                    {recipientDetail(r)}
                    {r.conversation_id && (r.state === 'sent' || r.state === 'failed' || r.state === 'queued') && (
                      <>
                        {recipientDetail(r) && ' · '}
                        <Link to="/inbox/$view/$conversationId" params={{ view: 'alle', conversationId: r.conversation_id }} className="text-ink hover:underline">
                          Gesprek
                        </Link>
                      </>
                    )}
                  </Td>
                  <Td className="hidden text-muted md:table-cell">{formatDateTime(r.finished_at)}</Td>
                </Tr>
              ))}
            </TBody>
          </Table>
          {list.hasNextPage && (
            <div className="flex justify-center border-t border-line p-3">
              <Button busy={list.isFetchingNextPage} onClick={() => void list.fetchNextPage()}>
                Meer laden
              </Button>
            </div>
          )}
        </>
      )}
    </Card>
  )
}
