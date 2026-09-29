import { queryOptions } from '@tanstack/react-query'

import { api } from './api'
import type { ConversationListItem, Ref } from './inbox'

export type AttributeEntity = 'contact' | 'organization' | 'conversation'
export type AttributeType = 'text' | 'number' | 'date' | 'boolean' | 'list' | 'link'
export type AttributeValue = string | number | boolean
export type Attributes = Record<string, AttributeValue>

export interface AttributeDef {
  id: string
  entity: AttributeEntity
  key: string
  label: string
  type: AttributeType
  options: string[]
  created_at: string
}

export const entityLabel: Record<AttributeEntity, string> = {
  contact: 'Contacten',
  organization: 'Organisaties',
  conversation: 'Gesprekken',
}

export const typeLabel: Record<AttributeType, string> = {
  text: 'Tekst',
  number: 'Getal',
  date: 'Datum',
  boolean: 'Ja of nee',
  list: 'Keuzelijst',
  link: 'Link',
}

export const attributeDefsQuery = (entity?: AttributeEntity) =>
  queryOptions({
    queryKey: ['custom-attributes', entity ?? 'all'],
    queryFn: async () =>
      (await api<{ attributes: AttributeDef[] }>('GET', `/custom-attributes${entity ? `?entity=${entity}` : ''}`)).attributes,
    staleTime: 60_000,
  })

export interface ContactItem {
  id: string
  name: string
  phone: string
  email: string
  organization: Ref | null
  custom_attributes: Attributes
  conversation_count: number
  created_at: string
  last_activity_at: string
}

export interface ContactAddress {
  email: string
  primary: boolean
  // Set after a permanent delivery failure.
  bounced_at?: string
}

export interface ContactFull {
  id: string
  name: string
  phone: string
  emails: ContactAddress[]
  organization: Ref | null
  custom_attributes: Attributes
  conversation_count: number
  created_at: string
  updated_at: string
  last_activity_at: string
  // Set when the contact opted out of campaigns.
  unsubscribed_at: string | null
}

export interface Note {
  id: string
  body: string
  author: Ref | null
  created_at: string
  updated_at: string
  can_edit: boolean
}

export interface ConversationPageResult {
  conversations: ConversationListItem[]
  next_cursor: string | null
}

export interface OrganizationItem {
  id: string
  name: string
  domains: string[]
  contact_count: number
  created_at: string
}

export interface OrganizationFull {
  id: string
  name: string
  domains: string[]
  custom_attributes: Attributes
  contact_count: number
  created_at: string
  updated_at: string
  contacts: { id: string; name: string; email: string; last_activity_at: string }[]
}

export const contactDisplayName = (c: { name: string; email?: string; emails?: ContactAddress[] }) =>
  c.name || c.email || c.emails?.[0]?.email || 'Onbekend contact'

export function formatAttribute(def: AttributeDef, value: AttributeValue | undefined): string {
  if (value === undefined) return '—'
  if (def.type === 'boolean') return value === true ? 'Ja' : 'Nee'
  if (def.type === 'date') {
    const day = new Date(`${String(value)}T00:00:00Z`)
    if (!Number.isNaN(day.getTime())) return attributeDay.format(day)
  }
  return String(value)
}

// A date attribute is a calendar day without a zone, so it is formatted in UTC to keep the day.
const attributeDay = new Intl.DateTimeFormat('nl-NL', { day: 'numeric', month: 'long', year: 'numeric', timeZone: 'UTC' })

// Values as typed in a form: numbers and booleans stay strings until submitted.
export function attributesToPatch(defs: AttributeDef[], drafts: Record<string, string>, current: Attributes): Record<string, AttributeValue | null> {
  const patch: Record<string, AttributeValue | null> = {}
  for (const def of defs) {
    const draft = (drafts[def.key] ?? '').trim()
    const before = current[def.key]
    if (draft === '') {
      if (before !== undefined) patch[def.key] = null
      continue
    }
    const next: AttributeValue = def.type === 'number' ? Number(draft.replace(',', '.')) : def.type === 'boolean' ? draft === 'true' : draft
    if (next !== before) patch[def.key] = next
  }
  return patch
}

