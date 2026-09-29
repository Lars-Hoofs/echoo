import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Pencil, Plus, Tag, Trash2 } from 'lucide-react'
import { type SubmitEvent, useState } from 'react'

import { ActionMenu } from '../../components/ActionMenu'
import { Dialog, DialogFooter } from '../../components/Dialog'
import { LabelChip } from '../../components/LabelChip'
import {
  Button,
  Card,
  EmptyState,
  ErrorNotice,
  Field,
  Input,
  Page,
  PageHeader,
  Skeleton,
  Table,
  TBody,
  Td,
  Th,
  THead,
  Tr,
} from '../../components/ui'
import { type Label, labelColorName, labelColors, labelsQuery } from '../../lib/actions'
import { api, ApiError } from '../../lib/api'
import { errorMessage, fieldError } from '../../lib/errors'
import type { LabelColor } from '../../lib/inbox'

type Editing = { kind: 'new' } | { kind: 'edit' | 'delete'; label: Label }

export function LabelsPage() {
  const labels = useQuery(labelsQuery)
  const [editing, setEditing] = useState<Editing | null>(null)

  return (
    <Page>
      <PageHeader
        breadcrumb="Werkruimte"
        title="Labels"
        description="Met labels sorteer je gesprekken, bijvoorbeeld op onderwerp. Iedereen kan ze gebruiken; alleen beheerders maken ze aan."
        actions={
          <Button variant="primary" onClick={() => setEditing({ kind: 'new' })}>
            <Plus size={16} aria-hidden />
            Label toevoegen
          </Button>
        }
      />
      {labels.isPending ? (
        <Skeleton className="h-40" />
      ) : labels.isError ? (
        <ErrorNotice>{errorMessage(labels.error)}</ErrorNotice>
      ) : labels.data.length === 0 ? (
        <Card>
          <EmptyState
            icon={<Tag size={20} />}
            title="Nog geen labels"
            description="Maak een label aan om gesprekken te sorteren."
            action={
              <Button variant="primary" onClick={() => setEditing({ kind: 'new' })}>
                Maak het eerste label aan
              </Button>
            }
          />
        </Card>
      ) : (
        <Card flush>
          <Table>
            <THead>
              <Th>Label</Th>
              <Th className="hidden md:table-cell">Omschrijving</Th>
              <Th className="w-px">
                <span className="sr-only">Acties</span>
              </Th>
            </THead>
            <TBody>
              {labels.data.map((l) => (
                <Tr key={l.id}>
                  <Td>
                    <div className="flex flex-col items-start gap-1">
                      <LabelChip label={l} />
                      {l.description && <span className="text-sm text-faint md:hidden">{l.description}</span>}
                    </div>
                  </Td>
                  <Td className="hidden text-muted md:table-cell">{l.description || '—'}</Td>
                  <Td>
                    <div className="flex items-center justify-end">
                      <ActionMenu
                        label={`Acties voor ${l.name}`}
                        items={[
                          { label: 'Bewerken', icon: <Pencil size={16} />, onSelect: () => setEditing({ kind: 'edit', label: l }) },
                          {
                            label: 'Verwijderen',
                            icon: <Trash2 size={16} />,
                            danger: true,
                            separated: true,
                            onSelect: () => setEditing({ kind: 'delete', label: l }),
                          },
                        ]}
                      />
                    </div>
                  </Td>
                </Tr>
              ))}
            </TBody>
          </Table>
        </Card>
      )}
      {(editing?.kind === 'new' || editing?.kind === 'edit') && (
        <LabelDialog label={editing.kind === 'edit' ? editing.label : null} onClose={() => setEditing(null)} />
      )}
      {editing?.kind === 'delete' && <DeleteDialog label={editing.label} onClose={() => setEditing(null)} />}
    </Page>
  )
}

const nameErrors: Record<string, string> = {
  invalid: 'Vul een naam in (maximaal 50 tekens).',
  required: 'Vul een naam in.',
  taken: 'Er bestaat al een label met deze naam.',
}

