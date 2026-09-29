import { queryOptions } from '@tanstack/react-query'

import { api } from './api'
import type { Role, User } from './session'

// Mirrors internal/policy/permission.go. The server decides; this list drives what the UI
// shows. A Go test checks that every key of the backend is named here.
export type Permission =
  | 'conversations.read'
  | 'conversations.write'
  | 'conversations.assign'
  | 'conversations.delete'
  | 'contacts.read'
  | 'contacts.write'
  | 'contacts.export'
  | 'contacts.import'
  | 'contacts.erase'
  | 'contacts.moderate'
  | 'campaigns.manage'
  | 'reports.view'
  | 'reports.view_all_agents'
  | 'kb.write'
  | 'kb.manage'
  | 'labels.manage'
  | 'templates.manage_shared'
  | 'automation.manage'
  | 'sla.manage'
  | 'mailboxes.manage'
  | 'users.manage'
  | 'teams.manage'
  | 'settings.manage'
  | 'audit.view'
  | 'api_tokens.manage_all'
  | 'webhooks.manage'

export interface PermissionInfo {
  key: Permission
  label: string
  description: string
}

export interface PermissionGroup {
  id: string
  label: string
  permissions: PermissionInfo[]
}

export const permissionGroups: PermissionGroup[] = [
  {
    id: 'conversations',
    label: 'Gesprekken',
    permissions: [
      { key: 'conversations.read', label: 'Gesprekken zien', description: 'Gesprekken lezen in de mailboxen van de eigen teams.' },
      { key: 'conversations.write', label: 'Beantwoorden', description: 'Antwoorden en notities schrijven, status, prioriteit en labels wijzigen.' },
      { key: 'conversations.assign', label: 'Toewijzen', description: 'Gesprekken aan een collega of team toewijzen.' },
      { key: 'conversations.delete', label: 'Als spam markeren', description: 'Gesprekken als spam markeren.' },
    ],
  },
  {
    id: 'contacts',
    label: 'Contacten',
    permissions: [
      { key: 'contacts.read', label: 'Contacten zien', description: 'Contacten en organisaties bekijken.' },
      { key: 'contacts.write', label: 'Contacten bewerken', description: 'Contacten, organisaties, notities en segmenten aanmaken en wijzigen.' },
      { key: 'contacts.export', label: 'Exporteren', description: 'Contactlijsten als CSV downloaden.' },
      { key: 'contacts.import', label: 'Importeren', description: 'Contacten uit een CSV-bestand laden.' },
      { key: 'contacts.erase', label: 'AVG-verzoeken', description: 'Alle gegevens van een contact exporteren of wissen.' },
      { key: 'contacts.moderate', label: 'Notities en segmenten van collega’s', description: 'Notities en segmenten van anderen wijzigen of verwijderen.' },
      { key: 'campaigns.manage', label: 'Campagnes', description: 'Campagnes naar contacten aanmaken en beheren.' },
    ],
  },
  {
    id: 'reports',
    label: 'Rapportage',
    permissions: [
      { key: 'reports.view', label: 'Rapporten bekijken', description: 'Cijfers over gesprekken in de eigen mailboxen.' },
      { key: 'reports.view_all_agents', label: 'Alle medewerkers', description: 'Rapporten per medewerker van anderen bekijken.' },
    ],
  },
  {
    id: 'kb',
    label: 'Kennisbank',
    permissions: [
      { key: 'kb.write', label: 'Artikelen schrijven', description: 'Artikelen aanmaken en bewerken en concepten zien.' },
      { key: 'kb.manage', label: 'Kennisbank beheren', description: 'Categorieën en instellingen van het portaal.' },
    ],
  },
  {
    id: 'admin',
    label: 'Beheer',
    permissions: [
      { key: 'labels.manage', label: 'Labels', description: 'Labels aanmaken, wijzigen en verwijderen.' },
      {
        key: 'templates.manage_shared',
        label: 'Gedeelde standaardantwoorden en weergaven',
        description: 'Standaardantwoorden, macro’s en weergaven voor het hele team beheren.',
      },
      { key: 'automation.manage', label: 'Regels en toewijzing', description: 'Automatiseringsregels en automatische toewijzing instellen.' },
      { key: 'sla.manage', label: 'SLA', description: 'SLA-beleid en kantooruren beheren.' },
      { key: 'mailboxes.manage', label: 'Mailboxen', description: 'Mailboxen koppelen en hun handtekening, toegang en tevredenheidsonderzoek instellen.' },
      { key: 'teams.manage', label: 'Teams', description: 'Teams en hun leden beheren.' },
      { key: 'users.manage', label: 'Gebruikers en rollen', description: 'Gebruikers uitnodigen en beheren en rollen maken. Alleen de eigenaar kent dit toe.' },
      {
        key: 'settings.manage',
        label: 'Werkruimte-instellingen',
        description: 'Beveiliging, e-mail, aangepaste velden, importen en taken. Alleen de eigenaar kent dit toe.',
      },
      { key: 'audit.view', label: 'Auditlog', description: 'De auditlog bekijken en exporteren.' },
      { key: 'api_tokens.manage_all', label: 'API-tokens van iedereen', description: 'Alle API-tokens zien en intrekken.' },
      { key: 'webhooks.manage', label: 'Webhooks', description: 'Webhooks aanmaken en beheren.' },
    ],
  },
]

export const allPermissions: Permission[] = permissionGroups.flatMap((g) => g.permissions.map((p) => p.key))

