import { queryOptions } from '@tanstack/react-query'

import { api } from './api'

export type Trigger = 'conversation_created' | 'message_received' | 'conversation_updated' | 'sla_at_risk' | 'sla_breached' | 'customer_idle'

// Each label completes the sentence "Als ...".
export const triggers: { value: Trigger; label: string }[] = [
  { value: 'conversation_created', label: 'een nieuw gesprek binnenkomt' },
  { value: 'message_received', label: 'de klant een nieuw bericht stuurt' },
  { value: 'conversation_updated', label: 'een gesprek wordt gewijzigd (status, toewijzing of labels)' },
  { value: 'sla_at_risk', label: 'een SLA-deadline dreigt te verlopen' },
  { value: 'sla_breached', label: 'een SLA-deadline is verlopen' },
  { value: 'customer_idle', label: 'de klant niet reageert op een wachtend gesprek' },
]

export const triggerLabel = (t: string): string => triggers.find((x) => x.value === t)?.label ?? t

export type FieldKey =
  | 'subject'
  | 'body'
  | 'from_address'
  | 'from_domain'
  | 'organization'
  | 'mailbox'
  | 'status'
  | 'priority'
  | 'label'
  | 'has_attachment'
  | 'has_assignee'
  | 'has_team'
  | 'auto_submitted'
  | 'within_business_hours'

export type FieldKind = 'text' | 'ids' | 'bool'

export const conditionFields: { key: FieldKey; label: string; kind: FieldKind }[] = [
  { key: 'subject', label: 'Onderwerp', kind: 'text' },
  { key: 'body', label: 'Berichttekst', kind: 'text' },
  { key: 'from_address', label: 'Afzender (adres)', kind: 'text' },
  { key: 'from_domain', label: 'Afzender (domein)', kind: 'text' },
  { key: 'organization', label: 'Organisatie van de contactpersoon', kind: 'text' },
  { key: 'mailbox', label: 'Mailbox', kind: 'ids' },
  { key: 'status', label: 'Status', kind: 'ids' },
  { key: 'priority', label: 'Prioriteit', kind: 'ids' },
  { key: 'label', label: 'Label', kind: 'ids' },
  { key: 'has_attachment', label: 'Bijlage', kind: 'bool' },
  { key: 'has_assignee', label: 'Toegewezen aan een agent', kind: 'bool' },
  { key: 'has_team', label: 'Toegewezen aan een team', kind: 'bool' },
  { key: 'auto_submitted', label: 'Automatisch verstuurd bericht', kind: 'bool' },
  { key: 'within_business_hours', label: 'Ontvangen binnen werktijden', kind: 'bool' },
]

export const fieldByKey = (key: string) => conditionFields.find((f) => f.key === key)

// An operator as the editor shows it: the API operator, prefixed with "!" for its negation.
export type EditorOp = string

const textOps: { value: string; label: string }[] = [
  { value: 'equals', label: 'is gelijk aan' },
  { value: '!equals', label: 'is niet gelijk aan' },
  { value: 'contains', label: 'bevat' },
  { value: '!contains', label: 'bevat niet' },
  { value: 'starts_with', label: 'begint met' },
  { value: '!starts_with', label: 'begint niet met' },
  { value: 'ends_with', label: 'eindigt op' },
  { value: '!ends_with', label: 'eindigt niet op' },
]

export function opsFor(field: FieldKey): { value: string; label: string }[] {
  const kind = fieldByKey(field)?.kind
  if (kind === 'text') return textOps
  if (field === 'label') return [{ value: 'has_any', label: 'heeft' }, { value: '!has_any', label: 'heeft niet' }]
  if (kind === 'ids') return [{ value: 'is', label: 'is' }, { value: '!is', label: 'is niet' }]
  return [{ value: 'is', label: 'is' }]
}

export interface ConditionLeaf {
  field: string
  op: string
  value: string | string[] | boolean
  negate?: boolean
}

export interface ConditionGroup {
  match: 'all' | 'any'
  items: ConditionNode[]
}

export type ConditionNode = ConditionLeaf | ConditionGroup

const isGroup = (n: ConditionNode): n is ConditionGroup => 'items' in n || 'match' in n

