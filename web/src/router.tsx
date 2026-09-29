import type { QueryClient } from '@tanstack/react-query'
import {
  createRootRouteWithContext,
  createRoute,
  createRouter,
  lazyRouteComponent,
  Link,
  Outlet,
  redirect,
} from '@tanstack/react-router'

import { ApiError } from './lib/api'
import { stepFromSlug, type StepId } from './lib/campaigns'
import { parseInboxSearch, viewFromSlug } from './lib/inbox'
import { safeReturnPath } from './lib/redirect'
import { parseReportSearch, tabFromSlug } from './lib/reports'
import { canOpenSettingsPage, settingsPages } from './lib/nav'
import { hasPermission, type Me, meQuery } from './lib/session'
import { LoginPage } from './pages/Login'

interface RouterContext {
  queryClient: QueryClient
}

const rootRoute = createRootRouteWithContext<RouterContext>()({ component: Outlet })

const loginRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: '/inloggen',
  validateSearch: (search: Record<string, unknown>): { terug?: string; sso_error?: string; mfa?: boolean } => {
    const terug = safeReturnPath(search.terug)
    return {
      ...(terug ? { terug } : {}),
      // Set by /auth/sso/callback: why sign-in failed, or that the code step is next.
      ...(typeof search.sso_error === 'string' ? { sso_error: search.sso_error } : {}),
      ...(search.mfa === 1 || search.mfa === '1' ? { mfa: true } : {}),
    }
  },
  component: LoginPage,
})

const forgotPasswordRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: '/wachtwoord-vergeten',
  component: lazyRouteComponent(() => import('./pages/AccountLinks'), 'ForgotPasswordPage'),
})

const resetPasswordRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: '/wachtwoord-herstellen/$token',
  component: lazyRouteComponent(() => import('./pages/AccountLinks'), 'ResetPasswordPage'),
})

const invitationRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: '/uitnodiging/$token',
  component: lazyRouteComponent(() => import('./pages/AccountLinks'), 'InvitationPage'),
})

const authRoute = createRoute({
  getParentRoute: () => rootRoute,
  id: 'auth',
  beforeLoad: async ({ context, location }) => {
    try {
      return { me: await context.queryClient.query(meQuery) }
    } catch (err) {
      if (err instanceof ApiError && err.status === 401) {
        throw redirect({ to: '/inloggen', search: { terug: location.href } })
      }
      throw err
    }
  },
  component: lazyRouteComponent(() => import('./pages/AuthLayout'), 'AuthLayout'),
})

const setupPasswordRoute = createRoute({
  getParentRoute: () => authRoute,
  path: '/account/wachtwoord',
  component: lazyRouteComponent(() => import('./pages/AccountSetup'), 'SetupPasswordPage'),
})

const setupMfaRoute = createRoute({
  getParentRoute: () => authRoute,
  path: '/account/tweestapsverificatie',
  component: lazyRouteComponent(() => import('./pages/AccountSetup'), 'SetupMfaPage'),
})

const readyRoute = createRoute({
  getParentRoute: () => authRoute,
  id: 'ready',
  beforeLoad: ({ context }) => {
    if (context.me.must_change_password) throw redirect({ to: '/account/wachtwoord' })
    if (context.me.mfa_enrollment_required) throw redirect({ to: '/account/tweestapsverificatie' })
  },
  component: lazyRouteComponent(() => import('./pages/AppShell'), 'AppShell'),
})

const indexRoute = createRoute({
  getParentRoute: () => readyRoute,
  path: '/',
  beforeLoad: () => {
    throw redirect({ to: '/inbox/$view', params: { view: 'alle' } })
  },
})

const inboxRoute = createRoute({
  getParentRoute: () => readyRoute,
  path: '/inbox/$view',
  validateSearch: parseInboxSearch,
  beforeLoad: ({ params }) => {
    if (!viewFromSlug(params.view)) throw redirect({ to: '/inbox/$view', params: { view: 'alle' } })
  },
  component: lazyRouteComponent(() => import('./pages/inbox/InboxPage'), 'InboxPage'),
})

