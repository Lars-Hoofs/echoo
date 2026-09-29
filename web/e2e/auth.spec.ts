import { expect, type Page, test } from '@playwright/test'

import { loginSlot } from './session'
import { totp } from './totp'
import { useTemporaryPassword } from './users'

const ownerEmail = process.env.ECHOO_E2E_OWNER_EMAIL ?? 'owner@example.com'
const ownerTempPassword = process.env.ECHOO_E2E_OWNER_PASSWORD ?? ''
const ownerPassword = 'een lange zin als wachtwoord'
const agentPassword = 'nog een lange zin voor de agent'

test.describe.configure({ mode: 'serial' })

// Every page must work under the strict CSP; tests assert that no violation was logged.
function watchCSP(page: Page): string[] {
  const violations: string[] = []
  page.on('console', (msg) => {
    if (msg.text().includes('Content Security Policy')) violations.push(msg.text())
  })
  return violations
}

// Uitloggen lives in the user menu at the bottom of the sidebar.
async function logout(page: Page) {
  await page.getByRole('button', { name: /^Gebruikersmenu/ }).click()
  await page.getByRole('menuitem', { name: 'Uitloggen' }).click()
}

async function login(page: Page, email: string, password: string) {
  await loginSlot()
  await page.goto('/')
  await expect(page).toHaveURL(/\/inloggen/)
  await page.getByLabel('E-mailadres').fill(email)
  await page.getByLabel('Wachtwoord').fill(password)
  await page.getByRole('button', { name: 'Inloggen' }).click()
}

test('owner replaces the temporary password and signs in again', async ({ page }) => {
  test.skip(!ownerTempPassword, 'ECHOO_E2E_OWNER_PASSWORD is not set')
  const csp = watchCSP(page)

  await login(page, ownerEmail, 'definitely wrong password')
  await expect(page.getByRole('alert')).toContainText('Inloggen mislukt')

  await page.getByLabel('Wachtwoord').fill(ownerTempPassword)
  await loginSlot()
  await page.getByRole('button', { name: 'Inloggen' }).click()
  await expect(page.getByRole('heading', { name: 'Kies een eigen wachtwoord' })).toBeVisible()

  await page.getByLabel('Tijdelijk wachtwoord').fill(ownerTempPassword)
  await page.getByLabel('Nieuw wachtwoord').fill(ownerPassword)
  await page.getByRole('button', { name: 'Wachtwoord wijzigen' }).click()
  await expect(page.getByRole('heading', { name: 'Gesprekken' })).toBeVisible()

  await logout(page)
  await login(page, ownerEmail, ownerPassword)
  await expect(page.getByRole('heading', { name: 'Gesprekken' })).toBeVisible()
  expect(csp).toEqual([])
})

test('admin adds an agent who sets up two-factor authentication', async ({ page, browser }) => {
  test.skip(!ownerTempPassword, 'ECHOO_E2E_OWNER_PASSWORD is not set')
  const agentEmail = `agent-${Date.now()}@example.com`
  const csp = watchCSP(page)

  await login(page, ownerEmail, ownerPassword)
  await page.getByRole('link', { name: 'Instellingen' }).click()
  await page.getByRole('link', { name: 'Gebruikers' }).click()
  await page.getByRole('button', { name: 'Gebruiker toevoegen' }).click()
  const dialog = page.getByRole('dialog')
  await dialog.getByLabel('Naam').fill('Sanne de Vries')
  await dialog.getByLabel('E-mailadres').fill(agentEmail)
  await useTemporaryPassword(dialog)
  await dialog.getByRole('button', { name: 'Toevoegen' }).click()
  const temp = (await dialog.locator('.select-all').textContent())?.trim() ?? ''
  expect(temp).toMatch(/^[a-z2-7]{4}(-[a-z2-7]{4}){5}$/)
  await dialog.getByRole('button', { name: 'Klaar' }).click()
  await expect(page.getByRole('cell', { name: `Sanne de Vries ${agentEmail}` })).toBeVisible()

  const agentContext = await browser.newContext()
  const agent = await agentContext.newPage()
  const agentCsp = watchCSP(agent)
  await login(agent, agentEmail, temp)
  await agent.getByLabel('Tijdelijk wachtwoord').fill(temp)
  await agent.getByLabel('Nieuw wachtwoord').fill(agentPassword)
  await agent.getByRole('button', { name: 'Wachtwoord wijzigen' }).click()
  await expect(agent.getByRole('heading', { name: 'Gesprekken' })).toBeVisible()

  await agent.getByRole('link', { name: 'Instellingen' }).click()
  await agent.getByRole('link', { name: 'Beveiliging' }).click()
  await agent.getByRole('button', { name: 'Instellen' }).click()
  const secret = (await agent.locator('.select-all').textContent())?.replace(/\s/g, '') ?? ''
  await agent.getByLabel('Wachtwoord', { exact: true }).fill(agentPassword)
  await agent.getByLabel('Verificatiecode').fill(totp(secret))
  await agent.getByRole('button', { name: 'Inschakelen' }).click()
  await expect(agent.getByRole('main').getByRole('listitem').first()).toHaveText(/^[a-z2-7]{4}-[a-z2-7]{4}-[a-z2-7]{4}-[a-z2-7]{4}$/)
  await agent.getByRole('button', { name: 'Ik heb de codes bewaard' }).click()
  await expect(agent.getByText('Nog 10 codes beschikbaar.')).toBeVisible()

  await logout(agent)
  await login(agent, agentEmail, agentPassword)
  // The enrollment code was consumed; the next step's code is accepted within the skew window.
  await agent.getByLabel('Verificatiecode').fill(totp(secret, 1))
  await loginSlot()
  await agent.getByRole('button', { name: 'Bevestigen' }).click()
  await expect(agent.getByRole('heading', { name: 'Gesprekken' })).toBeVisible()

  // Agents do not see workspace administration.
  await agent.getByRole('link', { name: 'Instellingen' }).click()
  await expect(agent.getByRole('link', { name: 'Profiel' })).toBeVisible()
  await expect(agent.getByRole('link', { name: 'Gebruikers' })).toHaveCount(0)
  await agentContext.close()
  expect([...csp, ...agentCsp]).toEqual([])
})
