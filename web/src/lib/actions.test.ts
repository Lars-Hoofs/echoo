import type { InfiniteData } from '@tanstack/react-query'
import { describe, expect, it } from 'vitest'

import {
  applyChange,
  bulkAction,
  type ChangeSet,
  describeChange,
  describeEvent,
  inverseChange,
  listMatchFromKey,
  matchesList,
  mergeTimeline,
  patchBody,
  patchPages,
  snoozePresets,
  splitBulk,
  type TimelineEvent,
} from './actions'
import type { ConversationListItem, ConversationPage, LabelRef } from './inbox'

const now = new Date(2026, 8, 28, 14, 30) // Monday 28 September 2026, local time
const future = new Date(2026, 8, 30, 9).toISOString()
const past = new Date(2026, 8, 1, 9).toISOString()

function item(over: Partial<ConversationListItem> = {}): ConversationListItem {
  return {
    id: 'c1',
    number: 1,
    subject: 's',
    status: 'open',
    priority: 'none',
    version: 3,
    snoozed_until: null,
    labels: [],
    preview: '',
    last_message_at: '2026-09-28T10:00:00Z',
    message_count: 1,
    has_attachments: false,
    last_direction: 'in',
    mailbox: { id: 'm1', name: 'Support' },
    contact: null,
    assignee: null,
    team: null,
    sla: null,
    unread: false,
    ...over,
  }
}

const billing: LabelRef = { id: 'l1', name: 'Factuur', color_token: 'blue' }
const urgent: LabelRef = { id: 'l2', name: 'Dringend', color_token: 'red' }

describe('applyChange', () => {
  it('sets fields, bumps the version and leaves the rest alone', () => {
    const next = applyChange(item(), { status: 'waiting', priority: 'high', assignee: { id: 'u1', name: 'Anna' } })
    expect(next).toMatchObject({ status: 'waiting', priority: 'high', assignee: { id: 'u1', name: 'Anna' }, version: 4, team: null })
  })

  it('clears with null', () => {
    const next = applyChange(item({ assignee: { id: 'u1', name: 'Anna' }, snoozed_until: future }), { assignee: null, snoozedUntil: null })
    expect(next.assignee).toBeNull()
    expect(next.snoozed_until).toBeNull()
  })

  it('ends a snooze when the conversation is closed or marked as spam', () => {
    expect(applyChange(item({ snoozed_until: future }), { status: 'closed' }).snoozed_until).toBeNull()
    expect(applyChange(item({ snoozed_until: future }), { status: 'spam' }).snoozed_until).toBeNull()
    expect(applyChange(item({ snoozed_until: future }), { status: 'waiting' }).snoozed_until).toBe(future)
  })

  it('adds and removes labels without duplicates, sorted by name', () => {
    const start = item({ labels: [urgent] })
    expect(applyChange(start, { addLabels: [billing, urgent] }).labels).toEqual([urgent, billing])
    expect(applyChange(start, { removeLabelIds: ['l2'] }).labels).toEqual([])
  })
})

describe('matchesList', () => {
  const open = { view: 'all', status: 'open' } as const

  it('keeps a conversation only in the lists it still belongs to', () => {
    expect(matchesList(item(), open, 'me', now)).toBe(true)
    expect(matchesList(item({ status: 'closed' }), open, 'me', now)).toBe(false)
    expect(matchesList(item({ status: 'closed' }), { view: 'all', status: 'closed' }, 'me', now)).toBe(true)
  })

  it('applies the view, mailbox, team and label filters', () => {
    const mine = item({ assignee: { id: 'me', name: 'Ik' } })
    expect(matchesList(mine, { view: 'mine', status: 'open' }, 'me', now)).toBe(true)
    expect(matchesList(mine, { view: 'mine', status: 'open' }, 'other', now)).toBe(false)
    expect(matchesList(mine, { view: 'unassigned', status: 'open' }, 'me', now)).toBe(false)
    expect(matchesList(item(), { ...open, mailbox: 'm2' }, 'me', now)).toBe(false)
    expect(matchesList(item(), { ...open, team: 't1' }, 'me', now)).toBe(false)
    expect(matchesList(item({ labels: [billing] }), { ...open, label: 'l1' }, 'me', now)).toBe(true)
    expect(matchesList(item(), { ...open, label: 'l1' }, 'me', now)).toBe(false)
  })

  it('treats comma separated ids as any-of', () => {
    const inM1 = item({ mailbox: { id: 'm1', name: 'A' } })
    expect(matchesList(inM1, { ...open, mailbox: 'm2,m1' }, 'me', now)).toBe(true)
    expect(matchesList(inM1, { ...open, mailbox: 'm2,m3' }, 'me', now)).toBe(false)
    expect(matchesList(item({ team: { id: 't2', name: 'T' } }), { ...open, team: 't1,t2' }, 'me', now)).toBe(true)
    expect(matchesList(item({ labels: [billing] }), { ...open, label: 'l9,l1' }, 'me', now)).toBe(true)
  })

  it('moves a snoozed conversation from the open list to the snoozed list', () => {
    const snoozed = item({ snoozed_until: future })
    expect(matchesList(snoozed, open, 'me', now)).toBe(false)
    expect(matchesList(snoozed, { view: 'all', status: 'snoozed' }, 'me', now)).toBe(true)
    expect(matchesList(item({ snoozed_until: past }), open, 'me', now)).toBe(true)
    expect(matchesList(item({ snoozed_until: past }), { view: 'all', status: 'snoozed' }, 'me', now)).toBe(false)
  })
})