const conversationRoute = createRoute({
  getParentRoute: () => inboxRoute,
  path: '$conversationId',
  component: lazyRouteComponent(() => import('./pages/inbox/ConversationPane'), 'ConversationPane'),
})

const searchRoute = createRoute({
  getParentRoute: () => readyRoute,
  path: '/zoeken',
  validateSearch: (search: Record<string, unknown>): { q?: string } => (typeof search.q === 'string' && search.q ? { q: search.q } : {}),
  component: lazyRouteComponent(() => import('./pages/search/SearchPage'), 'SearchPage'),
})

const reportsIndexRoute = createRoute({
  getParentRoute: () => readyRoute,
  path: '/rapportage',
  beforeLoad: () => {
    throw redirect({ to: '/rapportage/$tab', params: { tab: 'overzicht' } })
  },
})

const reportsRoute = createRoute({
  getParentRoute: () => readyRoute,
  path: '/rapportage/$tab',
  validateSearch: parseReportSearch,
  beforeLoad: ({ params }) => {
    if (!tabFromSlug(params.tab)) throw redirect({ to: '/rapportage/$tab', params: { tab: 'overzicht' } })
  },
  component: lazyRouteComponent(() => import('./pages/reports/ReportsPage'), 'ReportsPage'),
})

const settingsRoute = createRoute({
  getParentRoute: () => readyRoute,
  id: 'settings',
  component: lazyRouteComponent(() => import('./pages/settings/SettingsShell'), 'SettingsShell'),
})

const profileRoute = createRoute({
  getParentRoute: () => settingsRoute,
  path: '/instellingen/profiel',
  component: lazyRouteComponent(() => import('./pages/settings/Profile'), 'ProfilePage'),
})

const templatesRoute = createRoute({
  getParentRoute: () => settingsRoute,
  path: '/instellingen/standaardantwoorden',
  component: lazyRouteComponent(() => import('./pages/settings/Templates'), 'TemplatesPage'),
})

const macrosRoute = createRoute({
  getParentRoute: () => settingsRoute,
  path: '/instellingen/macros',
  component: lazyRouteComponent(() => import('./pages/settings/Macros'), 'MacrosPage'),
})

const securityRoute = createRoute({
  getParentRoute: () => settingsRoute,
  path: '/instellingen/beveiliging',
  component: lazyRouteComponent(() => import('./pages/settings/Security'), 'SecurityPage'),
})

const apiTokensRoute = createRoute({
  getParentRoute: () => settingsRoute,
  path: '/instellingen/api-tokens',
  component: lazyRouteComponent(() => import('./pages/settings/ApiTokens'), 'ApiTokensPage'),
})

const adminRoute = createRoute({
  getParentRoute: () => settingsRoute,
  id: 'admin',
  // Every page under here is listed in settingsPages with the access it needs, so the sidebar,
  // the command bar and this guard cannot disagree. A page that is not listed stays closed.
  beforeLoad: ({ context, location }) => {
    const page = settingsPages.find((p) => p.to === location.pathname)
    if (!page || !canOpenSettingsPage(context.me, page.access)) throw redirect({ to: '/instellingen/profiel' })
  },
  component: Outlet,
})

const usersRoute = createRoute({
  getParentRoute: () => adminRoute,
  path: '/instellingen/gebruikers',
  component: lazyRouteComponent(() => import('./pages/settings/Users'), 'UsersPage'),
})

const rolesRoute = createRoute({
  getParentRoute: () => adminRoute,
  path: '/instellingen/rollen',
  component: lazyRouteComponent(() => import('./pages/settings/Roles'), 'RolesPage'),
})

const ssoRoute = createRoute({
  getParentRoute: () => adminRoute,
  path: '/instellingen/inloggen',
  component: lazyRouteComponent(() => import('./pages/settings/Sso'), 'SsoPage'),
})

const mailboxesRoute = createRoute({
  getParentRoute: () => adminRoute,
  path: '/instellingen/mailboxen',
  // The OAuth callback redirects here with the outcome.
  validateSearch: (search: Record<string, unknown>): { oauth?: string; oauth_error?: string } => ({
    ...(typeof search.oauth === 'string' ? { oauth: search.oauth } : {}),
    ...(typeof search.oauth_error === 'string' ? { oauth_error: search.oauth_error } : {}),
  }),
  component: lazyRouteComponent(() => import('./pages/settings/Mailboxes'), 'MailboxesPage'),
})

