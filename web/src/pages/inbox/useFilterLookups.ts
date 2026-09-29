import { useQueries, useQuery } from '@tanstack/react-query'

import { assigneesQuery, labelsQuery } from '../../lib/actions'
import type { Lookups } from '../../lib/filters'
import { type Ref, summaryQuery } from '../../lib/inbox'

// Names for the ids in a filter, and the colleagues that can be picked as assignee.
export function useFilterLookups(): { lookups: Lookups; users: Ref[] } {
  const summary = useQuery(summaryQuery)
  const labels = useQuery(labelsQuery)
  const mailboxIds = summary.data?.mailboxes.map((m) => m.id) ?? []
  const assignees = useQueries({ queries: mailboxIds.map(assigneesQuery) })

  const users = new Map<string, string>()
  for (const a of assignees) for (const u of a.data?.users ?? []) users.set(u.id, u.name)
  return {
    lookups: {
      mailboxes: new Map(summary.data?.mailboxes.map((m) => [m.id, m.name])),
      teams: new Map(summary.data?.teams.map((t) => [t.id, t.name])),
      labels: new Map(labels.data?.map((l) => [l.id, l.name])),
      users,
    },
    users: [...users].map(([id, name]) => ({ id, name })).sort((a, b) => a.name.localeCompare(b.name, 'nl')),
  }
}
