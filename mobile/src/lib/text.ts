import type { AppNotification, ConversationStatus, ListStatus, Priority, TimelineEvent } from './types'

// Labels and wording as in the web app (web/src/lib/inbox.ts, actions.ts, composer.ts); keep
// them in step so both describe the same thing the same way.

export const statusLabel: Record<ConversationStatus, string> = { open: 'Open', waiting: 'Wachtend', closed: 'Gesloten', spam: 'Spam' }
export const listStatusLabel: Record<ListStatus, string> = { ...statusLabel, snoozed: 'Uitgesteld' }
export const priorityLabel: Record<Priority, string> = { none: 'Geen', low: 'Laag', normal: 'Normaal', high: 'Hoog', urgent: 'Urgent' }

const months = ['jan', 'feb', 'mrt', 'apr', 'mei', 'jun', 'jul', 'aug', 'sep', 'okt', 'nov', 'dec']
const pad = (n: number) => String(n).padStart(2, '0')

// "4 min", "3 u", "gisteren", "12 sep": how long ago, as short as a list row needs.
export function formatRelative(iso: string, now = new Date()): string {
  const t = new Date(iso)
  const mins = Math.floor((now.getTime() - t.getTime()) / 60_000)
  if (mins < 1) return 'nu'
  if (mins < 60) return `${mins} min`
  const hours = Math.floor(mins / 60)
  if (hours < 24) return `${hours} u`
  const yesterday = new Date(now)
  yesterday.setDate(now.getDate() - 1)
  if (t.toDateString() === yesterday.toDateString()) return 'gisteren'
  return t.getFullYear() === now.getFullYear() ? `${t.getDate()} ${months[t.getMonth()]}` : `${t.getDate()} ${months[t.getMonth()]} ${t.getFullYear()}`
}

// "vandaag 14:05", "12 sep, 09:30".
export function formatDateTime(iso: string, now = new Date()): string {
  const t = new Date(iso)
  const time = `${pad(t.getHours())}:${pad(t.getMinutes())}`
  if (t.toDateString() === now.toDateString()) return `vandaag ${time}`
  const day = `${t.getDate()} ${months[t.getMonth()]}${t.getFullYear() === now.getFullYear() ? '' : ` ${t.getFullYear()}`}`
  return `${day}, ${time}`
}

export function initials(name: string): string {
  const parts = name.trim().split(/\s+/).filter(Boolean)
  const first = parts[0]?.[0] ?? '?'
  const last = parts.length > 1 ? (parts[parts.length - 1]?.[0] ?? '') : ''
  return (first + last).toUpperCase()
}

export function notificationText(n: AppNotification): string {
  const who = n.actor?.name ?? 'Iemand'
  switch (n.kind) {
    case 'mention':
      return `${who} heeft je genoemd`
    case 'assigned':
      return `${who} heeft een gesprek aan je toegewezen`
    case 'reply':
      return 'Nieuw antwoord van de klant'
    case 'sla':
      return 'De reactietermijn loopt af'
    case 'csat':
      return 'Klant gaf een lage beoordeling'
  }
}

const str = (v: unknown) => (typeof v === 'string' ? v : '')

export function describeEvent(e: TimelineEvent, now = new Date()): string {
  const d = e.data
  const who = e.actor?.name ?? (d.source === 'rule' ? 'Een regel' : 'Echoo')
  let text: string
  switch (e.type) {
    case 'resolved':
      text = d.source === 'system' ? `${who} sloot het gesprek automatisch` : `${who} sloot het gesprek`
      break
    case 'reopened':
      text = `${who} heropende het gesprek`
      break
    case 'status_changed': {
      const to = str(d.to)
      text = `${who} zette de status op ${(to in statusLabel ? statusLabel[to as ConversationStatus] : to).toLowerCase()}`
      break
    }
    case 'assigned':
      text = e.actor && e.user && e.actor.id === e.user.id ? `${who} nam het gesprek op` : `${who} wees het gesprek toe aan ${e.user?.name ?? 'een gebruiker'}`
      break
    case 'unassigned':
      text = `${who} haalde de toewijzing weg`
      break
    case 'team_assigned':
      text = str(d.team) ? `${who} wees het gesprek toe aan team ${str(d.team)}` : `${who} haalde het team weg`
      break
    case 'priority_changed': {
      const to = str(d.to)
      text = `${who} zette de prioriteit op ${(to in priorityLabel ? priorityLabel[to as Priority] : to).toLowerCase()}`
      break
    }
    case 'labeled':
      text = `${who} voegde label ${str(d.label)} toe`
      break
    case 'unlabeled':
      text = `${who} verwijderde label ${str(d.label)}`
      break
    case 'snoozed':
      text = `${who} stelde het gesprek uit tot ${formatDateTime(str(d.until), now)}`
      break
    case 'woke':
      text = d.manual === true ? `${who} hief het uitstel op` : 'Uitstel verstreken, het gesprek staat weer in de lijst'
      break
    case 'sla_at_risk':
      text = d.target === 'first_response' ? 'Eerste reactie dreigt te laat te komen' : 'De oplostijd dreigt te worden overschreden'
      break
    case 'sla_breached':
      text = d.target === 'first_response' ? 'De eerste reactie is te laat' : 'De oplostijd is overschreden'
      break
    case 'bounced':
      text = str(d.recipient) ? `Bezorging aan ${str(d.recipient)} mislukt` : 'Bezorging mislukt'
      break
    case 'deleted':
      text = `${who} verplaatste het gesprek naar de prullenbak`
      break
    case 'restored':
      text = `${who} zette het gesprek terug uit de prullenbak`
      break
    case 'created':
      text = d.blocked_sender === true ? 'De afzender staat op de blokkeerlijst, dus het gesprek begon als spam' : 'Gesprek gestart'
      break
    default:
      text = `${who} wijzigde het gesprek`
  }
  return d.source === 'macro' ? `${text} via een macro` : text
}

