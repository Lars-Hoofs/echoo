import type { Editor } from '@tiptap/core'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { getRouteApi, Link, Navigate, useNavigate } from '@tanstack/react-router'
import { Check, Send } from 'lucide-react'
import { type ReactNode, useState } from 'react'

import { Dialog, DialogFooter } from '../../components/Dialog'
import { VariableHighlight } from '../../components/editor/extensions'
import { type EditorValue, RichEditor } from '../../components/editor/RichEditor'
import { useToast } from '../../components/Toast'
import { Button, Card, ErrorNotice, Field, Input, PageHeader, Segmented, Select, Skeleton } from '../../components/ui'
import { api, ApiError } from '../../lib/api'
import {
  type Campaign,
  campaignQuery,
  campaignsQuery,
  campaignVariables,
  type DraftForm,
  type FormErrors,
  hasErrors,
  parseSchedule,
  parseRate,
  type CampaignPreview,
  previewSummary,
  type StepId,
  steps,
  unknownVariables,
  validateStep,
} from '../../lib/campaigns'
import { segmentsQuery } from '../../lib/contacts'
import { errorMessage, fieldError } from '../../lib/errors'
import { summaryQuery } from '../../lib/inbox'

const editRoute = getRouteApi('/auth/ready/campagnes/$id/bewerken')

const editorExtensions = [VariableHighlight]
const numberFormat = new Intl.NumberFormat('nl-NL')

export function NewCampaignPage() {
  return <Wizard step="mailbox" />
}

export function EditCampaignPage() {
  const { id } = editRoute.useParams()
  const { stap } = editRoute.useSearch()
  const campaign = useQuery(campaignQuery(id))
  if (campaign.isPending) return <Frame><Skeleton className="h-64" /></Frame>
  if (campaign.isError) return <Frame><ErrorNotice>{errorMessage(campaign.error)}</ErrorNotice></Frame>
  if (campaign.data.status !== 'draft') return <Navigate to="/campagnes/$id" params={{ id }} replace />
  return <Wizard campaign={campaign.data} step={stap} />
}

function Frame({ children }: { children: ReactNode }) {
  return (
    <div className="relative h-full overflow-y-auto px-4 py-8 md:px-8">
      <div className="mx-auto flex w-full max-w-3xl flex-col gap-6">{children}</div>
    </div>
  )
}

function initialForm(c: Campaign | undefined, defaultRate: number): DraftForm {
  const text = (c?.body_html ?? '').replace(/<[^>]*>/g, '').trim()
  return {
    name: c?.name ?? '',
    mailboxId: c?.mailbox.id ?? '',
    rate: String(c?.rate_per_minute ?? defaultRate),
    segmentId: c?.segment?.id ?? '',
    subject: c?.subject ?? '',
    bodyHtml: c?.body_html ?? '',
    bodyEmpty: text === '' && !(c?.body_html ?? '').includes('<img'),
  }
}

function serverBodyError(err: unknown): string | undefined {
  if (!(err instanceof ApiError)) return undefined
  const code = err.fields.body_html
  if (!code) return undefined
  if (code === 'unknown_variable') return 'De tekst bevat een variabele die niet bestaat in een campagne.'
  if (code === 'too_large') return 'De tekst is te lang.'
  return 'Schrijf de tekst van de e-mail.'
}