describe('listMatchFromKey', () => {
  it('reads the filter back from the query key and ignores empty parts', () => {
    expect(listMatchFromKey(['inbox', 'conversations', 'mine', 'closed', '', 't1', ''])).toEqual({
      view: 'mine',
      status: 'closed',
      mailbox: undefined,
      team: 't1',
      label: undefined,
    })
    expect(listMatchFromKey(['inbox', 'conversations'])).toBeNull()
  })
})

describe('patchPages', () => {
  const data: InfiniteData<ConversationPage, string> = {
    pageParams: [''],
    pages: [{ conversations: [item({ id: 'a' }), item({ id: 'b' }), item({ id: 'c' })], next_cursor: null }],
  }

  it('updates the targeted conversations and drops those that leave the list', () => {
    const keepOpen = (i: ConversationListItem) => i.status === 'open'
    const out = patchPages(data, new Set(['a', 'b']), { status: 'closed' }, keepOpen)
    expect(out.pages[0]?.conversations.map((c) => c.id)).toEqual(['c'])
    const kept = patchPages(data, new Set(['b']), { priority: 'high' }, keepOpen)
    expect(kept.pages[0]?.conversations.map((c) => [c.id, c.priority])).toEqual([
      ['a', 'none'],
      ['b', 'high'],
      ['c', 'none'],
    ])
  })

  it('does not mutate the cached data it started from', () => {
    patchPages(data, new Set(['a']), { status: 'closed' }, () => false)
    expect(data.pages[0]?.conversations).toHaveLength(3)
  })
})

describe('request bodies', () => {
  it('builds the PATCH body from the change, with null for clearing', () => {
    expect(patchBody({ status: 'closed', assignee: null, team: { id: 't1', name: 'Support' }, snoozedUntil: future })).toEqual({
      status: 'closed',
      assignee_user_id: null,
      assignee_team_id: 't1',
      snoozed_until: future,
    })
    expect(patchBody({})).toEqual({})
  })

  it('names the snooze field for bulk and lists label ids', () => {
    expect(bulkAction({ snoozedUntil: future, addLabels: [billing], removeLabelIds: ['l2'], priority: 'low' })).toEqual({
      priority: 'low',
      snooze_until: future,
      add_label_ids: ['l1'],
      remove_label_ids: ['l2'],
    })
    expect(bulkAction({ status: 'open' })).toEqual({ status: 'open' })
  })

  it('splits bulk results', () => {
    const { ok, failed } = splitBulk([
      { id: 'a', ok: true },
      { id: 'b', ok: false, code: 'not_found' },
    ])
    expect(ok).toEqual(['a'])
    expect(failed).toEqual([{ id: 'b', ok: false, code: 'not_found' }])
  })
})

describe('inverseChange', () => {
  it('restores what the conversation had before', () => {
    const before = item({ status: 'waiting', assignee: { id: 'u1', name: 'Anna' }, labels: [urgent], snoozed_until: future })
    const change: ChangeSet = { status: 'closed', assignee: null, addLabels: [billing, urgent], removeLabelIds: ['l2'], snoozedUntil: null }
    expect(inverseChange(before, change)).toEqual({
      status: 'waiting',
      assignee: { id: 'u1', name: 'Anna' },
      snoozedUntil: future,
      removeLabelIds: ['l1'],
      addLabels: [urgent],
    })
  })

  it('only mentions the fields that were changed', () => {
    expect(inverseChange(item({ priority: 'low' }), { priority: 'high' })).toEqual({ priority: 'low' })
  })
})

describe('snoozePresets', () => {
  it('offers tomorrow, next Monday and a week from now', () => {
    const [tomorrow, monday, week] = snoozePresets(now)
    expect(tomorrow?.at).toEqual(new Date(2026, 8, 29, 9))
    expect(monday?.at).toEqual(new Date(2026, 9, 5, 9)) // today is a Monday: the next one
    expect(week?.at).toEqual(new Date(2026, 9, 5, 14, 30))
  })

  it('finds the coming Monday from other weekdays', () => {
    const friday = new Date(2026, 9, 2, 16)
    expect(snoozePresets(friday)[1]?.at).toEqual(new Date(2026, 9, 5, 9))
    const sunday = new Date(2026, 9, 4, 16)
    expect(snoozePresets(sunday)[1]?.at).toEqual(new Date(2026, 9, 5, 9))
  })
})

