import { describe, expect, it } from 'vitest'

import { describeEvent, formatDateTime, formatRelative, htmlToText, initials, normalizeServerUrl, snippetParts, snoozePresets, textToHtml } from './text'
import type { TimelineEvent } from './types'

const now = new Date(2026, 8, 30, 14, 0)

describe('formatRelative', () => {
  it('shortens to what a list row needs', () => {
    expect(formatRelative(new Date(2026, 8, 30, 13, 59, 40).toISOString(), now)).toBe('nu')
    expect(formatRelative(new Date(2026, 8, 30, 13, 56).toISOString(), now)).toBe('4 min')
    expect(formatRelative(new Date(2026, 8, 30, 11, 0).toISOString(), now)).toBe('3 u')
    expect(formatRelative(new Date(2026, 8, 29, 9, 0).toISOString(), now)).toBe('gisteren')
    expect(formatRelative(new Date(2026, 8, 12, 9, 0).toISOString(), now)).toBe('12 sep')
    expect(formatRelative(new Date(2025, 11, 1, 9, 0).toISOString(), now)).toBe('1 dec 2025')
  })
})

describe('formatDateTime', () => {
  it('says today and leaves out the current year', () => {
    expect(formatDateTime(new Date(2026, 8, 30, 9, 5).toISOString(), now)).toBe('vandaag 09:05')
    expect(formatDateTime(new Date(2026, 8, 12, 9, 30).toISOString(), now)).toBe('12 sep, 09:30')
  })
})

describe('initials', () => {
  it('takes the first and last name', () => {
    expect(initials('Sanne de Vries')).toBe('SV')
    expect(initials('lars')).toBe('L')
    expect(initials('  ')).toBe('?')
  })
})

describe('describeEvent', () => {
  const ev = (type: string, data: Record<string, unknown> = {}, actor = { id: 'u1', name: 'Lars' }): TimelineEvent => ({
    id: 'e',
    type,
    created_at: now.toISOString(),
    actor,
    user: null,
    data,
  })
  it('says who did what, in the words of the web app', () => {
    expect(describeEvent(ev('resolved'))).toBe('Lars sloot het gesprek')
    expect(describeEvent(ev('status_changed', { to: 'waiting' }))).toBe('Lars zette de status op wachtend')
    expect(describeEvent(ev('priority_changed', { to: 'urgent', source: 'macro' }))).toBe('Lars zette de prioriteit op urgent via een macro')
    expect(describeEvent({ ...ev('assigned'), user: { id: 'u1', name: 'Lars' } })).toBe('Lars nam het gesprek op')
    expect(describeEvent(ev('created', { blocked_sender: true }))).toContain('blokkeerlijst')
  })
})

describe('textToHtml and htmlToText', () => {
  it('turns typed text into safe paragraphs', () => {
    expect(textToHtml('Dag Sanne,\n\nWe komen <vandaag>.\nGroet')).toBe('<p>Dag Sanne,</p><p>We komen &lt;vandaag&gt;.<br>Groet</p>')
  })
  it('reads a canned response as text', () => {
    expect(htmlToText('<p>Dag {{contact.first_name}},</p><p>Bedankt &amp; tot <b>snel</b>!<br>Team</p><ul><li>Een</li></ul>')).toBe(
      'Dag {{contact.first_name}},\n\nBedankt & tot snel!\nTeam\n\n• Een',
    )
  })
})

describe('normalizeServerUrl', () => {
  it('accepts what people type and keeps only the https origin', () => {
    expect(normalizeServerUrl('support.example.com')).toBe('https://support.example.com')
    expect(normalizeServerUrl(' https://support.example.com/inbox/alle/ ')).toBe('https://support.example.com')
    expect(normalizeServerUrl('https://support.example.com:8443')).toBe('https://support.example.com:8443')
  })
  it('allows plain http only for a development server on the device itself', () => {
    expect(normalizeServerUrl('http://localhost:18200/')).toBe('http://localhost:18200')
    expect(normalizeServerUrl('http://10.0.2.2:8080')).toBe('http://10.0.2.2:8080')
  })
  it('refuses plain http, credentials and single labels', () => {
    expect(normalizeServerUrl('http://support.example.com')).toBeNull()
    expect(normalizeServerUrl('https://user:pw@support.example.com')).toBeNull()
    expect(normalizeServerUrl('http://support.localhost.example.com')).toBeNull()
    expect(normalizeServerUrl('localhost')).toBeNull()
    expect(normalizeServerUrl('')).toBeNull()
  })
})

describe('snippetParts', () => {
  it('splits the search markers', () => {
    expect(snippetParts('de ⟦warmtepomp⟧ valt uit')).toEqual([
      { text: 'de ', match: false },
      { text: 'warmtepomp', match: true },
      { text: ' valt uit', match: false },
    ])
  })
})

describe('snoozePresets', () => {
  it('offers tomorrow, next Monday and a week, never today', () => {
    const monday = new Date(2026, 8, 28, 16, 0)
    const [tomorrow, nextMonday, week] = snoozePresets(monday)
    expect(tomorrow?.at).toEqual(new Date(2026, 8, 29, 9, 0))
    expect(nextMonday?.at).toEqual(new Date(2026, 9, 5, 9, 0))
    expect(week?.at).toEqual(new Date(2026, 9, 5, 16, 0))
  })
})
