import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Pencil, Plus, Trash2, Zap } from 'lucide-react'
import { type SubmitEvent, useState } from 'react'

import { ActionMenu } from '../../components/ActionMenu'
import { ActionsEditor } from '../../components/automation/ActionsEditor'
import { Dialog, DialogFooter } from '../../components/Dialog'
import { Button, Card, ErrorNotice, Field, Input, Page, PageHeader, Segmented, Skeleton, Table, TBody, Td, Th, THead, Tr } from '../../components/ui'
import { api, ApiError } from '../../lib/api'
import { actionLabel, type Macro, macrosQuery, type RuleAction } from '../../lib/automation'
import { errorMessage, fieldError } from '../../lib/errors'
import { hasPermission, meQuery } from '../../lib/session'

type Editing = { kind: 'new'; scope: Macro['scope'] } | { kind: 'edit' | 'delete'; macro: Macro }

export function MacrosPage() {
  const me = useQuery(meQuery)
  const macros = useQuery(macrosQuery)
  const [editing, setEditing] = useState<Editing | null>(null)
  const admin = hasPermission(me.data, 'templates.manage_shared')
  const writer = hasPermission(me.data, 'conversations.write')
  const personal = macros.data?.filter((m) => m.scope === 'personal') ?? []
  const shared = macros.data?.filter((m) => m.scope === 'global') ?? []
  const editable = (m: Macro) => (m.scope === 'global' ? admin : writer)

  const table = (list: Macro[], empty: string) =>
    list.length === 0 ? (
      <p className="px-4 py-4 text-base text-muted sm:px-6">{empty}</p>
    ) : (
      <Table>
        <THead>
          <Th>Naam</Th>
          <Th className="hidden md:table-cell">Acties</Th>
          <Th className="w-px">
            <span className="sr-only">Acties</span>
          </Th>
        </THead>
        <TBody>
          {list.map((m) => (
            <Tr key={m.id}>
              <Td className="text-ink">{m.name}</Td>
              <Td className="hidden text-muted md:table-cell">{m.actions.map((a) => actionLabel(a.type).toLowerCase()).join(', ')}</Td>
              <Td>
                <div className="flex justify-end">
                  {editable(m) && (
                    <ActionMenu
                      label={`Acties voor ${m.name}`}
                      items={[
                        { label: 'Bewerken', icon: <Pencil size={16} />, onSelect: () => setEditing({ kind: 'edit', macro: m }) },
                        { label: 'Verwijderen', icon: <Trash2 size={16} />, danger: true, separated: true, onSelect: () => setEditing({ kind: 'delete', macro: m }) },
                      ]}
                    />
                  )}
                </div>
              </Td>
            </Tr>
          ))}
        </TBody>
      </Table>
    )

  return (
    <Page>
      <PageHeader
        breadcrumb="Persoonlijk"
        title="Macro's"
        description="Een macro voert een reeks acties uit op één of meer gesprekken, bijvoorbeeld label toevoegen en sluiten. Je start ze vanuit het gesprek of de selectie in de lijst."
        actions={
          writer ? (
            <Button variant="primary" onClick={() => setEditing({ kind: 'new', scope: 'personal' })}>
              <Plus size={16} aria-hidden />
              Macro toevoegen
            </Button>
          ) : undefined
        }
      />
      {macros.isPending ? (
        <Skeleton className="h-40" />
      ) : macros.isError ? (
        <ErrorNotice>{errorMessage(macros.error)}</ErrorNotice>
      ) : (
        <>
          <Card title="Persoonlijk" icon={<Zap size={16} />} description="Alleen voor jou." flush>
            {table(personal, 'Je hebt nog geen persoonlijke macro’s.')}
          </Card>
          <Card
            title="Werkruimte"
            icon={<Zap size={16} />}
            description={admin ? 'Voor iedereen. Alleen beheerders wijzigen deze.' : 'Gedeeld door je beheerders. Je kunt ze gebruiken, niet wijzigen.'}
            actions={
              admin ? (
                <Button size="sm" onClick={() => setEditing({ kind: 'new', scope: 'global' })}>
                  <Plus size={14} aria-hidden />
                  Toevoegen
                </Button>
              ) : undefined
            }
            flush
          >
            {table(shared, 'Er zijn nog geen macro’s voor de werkruimte.')}
          </Card>
        </>
      )}
      {(editing?.kind === 'new' || editing?.kind === 'edit') && (
        <MacroDialog macro={editing.kind === 'edit' ? editing.macro : null} scope={editing.kind === 'new' ? editing.scope : 'personal'} admin={admin} onClose={() => setEditing(null)} />
      )}
      {editing?.kind === 'delete' && <DeleteDialog macro={editing.macro} onClose={() => setEditing(null)} />}
    </Page>
  )
}

