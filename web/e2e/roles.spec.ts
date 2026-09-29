// Custom roles and the SSO settings.
//
// Runs after auth.spec.ts, which replaces the owner's temporary password:
//
//   ECHOO_PORT=18112 ECHOO_BASE_URL=http://localhost:18112 docker compose -p echoo-roles up -d --build --wait
//   docker compose -p echoo-roles exec echoo /echoo admin create-owner --email owner@example.com --name Owner
//   ECHOO_E2E_URL=http://localhost:18112 ECHOO_E2E_OWNER_PASSWORD=<temporary password> \
//     ECHOO_E2E_FAKE_IDP_HOST=host.docker.internal npx playwright test auth.spec.ts roles.spec.ts
//
// The SSO test starts a small provider on this machine that only serves discovery, and lets the
// stack reach it through ECHOO_E2E_FAKE_IDP_HOST. The sign-in itself is covered by the Go tests.
import { createServer, type Server } from 'node:http'
import type { AddressInfo } from 'node:net'

import { expect, type Page, test } from '@playwright/test'

import { loginSlot } from './session'
import { useTemporaryPassword } from './users'

const ownerEmail = process.env.ECHOO_E2E_OWNER_EMAIL ?? 'owner@example.com'
const ownerPassword = 'een lange zin als wachtwoord'
const leadPassword = 'een zin voor de teamleider'
const fakeIdpHost = process.env.ECHOO_E2E_FAKE_IDP_HOST ?? ''

test.describe.configure({ mode: 'serial' })
test.skip(!process.env.ECHOO_E2E_OWNER_PASSWORD, 'ECHOO_E2E_OWNER_PASSWORD is not set')

const stamp = Date.now()
const roleName = `Teamleider ${stamp}`
const leadEmail = `teamleider-${stamp}@example.com`

function watchCSP(page: Page): string[] {
  const violations: string[] = []
  page.on('console', (msg) => {
    if (msg.text().includes('Content Security Policy')) violations.push(msg.text())
  })
  return violations
}

async function login(page: Page, email: string, password: string) {
  await loginSlot()
  await page.goto('/')
  await expect(page).toHaveURL(/\/inloggen/)
  await page.getByLabel('E-mailadres').fill(email)
  await page.getByLabel('Wachtwoord').fill(password)
  await page.getByRole('button', { name: 'Inloggen' }).click()
}

let temporaryPassword = ''

// The owner signs in once for the whole file: logging in is rate limited per address, and a run
// with the other specs would otherwise hit that limit.
let page: Page
let csp: string[]

test.beforeAll(async ({ browser }) => {
  page = await (await browser.newContext()).newPage()
  csp = watchCSP(page)
  await login(page, ownerEmail, ownerPassword)
})

test.afterAll(async () => {
  await page.context().close()
})

test('owner creates a custom role and gives it to a new user', async () => {
  await page.getByRole('link', { name: 'Instellingen' }).click()
  await page.getByRole('link', { name: 'Rollen' }).click()

  // The built-in roles are listed, read-only, with a description.
  await expect(page.getByRole('cell', { name: 'Agent', exact: true })).toBeVisible()
  await page
    .getByRole('row', { name: /^Agent/ })
    .getByRole('button', { name: 'Bekijken' })
    .click()
  const view = page.getByRole('dialog', { name: 'Agent' })
  await expect(view.getByLabel('Beantwoorden')).toBeChecked()
  await expect(view.getByLabel('Beantwoorden')).toBeDisabled()
  await expect(view.getByRole('checkbox', { name: /^Mailboxen / })).not.toBeChecked()
  await view.getByRole('button', { name: 'Sluiten' }).first().click()

  await page.getByRole('button', { name: 'Rol toevoegen' }).first().click()
  const dialog = page.getByRole('dialog', { name: 'Rol toevoegen' })
  await dialog.getByLabel('Naam').fill(roleName)
  // Ticking a permission ticks what it needs.
  await dialog.getByLabel('Toewijzen').check()
  await expect(dialog.getByLabel('Beantwoorden')).toBeChecked()
  await expect(dialog.getByLabel('Gesprekken zien')).toBeChecked()
  await dialog.getByLabel('Rapporten bekijken').check()
  await expect(dialog.getByRole('checkbox', { name: /^Mailboxen / })).not.toBeChecked()
  await dialog.getByRole('button', { name: 'Opslaan' }).click()
  await expect(page.getByRole('cell', { name: roleName, exact: true })).toBeVisible()

  await page.getByRole('link', { name: 'Gebruikers' }).click()
  await page.getByRole('button', { name: 'Gebruiker toevoegen' }).click()
  const add = page.getByRole('dialog')
  await add.getByLabel('Naam').fill('Tim Teamleider')
  await add.getByLabel('E-mailadres').fill(leadEmail)
  await add.getByLabel('Rol').selectOption({ label: roleName })
  await useTemporaryPassword(add)
  await add.getByRole('button', { name: 'Toevoegen' }).click()
  temporaryPassword = (await add.locator('.select-all').textContent())?.trim() ?? ''
  expect(temporaryPassword).toMatch(/^[a-z2-7]{4}(-[a-z2-7]{4}){5}$/)
  await add.getByRole('button', { name: 'Klaar' }).click()
  await expect(
    page
      .getByRole('row', { name: new RegExp(leadEmail) })
      .getByText(roleName)
      .and(page.locator(':visible'))
      .first(),
  ).toBeVisible()
  expect(csp).toEqual([])
})

