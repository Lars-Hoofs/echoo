import { useMutation, useQuery, useQueryClient, useSuspenseQuery } from '@tanstack/react-query'
import { Crown, Eye, KeyRound, Mail, Plus, ShieldHalf, ShieldOff, UserCheck, UserCog, UserRound, UserX } from 'lucide-react'
import type { ReactNode } from 'react'
import { type SubmitEvent, useState } from 'react'

import { ActionMenu } from '../../components/ActionMenu'
import { Avatar } from '../../components/Avatar'
import { Dialog, DialogFooter } from '../../components/Dialog'
import { Badge, Button, Card, ErrorNotice, Field, Input, Page, PageHeader, Select, Skeleton, Table, TBody, Td, Th, THead, Tr } from '../../components/ui'
import { api } from '../../lib/api'
import { errorMessage, fieldError } from '../../lib/errors'
import { formatDateTime } from '../../lib/format'
import { assignableRoles, canManageUser, type RoleOption, roleChoiceOf, roleChoicePayload, rolesQuery } from '../../lib/permissions'
import { meQuery, type Role, roleLabel, roleName, type User } from '../../lib/session'

const usersQuery = { queryKey: ['users'], queryFn: () => api<{ users: User[] }>('GET', '/users') }

type Action = { kind: 'role' | 'reset-password' | 'reset-mfa' | 'deactivate' | 'reactivate' | 'resend-invitation' | 'revoke-invitation'; user: User }

interface Team {
  id: string
  name: string
}

interface SystemMail {
  available: boolean
}

export function UsersPage() {
  const { data: me } = useSuspenseQuery(meQuery)
  const users = useQuery(usersQuery)
  const roles = useQuery(rolesQuery)
  const options = roles.data ? assignableRoles(me.user, me.permissions, roles.data, roleLabel) : []
  const canManage = (target: User) => roles.data !== undefined && canManageUser(me.user, me.permissions, target, roles.data)
  const nameOf = (u: User) => roleName(u.role, roles.data?.roles.find((r) => r.id === u.custom_role_id)?.name)
  const systemMail = useQuery({ queryKey: ['system-mail'], queryFn: () => api<SystemMail>('GET', '/settings/system-mail') })
  const [adding, setAdding] = useState(false)
  const [action, setAction] = useState<Action | null>(null)

  return (
    <Page>
      <PageHeader breadcrumb="Werkruimte"
        title="Gebruikers"
        description="Iedereen met toegang tot deze werkruimte."
        actions={
          <Button variant="primary" onClick={() => setAdding(true)}>
            <Plus size={16} aria-hidden />
            Gebruiker toevoegen
          </Button>
        }
      />
      {users.isPending ? (
        <Skeleton className="h-40" />
      ) : users.isError ? (
        <ErrorNotice>{errorMessage(users.error)}</ErrorNotice>
      ) : (
        <Card flush>
          <Table>
            <THead>
              <Th>Naam</Th>
              <Th className="hidden md:table-cell">Rol</Th>
              <Th className="hidden md:table-cell">2FA</Th>
              <Th className="hidden md:table-cell">Laatst ingelogd</Th>
              <Th className="w-px">
                <span className="sr-only">Acties</span>
              </Th>
            </THead>
            <TBody>
              {users.data.users.map((u) => (
                <Tr key={u.id}>
                  <Td>
                    <div className="flex items-center gap-3">
                      <Avatar name={u.name} />
                      <div className="max-w-40 min-w-0 sm:max-w-xs md:max-w-none">
                        <div className={`truncate ${u.deactivated ? 'text-faint' : 'text-ink'}`}>
                          {u.name}
                          {u.invited_at && (
                            <span className="ml-2 align-middle">
                              <Badge icon={<Mail size={12} />}>Uitgenodigd</Badge>
                            </span>
                          )}
                          {u.deactivated && !u.invited_at && (
                            <span className="ml-2 align-middle">
                              <Badge icon={<UserX size={12} />}>Gedeactiveerd</Badge>
                            </span>
                          )}
                        </div>
                        <div className="truncate text-sm text-faint">{u.email}</div>
                        <div className="mt-1 flex flex-wrap gap-1 md:hidden">
                          <RoleBadge role={u.role} name={nameOf(u)} />
                          <MfaBadge enabled={u.mfa_enabled} labeled />
                        </div>
                      </div>
                    </div>
                  </Td>
                  <Td className="hidden md:table-cell">
                    <RoleBadge role={u.role} name={nameOf(u)} />
                  </Td>
                  <Td className="hidden md:table-cell">
                    <MfaBadge enabled={u.mfa_enabled} />
                  </Td>
                  <Td className="hidden text-muted tabular-nums md:table-cell">{formatDateTime(u.last_login_at)}</Td>
                  <Td className="text-right">
                    {canManage(u) && <RowMenu user={u} mailAvailable={systemMail.data?.available ?? false} onAction={setAction} />}
                  </Td>
                </Tr>
              ))}
            </TBody>
          </Table>
        </Card>
      )}
      <AddUserDialog open={adding} onClose={() => setAdding(false)} roles={options} mailAvailable={systemMail.data?.available ?? false} />
      {action && <ActionDialog action={action} roles={options} onClose={() => setAction(null)} />}
    </Page>
  )
}