// One line in the condition editor. Groups made through the API are kept as they are.
export type ConditionRow =
  | { kind: 'condition'; field: FieldKey; op: EditorOp; text: string; ids: string[]; flag: boolean }
  | { kind: 'group'; node: ConditionGroup }

export function newConditionRow(field: FieldKey = 'subject'): ConditionRow {
  return { kind: 'condition', field, op: opsFor(field)[0]?.value ?? 'is', text: '', ids: [], flag: true }
}

export function rowsFromConditions(c: ConditionGroup): { match: 'all' | 'any'; rows: ConditionRow[] } {
  const rows = c.items.map((n): ConditionRow => {
    if (isGroup(n)) return { kind: 'group', node: n }
    const field = (fieldByKey(n.field)?.key ?? 'subject') satisfies FieldKey
    return {
      kind: 'condition',
      field,
      op: n.negate ? `!${n.op}` : n.op,
      text: typeof n.value === 'string' ? n.value : '',
      ids: Array.isArray(n.value) ? n.value : [],
      flag: typeof n.value === 'boolean' ? n.value : true,
    }
  })
  return { match: c.match, rows }
}

export function conditionsFromRows(match: 'all' | 'any', rows: ConditionRow[]): ConditionGroup {
  const items = rows.map((r): ConditionNode => {
    if (r.kind === 'group') return r.node
    const kind = fieldByKey(r.field)?.kind
    const negate = r.op.startsWith('!')
    const op = negate ? r.op.slice(1) : r.op
    const value = kind === 'text' ? r.text.trim() : kind === 'bool' ? r.flag : r.ids
    return { field: r.field, op, value, ...(negate ? { negate: true } : {}) }
  })
  return { match, items }
}

export type ActionType =
  | 'assign_agent'
  | 'assign_team'
  | 'assign_round_robin'
  | 'set_priority'
  | 'add_label'
  | 'remove_label'
  | 'set_status'
  | 'mark_spam'
  | 'snooze'
  | 'apply_sla'
  | 'add_note'
  | 'send_webhook'
  | 'auto_reply'

export interface RuleAction {
  type: ActionType
  user_id?: string
  team_id?: string
  priority?: string
  label_id?: string
  status?: string
  hours?: number
  policy_id?: string
  template_id?: string
  subject?: string
  text?: string
}

// needsDirectory: the action picks a user, team or policy, which only admins can list.
export const actionTypes: { value: ActionType; label: string; needsDirectory?: boolean; rulesOnly?: boolean }[] = [
  { value: 'add_label', label: 'Label toevoegen' },
  { value: 'remove_label', label: 'Label verwijderen' },
  { value: 'set_priority', label: 'Prioriteit instellen' },
  { value: 'set_status', label: 'Status instellen' },
  { value: 'assign_agent', label: 'Toewijzen aan agent', needsDirectory: true },
  { value: 'assign_team', label: 'Toewijzen aan team', needsDirectory: true },
  { value: 'assign_round_robin', label: 'Verdelen over beschikbare agenten' },
  { value: 'snooze', label: 'Uitstellen' },
  { value: 'apply_sla', label: 'SLA-beleid toepassen', needsDirectory: true },
  { value: 'auto_reply', label: 'Automatisch antwoord versturen', rulesOnly: true },
  { value: 'add_note', label: 'Interne notitie toevoegen' },
  { value: 'send_webhook', label: 'Webhook versturen' },
  { value: 'mark_spam', label: 'Markeren als spam' },
]

export const actionLabel = (t: string): string => actionTypes.find((a) => a.value === t)?.label ?? t

export const settableStatuses = [
  { value: 'open', label: 'Open' },
  { value: 'waiting', label: 'Wachtend' },
  { value: 'closed', label: 'Gesloten' },
]

export interface Rule {
  id: string
  name: string
  mailbox_id: string | null
  trigger: Trigger
  idle_hours: number | null
  conditions: ConditionGroup
  actions: RuleAction[]
  stop_processing: boolean
  position: number
  enabled: boolean
  created_at: string
  updated_at: string
}

export interface RuleActionResult {
  type: ActionType
  result: 'applied' | 'skipped' | 'failed'
  detail?: string
}

