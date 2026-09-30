import type { Permission } from './permissions'
import { hasPermission, type Me } from './session'

// Who may open a settings page: everybody, only the owner, or holders of a permission. The
// router enforces the same rule.
export type SettingsAccess = 'everyone' | 'owner' | Permission

// The pages the command bar can open besides the inbox views.
export const settingsPages = [
  { to: '/instellingen/profiel', label: 'Profiel', access: 'everyone' },
  { to: '/instellingen/beveiliging', label: 'Beveiliging', access: 'everyone' },
  { to: '/instellingen/standaardantwoorden', label: 'Standaardantwoorden', access: 'everyone' },
  { to: '/instellingen/macros', label: "Macro's", access: 'everyone' },
  { to: '/instellingen/api-tokens', label: 'API-tokens', access: 'everyone' },
  { to: '/instellingen/gebruikers', label: 'Gebruikers', access: 'users.manage' },
  { to: '/instellingen/rollen', label: 'Rollen', access: 'users.manage' },
  { to: '/instellingen/teams', label: 'Teams', access: 'teams.manage' },
  { to: '/instellingen/mailboxen', label: 'Mailboxen', access: 'mailboxes.manage' },
  { to: '/instellingen/labels', label: 'Labels', access: 'labels.manage' },
  { to: '/instellingen/geblokkeerde-afzenders', label: 'Geblokkeerde afzenders', access: 'conversations.delete' },
  { to: '/instellingen/velden', label: 'Velden', access: 'settings.manage' },
  { to: '/instellingen/regels', label: 'Regels', access: 'automation.manage' },
  { to: '/instellingen/sla', label: 'SLA', access: 'sla.manage' },
  { to: '/instellingen/toewijzing', label: 'Toewijzing', access: 'automation.manage' },
  { to: '/instellingen/werkruimte', label: 'Beveiliging werkruimte', access: 'settings.manage' },
  { to: '/instellingen/inloggen', label: 'Inloggen met SSO', access: 'owner' },
  { to: '/instellingen/tokens', label: 'Alle API-tokens', access: 'api_tokens.manage_all' },
  { to: '/instellingen/webhooks', label: 'Webhooks', access: 'webhooks.manage' },
  { to: '/instellingen/taken', label: 'Taken', access: 'settings.manage' },
  { to: '/instellingen/privacy', label: 'Privacy en retentie', access: 'settings.manage' },
  { to: '/instellingen/auditlog', label: 'Auditlog', access: 'audit.view' },
] as const

export function canOpenSettingsPage(me: Me | undefined, access: SettingsAccess): boolean {
  if (access === 'everyone') return me !== undefined
  if (access === 'owner') return me?.user.role === 'owner'
  return hasPermission(me, access)
}

// hasWorkspaceSettings is true when at least one page under Werkruimte is open to the user.
export function hasWorkspaceSettings(me: Me | undefined): boolean {
  return settingsPages.some((p) => p.access !== 'everyone' && canOpenSettingsPage(me, p.access))
}

export const customerPages = [
  { to: '/contacten', label: 'Contacten' },
  { to: '/organisaties', label: 'Organisaties' },
] as const

// The other pages the command bar opens, with the permission the router demands for them.
export const sectionPages = [
  { to: '/prullenbak', label: 'Prullenbak', hint: 'Gesprekken', permission: 'conversations.delete' },
  { to: '/campagnes', label: 'Campagnes', hint: 'Klanten', permission: 'campaigns.manage' },
  { to: '/rapportage', label: 'Rapportage', permission: 'reports.view' },
  { to: '/kennisbank', label: 'Kennisbank' },
  { to: '/kennisbank/beheer', label: 'Kennisbank beheren', hint: 'Kennisbank', permission: 'kb.manage' },
] as const satisfies readonly { to: string; label: string; hint?: string; permission?: Permission }[]

export function visibleSectionPages(me: Me | undefined): (typeof sectionPages)[number][] {
  return sectionPages.filter((p) => !('permission' in p) || hasPermission(me, p.permission))
}
