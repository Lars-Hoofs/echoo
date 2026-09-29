import { useMutation, useQuery, useQueryClient, useSuspenseQuery } from '@tanstack/react-query'
import { Eye, Pencil, Plus, Trash2, UserCog } from 'lucide-react'
import { type SubmitEvent, useState } from 'react'

import { ActionMenu } from '../../components/ActionMenu'
import { Dialog, DialogFooter } from '../../components/Dialog'
import { PermissionPicker } from '../../components/PermissionPicker'
import { Num } from '../../components/Num'
import {
  Button,
  Card,
  EmptyState,
  ErrorNotice,
  Field,
  Input,
  Page,
  PageHeader,
  Section,
  Skeleton,
  Table,
  TBody,
  Td,
  Textarea,
  Th,
  THead,
  Tr,
} from '../../components/ui'
import { api } from '../../lib/api'
import { errorMessage, fieldError } from '../../lib/errors'
import { builtinRoleDescriptions, canGrant, type CustomRole, type Permission, privilegedPermissions, rolesQuery } from '../../lib/permissions'
import { meQuery, roleLabel, type Role } from '../../lib/session'

type Editing = { kind: 'new' } | { kind: 'edit' | 'delete'; role: CustomRole } | { kind: 'view'; title: string; permissions: Permission[] }

