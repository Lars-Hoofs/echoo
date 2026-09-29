import type { InfiniteData } from '@tanstack/react-query'
import { describe, expect, it } from 'vitest'

import type { ConversationListItem, ConversationPage } from './inbox'
import { withUnread } from './unread'

function item(id: string, unread: boolean): ConversationListItem {
  return {
    id,
    number: 1,
    subject: 's',
    status: 'open',
    priority: 'none',
    version: 1,
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
    unread,
  }
}

const data: InfiniteData<ConversationPage, string> = {
  pageParams: [''],
  pages: [{ conversations: [item('a', true), item('b', true)], next_cursor: 'x' }, { conversations: [item('c', false)], next_cursor: null }],
}

describe('withUnread', () => {
  it('changes only the given conversation, on any page', () => {
    const read = withUnread(data, 'b', false)
    expect(read.pages[0]?.conversations.map((c) => c.unread)).toEqual([true, false])
    expect(withUnread(read, 'c', true).pages[1]?.conversations[0]?.unread).toBe(true)
  })

  it('keeps unchanged rows identical and leaves the input alone', () => {
    const same = withUnread(data, 'a', true)
    expect(same.pages[0]?.conversations[0]).toBe(data.pages[0]?.conversations[0])
    withUnread(data, 'a', false)
    expect(data.pages[0]?.conversations[0]?.unread).toBe(true)
  })
})