test('the user sees menus that match the role', async ({ browser }) => {
  const lead = await (await browser.newContext()).newPage()
  const leadCsp = watchCSP(lead)
  await login(lead, leadEmail, temporaryPassword)
  await lead.getByLabel('Tijdelijk wachtwoord').fill(temporaryPassword)
  await lead.getByLabel('Nieuw wachtwoord').fill(leadPassword)
  await lead.getByRole('button', { name: 'Wachtwoord wijzigen' }).click()
  await expect(lead.getByRole('heading', { name: 'Gesprekken' })).toBeVisible()

  // Reports and conversation actions are there; customers were not granted.
  await expect(lead.getByRole('link', { name: 'Rapportage' })).toBeVisible()
  await expect(lead.getByRole('button', { name: 'Nieuw gesprek' })).toBeVisible()
  await expect(lead.getByRole('link', { name: 'Contacten' })).toHaveCount(0)

  await lead.getByRole('link', { name: 'Instellingen' }).click()
  await expect(lead.getByRole('link', { name: 'Profiel' })).toBeVisible()
  await expect(lead.getByText(roleName).and(lead.locator(':visible')).first()).toBeVisible()
  for (const hidden of ['Gebruikers', 'Rollen', 'Mailboxen', 'Teams', 'Auditlog', 'Webhooks', 'Inloggen met SSO']) {
    await expect(lead.getByRole('link', { name: hidden, exact: true })).toHaveCount(0)
  }
  await expect(lead.getByText('Werkruimte', { exact: true })).toHaveCount(0)

  // The pages are closed even when the address is typed in.
  await lead.goto('/instellingen/mailboxen')
  await expect(lead).toHaveURL(/\/instellingen\/profiel/)
  await lead.goto('/instellingen/rollen')
  await expect(lead).toHaveURL(/\/instellingen\/profiel/)

  await lead.goto('/rapportage')
  await expect(lead.getByRole('heading', { name: 'Rapportage' })).toBeVisible()
  expect(leadCsp).toEqual([])
  await lead.context().close()
})

test.describe('single sign-on settings', () => {
  test.skip(!fakeIdpHost, 'ECHOO_E2E_FAKE_IDP_HOST is not set')
  let idp: Server
  let issuer = ''

  test.beforeAll(async () => {
    idp = createServer((req, res) => {
      if (req.url === '/.well-known/openid-configuration') {
        res.setHeader('Content-Type', 'application/json')
        res.end(
          JSON.stringify({
            issuer,
            authorization_endpoint: `${issuer}/authorize`,
            token_endpoint: `${issuer}/token`,
            jwks_uri: `${issuer}/jwks`,
            response_types_supported: ['code'],
            subject_types_supported: ['public'],
            id_token_signing_alg_values_supported: ['RS256'],
          }),
        )
        return
      }
      res.statusCode = 404
      res.end()
    })
    await new Promise<void>((resolve) => idp.listen(0, '0.0.0.0', resolve))
    issuer = `http://${fakeIdpHost}:${(idp.address() as AddressInfo).port}`
  })

  test.afterAll(() => {
    idp.close()
  })

  test('the owner turns SSO on and the login page offers it', async ({ browser }) => {
    await page.goto('/instellingen/inloggen')
    await expect(page.getByLabel('Redirect-URI')).toHaveValue(/\/auth\/sso\/callback$/)

    await page.getByRole('switch', { name: 'Inloggen met SSO' }).setChecked(true)
    await page.getByLabel('Issuer-URL').fill(issuer)
    await page.getByLabel('Client-ID').fill('echoo')
    await page.getByLabel('Client secret').fill('een geheim')
    await page.getByLabel('Tekst op de knop').fill('Bedrijfsaccount')
    await page.getByRole('switch', { name: 'Provider op een intern netwerk' }).setChecked(true)
    await page.getByRole('button', { name: 'Opslaan' }).click()
    await expect(page.getByText('Opgeslagen.', { exact: true })).toBeVisible()
    await page.reload()
    await expect(page.getByLabel('Client secret')).toHaveValue('')
    await expect(page.getByText('Er is een secret opgeslagen.')).toBeVisible()

    const anonymous = await browser.newContext()
    const login1 = await anonymous.newPage()
    await login1.goto('/inloggen')
    await expect(login1.getByRole('link', { name: 'Inloggen met Bedrijfsaccount' })).toHaveAttribute('href', '/auth/sso/start')
    await expect(login1.getByLabel('Wachtwoord')).toBeVisible()

    // With SSO required only the owner's link to the password form is left.
    await page.getByRole('switch', { name: 'SSO verplicht' }).setChecked(true)
    await page.getByRole('button', { name: 'Opslaan' }).click()
    await expect(page.getByText('Opgeslagen.', { exact: true })).toBeVisible()
    await login1.reload()
    await expect(login1.getByLabel('Wachtwoord')).toHaveCount(0)
    await login1.getByRole('button', { name: 'Eigenaar? Inloggen met wachtwoord' }).click()
    await expect(login1.getByLabel('Wachtwoord')).toBeVisible()

    // A failed sign-in comes back to the login page with an explanation.
    await login1.goto('/inloggen?sso_error=no_account')
    await expect(login1.getByRole('alert')).toContainText('geen account')
    await anonymous.close()

    // Leave the stack the way the other specs expect it.
    await page.getByRole('switch', { name: 'SSO verplicht' }).setChecked(false)
    await page.getByRole('switch', { name: 'Inloggen met SSO' }).setChecked(false)
    await page.getByRole('button', { name: 'Opslaan' }).click()
    await expect(page.getByText('Opgeslagen.', { exact: true })).toBeVisible()
    expect(csp).toEqual([])
  })
})