function MacroDialog({ macro, scope: initialScope, admin, onClose }: { macro: Macro | null; scope: Macro['scope']; admin: boolean; onClose: () => void }) {
  const [name, setName] = useState(macro?.name ?? '')
  const [scope, setScope] = useState<Macro['scope']>(macro?.scope ?? initialScope)
  const [actions, setActions] = useState<RuleAction[]>(macro?.actions ?? [])
  const queryClient = useQueryClient()
  const save = useMutation({
    mutationFn: () => (macro ? api('PATCH', `/macros/${macro.id}`, { name, actions }) : api('POST', '/macros', { name, scope, actions })),
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: ['macros'] })
      onClose()
    },
  })
  const submit = (e: SubmitEvent) => {
    e.preventDefault()
    save.mutate()
  }
  const rejected = new Set<number>()
  if (save.error instanceof ApiError) {
    for (const key of Object.keys(save.error.fields)) {
      const m = /^actions\.(\d+)\./.exec(key)
      if (m?.[1]) rejected.add(Number(m[1]))
    }
  }
  return (
    <Dialog open onOpenChange={(o) => !o && onClose()} title={macro ? 'Macro bewerken' : 'Macro toevoegen'} size="lg">
      <form onSubmit={submit} className="flex flex-col gap-4" noValidate>
        {save.isError && <ErrorNotice>{errorMessage(save.error)}</ErrorNotice>}
        <Field label="Naam" error={fieldError(save.error, 'name')}>
          {(p) => <Input {...p} autoFocus maxLength={100} value={name} onChange={(e) => setName(e.target.value)} />}
        </Field>
        {!macro && admin && (
          <Segmented
            label="Voor wie"
            value={scope}
            options={[
              { value: 'personal', label: 'Alleen ik' },
              { value: 'global', label: 'Hele werkruimte' },
            ]}
            onChange={setScope}
          />
        )}
        <div className="flex flex-col gap-2">
          <h3 className="text-sm text-ink">Acties</h3>
          <ActionsEditor actions={actions} onChange={setActions} directory={admin} allowRulesOnly={false} errors={rejected} />
          {fieldError(save.error, 'actions') && <p className="text-sm text-danger-text">{fieldError(save.error, 'actions')}</p>}
        </div>
        <DialogFooter>
          <Button onClick={onClose}>Annuleren</Button>
          <Button type="submit" variant="primary" busy={save.isPending} disabled={!name.trim() || actions.length === 0}>
            Opslaan
          </Button>
        </DialogFooter>
      </form>
    </Dialog>
  )
}

function DeleteDialog({ macro, onClose }: { macro: Macro; onClose: () => void }) {
  const queryClient = useQueryClient()
  const del = useMutation({
    mutationFn: () => api('DELETE', `/macros/${macro.id}`),
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: ['macros'] })
      onClose()
    },
  })
  return (
    <Dialog open onOpenChange={(o) => !o && onClose()} title="Macro verwijderen">
      <div className="flex flex-col gap-4">
        <p className="text-base text-muted">De macro {macro.name} wordt verwijderd.</p>
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