export function RolesPage() {
  const roles = useQuery(rolesQuery)
  const [editing, setEditing] = useState<Editing | null>(null)

  return (
    <Page>
      <PageHeader breadcrumb="Werkruimte"
        title="Rollen"
        description="Bepaal wat mensen in deze werkruimte mogen. De ingebouwde rollen staan vast; met eigen rollen kies je zelf de rechten."
        actions={
          <Button variant="primary" onClick={() => setEditing({ kind: 'new' })}>
            <Plus size={16} aria-hidden />
            Rol toevoegen
          </Button>
        }
      />
      {roles.isPending ? (
        <Skeleton className="h-64" />
      ) : roles.isError ? (
        <ErrorNotice>{errorMessage(roles.error)}</ErrorNotice>
      ) : (
        <>
          <Section title="Ingebouwde rollen">
            <Card flush>
              <Table>
                <THead>
                  <Th>Rol</Th>
                  <Th className="hidden md:table-cell">Omschrijving</Th>
                  <Th className="w-px">
                    <span className="sr-only">Acties</span>
                  </Th>
                </THead>
                <TBody>
                  {roles.data.builtin.map((b) => (
                    <Tr key={b.id}>
                      <Td>
                        <div className="text-ink">{roleLabel[b.id]}</div>
                        <div className="text-sm text-faint md:hidden">{builtinRoleDescriptions[b.id as Exclude<Role, 'custom'>]}</div>
                      </Td>
                      <Td className="hidden text-muted md:table-cell">{builtinRoleDescriptions[b.id as Exclude<Role, 'custom'>]}</Td>
                      <Td>
                        <div className="flex justify-end">
                          <Button size="sm" onClick={() => setEditing({ kind: 'view', title: roleLabel[b.id], permissions: b.permissions })}>
                            <Eye size={14} aria-hidden />
                            Bekijken
                          </Button>
                        </div>
                      </Td>
                    </Tr>
                  ))}
                </TBody>
              </Table>
            </Card>
          </Section>
          <Section title="Eigen rollen">
            {roles.data.roles.length === 0 ? (
              <Card>
                <EmptyState
                  icon={<UserCog size={20} />}
                  title="Nog geen eigen rollen"
                  description="Maak bijvoorbeeld een rol voor een teamleider die rapporten ziet en gesprekken toewijst."
                  action={
                    <Button variant="primary" onClick={() => setEditing({ kind: 'new' })}>
                      Maak de eerste rol aan
                    </Button>
                  }
                />
              </Card>
            ) : (
              <Card flush>
                <Table>
                  <THead>
                    <Th>Rol</Th>
                    <Th className="hidden md:table-cell">Rechten</Th>
                    <Th className="hidden md:table-cell">Gebruikers</Th>
                    <Th className="w-px">
                      <span className="sr-only">Acties</span>
                    </Th>
                  </THead>
                  <TBody>
                    {roles.data.roles.map((r) => (
                      <Tr key={r.id}>
                        <Td>
                          <div className="text-ink">{r.name}</div>
                          {r.description && <div className="text-sm text-faint">{r.description}</div>}
                        </Td>
                        <Td className="hidden md:table-cell"><Num value={r.permissions.length} size="s" /></Td>
                        <Td className="hidden md:table-cell"><Num value={r.member_count} size="s" /></Td>
                        <Td>
                          <div className="flex justify-end">
                            <ActionMenu
                              label={`Acties voor ${r.name}`}
                              items={[
                                { label: 'Bewerken', icon: <Pencil size={16} />, onSelect: () => setEditing({ kind: 'edit', role: r }) },
                                {
                                  label: 'Verwijderen',
                                  icon: <Trash2 size={16} />,
                                  danger: true,
                                  separated: true,
                                  onSelect: () => setEditing({ kind: 'delete', role: r }),
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
          </Section>
        </>
      )}
      {(editing?.kind === 'new' || editing?.kind === 'edit') && (
        <RoleDialog role={editing.kind === 'edit' ? editing.role : null} onClose={() => setEditing(null)} />
      )}
      {editing?.kind === 'view' && (
        <Dialog open onOpenChange={(o) => !o && setEditing(null)} title={editing.title} size="lg">
          <div className="flex flex-col gap-4">
            <PermissionPicker value={editing.permissions} readOnly />
            <DialogFooter>
              <Button onClick={() => setEditing(null)}>Sluiten</Button>
            </DialogFooter>
          </div>
        </Dialog>
      )}
      {editing?.kind === 'delete' && <DeleteDialog role={editing.role} onClose={() => setEditing(null)} />}
    </Page>
  )
}

function RoleDialog({ role, onClose }: { role: CustomRole | null; onClose: () => void }) {
  const { data: me } = useSuspenseQuery(meQuery)
  const myRights = me.permissions
  const [name, setName] = useState(role?.name ?? '')
  const [description, setDescription] = useState(role?.description ?? '')
  const [permissions, setPermissions] = useState<Permission[]>(role?.permissions ?? [])
  const queryClient = useQueryClient()
  // Roles that grant access control are the owner's; anyone else can look but not change.
  const locked = role !== null && !canGrant(me.user, myRights, role.permissions)
  const save = useMutation({
    mutationFn: () => {
      const body = { name, description, permissions }
      return role ? api('PATCH', `/roles/${role.id}`, body) : api('POST', '/roles', body)
    },
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: ['roles'] })
      await queryClient.invalidateQueries({ queryKey: ['users'] })
      onClose()
    },
  })
  const submit = (e: SubmitEvent) => {
    e.preventDefault()
    save.mutate()
  }
  const canSet = (p: Permission) => me.user.role === 'owner' || (myRights.includes(p) && !privilegedPermissions.includes(p))

  return (
    <Dialog open onOpenChange={(o) => !o && onClose()} title={role ? (locked ? role.name : 'Rol bewerken') : 'Rol toevoegen'} size="lg">
      <form onSubmit={submit} className="flex flex-col gap-4" noValidate>
        {locked && <p className="text-base text-muted">Deze rol geeft rechten die alleen de eigenaar mag toekennen of wijzigen.</p>}
        {save.isError && !fieldError(save.error, 'name') && !fieldError(save.error, 'permissions') && <ErrorNotice>{errorMessage(save.error)}</ErrorNotice>}
        <Field label="Naam" error={fieldError(save.error, 'name')}>
          {(p) => <Input {...p} autoFocus maxLength={60} disabled={locked} value={name} onChange={(e) => setName(e.target.value)} />}
        </Field>
        <Field label="Omschrijving" help="Optioneel." error={fieldError(save.error, 'description')}>
          {(p) => <Textarea {...p} rows={2} maxLength={300} disabled={locked} value={description} onChange={(e) => setDescription(e.target.value)} />}
        </Field>
        <div>
          <div className="mb-2 text-sm text-ink">Rechten</div>
          {fieldError(save.error, 'permissions') && <p className="mb-2 text-sm text-danger-text">{fieldError(save.error, 'permissions')}</p>}
          <PermissionPicker value={permissions} onChange={setPermissions} readOnly={locked} canSet={canSet} />
          {!locked && me.user.role !== 'owner' && (
            <p className="mt-4 text-sm text-faint">
              Je kunt alleen rechten toekennen die je zelf hebt. Beheer van gebruikers en instellingen kent alleen de eigenaar toe.
            </p>
          )}
        </div>
        <DialogFooter>
          <Button onClick={onClose}>{locked ? 'Sluiten' : 'Annuleren'}</Button>
          {!locked && (
            <Button type="submit" variant="primary" busy={save.isPending} disabled={!name.trim() || permissions.length === 0}>
              Opslaan
            </Button>
          )}
        </DialogFooter>
      </form>
    </Dialog>
  )
}

function DeleteDialog({ role, onClose }: { role: CustomRole; onClose: () => void }) {
  const queryClient = useQueryClient()
  const remove = useMutation({
    mutationFn: () => api('DELETE', `/roles/${role.id}`),
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: ['roles'] })
      onClose()
    },
  })
  return (
    <Dialog open onOpenChange={(o) => !o && onClose()} title="Rol verwijderen">
      <div className="flex flex-col gap-4">
        <p className="text-base text-muted">
          {role.member_count > 0
            ? `${role.name} is nog toegewezen aan ${role.member_count} ${role.member_count === 1 ? 'gebruiker' : 'gebruikers'}. Wijs die eerst een andere rol toe.`
            : `${role.name} wordt verwijderd.`}
        </p>
        {remove.isError && <ErrorNotice>{errorMessage(remove.error)}</ErrorNotice>}
        <DialogFooter>
          <Button onClick={onClose}>Annuleren</Button>
          <Button variant="danger" busy={remove.isPending} disabled={role.member_count > 0} onClick={() => remove.mutate()}>
            Verwijderen
          </Button>
        </DialogFooter>
      </div>
    </Dialog>
  )
}