function escapeHtml(s: string): string {
  return s.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;').replace(/"/g, '&quot;')
}

// A reply typed on the phone as the HTML the API wants: paragraphs on blank lines, line
// breaks within them. The server sanitizes it again and adds the signature.
export function textToHtml(text: string): string {
  return text
    .trim()
    .split(/\n{2,}/)
    .map((p) => `<p>${escapeHtml(p).replace(/\n/g, '<br>')}</p>`)
    .join('')
}

const entities: Record<string, string> = { amp: '&', lt: '<', gt: '>', quot: '"', '#39': "'", apos: "'", nbsp: ' ' }

// A canned response (sanitized HTML from the server) as text for the reply field.
export function htmlToText(html: string): string {
  return html
    .replace(/<br\s*\/?>/gi, '\n')
    .replace(/<\/(p|div|li|h[1-6])>/gi, '\n\n')
    .replace(/<li[^>]*>/gi, '• ')
    .replace(/<[^>]+>/g, '')
    .replace(/&(#39|[a-z]+);/gi, (m, name: string) => entities[name.toLowerCase()] ?? m)
    .replace(/\n{3,}/g, '\n\n')
    .trim()
}

// Loopback addresses reach only the device itself (10.0.2.2 is the host as seen from the
// Android emulator), so a development server there may use plain http.
const loopback = ['localhost', '127.0.0.1', '10.0.2.2']

// What someone types as their Echoo address, as the origin the app talks to: "support.example.com"
// becomes "https://support.example.com". Only https is accepted; paths are dropped.
export function normalizeServerUrl(input: string): string | null {
  const raw = input.trim().replace(/\/+$/, '')
  if (!raw) return null
  const withScheme = /^[a-z]+:\/\//i.test(raw) ? raw : `https://${raw}`
  let u: URL
  try {
    u = new URL(withScheme)
  } catch {
    return null
  }
  if (u.username || u.password) return null
  if (u.protocol === 'http:' && loopback.includes(u.hostname)) return u.origin
  if (u.protocol !== 'https:' || !u.hostname.includes('.')) return null
  return u.origin
}

// Search snippets mark matches with ⟦ and ⟧.
export function snippetParts(snippet: string): { text: string; match: boolean }[] {
  const out: { text: string; match: boolean }[] = []
  const re = /⟦([^⟧]*)⟧/g
  let last = 0
  for (let m = re.exec(snippet); m; m = re.exec(snippet)) {
    if (m.index > last) out.push({ text: snippet.slice(last, m.index), match: false })
    out.push({ text: m[1] ?? '', match: true })
    last = m.index + m[0].length
  }
  if (last < snippet.length) out.push({ text: snippet.slice(last), match: false })
  return out
}

function atNine(from: Date, days: number): Date {
  const d = new Date(from)
  d.setDate(d.getDate() + days)
  d.setHours(9, 0, 0, 0)
  return d
}

// The same choices as the web app's snooze menu.
export function snoozePresets(now: Date): { label: string; at: Date }[] {
  // getDay(): 0 is Sunday, 1 is Monday. Today being Monday means next Monday, never today.
  const untilMonday = (8 - now.getDay()) % 7 || 7
  const week = new Date(now)
  week.setDate(week.getDate() + 7)
  return [
    { label: 'Morgen 09:00', at: atNine(now, 1) },
    { label: 'Maandag 09:00', at: atNine(now, untilMonday) },
    { label: 'Over een week', at: week },
  ]
}
