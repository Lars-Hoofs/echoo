import { infiniteQueryOptions, queryOptions } from '@tanstack/react-query'

import { api } from './api'

export type CampaignStatus = 'draft' | 'scheduled' | 'sending' | 'paused' | 'done' | 'cancelled'
export type RecipientState = 'pending' | 'queued' | 'sent' | 'failed' | 'skipped'

export interface CampaignCounts {
  total: number
  pending: number
  queued: number
  sent: number
  failed: number
  skipped: number
  skipped_reasons: Record<string, number>
}

export interface Campaign {
  id: string
  name: string
  status: CampaignStatus
  mailbox: { id: string; name: string; address: string }
  segment: { id: string; name: string } | null
  subject: string
  body_html: string
  rate_per_minute: number
  scheduled_at: string | null
  started_at: string | null
  finished_at: string | null
  error: string
  created_by: { id: string; name: string } | null
  created_at: string
  updated_at: string
  counts: CampaignCounts
}

export interface CampaignLimits {
  default_rate: number
  max_rate: number
}

export interface CampaignPreview {
  total: number
  sendable: number
  unsubscribed: number
  no_address: number
  duplicate: number
  bounced: number
  without_name: number
}

export interface CampaignRecipient {
  id: string
  email: string
  name: string
  state: RecipientState
  skip_reason: string
  delivery: string
  error: string
  conversation_id: string
  queued_at: string | null
  finished_at: string | null
}

export const statusLabel: Record<CampaignStatus, string> = {
  draft: 'Concept',
  scheduled: 'Ingepland',
  sending: 'Bezig met verzenden',
  paused: 'Gepauzeerd',
  done: 'Klaar',
  cancelled: 'Geannuleerd',
}

export const stateLabel: Record<RecipientState, string> = {
  pending: 'Wacht',
  queued: 'In wachtrij',
  sent: 'Verzonden',
  failed: 'Mislukt',
  skipped: 'Overgeslagen',
}

export const skipReasonLabel: Record<string, string> = {
  unsubscribed: 'Afgemeld',
  no_address: 'Geen e-mailadres',
  duplicate: 'Dubbel adres',
  bounced: 'Adres werkt niet',
  cancelled: 'Campagne geannuleerd',
}

// What the server tells about a campaign that stopped by itself.
export const stopReason: Record<string, string> = {
  segment_missing: 'Het segment bestaat niet meer of is niet zichtbaar voor de maker. De campagne is geannuleerd.',
  creator_unavailable: 'De maker van de campagne is niet meer actief. De campagne is geannuleerd.',
  mailbox_unavailable: 'De mailbox staat uit. Zet de mailbox weer aan en hervat de campagne.',
}

// Must match campaigns.Variables in internal/campaigns/render.go: a campaign has no
// conversation yet, so there is no conversation number.
export const campaignVariables = [
  { name: 'contact.name', label: 'Naam contactpersoon' },
  { name: 'contact.first_name', label: 'Voornaam contactpersoon' },
  { name: 'contact.email', label: 'E-mailadres contactpersoon' },
  { name: 'agent.name', label: 'Jouw naam' },
  { name: 'agent.first_name', label: 'Jouw voornaam' },
  { name: 'mailbox.name', label: 'Naam mailbox' },
] as const

const VARIABLE = /\{\{\s*([a-z_]+\.[a-z_]+)\s*\}\}/g

// The placeholders in the text that a campaign cannot fill. The server refuses them too.
export function unknownVariables(...texts: string[]): string[] {
  const known = new Set<string>(campaignVariables.map((v) => v.name))
  const found = new Set<string>()
  for (const text of texts) {
    for (const m of text.matchAll(VARIABLE)) {
      if (m[1] && !known.has(m[1])) found.add(m[1])
    }
  }
  return [...found]
}

export type StepId = 'mailbox' | 'segment' | 'content' | 'schedule'

export const steps: { id: StepId; label: string }[] = [
  { id: 'mailbox', label: 'Mailbox' },
  { id: 'segment', label: 'Segment' },
  { id: 'content', label: 'Inhoud' },
  { id: 'schedule', label: 'Planning' },
]

export const stepFromSlug = (slug: unknown): StepId => steps.find((s) => s.id === slug)?.id ?? 'mailbox'

export interface DraftForm {
  name: string
  mailboxId: string
  rate: string
  segmentId: string
  subject: string
  bodyHtml: string
  bodyEmpty: boolean
}

export type FormErrors = Partial<Record<'name' | 'mailboxId' | 'rate' | 'segmentId' | 'subject' | 'body', string>>

const runes = (s: string) => Array.from(s).length

const MAX_NAME = 100
const MAX_SUBJECT = 300

export function parseRate(value: string, maxRate: number): number | null {
  const n = Number(value)
  return /^\d+$/.test(value.trim()) && n >= 1 && n <= maxRate ? n : null
}

