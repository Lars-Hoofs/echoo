import type { InfiniteData } from '@tanstack/react-query'
import { describe, expect, it } from 'vitest'

import type { ConversationListItem, ConversationPage } from './inbox'
import { describeTrashAction, withoutConversations } from './trash'

describe('describeTrashAction', () => {
  it('names one or several conversations', () => {
    expect(describeTrashAction('trash', 1, 1)).toBe('Gesprek naar de prullenbak verplaatst')
    expect(describeTrashAction('restore', 3, 3)).toBe('3 gesprekken teruggezet')
    expect(describeTrashAction('purge', 2, 2)).toBe('2 gesprekken definitief verwijderd')
  })

  it('says how many of them worked when some failed', () => {
    expect(describeTrashAction('trash', 2, 5)).toBe('2 van 5 gesprekken naar de prullenbak verplaatst.')
    expect(describeTrashAction('purge', 0, 1)).toBe('0 van 1 gesprekken definitief verwijderd.')
  })
})

describe('withoutConversations', () => {
  const item = (id: string) => ({ id }) as ConversationListItem
  const data: InfiniteData<ConversationPage, string> = {
    pages: [
      { conversations: [item('a'), item('b')], next_cursor: 'x' },
      { conversations: [item('c')], next_cursor: null },
    ],
    pageParams: ['', 'x'],
  }

  it('drops the given conversations from every page and keeps the cursors', () => {
    const out = withoutConversations(data, new Set(['b', 'c']))
    expect(out.pages.map((p) => p.conversations.map((c) => c.id))).toEqual([['a'], []])
    expect(out.pages.map((p) => p.next_cursor)).toEqual(['x', null])
    expect(out.pageParams).toEqual(['', 'x'])
  })
})