export function attributeDrafts(defs: AttributeDef[], current: Attributes): Record<string, string> {
  const out: Record<string, string> = {}
  for (const def of defs) {
    const v = current[def.key]
    if (v !== undefined) out[def.key] = String(v)
  }
  return out
}

// ---- filters (mirror internal/contacts/filter.go) ----

export type FilterField =
  | 'name'
  | 'email'
  | 'domain'
  | 'organization'
  | 'has_open_conversation'
  | 'last_activity'
  | 'attribute'
  | 'conversation_attribute'

export interface FilterCondition {
  field: FilterField
  op: string
  key?: string
  value?: string
}

export interface ContactFilter {
  match: 'all' | 'any'
  conditions: FilterCondition[]
}

export const emptyFilter: ContactFilter = { match: 'all', conditions: [] }

export const fieldLabel: Record<FilterField, string> = {
  name: 'Naam',
  email: 'E-mailadres',
  domain: 'Domein',
  organization: 'Organisatie',
  has_open_conversation: 'Open gesprek',
  last_activity: 'Laatste activiteit',
  attribute: 'Aangepast veld',
  conversation_attribute: 'Gespreksveld',
}

export const opLabel: Record<string, string> = {
  contains: 'bevat',
  not_contains: 'bevat niet',
  equals: 'is',
  not_equals: 'is niet',
  starts_with: 'begint met',
  is_set: 'is ingevuld',
  is_not_set: 'is leeg',
  is_true: 'ja',
  is_false: 'nee',
  before: 'voor',
  on_or_after: 'vanaf',
  within_days: 'binnen dagen',
  older_than_days: 'langer dan dagen geleden',
  gt: 'groter dan',
  lt: 'kleiner dan',
}

const textOps = ['contains', 'not_contains', 'equals', 'not_equals', 'starts_with', 'is_set', 'is_not_set']
const attributeOps: Record<AttributeType, string[]> = {
  text: textOps,
  link: textOps,
  number: ['equals', 'not_equals', 'gt', 'lt', 'is_set', 'is_not_set'],
  date: ['equals', 'before', 'on_or_after', 'is_set', 'is_not_set'],
  boolean: ['is_true', 'is_false', 'is_set', 'is_not_set'],
  list: ['equals', 'not_equals', 'is_set', 'is_not_set'],
}

export function opsFor(field: FilterField, attributeType?: AttributeType): string[] {
  switch (field) {
    case 'name':
    case 'organization':
      return textOps
    case 'email':
      return ['contains', 'not_contains', 'equals', 'not_equals', 'starts_with']
    case 'domain':
      return ['contains', 'equals', 'not_equals']
    case 'has_open_conversation':
      return ['is_true', 'is_false']
    case 'last_activity':
      return ['before', 'on_or_after', 'within_days', 'older_than_days']
    default:
      return attributeOps[attributeType ?? 'text']
  }
}

export const opNeedsValue = (op: string) => !['is_set', 'is_not_set', 'is_true', 'is_false'].includes(op)

export const isAttributeField = (f: FilterField) => f === 'attribute' || f === 'conversation_attribute'

export type ValueKind = 'text' | 'number' | 'date' | 'days' | 'list' | 'none'

export function valueKind(c: FilterCondition, def?: AttributeDef): ValueKind {
  if (!opNeedsValue(c.op)) return 'none'
  if (c.field === 'last_activity') return c.op === 'before' || c.op === 'on_or_after' ? 'date' : 'days'
  if (def?.type === 'number') return 'number'
  if (def?.type === 'date') return 'date'
  if (def?.type === 'list') return 'list'
  return 'text'
}

export function conditionComplete(c: FilterCondition): boolean {
  if (isAttributeField(c.field) && !c.key) return false
  return !opNeedsValue(c.op) || (c.value ?? '').trim() !== ''
}

// Only complete conditions are sent, so half-built rows never reach the server.
export function cleanFilter(f: ContactFilter): ContactFilter {
  return { match: f.match, conditions: f.conditions.filter(conditionComplete) }
}

