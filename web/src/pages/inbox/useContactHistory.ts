import { useQuery } from '@tanstack/react-query'

import { api } from '../../lib/api'
import type { ConversationPage } from '../../lib/inbox'

export interface ContactHistory {
  open: number
  waitingOnUs: number
  // True when the contact has more conversations than the first page, so the counts are a floor.
  partial: boolean
}

// What a contact has open with us, counted from the first page of their conversations (the ones
// the signed-in user may see; open ones come first).
export function useContactHistory(contactId: string | undefined) {
  return useQuery({
    queryKey: ['inbox', 'contact-history', contactId],
    enabled: contactId !== undefined,
    queryFn: async (): Promise<ContactHistory> => {
      const page = await api<ConversationPage>('GET', `/contacts/${contactId}/conversations`)
      const open = page.conversations.filter((c) => c.status === 'open' || c.status === 'waiting')
      return {
        open: open.length,
        waitingOnUs: open.filter((c) => c.status === 'open' && c.last_direction === 'in').length,
        partial: page.next_cursor !== null,
      }
    },
  })
}