export interface RuleRun {
  id: string
  conversation_id: string
  conversation_number: number
  trigger: string
  matched: boolean
  actions: RuleActionResult[]
  error: string
  created_at: string
}

export interface BusinessHoursRange {
  start: string
  end: string
}

export const weekdays = [
  { key: 'mon', label: 'Maandag' },
  { key: 'tue', label: 'Dinsdag' },
  { key: 'wed', label: 'Woensdag' },
  { key: 'thu', label: 'Donderdag' },
  { key: 'fri', label: 'Vrijdag' },
  { key: 'sat', label: 'Zaterdag' },
  { key: 'sun', label: 'Zondag' },
] as const

export type WeekdayKey = (typeof weekdays)[number]['key']

export interface BusinessHours {
  id: string
  name: string
  timezone: string
  weekly: Partial<Record<WeekdayKey, BusinessHoursRange[]>>
  holidays: string[]
  is_default: boolean
}

export interface SlaPolicy {
  id: string
  name: string
  first_response_minutes: number | null
  resolution_minutes: number | null
  at_risk_percent: number
  business_hours_id: string | null
}

export type AssignMode = 'off' | 'round_robin' | 'balanced'

export const assignModeLabel: Record<AssignMode, string> = {
  off: 'Uit',
  round_robin: 'Om de beurt',
  balanced: 'Gelijkmatig op drukte',
}

export interface MailboxAutomation {
  id: string
  name: string
  email_address: string
  auto_assign_mode: AssignMode
  default_sla_policy_id: string | null
  business_hours_id: string | null
}

export type Availability = 'online' | 'busy' | 'offline'

export const availabilityLabel: Record<Availability, string> = { online: 'Online', busy: 'Bezet', offline: 'Offline' }

export interface Agent {
  id: string
  name: string
  email: string
  role: string
  max_open: number | null
  availability: Availability
  open_count: number
}

export interface Macro {
  id: string
  name: string
  scope: 'personal' | 'global'
  owner: boolean
  actions: RuleAction[]
}

export const rulesQuery = queryOptions({
  queryKey: ['automation', 'rules'],
  queryFn: () => api<{ rules: Rule[] }>('GET', '/rules').then((r) => r.rules),
})

export const ruleRunsQuery = (id: string) =>
  queryOptions({
    queryKey: ['automation', 'rules', id, 'runs'],
    queryFn: () => api<{ runs: RuleRun[] }>('GET', `/rules/${id}/runs`).then((r) => r.runs),
  })

export const businessHoursQuery = queryOptions({
  queryKey: ['automation', 'business-hours'],
  queryFn: () => api<{ business_hours: BusinessHours[] }>('GET', '/business-hours').then((r) => r.business_hours),
})

export const slaPoliciesQuery = queryOptions({
  queryKey: ['automation', 'sla-policies'],
  queryFn: () => api<{ sla_policies: SlaPolicy[] }>('GET', '/sla-policies').then((r) => r.sla_policies),
})

export const assignmentQuery = queryOptions({
  queryKey: ['automation', 'assignment'],
  queryFn: () => api<{ mailboxes: MailboxAutomation[]; agents: Agent[] }>('GET', '/assignment'),
})

export const automationSettingsQuery = queryOptions({
  queryKey: ['automation', 'settings'],
  queryFn: () => api<{ auto_resolve_days: number }>('GET', '/settings/automation'),
})

export const macrosQuery = queryOptions({
  queryKey: ['macros'],
  queryFn: () => api<{ macros: Macro[] }>('GET', '/macros').then((r) => r.macros),
  staleTime: 30_000,
})

// Time zones the schedule editor offers; the API accepts any IANA name.
export const timezones = ['Europe/Amsterdam', 'Europe/Brussels', 'Europe/London', 'Europe/Berlin', 'Europe/Paris', 'Europe/Madrid', 'Europe/Lisbon', 'America/New_York', 'America/Chicago', 'America/Los_Angeles', 'America/Sao_Paulo', 'Asia/Dubai', 'Asia/Kolkata', 'Asia/Singapore', 'Asia/Tokyo', 'Australia/Sydney', 'UTC']

