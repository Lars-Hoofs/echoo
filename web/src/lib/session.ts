import { queryOptions } from '@tanstack/react-query'

import { api } from './api'
import type { Availability } from './automation'
import type { Permission } from './permissions'

export type Role = 'owner' | 'admin' | 'agent' | 'readonly' | 'custom'
export type Theme = 'system' | 'light' | 'dark'

export interface User {
  id: string
  email: string
  name: string
  role: Role
  // Set when the role is 'custom'.
  custom_role_id: string | null
  theme: Theme
  availability: Availability
  // Capacity for auto-assignment; null means no limit.
  max_open: number | null
  mfa_enabled: boolean
  deactivated: boolean
  can_write_conversations: boolean
  // Set while the user has not accepted the invitation yet.
  invited_at?: string | null
  last_login_at: string | null
  created_at: string
}

export interface Me {
  user: User
  // What the user may do, from their built-in or custom role.
  permissions: Permission[]
  // Name of the custom role, empty for the built-in roles.
  custom_role_name: string
  csrf_token: string
  must_change_password: boolean
  mfa_enrollment_required: boolean
  recovery_codes_remaining: number
}

export const meQuery = queryOptions({
  queryKey: ['me'],
  queryFn: () => api<Me>('GET', '/me'),
  staleTime: 60_000,
  retry: false,
})

export function hasPermission(me: Me | undefined, permission: Permission): boolean {
  return me?.permissions.includes(permission) ?? false
}

export function hasAnyPermission(me: Me | undefined, permissions: readonly Permission[]): boolean {
  return permissions.some((p) => hasPermission(me, p))
}

export const roleLabel: Record<Role, string> = {
  owner: 'Eigenaar',
  admin: 'Beheerder',
  agent: 'Agent',
  readonly: 'Alleen lezen',
  custom: 'Eigen rol',
}

// roleName is what to show for a user's role: the custom role's own name when it has one.
export const roleName = (role: Role, customName?: string) => (role === 'custom' && customName ? customName : roleLabel[role])

export function applyTheme(theme: Theme) {
  if (theme === 'system') delete document.documentElement.dataset.theme
  else document.documentElement.dataset.theme = theme
}
