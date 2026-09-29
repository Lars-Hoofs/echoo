import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { type SubmitEvent, useState } from 'react'

import { Button, Card, ErrorNotice, Skeleton } from '../../components/ui'
import { api } from '../../lib/api'
import {
  type AttributeEntity,
  attributeDefsQuery,
  attributeDrafts,
  attributesToPatch,
  type Attributes,
  formatAttribute,
} from '../../lib/contacts'
import { errorMessage } from '../../lib/errors'
import { AttributeFields } from './fields'

// Shows the custom attributes of a contact or organization and lets editors change them.
export function AttributesTab({
  entity,
  patchPath,
  current,
  editable,
  invalidateKey,
}: {
  entity: AttributeEntity
  patchPath: string
  current: Attributes
  editable: boolean
  invalidateKey: string[]
}) {
  const defs = useQuery(attributeDefsQuery(entity))
  const [editing, setEditing] = useState(false)
  const [drafts, setDrafts] = useState<Record<string, string>>({})
  const queryClient = useQueryClient()
  const save = useMutation({
    mutationFn: () =>
      api('PATCH', patchPath, {
        custom_attributes: attributesToPatch(defs.data ?? [], drafts, current),
      }),
    onSuccess: async () => {
      setEditing(false)
      await queryClient.invalidateQueries({ queryKey: invalidateKey })
    },
  })
  if (defs.isPending) return <Skeleton className="h-24" />
  if (defs.isError) return <ErrorNotice>{errorMessage(defs.error)}</ErrorNotice>
  if (defs.data.length === 0) {
    return <p className="text-base text-muted">Er zijn nog geen aangepaste velden. Een beheerder maakt ze aan bij Instellingen.</p>
  }
  const submit = (e: SubmitEvent) => {
    e.preventDefault()
    save.mutate()
  }
  if (editing) {
    return (
      <Card>
        <form onSubmit={submit} className="flex flex-col gap-4" noValidate>
          <AttributeFields defs={defs.data} drafts={drafts} onChange={(k, v) => setDrafts((d) => ({ ...d, [k]: v }))} error={save.error} />
          {save.isError && <ErrorNotice>{errorMessage(save.error)}</ErrorNotice>}
          <div className="flex gap-2">
            <Button type="submit" variant="primary" busy={save.isPending}>
              Opslaan
            </Button>
            <Button onClick={() => setEditing(false)}>Annuleren</Button>
          </div>
        </form>
      </Card>
    )
  }
  return (
    <Card
      actions={
        editable ? (
          <Button
            size="sm"
            onClick={() => {
              setDrafts(attributeDrafts(defs.data, current))
              setEditing(true)
            }}
          >
            Velden bewerken
          </Button>
        ) : undefined
      }
      title="Aangepaste velden"
      flush
    >
      <dl className="divide-y divide-line">
        {defs.data.map((d) => {
          const value = current[d.key]
          return (
            <div key={d.id} className="flex items-baseline justify-between gap-4 px-4 py-3 sm:px-6">
              <dt className="text-base text-muted">{d.label}</dt>
              <dd className="min-w-0 text-right text-base wrap-anywhere text-ink">
                {d.type === 'link' && typeof value === 'string' ? (
                  <a href={value} target="_blank" rel="noreferrer noopener" className="text-ink hover:underline">
                    {value}
                  </a>
                ) : (
                  formatAttribute(d, value)
                )}
              </dd>
            </div>
          )
        })}
      </dl>
    </Card>
  )
}