function LabelDialog({ label, onClose }: { label: Label | null; onClose: () => void }) {
  const [name, setName] = useState(label?.name ?? '')
  const [color, setColor] = useState<LabelColor>(label?.color_token ?? 'blue')
  const [description, setDescription] = useState(label?.description ?? '')
  const queryClient = useQueryClient()
  const save = useMutation({
    mutationFn: () => {
      const body = { name, color_token: color, description }
      return label ? api('PATCH', `/labels/${label.id}`, body) : api('POST', '/labels', body)
    },
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: ['labels'] })
      await queryClient.invalidateQueries({ queryKey: ['inbox'] })
      onClose()
    },
  })
  const nameCode = save.error instanceof ApiError ? save.error.fields.name : undefined
  const submit = (e: SubmitEvent) => {
    e.preventDefault()
    save.mutate()
  }
  return (
    <Dialog open onOpenChange={(o) => !o && onClose()} title={label ? 'Label bewerken' : 'Label toevoegen'}>
      <form onSubmit={submit} className="flex flex-col gap-4" noValidate>
        {save.isError && !nameCode && !fieldError(save.error, 'description') && <ErrorNotice>{errorMessage(save.error)}</ErrorNotice>}
        <Field label="Naam" error={nameCode ? (nameErrors[nameCode] ?? 'Ongeldige waarde.') : undefined}>
          {(p) => <Input {...p} autoFocus maxLength={50} value={name} onChange={(e) => setName(e.target.value)} />}
        </Field>
        <fieldset className="flex flex-col gap-1.5">
          <legend className="mb-1.5 text-sm text-ink">Kleur</legend>
          <div className="flex flex-wrap gap-2">
            {labelColors.map((c) => (
              <label
                key={c}
                className="cursor-pointer rounded-full has-checked:ring-2 has-checked:ring-ink has-checked:ring-offset-2 has-checked:ring-offset-float has-focus-visible:outline-2 has-focus-visible:outline-offset-2 has-focus-visible:outline-ink"
              >
                <input type="radio" name="color" value={c} checked={color === c} onChange={() => setColor(c)} className="sr-only" />
                <LabelChip label={{ name: labelColorName[c], color_token: c }} />
              </label>
            ))}
          </div>
        </fieldset>
        <Field label="Omschrijving" help="Optioneel, maximaal 200 tekens." error={fieldError(save.error, 'description')}>
          {(p) => <Input {...p} maxLength={200} value={description} onChange={(e) => setDescription(e.target.value)} />}
        </Field>
        <div className="flex items-center gap-2 text-sm text-muted">
          Voorbeeld
          <LabelChip label={{ name: name.trim() || 'Label', color_token: color }} />
        </div>
        <DialogFooter>
          <Button onClick={onClose}>Annuleren</Button>
          <Button type="submit" variant="primary" busy={save.isPending} disabled={!name.trim()}>
            Opslaan
          </Button>
        </DialogFooter>
      </form>
    </Dialog>
  )
}

function DeleteDialog({ label, onClose }: { label: Label; onClose: () => void }) {
  const queryClient = useQueryClient()
  const del = useMutation({
    mutationFn: () => api('DELETE', `/labels/${label.id}`),
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: ['labels'] })
      await queryClient.invalidateQueries({ queryKey: ['inbox'] })
      onClose()
    },
  })
  return (
    <Dialog open onOpenChange={(o) => !o && onClose()} title="Label verwijderen">
      <div className="flex flex-col gap-4">
        <p className="text-base text-muted">
          Het label {label.name} wordt van alle gesprekken gehaald. De gesprekken zelf blijven bestaan.
        </p>
        {del.isError && <ErrorNotice>{errorMessage(del.error)}</ErrorNotice>}
        <DialogFooter>
          <Button onClick={onClose}>Annuleren</Button>
          <Button variant="danger" busy={del.isPending} onClick={() => del.mutate()}>
            Verwijderen
          </Button>
        </DialogFooter>
      </div>
    </Dialog>
  )
}