// What each permission is useless without; the server refuses roles that break this.
export const requires: Partial<Record<Permission, Permission>> = {
  'conversations.write': 'conversations.read',
  'conversations.assign': 'conversations.write',
  'conversations.delete': 'conversations.write',
  'contacts.write': 'contacts.read',
  'contacts.export': 'contacts.read',
  'contacts.import': 'contacts.read',
  'contacts.erase': 'contacts.read',
  'contacts.moderate': 'contacts.write',
  'campaigns.manage': 'contacts.read',
  'reports.view_all_agents': 'reports.view',
  'kb.manage': 'kb.write',
}

// Permissions that control who may do what; only the owner hands them out.
export const privilegedPermissions: Permission[] = [
  'users.manage',
  'settings.manage',
  'teams.manage',
  'mailboxes.manage',
  'contacts.erase',
  'contacts.import',
  'webhooks.manage',
  'automation.manage',
]

// toggle adds or removes a permission and keeps the set consistent: adding one adds what it
// requires, removing one removes what depends on it.
export function togglePermission(set: readonly Permission[], permission: Permission, on: boolean): Permission[] {
  const next = new Set(set)
  if (on) {
    for (let p: Permission | undefined = permission; p; p = requires[p]) next.add(p)
  } else {
    const drop = (target: Permission) => {
      next.delete(target)
      for (const [dependent, base] of Object.entries(requires)) {
        if (base === target && next.has(dependent as Permission)) drop(dependent as Permission)
      }
    }
    drop(permission)
  }
  return allPermissions.filter((p) => next.has(p))
}

export interface RolesResponse {
  permissions: Permission[]
  builtin: { id: Role; permissions: Permission[] }[]
  roles: CustomRole[]
}

export interface CustomRole {
  id: string
  name: string
  description: string
  permissions: Permission[]
  member_count: number
  created_at: string
  updated_at: string
}

export const rolesQuery = queryOptions({ queryKey: ['roles'], queryFn: () => api<RolesResponse>('GET', '/roles') })

// Shown next to the built-in roles; they cannot be changed.
export const builtinRoleDescriptions: Record<Exclude<Role, 'custom'>, string> = {
  owner: 'Volledige toegang. Er is precies één eigenaar; die kan niet worden weggehaald.',
  admin: 'Volledige toegang tot alle mailboxen en instellingen. Beheert geen andere beheerders.',
  agent: 'Beantwoordt en beheert gesprekken in de mailboxen van de eigen teams.',
  readonly: 'Ziet gesprekken en contacten van de eigen teams, zonder iets te wijzigen.',
}

// rightsOf lists what a user may do, from the role list the server sent.
export function rightsOf(roles: RolesResponse, user: Pick<User, 'role' | 'custom_role_id'>): Permission[] {
  if (user.role === 'custom') return roles.roles.find((r) => r.id === user.custom_role_id)?.permissions ?? []
  return roles.builtin.find((r) => r.id === user.role)?.permissions ?? []
}

const subset = (set: readonly Permission[], of: readonly Permission[]) => set.every((p) => of.includes(p))
const isPrivileged = (set: readonly Permission[]) => privilegedPermissions.some((p) => set.includes(p))

// The next three mirror policy.CanGrant, CanAssignRole and CanManageUser.
export function canGrant(actor: Pick<User, 'role'>, actorRights: readonly Permission[], perms: readonly Permission[]): boolean {
  if (!actorRights.includes('users.manage')) return false
  if (actor.role === 'owner') return true
  return !isPrivileged(perms) && subset(perms, actorRights)
}

export function canAssignBuiltin(actor: Pick<User, 'role'>, actorRights: readonly Permission[], role: Role, roles: RolesResponse): boolean {
  if (role === 'owner' || role === 'custom' || !actorRights.includes('users.manage')) return false
  if (actor.role === 'owner') return true
  return role !== 'admin' && subset(rightsOf(roles, { role, custom_role_id: null }), actorRights)
}

export function canManageUser(actor: User, actorRights: readonly Permission[], target: User, roles: RolesResponse): boolean {
  if (actor.id === target.id || target.role === 'owner' || !actorRights.includes('users.manage')) return false
  if (actor.role === 'owner') return true
  const rights = rightsOf(roles, target)
  return target.role !== 'admin' && !isPrivileged(rights) && subset(rights, actorRights)
}

// A role picked in a form travels as one string: a built-in role name or `custom:<id>`.
export const customChoicePrefix = 'custom:'

export function roleChoiceOf(user: Pick<User, 'role' | 'custom_role_id'>): string {
  return user.role === 'custom' && user.custom_role_id ? `${customChoicePrefix}${user.custom_role_id}` : user.role
}

export function roleChoicePayload(choice: string): { role: string } | { custom_role_id: string } {
  return choice.startsWith(customChoicePrefix) ? { custom_role_id: choice.slice(customChoicePrefix.length) } : { role: choice }
}

export interface RoleOption {
  value: string
  label: string
  custom: boolean
}

// assignableRoles lists what the actor may give to somebody, built-in roles first.
export function assignableRoles(
  actor: Pick<User, 'role'>,
  actorRights: readonly Permission[],
  roles: RolesResponse,
  labels: Record<Role, string>,
): RoleOption[] {
  const builtin = roles.builtin
    .filter((b) => canAssignBuiltin(actor, actorRights, b.id, roles))
    .map((b) => ({ value: b.id, label: labels[b.id], custom: false }))
  const custom = roles.roles
    .filter((r) => canGrant(actor, actorRights, r.permissions))
    .map((r) => ({ value: `${customChoicePrefix}${r.id}`, label: r.name, custom: true }))
  return [...builtin, ...custom]
}