const teamsRoute = createRoute({
  getParentRoute: () => adminRoute,
  path: '/instellingen/teams',
  component: lazyRouteComponent(() => import('./pages/settings/Teams'), 'TeamsPage'),
})

const labelsRoute = createRoute({
  getParentRoute: () => adminRoute,
  path: '/instellingen/labels',
  component: lazyRouteComponent(() => import('./pages/settings/Labels'), 'LabelsPage'),
})

const workspaceRoute = createRoute({
  getParentRoute: () => adminRoute,
  path: '/instellingen/werkruimte',
  component: lazyRouteComponent(() => import('./pages/settings/Workspace'), 'WorkspacePage'),
})

const allTokensRoute = createRoute({
  getParentRoute: () => adminRoute,
  path: '/instellingen/tokens',
  component: lazyRouteComponent(() => import('./pages/settings/ApiTokens'), 'AllApiTokensPage'),
})

const webhooksRoute = createRoute({
  getParentRoute: () => adminRoute,
  path: '/instellingen/webhooks',
  component: lazyRouteComponent(() => import('./pages/settings/Webhooks'), 'WebhooksPage'),
})

const jobsRoute = createRoute({
  getParentRoute: () => adminRoute,
  path: '/instellingen/taken',
  component: lazyRouteComponent(() => import('./pages/settings/Jobs'), 'JobsPage'),
})

const retentionRoute = createRoute({
  getParentRoute: () => adminRoute,
  path: '/instellingen/privacy',
  component: lazyRouteComponent(() => import('./pages/settings/Retention'), 'RetentionPage'),
})

const auditRoute = createRoute({
  getParentRoute: () => adminRoute,
  path: '/instellingen/auditlog',
  component: lazyRouteComponent(() => import('./pages/settings/Audit'), 'AuditPage'),
})

const contactsRoute = createRoute({
  getParentRoute: () => readyRoute,
  path: '/contacten',
  component: lazyRouteComponent(() => import('./pages/contacts/ContactsPage'), 'ContactsPage'),
})

const contactRoute = createRoute({
  getParentRoute: () => readyRoute,
  path: '/contacten/$id',
  component: lazyRouteComponent(() => import('./pages/contacts/ContactPage'), 'ContactPage'),
})

const organizationsRoute = createRoute({
  getParentRoute: () => readyRoute,
  path: '/organisaties',
  component: lazyRouteComponent(() => import('./pages/contacts/OrganizationsPage'), 'OrganizationsPage'),
})

const organizationRoute = createRoute({
  getParentRoute: () => readyRoute,
  path: '/organisaties/$id',
  component: lazyRouteComponent(() => import('./pages/contacts/OrganizationPage'), 'OrganizationPage'),
})

const rulesRoute = createRoute({
  getParentRoute: () => adminRoute,
  path: '/instellingen/regels',
  component: lazyRouteComponent(() => import('./pages/settings/Rules'), 'RulesPage'),
})

const slaRoute = createRoute({
  getParentRoute: () => adminRoute,
  path: '/instellingen/sla',
  component: lazyRouteComponent(() => import('./pages/settings/Sla'), 'SlaPage'),
})

const assignmentRoute = createRoute({
  getParentRoute: () => adminRoute,
  path: '/instellingen/toewijzing',
  component: lazyRouteComponent(() => import('./pages/settings/Assignment'), 'AssignmentPage'),
})

const kbRoute = createRoute({
  getParentRoute: () => readyRoute,
  path: '/kennisbank',
  component: lazyRouteComponent(() => import('./pages/kb/KbListPage'), 'KbListPage'),
})

const kbNewRoute = createRoute({
  getParentRoute: () => readyRoute,
  path: '/kennisbank/nieuw',
  component: lazyRouteComponent(() => import('./pages/kb/KbEditorPage'), 'KbNewPage'),
})

