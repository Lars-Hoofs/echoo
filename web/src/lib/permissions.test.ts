import { describe, expect, it } from 'vitest'

import {
  allPermissions,
  assignableRoles,
  canAssignBuiltin,
  canGrant,
  canManageUser,
  type Permission,
  permissionGroups,
  requires,
  roleChoiceOf,
  roleChoicePayload,
  type RolesResponse,
  rightsOf,
  togglePermission,
} from './permissions'
import { hasAnyPermission, hasPermission, type Me, roleLabel, roleName, type User } from './session'
import { canOpenSettingsPage, hasWorkspaceSettings, settingsPages } from './nav'
import { parseDomains, ssoErrorMessage } from './sso'

const agentRights: Permission[] = [
  'conversations.read',
  'conversations.write',
  'conversations.assign',
  'conversations.delete',
  'contacts.read',
  'contacts.write',
  'contacts.export',
  'reports.view',
  'kb.write',
]

const roles: RolesResponse = {
  permissions: allPermissions,
  builtin: [
    { id: 'owner', permissions: allPermissions },
    { id: 'admin', permissions: allPermissions },
    { id: 'agent', permissions: agentRights },
    { id: 'readonly', permissions: ['conversations.read', 'contacts.read', 'contacts.export', 'reports.view'] },
  ],
  roles: [
    {
      id: 'r-lead',
      name: 'Teamleider',
      description: '',
      permissions: ['conversations.read', 'conversations.write', 'conversations.assign', 'reports.view'],
      member_count: 1,
      created_at: '',
      updated_at: '',
    },
    {
      id: 'r-people',
      name: 'Personeelszaken',
      description: '',
      permissions: ['users.manage', 'conversations.read'],
      member_count: 0,
      created_at: '',
      updated_at: '',
    },
  ],
}

function user(id: string, role: User['role'], customRoleId: string | null = null): User {
  return {
    id,
    email: `${id}@example.com`,
    name: id,
    role,
    custom_role_id: customRoleId,
    theme: 'system',
    availability: 'online',
    max_open: null,
    mfa_enabled: false,
    deactivated: false,
    can_write_conversations: role !== 'readonly',
    last_login_at: null,
    created_at: '',
  }
}

function me(u: User, permissions: Permission[]): Me {
  return {
    user: u,
    permissions,
    custom_role_name: '',
    csrf_token: '',
    must_change_password: false,
    mfa_enrollment_required: false,
    recovery_codes_remaining: 0,
  }
}

describe('permission list', () => {
  it('has each key once and a label and description for every key', () => {
    expect(new Set(allPermissions).size).toBe(allPermissions.length)
    for (const group of permissionGroups) {
      for (const p of group.permissions) {
        expect(p.label, p.key).not.toBe('')
        expect(p.description, p.key).not.toBe('')
      }
    }
  })

  it('only requires permissions that exist', () => {
    for (const [permission, base] of Object.entries(requires)) {
      expect(allPermissions).toContain(permission)
      expect(allPermissions).toContain(base)
    }
  })
})

describe('hasPermission', () => {
  const lead = me(user('lead', 'custom', 'r-lead'), ['conversations.read', 'reports.view'])

  it('reads the list from /me', () => {
    expect(hasPermission(lead, 'reports.view')).toBe(true)
    expect(hasPermission(lead, 'mailboxes.manage')).toBe(false)
    expect(hasPermission(undefined, 'reports.view')).toBe(false)
  })

  it('has an any-of variant', () => {
    expect(hasAnyPermission(lead, ['mailboxes.manage', 'reports.view'])).toBe(true)
    expect(hasAnyPermission(lead, ['mailboxes.manage', 'audit.view'])).toBe(false)
    expect(hasAnyPermission(lead, [])).toBe(false)
  })
})

