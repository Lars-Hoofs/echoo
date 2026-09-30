import { describe, expect, it } from 'vitest'

import { visibleSectionPages } from './nav'
import type { Permission } from './permissions'
import type { Me } from './session'

function meWith(permissions: Permission[]): Me {
  return {
    user: {
      id: 'u1',
      email: 'u1@example.com',
      name: 'u1',
      role: 'agent',
      custom_role_id: null,
      theme: 'system',
      availability: 'online',
      max_open: null,
      mfa_enabled: false,
      deactivated: false,
      can_write_conversations: true,
      last_login_at: null,
      created_at: '',
    },
    permissions,
    custom_role_name: '',
    csrf_token: '',
    must_change_password: false,
    mfa_enrollment_required: false,
    recovery_codes_remaining: 0,
  }
}

const labels = (permissions: Permission[]) => visibleSectionPages(meWith(permissions)).map((p) => p.label)

describe('visibleSectionPages', () => {
  it('offers only the knowledge base without permissions', () => {
    expect(labels([])).toEqual(['Kennisbank'])
    expect(visibleSectionPages(undefined).map((p) => p.label)).toEqual(['Kennisbank'])
  })

  it('adds each page for the permission its route needs', () => {
    expect(labels(['reports.view'])).toEqual(['Rapportage', 'Kennisbank'])
    expect(labels(['campaigns.manage'])).toEqual(['Campagnes', 'Kennisbank'])
    expect(labels(['kb.manage'])).toEqual(['Kennisbank', 'Kennisbank beheren'])
    expect(labels(['conversations.delete'])).toEqual(['Prullenbak', 'Kennisbank'])
  })
})