// Turns a number of minutes into the shortest exact phrase: 90 becomes "1 u 30 min".
export function formatMinutes(minutes: number): string {
  const days = Math.floor(minutes / 1440)
  const hours = Math.floor((minutes % 1440) / 60)
  const rest = minutes % 60
  return [days ? `${days} d` : '', hours ? `${hours} u` : '', rest ? `${rest} min` : ''].filter(Boolean).join(' ') || '0 min'
}

// Summarises one action for the run log. The API's detail texts are for developers; the known
// ones are translated and an unknown failure is shown as plain "mislukt".
export function describeRuleAction(a: RuleActionResult): string {
  const name = actionLabel(a.type)
  if (a.result === 'applied') return name
  const detail = a.detail ?? ''
  const reason = a.result === 'skipped' ? (skipReasons[detail] ?? '') : failureReason(detail)
  return reason ? `${name}: ${reason}` : `${name}: ${a.result === 'skipped' ? 'overgeslagen' : 'mislukt'}`
}

const skipReasons: Record<string, string> = {
  'no agent available': 'geen agent beschikbaar',
  'message is auto-submitted': 'automatisch bericht, niet beantwoord',
  'message is bulk or list mail': 'bulkmail, niet beantwoord',
  'sender does not accept replies': 'afzender neemt geen antwoorden aan',
  'sender is one of our mailboxes': 'afzender is een eigen mailbox',
  'conversation is spam': 'gesprek is spam',
  'no inbound message': 'geen bericht van de klant',
  'policy already applied': 'beleid was al toegepast',
  'already replied to this address in the last 24 hours': 'al beantwoord in de afgelopen 24 uur',
}

const failureReasons: [string, string][] = [
  ['unknown label', 'het label bestaat niet meer'],
  ['assignee cannot write to the mailbox', 'deze persoon heeft geen schrijfrechten op de mailbox'],
  ['team has no access to the mailbox', 'dit team heeft geen toegang tot de mailbox'],
  ['sla policy no longer exists', 'het SLA-beleid bestaat niet meer'],
  ['canned response no longer exists', 'het standaardantwoord bestaat niet meer'],
  ['reply is empty', 'het antwoord is leeg'],
  ['cannot be snoozed', 'gesloten gesprekken en spam kun je niet uitstellen'],
]

function failureReason(detail: string): string {
  return failureReasons.find(([needle]) => detail.includes(needle))?.[1] ?? ''
}

// A run's own error text: only the case where the stored rule itself is broken is worth showing.
export function runProblem(run: RuleRun): string {
  return run.error.includes('stored conditions are invalid') || run.error.includes('stored actions are invalid')
    ? 'De regel is ongeldig opgeslagen. Open en bewaar hem opnieuw.'
    : ''
}

export type TimeUnit = 'minutes' | 'hours' | 'days'

export const timeUnitLabel: Record<TimeUnit, string> = { minutes: 'minuten', hours: 'uur', days: 'dagen' }

const unitMinutes: Record<TimeUnit, number> = { minutes: 1, hours: 60, days: 1440 }

export function toMinutes(value: number, unit: TimeUnit): number {
  return Math.round(value * unitMinutes[unit])
}

// The largest unit that divides the duration exactly, so 120 is shown as "2 uur".
export function splitMinutes(minutes: number): { value: number; unit: TimeUnit } {
  if (minutes % 1440 === 0) return { value: minutes / 1440, unit: 'days' }
  if (minutes % 60 === 0) return { value: minutes / 60, unit: 'hours' }
  return { value: minutes, unit: 'minutes' }
}

export const defaultWeek: BusinessHours['weekly'] = Object.fromEntries(
  (['mon', 'tue', 'wed', 'thu', 'fri'] as const).map((d) => [d, [{ start: '09:00', end: '17:00' }]]),
)

// What the toast says after a macro ran; failed is how many conversations were not fully updated.
export function macroToast(name: string, total: number, failed: number): string {
  if (failed === 0) return total === 1 ? `Macro ${name} uitgevoerd` : `Macro ${name} uitgevoerd op ${total} gesprekken`
  if (failed === total) return `Macro ${name} is niet uitgevoerd`
  return `Macro ${name} uitgevoerd, maar ${failed} van ${total} gesprekken zijn niet volledig bijgewerkt`
}