function Wizard({ campaign, step }: { campaign?: Campaign; step: StepId }) {
  const navigate = useNavigate()
  const queryClient = useQueryClient()
  const toast = useToast()
  const limits = useQuery(campaignsQuery)
  const maxRate = limits.data?.limits.max_rate ?? 120
  const defaultRate = Math.min(limits.data?.limits.default_rate ?? 60, maxRate)
  const [form, setForm] = useState(() => initialForm(campaign, defaultRate))
  const [shown, setShown] = useState<FormErrors>({})
  const [editor, setEditor] = useState<Editor | null>(null)
  const [testedTo, setTestedTo] = useState('')
  const set = <K extends keyof DraftForm>(key: K, value: DraftForm[K]) => setForm((f) => ({ ...f, [key]: value }))
  const index = steps.findIndex((s) => s.id === step)

  const goTo = (id: string, next: StepId) =>
    navigate({ to: '/campagnes/$id/bewerken', params: { id }, search: { stap: next }, replace: campaign === undefined })

  const save = useMutation({
    mutationFn: async (): Promise<string> => {
      const body: Record<string, unknown> =
        step === 'mailbox'
          ? { name: form.name.trim(), mailbox_id: form.mailboxId, rate_per_minute: parseRate(form.rate, maxRate) }
          : step === 'segment'
            ? { segment_id: form.segmentId }
            : { subject: form.subject.trim(), body_html: form.bodyHtml }
      if (campaign) {
        await api('PATCH', `/campaigns/${campaign.id}`, body)
        return campaign.id
      }
      return (await api<{ campaign: Campaign }>('POST', '/campaigns', body)).campaign.id
    },
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: ['campaigns'] })
    },
  })

  const next = async () => {
    const errors = validateStep(step, form, maxRate)
    setShown(errors)
    if (hasErrors(errors)) return
    const id = await save.mutateAsync()
    const target = steps[index + 1]
    if (target) await goTo(id, target.id)
  }

  const sendTest = useMutation({
    mutationFn: async () => {
      const id = await save.mutateAsync()
      return api<{ sent_to: string }>('POST', `/campaigns/${id}/test`, {})
    },
    onSuccess: (r) => {
      setTestedTo(r.sent_to)
      toast(`Testmail verstuurd naar ${r.sent_to}.`)
    },
  })
  const testMail = () => {
    const errors = validateStep('content', form, maxRate)
    setShown(errors)
    if (!hasErrors(errors)) sendTest.mutate()
  }

  const saveError = save.error ?? undefined
  const testReason = sendTest.error instanceof ApiError && sendTest.error.code === 'test_send_failed' ? sendTest.error.fields.reason : undefined

  return (
    <Frame>
      <PageHeader
        title={campaign ? `Campagne bewerken` : 'Nieuwe campagne'}
        {...(campaign ? { description: campaign.name } : {})}
        breadcrumb={
          <Link to="/campagnes" className="hover:text-ink">
            Campagnes
          </Link>
        }
      />
      <Steps current={index} />
      {step === 'mailbox' && <MailboxStep form={form} set={set} errors={shown} serverError={saveError} maxRate={maxRate} />}
      {step === 'segment' && <SegmentStep form={form} set={set} errors={shown} serverError={saveError} />}
      {step === 'content' && (
        <ContentStep
          form={form}
          set={set}
          errors={shown}
          serverError={saveError}
          setEditor={setEditor}
          editor={editor}
          onTest={testMail}
          testing={sendTest.isPending}
          testedTo={testedTo}
          testFailed={sendTest.isError}
          testReason={testReason}
        />
      )}
      {step === 'schedule' && campaign && <ScheduleStep campaign={campaign} onBack={() => goTo(campaign.id, 'content')} />}
      {save.isError && step !== 'schedule' && <ErrorNotice>{errorMessage(save.error)}</ErrorNotice>}
      {step !== 'schedule' && (
        <div className="flex items-center justify-between gap-2">
          {index > 0 && campaign ? (
            <Button onClick={() => void goTo(campaign.id, steps[index - 1]?.id ?? 'mailbox')}>Terug</Button>
          ) : (
            <span />
          )}
          <Button variant="primary" busy={save.isPending} onClick={() => void next()}>
            Volgende
          </Button>
        </div>
      )}
    </Frame>
  )
}

