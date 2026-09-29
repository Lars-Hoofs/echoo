import { useInfiniteQuery, useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Pencil, Plus, Power, PowerOff, Send, Trash2, Webhook as WebhookIcon } from 'lucide-react'
import { type SubmitEvent, useState } from 'react'

import { ActionMenu } from '../../components/ActionMenu'
import { CopyButton } from '../../components/CopyButton'
import { Dialog, DialogFooter } from '../../components/Dialog'
import { useToast } from '../../components/Toast'
import { Badge, Button, Card, EmptyState, ErrorNotice, Field, Input, Page, PageHeader, Skeleton, Table, TBody, Td, Th, THead, Tr } from '../../components/ui'
import { api } from '../../lib/api'
import { errorMessage, fieldError } from '../../lib/errors'
import { formatDateTime } from '../../lib/format'
import { type DeliveryStatus, deliveryErrorLabel, deliveryStatusLabel, disabledReasonLabel, eventLabel, webhookEvents } from '../../lib/platform'
import { meQuery } from '../../lib/session'

interface Webhook {
  id: string
  url: string
  events: string[]
  include_content: boolean
  allow_http: boolean
  enabled: boolean
  disabled_reason: string
  consecutive_failures: number
}

interface Delivery {
  id: string
  event: string
  status: DeliveryStatus
  status_code: number | null
  error: string
  duration_ms: number | null
  attempt: number
  created_at: string
  updated_at: string
}

type Editing = { kind: 'new' } | { kind: 'edit' | 'log' | 'delete'; hook: Webhook }

const statusTone = (s: DeliveryStatus) => (s === 'failed' ? 'danger' : 'neutral')

function displayUrl(url: string): string {
  try {
    const u = new URL(url)
    return u.host + (u.pathname === '/' ? '' : u.pathname)
  } catch {
    return url
  }
}

