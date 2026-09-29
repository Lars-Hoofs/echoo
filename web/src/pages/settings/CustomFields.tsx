import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Pencil, Plus, SlidersHorizontal, Trash2 } from 'lucide-react'
import { type SubmitEvent, useState } from 'react'

import { ActionMenu } from '../../components/ActionMenu'
import { Dialog, DialogFooter } from '../../components/Dialog'
import { Button, Card, EmptyState, ErrorNotice, Field, Input, Page, PageHeader, Select, Skeleton, Table, TBody, Td, Textarea, Th, THead, Tr } from '../../components/ui'
import { api } from '../../lib/api'
import { type AttributeDef, type AttributeEntity, attributeDefsQuery, type AttributeType, entityLabel, slugFromLabel, typeLabel } from '../../lib/contacts'
import { errorMessage, fieldError } from '../../lib/errors'
import { Tabs } from '../contacts/Tabs'

type Editing = { kind: 'new' } | { kind: 'edit' | 'delete'; def: AttributeDef }

const entities: AttributeEntity[] = ['contact', 'organization', 'conversation']

export function CustomFieldsPage() {
  const defs = useQuery(attributeDefsQuery())
  const [entity, setEntity] = useState<AttributeEntity>('contact')
  const [editing, setEditing] = useState<Editing | null>(null)
  const list = defs.data?.filter((d) => d.entity === entity) ?? []
  return (
    <Page>
      <PageHeader
        breadcrumb="Werkruimte"
        title="Aangepaste velden"
        description="Leg extra gegevens vast bij contacten, organisaties en gesprekken, bijvoorbeeld een klantnummer. Iedereen met schrijfrechten kan de waarden invullen."
        actions={
          <Button variant="primary" onClick={() => setEditing({ kind: 'new' })}>
            <Plus size={16} aria-hidden />
            Veld toevoegen
          </Button>
        }
      />
      <Tabs tabs={entities.map((e) => ({ id: e, label: entityLabel[e] }))} value={entity} onChange={setEntity}>
        {defs.isPending ? (
          <Skeleton className="h-40" />
        ) : defs.isError ? (
          <ErrorNotice>{errorMessage(defs.error)}</ErrorNotice>
        ) : list.length === 0 ? (
          <Card>
            <EmptyState icon={<SlidersHorizontal size={20} />} title="Nog geen velden" description="Voeg een veld toe om extra gegevens vast te leggen." />
          </Card>
        ) : (
          <Card flush>
            <Table>
              <THead>
                <Th>Naam</Th>
                <Th className="hidden md:table-cell">Sleutel</Th>
                <Th>Type</Th>
                <Th className="w-px">
                  <span className="sr-only">Acties</span>
                </Th>
              </THead>
              <TBody>
                {list.map((d) => (
                  <Tr key={d.id}>
                    <Td>
                      <div className="text-ink">{d.label}</div>
                      {d.type === 'list' && <div className="text-sm text-faint">{d.options.join(', ')}</div>}
                    </Td>
                    <Td className="hidden font-mono text-sm text-muted md:table-cell">{d.key}</Td>
                    <Td className="text-muted">{typeLabel[d.type]}</Td>
                    <Td>
                      <div className="flex justify-end">
                        <ActionMenu
                          label={`Acties voor ${d.label}`}
                          items={[
                            { label: 'Bewerken', icon: <Pencil size={16} />, onSelect: () => setEditing({ kind: 'edit', def: d }) },
                            { label: 'Verwijderen', icon: <Trash2 size={16} />, danger: true, separated: true, onSelect: () => setEditing({ kind: 'delete', def: d }) },
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
      </Tabs>
      {(editing?.kind === 'new' || editing?.kind === 'edit') && (
        <FieldDialog entity={entity} def={editing.kind === 'edit' ? editing.def : null} onClose={() => setEditing(null)} />
      )}
      {editing?.kind === 'delete' && <DeleteDialog def={editing.def} onClose={() => setEditing(null)} />}
    </Page>
  )
}

function FieldDialog({ entity, def, onClose }: { entity: AttributeEntity; def: AttributeDef | null; onClose: () => void }) {
  const [label, setLabel] = useState(def?.label ?? '')
  const [key, setKey] = useState(def?.key ?? '')
  const [keyTouched, setKeyTouched] = useState(false)
  const [type, setType] = useState<AttributeType>(def?.type ?? 'text')
  const [options, setOptions] = useState((def?.options ?? []).join('\n'))
  const queryClient = useQueryClient()
  const optionList = options.split('\n').map((o) => o.trim()).filter(Boolean)
  const save = useMutation({
    mutationFn: () =>
      def
        ? api('PATCH', `/custom-attributes/${def.id}`, { label, ...(def.type === 'list' ? { options: optionList } : {}) })
        : api('POST', '/custom-attributes', { entity, key, label, type, ...(type === 'list' ? { options: optionList } : {}) }),
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: ['custom-attributes'] })
      onClose()
    },
  })
  const submit = (e: SubmitEvent) => {
    e.preventDefault()
    save.mutate()
  }
  const known = ['label', 'key', 'type', 'options', 'entity'].some((f) => fieldError(save.error, f))
  return (
    <Dialog open onOpenChange={(o) => !o && onClose()} title={def ? 'Veld bewerken' : 'Veld toevoegen'} description={`Voor ${entityLabel[entity].toLowerCase()}.`}>
      <form onSubmit={submit} className="flex flex-col gap-4" noValidate>
        {save.isError && !known && <ErrorNotice>{errorMessage(save.error)}</ErrorNotice>}
        <Field label="Naam" error={fieldError(save.error, 'label')}>
          {(p) => (
            <Input
              {...p}
              autoFocus
              maxLength={60}
              value={label}
              onChange={(e) => {
                setLabel(e.target.value)
                if (!def && !keyTouched) setKey(slugFromLabel(e.target.value))
              }}
            />
          )}
        </Field>
        <Field label="Sleutel" help="Kleine letters, cijfers en _. Kun je later niet meer wijzigen." error={fieldError(save.error, 'key') ?? fieldError(save.error, 'entity')}>
          {(p) => (
            <Input
              {...p}
              maxLength={40}
              disabled={def !== null}
              value={key}
              onChange={(e) => {
                setKeyTouched(true)
                setKey(e.target.value)
              }}
            />
          )}
        </Field>
        <Field label="Type" error={fieldError(save.error, 'type')}>
          {(p) => (
            <Select {...p} disabled={def !== null} value={type} onChange={(e) => setType(e.target.value as AttributeType)}>
              {(Object.keys(typeLabel) as AttributeType[]).map((t) => (
                <option key={t} value={t}>
                  {typeLabel[t]}
                </option>
              ))}
            </Select>
          )}
        </Field>
        {type === 'list' && (
          <Field label="Keuzes" help="Eén keuze per regel." error={fieldError(save.error, 'options')}>
            {(p) => <Textarea {...p} value={options} onChange={(e) => setOptions(e.target.value)} />}
          </Field>
        )}
        <DialogFooter>
          <Button onClick={onClose}>Annuleren</Button>
          <Button type="submit" variant="primary" busy={save.isPending} disabled={!label.trim() || (!def && !key)}>
            Opslaan
          </Button>
        </DialogFooter>
      </form>
    </Dialog>
  )
}

function DeleteDialog({ def, onClose }: { def: AttributeDef; onClose: () => void }) {
  const queryClient = useQueryClient()
  const del = useMutation({
    mutationFn: () => api('DELETE', `/custom-attributes/${def.id}`),
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: ['custom-attributes'] })
      onClose()
    },
  })
  return (
    <Dialog open onOpenChange={(o) => !o && onClose()} title="Veld verwijderen">
      <div className="flex flex-col gap-4">
        <p className="text-base text-muted">
          Het veld {def.label} wordt verwijderd, samen met alle waarden die ervoor zijn ingevuld. Dit kan niet ongedaan worden gemaakt.
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