export const filterIsActive = (f: ContactFilter) => cleanFilter(f).conditions.length > 0

export function defaultCondition(field: FilterField, defs: AttributeDef[]): FilterCondition {
  if (isAttributeField(field)) {
    const def = defs.find((d) => d.entity === (field === 'attribute' ? 'contact' : 'conversation'))
    return { field, key: def?.key ?? '', op: opsFor(field, def?.type)[0] ?? 'contains', value: '' }
  }
  return { field, op: opsFor(field)[0] ?? 'contains', value: '' }
}

export interface ContactQuery {
  q: string
  sort: 'name' | 'last_activity' | 'created'
  dir: 'asc' | 'desc'
  segmentId: string
  filter: ContactFilter
  organizationId?: string
}

export function contactQueryString(p: ContactQuery, extra: Record<string, string> = {}): string {
  const s = new URLSearchParams()
  if (p.q.trim()) s.set('q', p.q.trim())
  s.set('sort', p.sort)
  s.set('dir', p.dir)
  if (p.organizationId) s.set('organization_id', p.organizationId)
  if (p.segmentId) s.set('segment_id', p.segmentId)
  else if (filterIsActive(p.filter)) s.set('filter', JSON.stringify(cleanFilter(p.filter)))
  for (const [k, v] of Object.entries(extra)) s.set(k, v)
  return s.toString()
}

export interface Segment {
  id: string
  name: string
  shared: boolean
  owner: Ref
  filter: ContactFilter
  can_edit: boolean
  created_at: string
  updated_at: string
}

export const segmentsQuery = queryOptions({
  queryKey: ['contact-segments'],
  queryFn: async () => (await api<{ segments: Segment[] }>('GET', '/contact-segments')).segments,
})

// ---- CSV import preview (the server parses again and is authoritative) ----

export const MAX_IMPORT_ROWS = 50_000
export const MAX_IMPORT_BYTES = 20 * 1024 * 1024
export const PREVIEW_ROWS = 5

export function detectDelimiter(text: string): ';' | ',' {
  let semi = 0
  let comma = 0
  let quoted = false
  for (const ch of text) {
    if (ch === '"') quoted = !quoted
    else if (ch === '\n' && !quoted) break
    else if (!quoted && ch === ';') semi++
    else if (!quoted && ch === ',') comma++
  }
  return semi > comma ? ';' : ','
}

export interface CsvPreview {
  delimiter: ';' | ','
  header: string[]
  preview: string[][]
  rowCount: number
}

export type CsvProblem = 'empty' | 'too_many_rows' | 'too_large' | 'unterminated'

export class CsvError extends Error {
  constructor(readonly problem: CsvProblem) {
    super(problem)
  }
}

export const csvProblemText: Record<CsvProblem, string> = {
  empty: 'Het bestand heeft geen kopregel met minstens één rij eronder.',
  too_many_rows: 'Het bestand heeft meer dan 50.000 rijen. Splits het in meerdere bestanden.',
  too_large: 'Het bestand is groter dan 20 MB. Splits het in meerdere bestanden.',
  unterminated: 'Het bestand bevat een aanhalingsteken dat niet wordt gesloten.',
}

// Reads all records: quoted fields may hold delimiters, doubled quotes and line breaks.
export function parseCsv(text: string, delimiter?: ';' | ','): { delimiter: ';' | ','; records: string[][] } {
  const body = text.startsWith('﻿') ? text.slice(1) : text
  const d = delimiter ?? detectDelimiter(body)
  const records: string[][] = []
  let row: string[] = []
  let field = ''
  let quoted = false
  const endField = () => {
    row.push(field)
    field = ''
  }
  const endRow = () => {
    endField()
    if (row.some((c) => c.trim() !== '')) records.push(row)
    row = []
  }
  for (let i = 0; i < body.length; i++) {
    const ch = body.charAt(i)
    if (quoted) {
      if (ch === '"') {
        if (body.charAt(i + 1) === '"') {
          field += '"'
          i++
        } else quoted = false
      } else field += ch
    } else if (ch === '"' && field === '') quoted = true
    else if (ch === d) endField()
    else if (ch === '\n' || ch === '\r') {
      if (ch === '\r' && body.charAt(i + 1) === '\n') i++
      endRow()
    } else field += ch
  }
  if (quoted) throw new CsvError('unterminated')
  if (field !== '' || row.length > 0) endRow()
  return { delimiter: d, records }
}

