import { queryOptions } from '@tanstack/react-query'

import { api } from '../../lib/api'

interface Named {
  id: string
  name: string
}

// The lists the rule and macro editors pick from. Users, teams, mailboxes and policies can only
// be listed by admins; the editors only ask for them when the caller is one.
export const usersQuery = queryOptions({
  queryKey: ['users'],
  queryFn: () => api<{ users: (Named & { deactivated: boolean; can_write_conversations: boolean })[] }>('GET', '/users').then((r) => r.users.filter((u) => !u.deactivated && u.can_write_conversations)),
  staleTime: 60_000,
})

export const teamsQuery = queryOptions({
  queryKey: ['teams'],
  queryFn: () => api<{ teams: Named[] }>('GET', '/teams'),
  staleTime: 60_000,
})

export const mailboxesQuery = queryOptions({
  queryKey: ['mailboxes'],
  queryFn: () => api<{ mailboxes: Named[] }>('GET', '/mailboxes'),
  staleTime: 60_000,
})