export function WebhooksPage() {
  const queryClient = useQueryClient()
  const hooks = useQuery({ queryKey: ['webhooks'], queryFn: () => api<{ webhooks: Webhook[] }>('GET', '/webhooks') })
  const [editing, setEditing] = useState<Editing | null>(null)
  const toast = useToast()

  const toggle = useMutation({
    mutationFn: (h: Webhook) => api('PATCH', `/webhooks/${h.id}`, { enabled: !h.enabled }),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ['webhooks'] }),
    onError: (err) => toast(errorMessage(err), { tone: 'error' }),
  })
  const test = useMutation({
    mutationFn: (h: Webhook) => api('POST', `/webhooks/${h.id}/test`),
    onSuccess: (_, h) => {
      toast(`Het testbericht voor ${displayUrl(h.url)} staat in de wachtrij. Het resultaat zie je onder Berichten.`)
      void queryClient.invalidateQueries({ queryKey: ['webhooks', h.id, 'deliveries'] })
    },
    onError: (err) => toast(errorMessage(err), { tone: 'error' }),
  })

  return (
    <Page>
      <PageHeader
        breadcrumb="Werkruimte"
        title="Webhooks"
        description="Echoo stuurt een bericht naar een webadres zodra er iets gebeurt. Elk bericht is ondertekend, zodat de ontvanger kan controleren dat het van Echoo komt."
        actions={
          <Button variant="primary" onClick={() => setEditing({ kind: 'new' })}>
            <Plus size={16} aria-hidden />
            Webhook toevoegen
          </Button>
        }
      />
      {hooks.isPending ? (
        <Skeleton className="h-40" />
      ) : hooks.isError ? (
        <ErrorNotice>{errorMessage(hooks.error)}</ErrorNotice>
      ) : hooks.data.webhooks.length === 0 ? (
        <Card>
          <EmptyState
            icon={<WebhookIcon size={20} />}
            title="Nog geen webhooks"
            description="Voeg een webadres toe om berichten te ontvangen over gesprekken en contacten."
            action={
              <Button variant="primary" onClick={() => setEditing({ kind: 'new' })}>
                Voeg de eerste webhook toe
              </Button>
            }
          />
        </Card>
      ) : (
        <Card flush>
          <Table>
            <THead>
              <Th>Webadres</Th>
              <Th className="hidden md:table-cell">Gebeurtenissen</Th>
              <Th>Status</Th>
              <Th className="w-px">
                <span className="sr-only">Acties</span>
              </Th>
            </THead>
            <TBody>
              {hooks.data.webhooks.map((h) => (
                <Tr key={h.id}>
                  <Td>
                    <div className="truncate text-ink">{displayUrl(h.url)}</div>
                    {!h.enabled && h.disabled_reason && <div className="text-sm text-danger-text">{disabledReasonLabel(h.disabled_reason)}</div>}
                  </Td>
                  <Td className="hidden text-muted md:table-cell">{h.events.length === 1 ? '1 gebeurtenis' : `${h.events.length} gebeurtenissen`}</Td>
                  <Td>{h.enabled ? <Badge dot>Actief</Badge> : <Badge>Uit</Badge>}</Td>
                  <Td>
                    <div className="flex items-center justify-end gap-1">
                      <Button size="sm" aria-label={`Berichten van ${displayUrl(h.url)}`} onClick={() => setEditing({ kind: 'log', hook: h })}>
                        Berichten
                      </Button>
                      <ActionMenu
                        label={`Acties voor ${displayUrl(h.url)}`}
                        items={[
                          { label: 'Bewerken', icon: <Pencil size={16} />, onSelect: () => setEditing({ kind: 'edit', hook: h }) },
                          { label: 'Testbericht versturen', icon: <Send size={16} />, onSelect: () => test.mutate(h) },
                          h.enabled
                            ? { label: 'Uitzetten', icon: <PowerOff size={16} />, onSelect: () => toggle.mutate(h) }
                            : { label: 'Aanzetten', icon: <Power size={16} />, onSelect: () => toggle.mutate(h) },
                          { label: 'Verwijderen', icon: <Trash2 size={16} />, danger: true, separated: true, onSelect: () => setEditing({ kind: 'delete', hook: h }) },
                        ]}
                      />
                    </div>
                  </Td>
                </Tr>
              ))}
            </TBody>
          </Table>
        </Card>
      )}
      {(editing?.kind === 'new' || editing?.kind === 'edit') && (
        <WebhookDialog hook={editing.kind === 'edit' ? editing.hook : null} onClose={() => setEditing(null)} />
      )}
      {editing?.kind === 'log' && <DeliveryLogDialog hook={editing.hook} onClose={() => setEditing(null)} />}
      {editing?.kind === 'delete' && <DeleteDialog hook={editing.hook} onClose={() => setEditing(null)} />}
    </Page>
  )
}

