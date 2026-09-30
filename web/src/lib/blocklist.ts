import { queryOptions } from '@tanstack/react-query'

import { api } from './api'
import type { Ref } from './inbox'

export interface BlockedSender {
  id: string
  mailbox: Ref
  // A lower-case address (naam@voorbeeld.nl) or domain (voorbeeld.nl).
  pattern: string
  created_by: Ref | null
  created_at: string
}

export interface BlockedSenders {
  blocked_senders: BlockedSender[]
  // The mailboxes the user may block senders for.
  mailboxes: Ref[]
}

export const blockedSendersQuery = queryOptions({
  queryKey: ['blocked-senders'],
  queryFn: () => api<BlockedSenders>('GET', '/blocked-senders'),
})

export function blockSender(mailboxId: string, pattern: string): Promise<BlockedSender> {
  return api<BlockedSender>('POST', '/blocked-senders', { mailbox_id: mailboxId, pattern })
}

// The same normalization the server applies, so the form can say what will be blocked.
export function normalizePattern(input: string): string {
  return input.trim().toLowerCase().replace(/^@/, '')
}

export function patternKind(pattern: string): 'address' | 'domain' {
  return pattern.includes('@') ? 'address' : 'domain'
}

export function describePattern(pattern: string): string {
  return patternKind(pattern) === 'address' ? `Alle nieuwe mail van ${pattern}` : `Alle nieuwe mail van adressen op ${pattern}`
}