describe('togglePermission', () => {
  it('adds what a permission needs', () => {
    expect(togglePermission([], 'conversations.assign', true)).toEqual(['conversations.read', 'conversations.write', 'conversations.assign'])
    expect(togglePermission(['reports.view'], 'kb.manage', true)).toEqual(['reports.view', 'kb.write', 'kb.manage'])
  })

  it('removes what depends on a permission', () => {
    const full: Permission[] = ['conversations.read', 'conversations.write', 'conversations.assign', 'conversations.delete', 'reports.view']
    expect(togglePermission(full, 'conversations.write', false)).toEqual(['conversations.read', 'reports.view'])
    expect(togglePermission(full, 'conversations.read', false)).toEqual(['reports.view'])
    expect(togglePermission(full, 'conversations.assign', false)).toEqual(['conversations.read', 'conversations.write', 'conversations.delete', 'reports.view'])
  })

  it('keeps the canonical order and is idempotent', () => {
    const once = togglePermission(['reports.view'], 'contacts.write', true)
    expect(once).toEqual(['contacts.read', 'contacts.write', 'reports.view'])
    expect(togglePermission(once, 'contacts.write', true)).toEqual(once)
    expect(togglePermission(['reports.view'], 'audit.view', false)).toEqual(['reports.view'])
  })
})

describe('mirroring the server rules', () => {
  const owner = user('owner', 'owner')
  const admin = user('admin', 'admin')
  const agent = user('agent', 'agent')
  const people = user('people', 'custom', 'r-people')
  const lead = user('lead', 'custom', 'r-lead')
  const peopleRights = rightsOf(roles, people)

  it('finds the rights of built-in and custom users', () => {
    expect(rightsOf(roles, agent)).toEqual(agentRights)
    expect(rightsOf(roles, lead)).toContain('conversations.assign')
    expect(rightsOf(roles, user('x', 'custom', 'gone'))).toEqual([])
  })

  it('lets the owner grant anything and others only what they hold and is not privileged', () => {
    expect(canGrant(owner, allPermissions, ['users.manage'])).toBe(true)
    expect(canGrant(admin, allPermissions, ['reports.view'])).toBe(true)
    expect(canGrant(admin, allPermissions, ['users.manage'])).toBe(false)
    expect(canGrant(admin, allPermissions, ['settings.manage'])).toBe(false)
    expect(canGrant(people, peopleRights, ['conversations.read'])).toBe(true)
    expect(canGrant(people, peopleRights, ['reports.view'])).toBe(false)
    expect(canGrant(agent, agentRights, ['reports.view'])).toBe(false)
  })

  it('keeps every permission that can widen access owner-only, like policy.Privileged', () => {
    for (const p of ['teams.manage', 'mailboxes.manage', 'contacts.erase', 'contacts.import', 'webhooks.manage', 'automation.manage'] as const) {
      expect(canGrant(admin, allPermissions, [p])).toBe(false)
      expect(canGrant(owner, allPermissions, [p])).toBe(true)
    }
  })

  it('assigns built-in roles the way policy.CanAssignRole does', () => {
    expect(canAssignBuiltin(owner, allPermissions, 'admin', roles)).toBe(true)
    expect(canAssignBuiltin(owner, allPermissions, 'owner', roles)).toBe(false)
    expect(canAssignBuiltin(admin, allPermissions, 'admin', roles)).toBe(false)
    expect(canAssignBuiltin(admin, allPermissions, 'agent', roles)).toBe(true)
    expect(canAssignBuiltin(people, peopleRights, 'readonly', roles)).toBe(false)
    expect(canAssignBuiltin(agent, agentRights, 'readonly', roles)).toBe(false)
  })

  it('manages users the way policy.CanManageUser does', () => {
    expect(canManageUser(owner, allPermissions, admin, roles)).toBe(true)
    expect(canManageUser(owner, allPermissions, owner, roles)).toBe(false)
    expect(canManageUser(admin, allPermissions, agent, roles)).toBe(true)
    expect(canManageUser(admin, allPermissions, user('admin2', 'admin'), roles)).toBe(false)
    expect(canManageUser(admin, allPermissions, owner, roles)).toBe(false)
    expect(canManageUser(admin, allPermissions, people, roles)).toBe(false)
    expect(canManageUser(owner, allPermissions, people, roles)).toBe(true)
    expect(canManageUser(agent, agentRights, lead, roles)).toBe(false)
  })

  it('offers the roles an actor may hand out', () => {
    const ownerOptions = assignableRoles(owner, allPermissions, roles, roleLabel).map((o) => o.value)
    expect(ownerOptions).toEqual(['admin', 'agent', 'readonly', 'custom:r-lead', 'custom:r-people'])
    const adminOptions = assignableRoles(admin, allPermissions, roles, roleLabel).map((o) => o.value)
    expect(adminOptions).toEqual(['agent', 'readonly', 'custom:r-lead'])
    expect(assignableRoles(agent, agentRights, roles, roleLabel)).toEqual([])
  })
})