function WebhookDialog({ hook, onClose }: { hook: Webhook | null; onClose: () => void }) {
  const queryClient = useQueryClient()
  const me = useQuery(meQuery)
  const isOwner = me.data?.user.role === 'owner'
  const [url, setUrl] = useState(hook?.url ?? '')
  const [events, setEvents] = useState<Set<string>>(new Set(hook?.events ?? []))
  const [includeContent, setIncludeContent] = useState(hook?.include_content ?? false)
  const [allowHttp, setAllowHttp] = useState(hook?.allow_http ?? false)

  const save = useMutation({
    mutationFn: () => {
      const body = { url, events: [...events], include_content: includeContent, ...(isOwner ? { allow_http: allowHttp } : {}) }
      return hook ? api<{ secret?: string }>('PATCH', `/webhooks/${hook.id}`, body) : api<{ secret: string }>('POST', '/webhooks', body)
    },
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ['webhooks'] }),
  })
  const submit = (e: SubmitEvent) => {
    e.preventDefault()
    save.mutate()
  }
  const toggleEvent = (value: string) => {
    const next = new Set(events)
    if (next.has(value)) next.delete(value)
    else next.add(value)
    setEvents(next)
  }

  if (save.isSuccess && save.data.secret) {
    return (
      <Dialog open onOpenChange={(o) => !o && onClose()} title="Webhook toegevoegd">
        <div className="flex flex-col gap-4">
          <p className="text-base text-ink">
            Gebruik dit geheim om de handtekening van elk bericht te controleren. Kopieer het nu: het wordt niet opnieuw getoond.
          </p>
          <code className="rounded-md border border-line bg-subtle px-3 py-2 font-mono text-base break-all text-ink select-all">{save.data.secret}</code>
          <div>
            <CopyButton text={save.data.secret} label="Geheim kopiëren" />
          </div>
          <DialogFooter>
            <Button variant="primary" onClick={onClose}>
              Klaar
            </Button>
          </DialogFooter>
        </div>
      </Dialog>
    )
  }

  const fieldErrors = ['url', 'events', 'allow_http'].some((f) => fieldError(save.error, f))
  return (
    <Dialog open onOpenChange={(o) => !o && onClose()} title={hook ? 'Webhook bewerken' : 'Webhook toevoegen'} size="lg">
      <form onSubmit={submit} className="flex flex-col gap-4" noValidate>
        {save.isError && !fieldErrors && <ErrorNotice>{errorMessage(save.error)}</ErrorNotice>}
        <Field label="Webadres" help="Moet met https beginnen en publiek bereikbaar zijn." error={fieldError(save.error, 'url')}>
          {(p) => <Input {...p} autoFocus type="url" inputMode="url" placeholder="https://voorbeeld.nl/echoo" value={url} onChange={(e) => setUrl(e.target.value)} />}
        </Field>
        <fieldset className="flex flex-col gap-1.5">
          <legend className="mb-1.5 text-sm text-ink">Gebeurtenissen</legend>
          <div className="rounded-md border border-line">
            {webhookEvents.map((ev) => (
              <label key={ev.value} className="flex cursor-pointer items-center gap-3 border-b border-line px-3 py-2 last:border-b-0 hover:bg-subtle max-md:py-3">
                <input type="checkbox" checked={events.has(ev.value)} onChange={() => toggleEvent(ev.value)} className="size-4 shrink-0 accent-(--ink)" />
                <span className="text-base text-ink">{ev.label}</span>
                <span className="ml-auto hidden font-mono text-sm text-faint sm:inline">{ev.value}</span>
              </label>
            ))}
          </div>
          {fieldError(save.error, 'events') && <p className="text-sm text-danger-text">{fieldError(save.error, 'events')}</p>}
        </fieldset>
        <label className="flex cursor-pointer items-start gap-3">
          <input type="checkbox" checked={includeContent} onChange={(e) => setIncludeContent(e.target.checked)} className="mt-1 size-4 shrink-0 accent-(--ink)" />
          <span>
            <span className="block text-base text-ink">Inhoud meesturen</span>
            <span className="block text-sm text-muted">
              Voegt onderwerp, voorbeeldtekst, naam en e-mailadres toe. Zonder dit bevat het bericht alleen id&apos;s en statussen.
            </span>
          </span>
        </label>
        {isOwner && (
          <label className="flex cursor-pointer items-start gap-3">
            <input type="checkbox" checked={allowHttp} onChange={(e) => setAllowHttp(e.target.checked)} className="mt-1 size-4 shrink-0 accent-(--ink)" />
            <span>
              <span className="block text-base text-ink">Onversleutelde http toestaan</span>
              <span className="block text-sm text-muted">Alleen voor ontvangers die je zelf beheert. Berichten zijn dan onderweg leesbaar.</span>
              {fieldError(save.error, 'allow_http') && <span className="block text-sm text-danger-text">{fieldError(save.error, 'allow_http')}</span>}
            </span>
          </label>
        )}
        <DialogFooter>
          <Button onClick={onClose}>Annuleren</Button>
          <Button type="submit" variant="primary" busy={save.isPending} disabled={!url.trim() || events.size === 0}>
            Opslaan
          </Button>
        </DialogFooter>
      </form>
    </Dialog>
  )
}