describe('describeChange', () => {
  it('speaks of one or several conversations', () => {
    expect(describeChange({ status: 'closed' }, 1)).toBe('Gesprek gesloten')
    expect(describeChange({ status: 'closed' }, 3)).toBe('3 gesprekken gesloten')
    expect(describeChange({ assignee: { id: 'u1', name: 'Anna' } }, 1)).toBe('Gesprek toegewezen aan Anna')
    expect(describeChange({ assignee: null }, 2)).toBe('Toewijzing verwijderd van 2 gesprekken')
    expect(describeChange({ status: 'open' }, 1)).toBe('Gesprek heropend')
  })
})

describe('describeEvent', () => {
  const ev = (over: Partial<TimelineEvent>): TimelineEvent => ({
    id: 'e',
    type: 'resolved',
    created_at: '2026-09-28T10:00:00Z',
    actor: { id: 'u1', name: 'Lars' },
    user: null,
    data: {},
    ...over,
  })

  it.each([
    [ev({ type: 'resolved' }), 'Lars sloot het gesprek'],
    [ev({ type: 'reopened', data: { to: 'open' } }), 'Lars heropende het gesprek'],
    [ev({ type: 'campaign', data: { name: 'Herfstactie', campaign_id: 'c1' } }), 'Verstuurd in campagne Herfstactie'],
    [ev({ type: 'status_changed', data: { from: 'open', to: 'waiting' } }), 'Lars zette de status op wachtend'],
    [ev({ type: 'assigned', user: { id: 'u2', name: 'Anna' } }), 'Lars wees het gesprek toe aan Anna'],
    [ev({ type: 'assigned', user: { id: 'u1', name: 'Lars' } }), 'Lars nam het gesprek op'],
    [ev({ type: 'unassigned' }), 'Lars haalde de toewijzing weg'],
    [ev({ type: 'team_assigned', data: { team: 'Support' } }), 'Lars wees het gesprek toe aan team Support'],
    [ev({ type: 'team_assigned', data: { team: '' } }), 'Lars haalde het team weg'],
    [ev({ type: 'priority_changed', data: { to: 'urgent' } }), 'Lars zette de prioriteit op urgent'],
    [ev({ type: 'labeled', data: { label: 'Factuur' } }), 'Lars voegde label Factuur toe'],
    [ev({ type: 'unlabeled', data: { label: 'Factuur' } }), 'Lars verwijderde label Factuur'],
    [ev({ type: 'bounced', actor: null, data: { recipient: 'a@b.nl' } }), 'Bezorging aan a@b.nl mislukt'],
    [ev({ type: 'woke', actor: null, data: {} }), 'Uitstel verstreken, het gesprek staat weer in de lijst'],
    [ev({ type: 'woke', data: { manual: true } }), 'Lars hief het uitstel op'],
    [ev({ type: 'resolved', actor: null }), 'Echoo sloot het gesprek'],
    [ev({ type: 'deleted' }), 'Lars verplaatste het gesprek naar de prullenbak'],
    [ev({ type: 'restored' }), 'Lars zette het gesprek terug uit de prullenbak'],
    [ev({ type: 'created', actor: null, data: { reason: 'new_conversation', blocked_sender: true } }), 'De afzender staat op de blokkeerlijst, dus het gesprek begon als spam'],
  ])('describes %#', (event, text) => {
    expect(describeEvent(event, now)).toBe(text)
  })
})

describe('mergeTimeline', () => {
  const message = (id: string, at: string) => ({ id, received_at: at, sent_at: null })
  const event = (id: string, at: string): TimelineEvent => ({ id, type: 'resolved', created_at: at, actor: null, user: null, data: {} })

  it('interleaves events and messages by time, messages first on a tie', () => {
    const merged = mergeTimeline(
      [message('m1', '2026-09-28T10:00:00Z'), message('m2', '2026-09-28T12:00:00Z')],
      [event('e2', '2026-09-28T12:00:00Z'), event('e1', '2026-09-28T11:00:00Z')],
    )
    expect(merged.map((e) => (e.kind === 'message' ? e.message.id : e.event.id))).toEqual(['m1', 'e1', 'm2', 'e2'])
  })

  it('leaves out the created event', () => {
    const created: TimelineEvent = { ...event('e0', '2026-09-28T09:00:00Z'), type: 'created' }
    expect(mergeTimeline([message('m1', '2026-09-28T10:00:00Z')], [created]).map((e) => e.kind)).toEqual(['message'])
  })

  it('keeps the created event of a conversation from a blocked sender', () => {
    const created: TimelineEvent = { ...event('e0', '2026-09-28T10:00:01Z'), type: 'created', data: { blocked_sender: true } }
    expect(mergeTimeline([message('m1', '2026-09-28T10:00:00Z')], [created]).map((e) => e.kind)).toEqual(['message', 'event'])
  })
})