const roleIcons: Record<Role, ReactNode> = {
  owner: <Crown size={12} />,
  admin: <ShieldHalf size={12} />,
  agent: <UserRound size={12} />,
  readonly: <Eye size={12} />,
  custom: <UserCog size={12} />,
}

function RoleBadge({ role, name }: { role: Role; name: string }) {
  return (
    <Badge icon={roleIcons[role]}>
      {name}
    </Badge>
  )
}

// labeled adds the column name back for layouts where the 2FA column is hidden.
function MfaBadge({ enabled, labeled = false }: { enabled: boolean; labeled?: boolean }) {
  return enabled ? (
    <Badge dot>
      {labeled ? '2FA aan' : 'Aan'}
    </Badge>
  ) : (
    <Badge icon={<ShieldOff size={12} />}>{labeled ? '2FA uit' : 'Uit'}</Badge>
  )
}

function RowMenu({ user, mailAvailable, onAction }: { user: User; mailAvailable: boolean; onAction: (a: Action) => void }) {
  if (user.invited_at) {
    return (
      <ActionMenu
        label={`Acties voor ${user.name}`}
        items={[
          ...(mailAvailable
            ? [{ label: 'Uitnodiging opnieuw versturen', icon: <Mail size={16} />, onSelect: () => onAction({ kind: 'resend-invitation', user }) }]
            : []),
          {
            label: 'Uitnodiging intrekken',
            icon: <UserX size={16} />,
            danger: true,
            separated: mailAvailable,
            onSelect: () => onAction({ kind: 'revoke-invitation', user }),
          },
        ]}
      />
    )
  }
  return (
    <ActionMenu
      label={`Acties voor ${user.name}`}
      items={[
        { label: 'Rol wijzigen', icon: <UserRound size={16} />, onSelect: () => onAction({ kind: 'role', user }) },
        { label: 'Wachtwoord resetten', icon: <KeyRound size={16} />, onSelect: () => onAction({ kind: 'reset-password', user }) },
        ...(user.mfa_enabled
          ? [{ label: 'Tweestapsverificatie resetten', icon: <ShieldOff size={16} />, onSelect: () => onAction({ kind: 'reset-mfa', user }) }]
          : []),
        user.deactivated
          ? { label: 'Heractiveren', icon: <UserCheck size={16} />, separated: true, onSelect: () => onAction({ kind: 'reactivate', user }) }
          : { label: 'Deactiveren', icon: <UserX size={16} />, danger: true, separated: true, onSelect: () => onAction({ kind: 'deactivate', user }) },
      ]}
    />
  )
}

function RoleSelect({ roles, value, onChange, id }: { roles: RoleOption[]; value: string; onChange: (r: string) => void; id: string }) {
  return (
    <Select id={id} value={value} onChange={(e) => onChange(e.target.value)}>
      {roles
        .filter((r) => !r.custom)
        .map((r) => (
          <option key={r.value} value={r.value}>
            {r.label}
          </option>
        ))}
      {roles.some((r) => r.custom) && (
        <optgroup label="Eigen rollen">
          {roles
            .filter((r) => r.custom)
            .map((r) => (
              <option key={r.value} value={r.value}>
                {r.label}
              </option>
            ))}
        </optgroup>
      )}
    </Select>
  )
}

function TemporaryPassword({ email, password, onClose }: { email: string; password: string; onClose: () => void }) {
  return (
    <div className="flex flex-col gap-4">
      <p className="text-base text-muted">
        Geef dit tijdelijke wachtwoord via een veilig kanaal door aan {email}. Het wordt niet opnieuw getoond en moet bij de eerste keer inloggen worden
        gewijzigd.
      </p>
      <div className="rounded-md border border-line bg-subtle px-3 py-2 font-mono text-read select-all">{password}</div>
      <DialogFooter>
        <Button variant="primary" onClick={onClose}>
          Klaar
        </Button>
      </DialogFooter>
    </div>
  )
}