export function previewCsv(text: string, delimiter?: ';' | ','): CsvPreview {
  const { delimiter: d, records } = parseCsv(text, delimiter)
  const [header, ...rows] = records
  if (!header || rows.length === 0) throw new CsvError('empty')
  if (rows.length > MAX_IMPORT_ROWS) throw new CsvError('too_many_rows')
  return { delimiter: d, header: header.map((h) => h.trim()), preview: rows.slice(0, PREVIEW_ROWS), rowCount: rows.length }
}

export type ImportTarget = '' | 'email' | 'name' | 'phone' | 'organization' | `attribute:${string}`

const headerAliases: Record<Exclude<ImportTarget, '' | `attribute:${string}`>, string[]> = {
  email: ['email', 'emailadres', 'emailaddress', 'mail', 'emailadresprimair'],
  name: ['naam', 'name', 'volledigenaam', 'fullname', 'contactpersoon', 'contact'],
  phone: ['telefoon', 'telefoonnummer', 'phone', 'phonenumber', 'tel', 'mobiel', 'mobile'],
  organization: ['organisatie', 'organization', 'organisation', 'bedrijf', 'bedrijfsnaam', 'company'],
}

const normalizeHeader = (h: string) => h.toLowerCase().replace(/[^a-z0-9]/g, '')

// Suggests a target per column; every target is used at most once.
export function suggestMapping(header: string[], contactAttributes: AttributeDef[]): ImportTarget[] {
  const used = new Set<string>()
  return header.map((h) => {
    const n = normalizeHeader(h)
    if (n === '') return ''
    let target: ImportTarget = ''
    for (const [t, aliases] of Object.entries(headerAliases)) {
      if (aliases.includes(n)) target = t as ImportTarget
    }
    if (target === '') {
      const def = contactAttributes.find((a) => normalizeHeader(a.key) === n || normalizeHeader(a.label) === n)
      if (def) target = `attribute:${def.key}`
    }
    if (target === '' || used.has(target)) return ''
    used.add(target)
    return target
  })
}

export type DedupeMode = 'update' | 'skip'

export interface ImportOptions {
  delimiter: ';' | ','
  dedupe: DedupeMode
  columns: { index: number; target: string }[]
}

export function buildImportOptions(delimiter: ';' | ',', dedupe: DedupeMode, targets: ImportTarget[]): ImportOptions {
  const columns: { index: number; target: string }[] = []
  targets.forEach((target, index) => {
    if (target !== '') columns.push({ index, target })
  })
  return { delimiter, dedupe, columns }
}

// A Dutch message when the mapping cannot be imported, otherwise null.
export function mappingProblem(targets: ImportTarget[]): string | null {
  const seen = new Set<string>()
  for (const t of targets) {
    if (t === '') continue
    if (seen.has(t)) return 'Elk veld kan maar aan één kolom gekoppeld worden.'
    seen.add(t)
  }
  return seen.has('email') ? null : 'Koppel een kolom aan E-mail. Zonder e-mailadres kunnen we contacten niet herkennen.'
}

export interface ContactImport {
  id: string
  status: 'queued' | 'running' | 'done' | 'failed'
  filename: string
  total_rows: number
  processed_rows: number
  created_count: number
  updated_count: number
  skipped_count: number
  failed_count: number
  has_errors: boolean
  error: string
  created_at: string
  finished_at: string | null
}

export function slugFromLabel(label: string): string {
  const s = label
    .normalize('NFD')
    .replace(/[̀-ͯ]/g, '')
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, '_')
    .replace(/^_+|_+$/g, '')
    .slice(0, 40)
  return /^[a-z]/.test(s) ? s : s ? `veld_${s}`.slice(0, 40) : ''
}
