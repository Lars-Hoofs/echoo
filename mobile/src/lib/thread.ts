import type { Message, TimelineEvent } from './types'

export type ThreadEntry = { kind: 'message'; message: Message } | { kind: 'event'; event: TimelineEvent }

const at = (m: Message) => m.received_at ?? m.sent_at ?? ''

// Messages and timeline events in the order they happened, so "Lars sloot het gesprek" sits
// between the reply before it and the customer's answer after it.
export function mergeThread(messages: Message[], events: TimelineEvent[]): ThreadEntry[] {
  const entries: (ThreadEntry & { t: string })[] = [
    ...messages.map((m) => ({ kind: 'message' as const, message: m, t: at(m) })),
    // The creation of the conversation is the first message; it says nothing on its own.
    ...events.filter((e) => e.type !== 'created' || e.data.blocked_sender === true).map((e) => ({ kind: 'event' as const, event: e, t: e.created_at })),
  ]
  // A stable sort keeps a message before an event with the same timestamp.
  entries.sort((a, b) => (a.t < b.t ? -1 : a.t > b.t ? 1 : 0))
  return entries.map(({ t: _t, ...entry }) => entry)
}