function Steps({ current }: { current: number }) {
  return (
    <ol aria-label="Stappen" className="flex flex-wrap gap-2 text-base">
      {steps.map((s, i) => (
        <li
          key={s.id}
          aria-current={i === current ? 'step' : undefined}
          className={`flex h-8 items-center gap-2 rounded-full pr-4 pl-1 ${i === current ? 'bg-ink text-on-ink' : 'border border-line-strong text-muted'}`}
        >
          <span
            aria-hidden
            className={`inline-flex size-6 items-center justify-center rounded-full text-xs tabular-nums ${i === current ? 'bg-on-ink text-ink' : 'bg-subtle'}`}
          >
            {i < current ? <Check size={12} /> : i + 1}
          </span>
          {s.label}
        </li>
      ))}
    </ol>
  )
}

interface StepProps {
  form: DraftForm
  set: <K extends keyof DraftForm>(key: K, value: DraftForm[K]) => void
  errors: FormErrors
  serverError: unknown
}

function MailboxStep({ form, set, errors, serverError, maxRate }: StepProps & { maxRate: number }) {
  const summary = useQuery(summaryQuery)
  return (
    <Card title="Mailbox" description="Vanuit welke mailbox gaat de campagne? Antwoorden komen in de inbox van deze mailbox.">
      <div className="flex flex-col gap-4">
        <Field label="Naam van de campagne" help="Alleen voor jezelf, ontvangers zien hem niet." error={errors.name ?? fieldError(serverError, 'name')}>
          {(p) => <Input {...p} value={form.name} onChange={(e) => set('name', e.target.value)} maxLength={100} />}
        </Field>
        <Field label="Mailbox" error={errors.mailboxId ?? fieldError(serverError, 'mailbox_id')}>
          {(p) => (
            <Select {...p} value={form.mailboxId} onChange={(e) => set('mailboxId', e.target.value)}>
              <option value="">Kies een mailbox</option>
              {(summary.data?.mailboxes ?? []).map((m) => (
                <option key={m.id} value={m.id}>
                  {m.name} ({m.email_address})
                </option>
              ))}
            </Select>
          )}
        </Field>
        <Field
          label="Verzendsnelheid (berichten per minuut)"
          help={`Maximaal ${maxRate} per minuut. Kijk naar de limieten van je e-mailprovider: te snel versturen kan je mailbox blokkeren.`}
          error={errors.rate ?? fieldError(serverError, 'rate_per_minute')}
        >
          {(p) => <Input {...p} inputMode="numeric" value={form.rate} onChange={(e) => set('rate', e.target.value)} className="w-32" />}
        </Field>
      </div>
    </Card>
  )
}

const previewKey = (segmentId: string) => ['campaigns', 'preview', segmentId]

function usePreview(segmentId: string) {
  return useQuery({
    queryKey: previewKey(segmentId),
    queryFn: () => api<CampaignPreview>('POST', '/campaigns/preview', { segment_id: segmentId }),
    enabled: segmentId !== '',
    staleTime: 30_000,
  })
}

function SegmentStep({ form, set, errors, serverError }: StepProps) {
  const segments = useQuery(segmentsQuery)
  const preview = usePreview(form.segmentId)
  return (
    <Card title="Segment" description="Wie krijgt de e-mail? Je ziet alleen contacten die je zelf ook in Echoo mag zien.">
      <div className="flex flex-col gap-4">
        <Field
          label="Segment"
          help={
            <>
              Een segment maak je op de pagina{' '}
              <Link to="/contacten" className="text-ink hover:underline">
                Contacten
              </Link>
              .
            </>
          }
          error={errors.segmentId ?? fieldError(serverError, 'segment_id')}
        >
          {(p) => (
            <Select {...p} value={form.segmentId} onChange={(e) => set('segmentId', e.target.value)}>
              <option value="">Kies een segment</option>
              {(segments.data ?? []).map((s) => (
                <option key={s.id} value={s.id}>
                  {s.name}
                  {s.shared ? '' : ' (persoonlijk)'}
                </option>
              ))}
            </Select>
          )}
        </Field>
        {form.segmentId !== '' && (
          <div aria-live="polite" className="card card-line card-s">
            {preview.isPending ? (
              <span className="text-muted">Ontvangers tellen…</span>
            ) : preview.isError ? (
              <span className="text-danger-text">{errorMessage(preview.error)}</span>
            ) : (
              <>
                <div className="t-title tabular-nums">
                  {numberFormat.format(preview.data.sendable)} {preview.data.sendable === 1 ? 'ontvanger' : 'ontvangers'}
                </div>
                <div className="t-label mt-1">{previewSummary(preview.data)}</div>
                {preview.data.without_name > 0 && (
                  <div className="t-label mt-1">
                    {numberFormat.format(preview.data.without_name)} van hen hebben geen naam; {'{{contact.first_name}}'} blijft dan leeg.
                  </div>
                )}
              </>
            )}
          </div>
        )}
      </div>
    </Card>
  )
}