function AddUserDialog({ open, onClose, roles, mailAvailable }: { open: boolean; onClose: () => void; roles: RoleOption[]; mailAvailable: boolean }) {
  const [email, setEmail] = useState('')
  const [name, setName] = useState('')
  const [picked, setPicked] = useState('agent')
  const [teamIds, setTeamIds] = useState<Set<string>>(new Set())
  // Invitations need system mail; without it the temporary password is the only way in.
  const [wantsTemporary, setWantsTemporary] = useState(false)
  const invite = mailAvailable && !wantsTemporary
  // The first role the actor may hand out is the default when 'agent' is not among them.
  const role = roles.some((r) => r.value === picked) ? picked : (roles[0]?.value ?? picked)
  const queryClient = useQueryClient()
  const teams = useQuery({ queryKey: ['teams'], queryFn: () => api<{ teams: Team[] }>('GET', '/teams'), enabled: open })
  const create = useMutation({
    mutationFn: (): Promise<{ user: User; temporary_password?: string }> =>
      invite
        ? api('POST', '/invitations', { email, name, ...roleChoicePayload(role), team_ids: [...teamIds] })
        : api('POST', '/users', { email, name, ...roleChoicePayload(role) }),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ['users'] }),
  })
  const close = () => {
    setEmail('')
    setName('')
    setPicked('agent')
    setTeamIds(new Set())
    setWantsTemporary(false)
    create.reset()
    onClose()
  }
  const submit = (e: SubmitEvent) => {
    e.preventDefault()
    create.mutate()
  }
  const toggleTeam = (id: string) => {
    const next = new Set(teamIds)
    if (!next.delete(id)) next.add(id)
    setTeamIds(next)
  }
  const result = create.data
  const temporary = result?.temporary_password

  return (
    <Dialog
      open={open}
      onOpenChange={(o) => !o && close()}
      title={result ? (temporary ? 'Gebruiker toegevoegd' : 'Uitnodiging verstuurd') : invite ? 'Gebruiker uitnodigen' : 'Gebruiker toevoegen'}
    >
      {result ? (
        temporary ? (
          <TemporaryPassword email={result.user.email} password={temporary} onClose={close} />
        ) : (
          <div className="flex flex-col gap-4">
            <p className="text-base text-muted">
              {result.user.email} krijgt een e-mail met een link om een wachtwoord te kiezen. De link is 72 uur geldig. Je kunt de uitnodiging in de lijst
              opnieuw versturen of intrekken.
            </p>
            <DialogFooter>
              <Button variant="primary" onClick={close}>
                Klaar
              </Button>
            </DialogFooter>
          </div>
        )
      ) : (
        <form onSubmit={submit} className="flex flex-col gap-4" noValidate>
          {create.isError && !Object.keys(fieldErrors(create.error)).length && <ErrorNotice>{errorMessage(create.error)}</ErrorNotice>}
          {!mailAvailable && (
            <p className="text-sm text-muted">
              Uitnodigen per e-mail staat uit: stel ECHOO_SYSTEM_MAILBOX of ECHOO_SMTP_URL in. Tot die tijd krijgt een nieuwe gebruiker een tijdelijk wachtwoord
              dat je zelf doorgeeft.
            </p>
          )}
          <Field label="Naam" error={fieldError(create.error, 'name')}>
            {(p) => <Input {...p} autoFocus value={name} onChange={(e) => setName(e.target.value)} />}
          </Field>
          <Field label="E-mailadres" error={fieldError(create.error, 'email')}>
            {(p) => <Input {...p} type="email" value={email} onChange={(e) => setEmail(e.target.value)} />}
          </Field>
          <Field label="Rol" error={fieldError(create.error, 'role') ?? fieldError(create.error, 'custom_role_id')}>
            {(p) => <RoleSelect id={p.id} roles={roles} value={role} onChange={setPicked} />}
          </Field>
          {invite && (teams.data?.teams.length ?? 0) > 0 && (
            <fieldset className="flex flex-col gap-1.5">
              <legend className="mb-1.5 text-sm text-ink">Teams</legend>
              {teams.data?.teams.map((t) => (
                <label key={t.id} className="flex cursor-pointer items-center gap-3 text-base text-ink">
                  <input type="checkbox" checked={teamIds.has(t.id)} onChange={() => toggleTeam(t.id)} className="size-4 shrink-0 accent-(--accent)" />
                  {t.name}
                </label>
              ))}
              {fieldError(create.error, 'team_ids') && <p className="text-sm text-danger-text">{fieldError(create.error, 'team_ids')}</p>}
            </fieldset>
          )}
          <DialogFooter>
            {mailAvailable && (
              <Button variant="ghost" className="mr-auto" onClick={() => setWantsTemporary(!wantsTemporary)}>
                {wantsTemporary ? 'Per e-mail uitnodigen' : 'Tijdelijk wachtwoord tonen'}
              </Button>
            )}
            <Button onClick={close}>Annuleren</Button>
            <Button type="submit" variant="primary" busy={create.isPending} disabled={!email || !name}>
              {invite ? 'Uitnodigen' : 'Toevoegen'}
            </Button>
          </DialogFooter>
        </form>
      )}
    </Dialog>
  )
}