function DeliveryLogDialog({ hook, onClose }: { hook: Webhook; onClose: () => void }) {
  const queryClient = useQueryClient()
  const log = useInfiniteQuery({
    queryKey: ['webhooks', hook.id, 'deliveries'],
    initialPageParam: '',
    queryFn: ({ pageParam }) =>
      api<{ deliveries: Delivery[]; next_before: string | null }>('GET', `/webhooks/${hook.id}/deliveries${pageParam ? `?before=${pageParam}` : ''}`),
    getNextPageParam: (last) => last.next_before ?? undefined,
    refetchInterval: (q) => (q.state.data?.pages.some((p) => p.deliveries.some((d) => d.status === 'pending' || d.status === 'retrying')) ? 5000 : false),
  })
  const resend = useMutation({
    mutationFn: (d: Delivery) => api('POST', `/webhooks/${hook.id}/deliveries/${d.id}/resend`),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ['webhooks', hook.id, 'deliveries'] }),
  })
  const deliveries = log.data?.pages.flatMap((p) => p.deliveries) ?? []

  return (
    <Dialog open onOpenChange={(o) => !o && onClose()} title="Berichten" description={`${displayUrl(hook.url)}. Het log wordt 14 dagen bewaard.`} size="xl">
      {log.isPending ? (
        <Skeleton className="h-32" />
      ) : log.isError ? (
        <ErrorNotice>{errorMessage(log.error)}</ErrorNotice>
      ) : deliveries.length === 0 ? (
        <p className="py-6 text-center text-base text-muted">Er zijn nog geen berichten verstuurd.</p>
      ) : (
        <div className="flex flex-col gap-3">
          {resend.isError && <ErrorNotice>{errorMessage(resend.error)}</ErrorNotice>}
          <div className="rounded-md border border-line">
            <Table>
              <THead>
                <Th>Gebeurtenis</Th>
                <Th>Status</Th>
                <Th numeric className="hidden sm:table-cell">
                  Code
                </Th>
                <Th numeric className="hidden sm:table-cell">
                  Duur
                </Th>
                <Th numeric className="hidden md:table-cell">
                  Poging
                </Th>
                <Th className="hidden md:table-cell">Tijdstip</Th>
                <Th className="w-px">
                  <span className="sr-only">Acties</span>
                </Th>
              </THead>
              <TBody>
                {deliveries.map((d) => (
                  <Tr key={d.id}>
                    <Td>
                      <div className="text-ink">{eventLabel(d.event)}</div>
                      {d.error && <div className="text-sm text-danger-text">{deliveryErrorLabel(d.error)}</div>}
                    </Td>
                    <Td>
                      <Badge tone={statusTone(d.status)}>{deliveryStatusLabel[d.status]}</Badge>
                    </Td>
                    <Td numeric className="hidden text-muted sm:table-cell">
                      {d.status_code ?? '—'}
                    </Td>
                    <Td numeric className="hidden whitespace-nowrap text-muted sm:table-cell">
                      {d.duration_ms === null ? '—' : `${d.duration_ms} ms`}
                    </Td>
                    <Td numeric className="hidden text-muted md:table-cell">
                      {d.attempt || '—'}
                    </Td>
                    <Td className="hidden whitespace-nowrap text-muted md:table-cell">{formatDateTime(d.created_at)}</Td>
                    <Td>
                      <div className="flex justify-end">
                        <Button size="sm" busy={resend.isPending && resend.variables.id === d.id} onClick={() => resend.mutate(d)} aria-label={`${eventLabel(d.event)} opnieuw versturen`}>
                          Opnieuw versturen
                        </Button>
                      </div>
                    </Td>
                  </Tr>
                ))}
              </TBody>
            </Table>
          </div>
          {log.hasNextPage && (
            <div>
              <Button busy={log.isFetchingNextPage} onClick={() => void log.fetchNextPage()}>
                Meer laden
              </Button>
            </div>
          )}
        </div>
      )}
    </Dialog>
  )
}

function DeleteDialog({ hook, onClose }: { hook: Webhook; onClose: () => void }) {
  const queryClient = useQueryClient()
  const del = useMutation({
    mutationFn: () => api('DELETE', `/webhooks/${hook.id}`),
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: ['webhooks'] })
      onClose()
    },
  })
  return (
    <Dialog open onOpenChange={(o) => !o && onClose()} title="Webhook verwijderen">
      <div className="flex flex-col gap-4">
        <p className="text-base text-muted">{displayUrl(hook.url)} krijgt geen berichten meer en het bijbehorende log verdwijnt.</p>
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