describe('role choices', () => {
  it('round-trips between the form value and the request', () => {
    expect(roleChoiceOf(user('a', 'agent'))).toBe('agent')
    expect(roleChoiceOf(user('a', 'custom', 'r-lead'))).toBe('custom:r-lead')
    expect(roleChoicePayload('agent')).toEqual({ role: 'agent' })
    expect(roleChoicePayload('custom:r-lead')).toEqual({ custom_role_id: 'r-lead' })
  })

  it('names a custom role by its own name', () => {
    expect(roleName('custom', 'Teamleider')).toBe('Teamleider')
    expect(roleName('custom')).toBe('Eigen rol')
    expect(roleName('agent', 'ignored')).toBe('Agent')
  })
})

describe('settings pages', () => {
  const lead = me(user('lead', 'custom', 'r-lead'), ['conversations.read', 'reports.view', 'automation.manage'])
  const owner = me(user('owner', 'owner'), allPermissions)

  it('opens a page for whoever holds its permission', () => {
    const rules = settingsPages.find((p) => p.to === '/instellingen/regels')
    const mailboxes = settingsPages.find((p) => p.to === '/instellingen/mailboxen')
    expect(rules && canOpenSettingsPage(lead, rules.access)).toBe(true)
    expect(mailboxes && canOpenSettingsPage(lead, mailboxes.access)).toBe(false)
  })

  it('keeps SSO to the owner', () => {
    const sso = settingsPages.find((p) => p.to === '/instellingen/inloggen')
    expect(sso && canOpenSettingsPage(owner, sso.access)).toBe(true)
    expect(sso && canOpenSettingsPage(me(user('admin', 'admin'), allPermissions), sso.access)).toBe(false)
  })

  it('shows the workspace section only when a page is open', () => {
    expect(hasWorkspaceSettings(lead)).toBe(true)
    // Agents may mark spam, so they manage the blocked senders of their mailboxes.
    expect(hasWorkspaceSettings(me(user('a', 'agent'), agentRights))).toBe(true)
    expect(hasWorkspaceSettings(me(user('a', 'agent'), ['conversations.read', 'conversations.write']))).toBe(false)
    expect(hasWorkspaceSettings(undefined)).toBe(false)
  })

  it('lists every page once', () => {
    const paths = settingsPages.map((p) => p.to)
    expect(new Set(paths).size).toBe(paths.length)
  })
})

describe('sso helpers', () => {
  it('parses domains from a text box', () => {
    expect(parseDomains('Voorbeeld.nl\n@bedrijf.nl, voorbeeld.nl;  extra.org ')).toEqual(['voorbeeld.nl', 'bedrijf.nl', 'extra.org'])
    expect(parseDomains('  \n')).toEqual([])
  })

  it('explains known reasons and falls back for the rest', () => {
    expect(ssoErrorMessage('no_account')).toContain('geen account')
    expect(ssoErrorMessage('deactivated')).toContain('gedeactiveerd')
    expect(ssoErrorMessage('something_new')).toContain('niet gelukt')
  })
})