// The checks of one wizard step, in Dutch. The server repeats them; this keeps the person on the
// step until it can be saved.
export function validateStep(step: StepId, form: DraftForm, maxRate: number): FormErrors {
  const errors: FormErrors = {}
  if (step === 'mailbox') {
    const name = form.name.trim()
    if (name === '') errors.name = 'Geef de campagne een naam.'
    else if (runes(name) > MAX_NAME) errors.name = `Gebruik maximaal ${MAX_NAME} tekens.`
    if (form.mailboxId === '') errors.mailboxId = 'Kies een mailbox.'
    if (parseRate(form.rate, maxRate) === null) errors.rate = `Vul een aantal in van 1 tot ${maxRate}.`
  }
  if (step === 'segment' && form.segmentId === '') errors.segmentId = 'Kies een segment.'
  if (step === 'content') {
    const subject = form.subject.trim()
    if (subject === '') errors.subject = 'Vul een onderwerp in.'
    else if (runes(subject) > MAX_SUBJECT) errors.subject = `Gebruik maximaal ${MAX_SUBJECT} tekens.`
    else if (/[\r\n]/.test(subject)) errors.subject = 'Een onderwerp bestaat uit één regel.'
    if (form.bodyEmpty) errors.body = 'Schrijf de tekst van de e-mail.'
    const unknown = unknownVariables(form.subject, form.bodyHtml)
    if (unknown.length > 0 && !errors.subject && !errors.body) {
      errors.body = `Deze variabelen bestaan niet in een campagne: ${unknown.map((n) => `{{${n}}}`).join(', ')}.`
    }
  }
  return errors
}

export const hasErrors = (errors: FormErrors) => Object.keys(errors).length > 0

export type Schedule = { ok: true; at: string | null } | { ok: false; error: string }

// The planning step: "now" sends at once; otherwise the value of a datetime-local input, which
// is in the browser's time zone, must lie at least a minute ahead and at most a year.
export function parseSchedule(when: 'now' | 'later', local: string, now = new Date()): Schedule {
  if (when === 'now') return { ok: true, at: null }
  const at = new Date(local)
  if (local === '' || Number.isNaN(at.getTime())) return { ok: false, error: 'Kies een datum en tijd.' }
  if (at.getTime() < now.getTime() + 60_000) return { ok: false, error: 'Kies een moment in de toekomst.' }
  if (at.getTime() > now.getTime() + 365 * 24 * 3600_000) return { ok: false, error: 'Plan maximaal een jaar vooruit.' }
  return { ok: true, at: at.toISOString() }
}

// The progress bar counts the recipients that are settled: sent, failed and skipped. Queued
// and pending ones are still to come.
export function progress(counts: CampaignCounts): { done: number; total: number; percent: number } {
  const done = counts.sent + counts.failed + counts.skipped
  return { done, total: counts.total, percent: counts.total === 0 ? 0 : Math.floor((done / counts.total) * 100) }
}

export const isActive = (c: Pick<Campaign, 'status' | 'counts'>) =>
  c.status === 'scheduled' || c.status === 'sending' || c.status === 'paused' || c.counts.queued > 0

export function previewSummary(p: CampaignPreview): string {
  const skipped = [
    p.unsubscribed > 0 ? `${p.unsubscribed} afgemeld` : '',
    p.no_address > 0 ? `${p.no_address} zonder e-mailadres` : '',
    p.bounced > 0 ? `${p.bounced} met een adres dat niet werkt` : '',
    p.duplicate > 0 ? `${p.duplicate} dubbel` : '',
  ].filter(Boolean)
  return skipped.length > 0 ? `Wordt overgeslagen: ${skipped.join(', ')}.` : 'Niemand wordt overgeslagen.'
}

export const campaignsQuery = queryOptions({
  queryKey: ['campaigns'],
  queryFn: () => api<{ campaigns: Campaign[]; limits: CampaignLimits }>('GET', '/campaigns'),
  refetchInterval: (query) => (query.state.data?.campaigns.some(isActive) ? 10_000 : false),
})

export const campaignQuery = (id: string) =>
  queryOptions({
    queryKey: ['campaigns', id],
    queryFn: () => api<{ campaign: Campaign }>('GET', `/campaigns/${id}`).then((r) => r.campaign),
    refetchInterval: (query) => (query.state.data && isActive(query.state.data) ? 5_000 : false),
  })

export const recipientsQuery = (id: string, state: RecipientState | '') =>
  infiniteQueryOptions({
    queryKey: ['campaigns', id, 'recipients', state],
    queryFn: ({ pageParam }) =>
      api<{ recipients: CampaignRecipient[]; next: string | null }>(
        'GET',
        `/campaigns/${id}/recipients?limit=50${state ? `&state=${state}` : ''}${pageParam ? `&after=${pageParam}` : ''}`,
      ),
    initialPageParam: '',
    getNextPageParam: (last) => last.next ?? undefined,
  })
