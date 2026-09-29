import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { CircleCheck } from 'lucide-react'

import { Num } from '../../components/Num'
import { Reveal } from '../../components/Reveal'
import { Badge, Button, Card, EmptyState, ErrorNotice, Page, PageHeader, Skeleton, Table, TBody, Td, Th, THead, Tr } from '../../components/ui'
import { api } from '../../lib/api'
import { errorMessage } from '../../lib/errors'
import { formatDateTime } from '../../lib/format'

interface Job {
  id: number
  kind: string
  state: 'retryable' | 'discarded'
  attempt: number
  max_attempts: number
  last_error: string
  scheduled_at: string
}

interface RawMessage {
  id: string
  mailbox_name: string
  parse_status: 'failed' | 'skipped'
  reason: string
  size_bytes: number
  received_at: string
}

const kindLabels: Record<string, string> = {
  'mail.parse': 'Mail verwerken',
  'mail.send': 'Mail verzenden',
  'webhook.deliver': 'Webhook versturen',
  'webhook.fanout': 'Webhooks klaarzetten',
  'webhook.purge': 'Webhooklog opruimen',
  'retention.purge': 'Bewaartermijnen toepassen',
  'uploads.purge': 'Verlopen uploads opruimen',
}

export function JobsPage() {
  const queryClient = useQueryClient()
  const jobs = useQuery({ queryKey: ['admin', 'jobs'], queryFn: () => api<{ jobs: Job[] }>('GET', '/jobs'), refetchInterval: 15_000 })
  const raw = useQuery({ queryKey: ['admin', 'raw-messages'], queryFn: () => api<{ raw_messages: RawMessage[] }>('GET', '/raw-messages') })
  const retryJob = useMutation({
    mutationFn: (j: Job) => api('POST', `/jobs/${j.id}/retry`),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ['admin', 'jobs'] }),
  })
  const retryRaw = useMutation({
    mutationFn: (m: RawMessage) => api('POST', `/raw-messages/${m.id}/retry`),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ['admin', 'raw-messages'] }),
  })

  return (
    <Page>
      <PageHeader breadcrumb="Werkruimte" title="Taken" description="Achtergrondwerk dat is misgegaan. Herhalende taken proberen het vanzelf opnieuw; mislukte taken doen dat niet meer." />

      {jobs.data && raw.data && (
        <Reveal>
          <dl className="grid gap-4 sm:grid-cols-3">
            {[
              { label: 'Openstaande fouten', value: jobs.data.jobs.length },
              { label: 'Nieuwe pogingen gepland', value: jobs.data.jobs.filter((j) => j.state === 'retryable').length },
              { label: 'Mail niet verwerkt', value: raw.data.raw_messages.length },
            ].map((k) => (
              <div key={k.label} className="card card-line flex flex-col gap-2">
                <dt className="t-label">{k.label}</dt>
                <dd>
                  <Num value={k.value} size="m" />
                </dd>
              </div>
            ))}
          </dl>
        </Reveal>
      )}

      <Card title="Taken met fouten" description="De laatste 100." flush>
        {jobs.isPending ? (
          <div className="p-4">
            <Skeleton className="h-24" />
          </div>
        ) : jobs.isError ? (
          <div className="p-4">
            <ErrorNotice>{errorMessage(jobs.error)}</ErrorNotice>
          </div>
        ) : jobs.data.jobs.length === 0 ? (
          <EmptyState icon={<CircleCheck size={20} />} title="Geen taken met fouten" />
        ) : (
          <>
            {retryJob.isError && (
              <div className="p-4">
                <ErrorNotice>{errorMessage(retryJob.error)}</ErrorNotice>
              </div>
            )}
            <Table>
              <THead>
                <Th>Taak</Th>
                <Th>Status</Th>
                <Th numeric className="hidden md:table-cell">
                  Poging
                </Th>
                <Th className="hidden lg:table-cell">Laatste fout</Th>
                <Th className="hidden md:table-cell">Gepland</Th>
                <Th className="w-px">
                  <span className="sr-only">Acties</span>
                </Th>
              </THead>
              <TBody>
                {jobs.data.jobs.map((j) => (
                  <Tr key={j.id}>
                    <Td>
                      <div className="text-ink">{kindLabels[j.kind] ?? j.kind}</div>
                      <div className="font-mono text-sm text-faint">
                        {j.kind} #{j.id}
                      </div>
                    </Td>
                    <Td>{j.state === 'retryable' ? <Badge>Herhaalt</Badge> : <Badge tone="danger">Mislukt</Badge>}</Td>
                    <Td numeric className="hidden text-muted md:table-cell">
                      {j.attempt} van {j.max_attempts}
                    </Td>
                    <Td className="hidden max-w-sm text-sm break-words text-muted lg:table-cell">{j.last_error || '—'}</Td>
                    <Td className="hidden whitespace-nowrap text-muted md:table-cell">{formatDateTime(j.scheduled_at)}</Td>
                    <Td>
                      <div className="flex justify-end">
                        <Button size="sm" busy={retryJob.isPending && retryJob.variables.id === j.id} aria-label={`Taak ${j.id} opnieuw proberen`} onClick={() => retryJob.mutate(j)}>
                          Opnieuw proberen
                        </Button>
                      </div>
                    </Td>
                  </Tr>
                ))}
              </TBody>
            </Table>
          </>
        )}
      </Card>

      <Card title="Mail die niet is verwerkt" description="Opgeslagen berichten die niet zijn omgezet in een gesprek. De mail zelf is bewaard." flush>
        {raw.isPending ? (
          <div className="p-4">
            <Skeleton className="h-24" />
          </div>
        ) : raw.isError ? (
          <div className="p-4">
            <ErrorNotice>{errorMessage(raw.error)}</ErrorNotice>
          </div>
        ) : raw.data.raw_messages.length === 0 ? (
          <EmptyState icon={<CircleCheck size={20} />} title="Alle mail is verwerkt" />
        ) : (
          <>
            {retryRaw.isError && (
              <div className="p-4">
                <ErrorNotice>{errorMessage(retryRaw.error)}</ErrorNotice>
              </div>
            )}
            <Table>
              <THead>
                <Th>Mailbox</Th>
                <Th>Status</Th>
                <Th className="hidden md:table-cell">Reden</Th>
                <Th className="hidden md:table-cell">Ontvangen</Th>
                <Th className="w-px">
                  <span className="sr-only">Acties</span>
                </Th>
              </THead>
              <TBody>
                {raw.data.raw_messages.map((m) => (
                  <Tr key={m.id}>
                    <Td className="text-ink">{m.mailbox_name}</Td>
                    <Td>{m.parse_status === 'failed' ? <Badge tone="danger">Mislukt</Badge> : <Badge>Overgeslagen</Badge>}</Td>
                    <Td className="hidden max-w-sm text-sm break-words text-muted md:table-cell">{m.reason || '—'}</Td>
                    <Td className="hidden whitespace-nowrap text-muted md:table-cell">{formatDateTime(m.received_at)}</Td>
                    <Td>
                      <div className="flex justify-end">
                        <Button size="sm" busy={retryRaw.isPending && retryRaw.variables.id === m.id} aria-label={`Mail van ${formatDateTime(m.received_at)} opnieuw proberen`} onClick={() => retryRaw.mutate(m)}>
                          Opnieuw proberen
                        </Button>
                      </div>
                    </Td>
                  </Tr>
                ))}
              </TBody>
            </Table>
          </>
        )}
      </Card>
    </Page>
  )
}