function ContentStep({
  form,
  set,
  errors,
  serverError,
  setEditor,
  editor,
  onTest,
  testing,
  testedTo,
  testFailed,
  testReason,
}: StepProps & {
  setEditor: (e: Editor | null) => void
  editor: Editor | null
  onTest: () => void
  testing: boolean
  testedTo: string
  testFailed: boolean
  testReason: string | undefined
}) {
  const bodyError = errors.body ?? serverBodyError(serverError)
  const unknown = unknownVariables(form.subject)
  return (
    <Card title="Inhoud" description="Schrijf de e-mail. Onder elk bericht komt automatisch een afmeldlink.">
      <div className="flex flex-col gap-4">
        <Field
          label="Onderwerp"
          error={errors.subject ?? fieldError(serverError, 'subject') ?? (unknown.length > 0 ? `Onbekende variabele: {{${unknown[0] ?? ''}}}.` : undefined)}
        >
          {(p) => <Input {...p} value={form.subject} onChange={(e) => set('subject', e.target.value)} />}
        </Field>
        <div>
          <span className="text-sm text-ink">Tekst</span>
          {bodyError && <p className="mt-1 text-sm text-danger-text">{bodyError}</p>}
          <div className="mt-1.5">
            <RichEditor
              initialHtml={form.bodyHtml}
              ariaLabel="Tekst van de e-mail"
              placeholder="Schrijf de tekst"
              extensions={editorExtensions}
              onChange={(v: EditorValue) => {
                set('bodyHtml', v.html)
                set('bodyEmpty', v.empty)
              }}
              onEditor={setEditor}
            />
          </div>
          <div className="mt-2 flex flex-wrap gap-2" role="group" aria-label="Variabelen invoegen">
            <span className="t-label w-full">Klik in de tekst en voeg een variabele in. Ontvangers zonder waarde krijgen niets op die plek.</span>
            {campaignVariables.map((v) => (
              <button
                key={v.name}
                type="button"
                title={`{{${v.name}}}`}
                onMouseDown={(e) => {
                  // Keep the caret in the editor, so the variable lands where the person was typing.
                  e.preventDefault()
                }}
                onClick={() => editor?.chain().focus().insertContent(`{{${v.name}}}`).run()}
                className="inline-flex h-8 items-center rounded-full border border-line-strong px-3 text-sm text-ink hover:bg-subtle"
              >
                {v.label}
              </button>
            ))}
          </div>
        </div>
        <div className="flex flex-wrap items-center gap-3 border-t border-line pt-4">
          <Button busy={testing} onClick={onTest}>
            <Send size={16} aria-hidden />
            Testmail naar mezelf
          </Button>
          <span role="status" className="t-label">
            {testedTo && !testing && !testFailed ? `Verstuurd naar ${testedTo}. De afmeldlink in de testmail werkt niet.` : ''}
          </span>
        </div>
        {testFailed && (
          <ErrorNotice>
            De testmail kon niet worden verstuurd.{testReason ? ` De mailserver antwoordde: ${testReason}` : ''}
          </ErrorNotice>
        )}
      </div>
    </Card>
  )
}

