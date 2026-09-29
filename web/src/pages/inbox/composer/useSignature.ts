import { useQuery } from '@tanstack/react-query'

import { api } from '../../../lib/api'

// The signature that will be added to a message from this mailbox, or '' when there is none.
export function useSignature(mailboxId: string): string {
  const q = useQuery({
    queryKey: ['composer', 'signature', mailboxId],
    queryFn: () => api<{ body_html: string; source: string }>('GET', `/signatures/effective?mailbox_id=${encodeURIComponent(mailboxId)}`),
    staleTime: 60_000,
  })
  return q.data?.body_html ?? ''
}