const kbManageRoute = createRoute({
  getParentRoute: () => readyRoute,
  path: '/kennisbank/beheer',
  beforeLoad: ({ context }) => {
    if (!hasPermission(context.me, 'kb.manage')) throw redirect({ to: '/kennisbank' })
  },
  component: lazyRouteComponent(() => import('./pages/kb/KbManagePage'), 'KbManagePage'),
})

const kbArticleRoute = createRoute({
  getParentRoute: () => readyRoute,
  path: '/kennisbank/$id',
  component: lazyRouteComponent(() => import('./pages/kb/KbEditorPage'), 'KbArticlePage'),
})

const requireCampaigns = ({ context }: { context: { me: Me } }) => {
  if (!hasPermission(context.me, 'campaigns.manage')) throw redirect({ to: '/inbox/$view', params: { view: 'alle' } })
}

const parseStep = (search: Record<string, unknown>): { stap: StepId } => ({ stap: stepFromSlug(search.stap) })

const campaignsRoute = createRoute({
  getParentRoute: () => readyRoute,
  path: '/campagnes',
  beforeLoad: requireCampaigns,
  component: lazyRouteComponent(() => import('./pages/campaigns/CampaignsPage'), 'CampaignsPage'),
})

const campaignNewRoute = createRoute({
  getParentRoute: () => readyRoute,
  path: '/campagnes/nieuw',
  validateSearch: parseStep,
  beforeLoad: requireCampaigns,
  component: lazyRouteComponent(() => import('./pages/campaigns/CampaignWizard'), 'NewCampaignPage'),
})

const campaignEditRoute = createRoute({
  getParentRoute: () => readyRoute,
  path: '/campagnes/$id/bewerken',
  validateSearch: parseStep,
  beforeLoad: requireCampaigns,
  component: lazyRouteComponent(() => import('./pages/campaigns/CampaignWizard'), 'EditCampaignPage'),
})

const campaignRoute = createRoute({
  getParentRoute: () => readyRoute,
  path: '/campagnes/$id',
  beforeLoad: requireCampaigns,
  component: lazyRouteComponent(() => import('./pages/campaigns/CampaignPage'), 'CampaignPage'),
})

const customFieldsRoute = createRoute({
  getParentRoute: () => adminRoute,
  path: '/instellingen/velden',
  component: lazyRouteComponent(() => import('./pages/settings/CustomFields'), 'CustomFieldsPage'),
})

const routeTree = rootRoute.addChildren([
  loginRoute,
  forgotPasswordRoute,
  resetPasswordRoute,
  invitationRoute,
  authRoute.addChildren([
    setupPasswordRoute,
    setupMfaRoute,
    readyRoute.addChildren([
      indexRoute,
      inboxRoute.addChildren([conversationRoute]),
      contactsRoute,
      contactRoute,
      organizationsRoute,
      organizationRoute,
      searchRoute,
      kbRoute,
      kbNewRoute,
      kbManageRoute,
      kbArticleRoute,
      campaignsRoute,
      campaignNewRoute,
      campaignEditRoute,
      campaignRoute,
      reportsIndexRoute,
      reportsRoute,
      settingsRoute.addChildren([
        profileRoute,
        securityRoute,
        templatesRoute,
        macrosRoute,
        apiTokensRoute,
        adminRoute.addChildren([rulesRoute, slaRoute, assignmentRoute, mailboxesRoute, usersRoute, rolesRoute, ssoRoute, teamsRoute, labelsRoute, customFieldsRoute, workspaceRoute, allTokensRoute, webhooksRoute, jobsRoute, retentionRoute, auditRoute]),
      ]),
    ]),
  ]),
])

export function createAppRouter(queryClient: QueryClient) {
  return createRouter({
    routeTree,
    context: { queryClient },
    defaultPreload: 'intent',
    defaultNotFoundComponent: () => (
      <main className="flex h-full flex-col items-center justify-center gap-2 p-4 text-base text-muted">
        <p>Deze pagina bestaat niet.</p>
        <Link to="/" className="rounded-sm text-accent-text hover:underline">
          Naar de inbox
        </Link>
      </main>
    ),
  })
}

declare module '@tanstack/react-router' {
  interface Register {
    router: ReturnType<typeof createAppRouter>
  }
}