function ScheduleStep({ campaign, onBack }: { campaign: Campaign; onBack: () => Promise<void> }) {
  const navigate = useNavigate()
  const queryClient = useQueryClient()
  const preview = usePreview(campaign.segment?.id ?? '')
  const [when, setWhen] = useState<'now' | 'later'>('now')
  const [local, setLocal] = useState('')
  const [error, setError] = useState('')
  const [confirming, setConfirming] = useState(false)

  const start = useMutation({
    mutationFn: (at: string | null) => api('POST', `/campaigns/${campaign.id}/start`, at ? { scheduled_at: at } : {}),
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: ['campaigns'] })
      await navigate({ to: '/campagnes/$id', params: { id: campaign.id } })
    },
  })

  const submit = () => {
    const s = parseSchedule(when, local)
    if (!s.ok) {
      setError(s.error)
      return
    }
    setError('')
    if (s.at === null) setConfirming(true)
    else start.mutate(s.at)
  }
  const sendable = preview.data?.sendable
  const minutes = sendable === undefined ? null : Math.max(1, Math.ceil(sendable / campaign.rate_per_minute))

  return (
    <>
      <Card title="Planning" description="Controleer de campagne en kies wanneer hij weggaat.">
        <div className="flex flex-col gap-4">
          <dl className="grid grid-cols-[max-content_1fr] items-baseline gap-x-6 gap-y-2 text-base">
            <dt className="t-label">Naam</dt>
            <dd>{campaign.name}</dd>
            <dt className="t-label">Mailbox</dt>
            <dd>{campaign.mailbox.name}</dd>
            <dt className="t-label">Segment</dt>
            <dd>
              {campaign.segment?.name ?? '—'}
              {sendable !== undefined && ` · ${numberFormat.format(sendable)} ontvangers`}
            </dd>
            <dt className="t-label">Onderwerp</dt>
            <dd>{campaign.subject}</dd>
            <dt className="t-label">Snelheid</dt>
            <dd>
              {campaign.rate_per_minute} per minuut{minutes !== null && `, dus ongeveer ${numberFormat.format(minutes)} ${minutes === 1 ? 'minuut' : 'minuten'}`}
            </dd>
          </dl>
          <Segmented
            label="Wanneer versturen"
            value={when}
            options={[
              { value: 'now', label: 'Nu versturen' },
              { value: 'later', label: 'Inplannen' },
            ]}
            onChange={(v) => {
              setWhen(v)
              setError('')
            }}
          />
          {when === 'later' && (
            <Field label="Datum en tijd" help="In jouw tijdzone." error={error}>
              {(p) => <Input {...p} type="datetime-local" value={local} onChange={(e) => setLocal(e.target.value)} className="w-64" />}
            </Field>
          )}
          {when === 'now' && error && <p className="text-sm text-danger-text">{error}</p>}
          <p className="t-label">
            De ontvangers worden bepaald op het moment dat de campagne start. Wie zich dan heeft afgemeld, wordt overgeslagen.
          </p>
        </div>
      </Card>
      {start.isError && (
        <ErrorNotice>
          {start.error instanceof ApiError && start.error.code === 'validation_failed'
            ? 'De campagne is nog niet compleet. Ga terug en vul alles in.'
            : errorMessage(start.error)}
        </ErrorNotice>
      )}
      <div className="flex items-center justify-between gap-2">
        <Button onClick={() => void onBack()}>Terug</Button>
        <Button variant="primary" busy={start.isPending} onClick={submit}>
          {when === 'now' ? 'Versturen' : 'Inplannen'}
        </Button>
      </div>
      <Dialog
        open={confirming}
        onOpenChange={setConfirming}
        title="Campagne nu versturen?"
        description={`${sendable === undefined ? 'De ontvangers' : `${numberFormat.format(sendable)} ontvangers`} krijgen deze e-mail. Verzonden mail haal je niet terug; je kunt de campagne wel pauzeren of annuleren.`}
      >
        <DialogFooter>
          <Button onClick={() => setConfirming(false)}>Annuleren</Button>
          <Button
            variant="primary"
            busy={start.isPending}
            onClick={() => {
              setConfirming(false)
              start.mutate(null)
            }}
          >
            Nu versturen
          </Button>
        </DialogFooter>
      </Dialog>
    </>
  )
}