function fieldErrors(err: unknown): Record<string, string> {
  return err && typeof err === 'object' && 'fields' in err ? (err as { fields: Record<string, string> }).fields : {}
}

const actionCopy: Record<Action['kind'], { title: string; body: (u: User) => string; confirm: string; danger?: boolean }> = {
  role: { title: 'Rol wijzigen', body: (u) => `Kies een nieuwe rol voor ${u.name}. De wijziging geldt direct.`, confirm: 'Opslaan' },
  'reset-password': {
    title: 'Wachtwoord resetten',
    body: (u) => `${u.name} wordt overal afgemeld en krijgt een tijdelijk wachtwoord.`,
    confirm: 'Resetten',
  },
  'reset-mfa': {
    title: 'Tweestapsverificatie resetten',
    body: (u) => `${u.name} wordt overal afgemeld en kan daarna inloggen met alleen het wachtwoord.`,
    confirm: 'Resetten',
    danger: true,
  },
  deactivate: {
    title: 'Gebruiker deactiveren',
    body: (u) => `${u.name} wordt direct afgemeld en kan niet meer inloggen. Gesprekken en notities blijven bewaard.`,
    confirm: 'Deactiveren',
    danger: true,
  },
  reactivate: { title: 'Gebruiker heractiveren', body: (u) => `${u.name} kan weer inloggen.`, confirm: 'Heractiveren' },
  'resend-invitation': {
    title: 'Uitnodiging opnieuw versturen',
    body: (u) => `${u.email} krijgt een nieuwe e-mail. De vorige link werkt daarna niet meer.`,
    confirm: 'Versturen',
  },
  'revoke-invitation': {
    title: 'Uitnodiging intrekken',
    body: (u) => `${u.email} kan de uitnodiging niet meer accepteren. De gebruiker wordt verwijderd.`,
    confirm: 'Intrekken',
    danger: true,
  },
}

function ActionDialog({ action, roles, onClose }: { action: Action; roles: RoleOption[]; onClose: () => void }) {
  const { user, kind } = action
  const current = roleChoiceOf(user)
  const [role, setRole] = useState(roles.some((r) => r.value === current) ? current : (roles[0]?.value ?? 'agent'))
  const queryClient = useQueryClient()
  const run = useMutation({
    mutationFn: async (): Promise<{ temporary_password?: string } | undefined> => {
      switch (kind) {
        case 'role':
          return api('PATCH', `/users/${user.id}`, roleChoicePayload(role))
        case 'reset-password':
          return api('POST', `/users/${user.id}/reset-password`)
        case 'reset-mfa':
          return api('POST', `/users/${user.id}/reset-mfa`)
        case 'resend-invitation':
          return api('POST', `/users/${user.id}/invitation/resend`)
        case 'revoke-invitation':
          return api('DELETE', `/users/${user.id}/invitation`)
        case 'deactivate':
        case 'reactivate':
          return api('PATCH', `/users/${user.id}`, { deactivated: kind === 'deactivate' })
      }
    },
    onSuccess: async (r) => {
      await queryClient.invalidateQueries({ queryKey: ['users'] })
      if (!r?.temporary_password) onClose()
    },
  })
  const copy = actionCopy[kind]
  const temp = run.data?.temporary_password

  return (
    <Dialog open onOpenChange={(o) => !o && onClose()} title={copy.title}>
      {temp ? (
        <TemporaryPassword email={user.email} password={temp} onClose={onClose} />
      ) : (
        <div className="flex flex-col gap-4">
          <p className="text-base text-muted">{copy.body(user)}</p>
          {kind === 'role' && <Field label="Rol">{(p) => <RoleSelect id={p.id} roles={roles} value={role} onChange={setRole} />}</Field>}
          {run.isError && <ErrorNotice>{errorMessage(run.error)}</ErrorNotice>}
          <DialogFooter>
            <Button onClick={onClose}>Annuleren</Button>
            <Button variant={copy.danger ? 'danger' : 'primary'} busy={run.isPending} onClick={() => run.mutate()}>
              {copy.confirm}
            </Button>
          </DialogFooter>
        </div>
      )}
    </Dialog>
  )
}
