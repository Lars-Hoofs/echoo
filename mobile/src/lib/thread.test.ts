import { describe, expect, it } from 'vitest'

import { mergeThread } from './thread'
import type { Message, TimelineEvent } from './types'

const msg = (id: string, at: string): Message => ({
  id,
  kind: 'email',
  direction: 'in',
  from: { name: '', address: 'a@example.com' },
  to: [],
  cc: [],
  subject: '',
  body_text: '',
  sent_at: null,
  received_at: at,
  author: null,
  attachments: [],
  outbound_status: null,
  outbound_error: null,
  has_html: false,
  render_url: '',
  blocked_images: 0,
  phishing_warnings: [],
})
const ev = (id: string, type: string, at: string, data: Record<string, unknown> = {}): TimelineEvent => ({ id, type, created_at: at, actor: null, user: null, data })

describe('mergeThread', () => {
  it('orders messages and events by time and hides the plain creation event', () => {
    const out = mergeThread(
      [msg('m2', '2026-09-30T10:05:00Z'), msg('m1', '2026-09-30T10:00:00Z')],
      [ev('e1', 'resolved', '2026-09-30T10:02:00Z'), ev('e0', 'created', '2026-09-30T10:00:00Z')],
    )
    expect(out.map((e) => (e.kind === 'message' ? e.message.id : e.event.id))).toEqual(['m1', 'e1', 'm2'])
  })
  it('keeps the creation event when it explains a blocked sender', () => {
    const out = mergeThread([], [ev('e0', 'created', '2026-09-30T10:00:00Z', { blocked_sender: true })])
    expect(out).toHaveLength(1)
  })
})
