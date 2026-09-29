import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Link } from '@tanstack/react-router'
import { useState } from 'react'

import { Button } from '../../components/ui'
import { api } from '../../lib/api'
import { type AttributeDef, attributeDefsQuery, type Attributes, attributesToPatch, formatAttribute, type Note } from '../../lib/contacts'
import { errorMessage } from '../../lib/errors'
import { formatRelative } from '../../lib/format'
import { AttributeInput } from './fields'

// Compact read-only attributes and the latest notes of a contact, for the inbox side panel.
export function ContactPanelExtras({ contactId }: { contactId: string }) {
  const defs = useQuery(attributeDefsQuery('contact'))
  const contact = useQuery({
    queryKey: ['contact', contactId],
    queryFn: async () => (await api<{ contact: { custom_attributes: Attributes } }>('GET', `/contacts/${contactId}`)).contact,
  })
  const notes = useQuery({
    queryKey: ['notes', 'contacts', contactId, 3],
    queryFn: async () => (await api<{ notes: Note[] }>('GET', `/contacts/${contactId}/notes?limit=3`)).notes,
  })
  const filled = (defs.data ?? []).filter((d) => contact.data?.custom_attributes[d.key] !== undefined)
  const latest = notes.data ?? []
  if (filled.length === 0 && latest.length === 0) return null
  return (
    <div className="w-full text-left">
      {filled.length > 0 && (
        <dl className="mt-2 divide-y divide-line">
          {filled.map((d) => (
            <div key={d.id} className="flex items-baseline justify-between gap-3 py-1">
              <dt className="shrink-0 text-base text-muted">{d.label}</dt>
              <dd className="min-w-0 text-right text-base wrap-anywhere text-ink">
                {formatAttribute(d, contact.data?.custom_attributes[d.key])}
              </dd>
            </div>
          ))}
        </dl>
      )}
      {latest.length > 0 && (
        <section className="mt-3" aria-label="Laatste notities">
          <h3 className="mb-1 text-sm text-faint">Laatste notities</h3>
          <ul className="flex flex-col gap-2">
            {latest.map((n) => (
              <li key={n.id} className="rounded-md border border-line px-3 py-2 text-sm">
                <p className="line-clamp-3 wrap-anywhere whitespace-pre-wrap text-ink">{n.body}</p>
                <p className="mt-1 text-faint">
                  {n.author?.name ?? 'Onbekend'} · {formatRelative(n.created_at)}
                </p>
              </li>
            ))}
          </ul>
          <Link to="/contacten/$id" params={{ id: contactId }} className="mt-1 inline-block text-sm text-ink hover:underline">
            Alle notities
          </Link>
        </section>
      )}
    </div>
  )
}

// Custom attributes of one conversation, editable by anyone who may modify it.
export function ConversationAttributes({ conversationId, editable }: { conversationId: string; editable: boolean }) {
  const defs = useQuery(attributeDefsQuery('conversation'))
  const values = useQuery({
    queryKey: ['conversation-attributes', conversationId],
    queryFn: async () => (await api<{ attributes: Attributes }>('GET', `/conversations/${conversationId}/attributes`)).attributes,
  })
  const list = defs.data ?? []
  if (list.length === 0 || !values.data) return null
  return <AttributeRows defs={list} current={values.data} conversationId={conversationId} editable={editable} />
}

function AttributeRows({
  defs,
  current,
  conversationId,
  editable,
}: {
  defs: AttributeDef[]
  current: Attributes
  conversationId: string
  editable: boolean
}) {
  const [drafts, setDrafts] = useState<Record<string, string>>(() =>
    Object.fromEntries(defs.filter((d) => current[d.key] !== undefined).map((d) => [d.key, String(current[d.key])])),
  )
  const [seenCurrent, setSeenCurrent] = useState(current)
  if (seenCurrent !== current) {
    setSeenCurrent(current)
    setDrafts(Object.fromEntries(defs.filter((d) => current[d.key] !== undefined).map((d) => [d.key, String(current[d.key])])))
  }
  const queryClient = useQueryClient()
  const save = useMutation({
    mutationFn: () =>
      api('PATCH', `/conversations/${conversationId}/attributes`, {
        attributes: attributesToPatch(defs, drafts, current),
      }),
    onSuccess: () =>
      queryClient.invalidateQueries({
        queryKey: ['conversation-attributes', conversationId],
      }),
  })
  const dirty = Object.keys(attributesToPatch(defs, drafts, current)).length > 0
  if (!editable) {
    return (
      <dl className="divide-y divide-line">
        {defs.map((d) => (
          <div key={d.id} className="flex items-baseline justify-between gap-3 py-2">
            <dt className="text-base text-muted">{d.label}</dt>
            <dd className="text-right text-base text-ink">{formatAttribute(d, current[d.key])}</dd>
          </div>
        ))}
      </dl>
    )
  }
  return (
    <form
      className="flex flex-col gap-2 pt-2"
      onSubmit={(e) => {
        e.preventDefault()
        save.mutate()
      }}
    >
      {defs.map((d) => (
        <label key={d.id} className="flex flex-col gap-1 t-label">
          {d.label}
          <AttributeInput def={d} value={drafts[d.key] ?? ''} onChange={(v) => setDrafts((cur) => ({ ...cur, [d.key]: v }))} />
        </label>
      ))}
      {save.isError && (
        <p role="alert" className="text-sm text-danger-text">
          {errorMessage(save.error)}
        </p>
      )}
      <div>
        <Button type="submit" size="sm" busy={save.isPending} disabled={!dirty}>
          Opslaan
        </Button>
      </div>
    </form>
  )
}
