import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Pencil, Plus, Trash2, Users } from 'lucide-react'
import { type SubmitEvent, useState } from 'react'

import { ActionMenu } from '../../components/ActionMenu'
import { Avatar } from '../../components/Avatar'
import { Dialog, DialogFooter } from '../../components/Dialog'
import { Num } from '../../components/Num'
import { Button, Card, EmptyState, ErrorNotice, Field, Input, Page, PageHeader, Skeleton, Table, TBody, Td, Th, THead, Tr } from '../../components/ui'
import { api } from '../../lib/api'
import { errorMessage, fieldError } from '../../lib/errors'
import type { User } from '../../lib/session'

interface Team {
  id: string
  name: string
  member_count: number
}

type Editing = { kind: 'new' } | { kind: 'rename' | 'members' | 'delete'; team: Team }

const memberCount = (t: Team) => (t.member_count === 1 ? '1 lid' : `${t.member_count} leden`)

export function TeamsPage() {
  const teams = useQuery({ queryKey: ['teams'], queryFn: () => api<{ teams: Team[] }>('GET', '/teams') })
  const [editing, setEditing] = useState<Editing | null>(null)

  return (
    <Page>
      <PageHeader breadcrumb="Werkruimte"
        title="Teams"
        description="Teams bepalen welke mailboxen iemand ziet."
        actions={
          <Button variant="primary" onClick={() => setEditing({ kind: 'new' })}>
            <Plus size={16} aria-hidden />
            Team toevoegen
          </Button>
        }
      />
      {teams.isPending ? (
        <Skeleton className="h-40" />
      ) : teams.isError ? (
        <ErrorNotice>{errorMessage(teams.error)}</ErrorNotice>
      ) : teams.data.teams.length === 0 ? (
        <Card>
          <EmptyState
            icon={<Users size={20} />}
            title="Nog geen teams"
            description="Maak een team aan om gebruikers te groeperen."
            action={
              <Button variant="primary" onClick={() => setEditing({ kind: 'new' })}>
                Maak het eerste team aan
              </Button>
            }
          />
        </Card>
      ) : (
        <Card flush>
          <Table>
            <THead>
              <Th>Team</Th>
              <Th numeric className="hidden md:table-cell">
                Leden
              </Th>
              <Th className="w-px">
                <span className="sr-only">Acties</span>
              </Th>
            </THead>
            <TBody>
              {teams.data.teams.map((t) => (
                <Tr key={t.id}>
                  <Td>
                    <div className="flex items-center gap-3">
                      <span aria-hidden className="flex size-8 shrink-0 items-center justify-center rounded-md bg-subtle text-muted">
                        <Users size={16} />
                      </span>
                      <div className="min-w-0">
                        <div className="truncate text-ink">{t.name}</div>
                        <div className="text-sm text-faint md:hidden">{memberCount(t)}</div>
                      </div>
                    </div>
                  </Td>
                  <Td numeric className="hidden whitespace-nowrap text-muted md:table-cell">
                    <Num value={t.member_count} size="s" unit={t.member_count === 1 ? 'lid' : 'leden'} />
                  </Td>
                  <Td>
                    <div className="flex items-center justify-end gap-1">
                      <Button size="sm" onClick={() => setEditing({ kind: 'members', team: t })} aria-label={`Leden van ${t.name}`}>
                        Leden
                      </Button>
                      <ActionMenu
                        label={`Acties voor ${t.name}`}
                        items={[
                          { label: 'Naam wijzigen', icon: <Pencil size={16} />, onSelect: () => setEditing({ kind: 'rename', team: t }) },
                          { label: 'Verwijderen', icon: <Trash2 size={16} />, danger: true, separated: true, onSelect: () => setEditing({ kind: 'delete', team: t }) },
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
      {(editing?.kind === 'new' || editing?.kind === 'rename') && (
        <NameDialog team={editing.kind === 'rename' ? editing.team : null} onClose={() => setEditing(null)} />
      )}
      {editing?.kind === 'members' && <MembersDialog team={editing.team} onClose={() => setEditing(null)} />}
      {editing?.kind === 'delete' && <DeleteDialog team={editing.team} onClose={() => setEditing(null)} />}
    </Page>
  )
}

function NameDialog({ team, onClose }: { team: Team | null; onClose: () => void }) {
  const [name, setName] = useState(team?.name ?? '')
  const queryClient = useQueryClient()
  const save = useMutation({
    mutationFn: () => (team ? api('PATCH', `/teams/${team.id}`, { name }) : api('POST', '/teams', { name })),
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: ['teams'] })
      onClose()
    },
  })
  const submit = (e: SubmitEvent) => {
    e.preventDefault()
    save.mutate()
  }
  return (
    <Dialog open onOpenChange={(o) => !o && onClose()} title={team ? 'Naam wijzigen' : 'Team toevoegen'}>
      <form onSubmit={submit} className="flex flex-col gap-4" noValidate>
        {save.isError && !fieldError(save.error, 'name') && <ErrorNotice>{errorMessage(save.error)}</ErrorNotice>}
        <Field label="Naam" error={fieldError(save.error, 'name')}>
          {(p) => <Input {...p} autoFocus value={name} onChange={(e) => setName(e.target.value)} />}
        </Field>
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

function MembersDialog({ team, onClose }: { team: Team; onClose: () => void }) {
  const queryClient = useQueryClient()
  const users = useQuery({ queryKey: ['users'], queryFn: () => api<{ users: User[] }>('GET', '/users') })
  const members = useQuery({
    queryKey: ['teams', team.id, 'members'],
    queryFn: () => api<{ user_ids: string[] }>('GET', `/teams/${team.id}/members`),
  })
  const [selected, setSelected] = useState<Set<string> | null>(null)
  const current = selected ?? new Set(members.data?.user_ids ?? [])

  const save = useMutation({
    mutationFn: () => api('PUT', `/teams/${team.id}/members`, { user_ids: [...current] }),
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: ['teams'] })
      onClose()
    },
  })

  const toggle = (id: string) => {
    const next = new Set(current)
    if (next.has(id)) next.delete(id)
    else next.add(id)
    setSelected(next)
  }

  const active = users.data?.users.filter((u) => !u.deactivated) ?? []

  return (
    <Dialog open onOpenChange={(o) => !o && onClose()} title={`Leden van ${team.name}`}>
      {users.isPending || members.isPending ? (
        <Skeleton className="h-32" />
      ) : users.isError || members.isError ? (
        <ErrorNotice>{errorMessage(users.error ?? members.error)}</ErrorNotice>
      ) : (
        <div className="flex flex-col gap-4">
          <fieldset className="max-h-72 overflow-y-auto rounded-md border border-line">
            <legend className="sr-only">Leden</legend>
            {active.map((u) => (
              <label key={u.id} className="flex cursor-pointer items-center gap-3 border-b border-line px-3 py-2 last:border-b-0 hover:bg-subtle max-md:py-3">
                <input type="checkbox" checked={current.has(u.id)} onChange={() => toggle(u.id)} className="size-4 shrink-0 accent-(--accent)" />
                <Avatar name={u.name} size={24} />
                <span className="min-w-0">
                  <span className="block truncate text-base text-ink">{u.name}</span>
                  <span className="block truncate text-sm text-faint">{u.email}</span>
                </span>
              </label>
            ))}
          </fieldset>
          {save.isError && <ErrorNotice>{errorMessage(save.error)}</ErrorNotice>}
          <DialogFooter>
            <Button onClick={onClose}>Annuleren</Button>
            <Button variant="primary" busy={save.isPending} onClick={() => save.mutate()}>
              Opslaan
            </Button>
          </DialogFooter>
        </div>
      )}
    </Dialog>
  )
}

function DeleteDialog({ team, onClose }: { team: Team; onClose: () => void }) {
  const queryClient = useQueryClient()
  const del = useMutation({
    mutationFn: () => api('DELETE', `/teams/${team.id}`),
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: ['teams'] })
      onClose()
    },
  })
  return (
    <Dialog open onOpenChange={(o) => !o && onClose()} title="Team verwijderen">
      <div className="flex flex-col gap-4">
        <p className="text-base text-muted">
          {team.name} wordt verwijderd. De {team.member_count === 1 ? 'gebruiker blijft' : 'gebruikers blijven'} bestaan.
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
