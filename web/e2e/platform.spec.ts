import { expect, type Page, test } from '@playwright/test'

import { signInAsOwner } from './session'

const ownerEmail = process.env.ECHOO_E2E_OWNER_EMAIL ?? 'owner@example.com'

test.describe.configure({ mode: 'serial' })

async function signIn(page: Page) {
  await signInAsOwner(page)
  await expect(page.getByRole('heading', { name: 'Gesprekken' })).toBeVisible()
}

function watchCSP(page: Page): string[] {
  const violations: string[] = []
  page.on('console', (msg) => {
    if (msg.text().includes('Content Security Policy')) violations.push(msg.text())
  })
  return violations
}

test('owner creates an API token, calls the API with it and revokes it', async ({ page, request }) => {
  test.skip(!process.env.ECHOO_E2E_OWNER_PASSWORD, 'ECHOO_E2E_OWNER_PASSWORD is not set')
  const violations = watchCSP(page)
  await signIn(page)

  await page.goto('/instellingen/api-tokens')
  await expect(page.getByRole('heading', { name: 'API-tokens' })).toBeVisible()
  await page.getByRole('button', { name: 'Token aanmaken' }).click()
  await page.getByLabel('Naam').fill('Playwright')
  await page.getByLabel('Rechten').selectOption('write')
  await page.getByRole('button', { name: 'Aanmaken' }).click()

  const dialog = page.getByRole('dialog', { name: 'Token aangemaakt' })
  await expect(dialog).toBeVisible()
  const token = (await dialog.locator('code').innerText()).trim()
  expect(token).toMatch(/^ech_[A-Za-z0-9_-]{43}$/)
  await dialog.getByRole('button', { name: 'Klaar' }).click()

  const row = page.getByRole('row').filter({ hasText: 'Playwright' })
  await expect(row).toContainText(token.slice(0, 8))
  await expect(row).toContainText('Lezen en schrijven')
  await expect(row).toContainText('Nog niet gebruikt')

  // The request context has no cookies and sends no Origin: only the bearer token.
  const auth = { Authorization: `Bearer ${token}` }
  const me = await request.get('/api/v1/me', { headers: auth })
  expect(me.status()).toBe(200)
  expect(((await me.json()) as { user: { email: string } }).user.email).toBe(ownerEmail)
  const rename = await request.patch('/api/v1/me', { headers: auth, data: { theme: 'system' } })
  expect(rename.status()).toBe(200)
  expect((await request.get('/api/v1/me')).status()).toBe(401)
  // A token cannot mint another token.
  const mint = await request.post('/api/v1/me/tokens', { headers: auth, data: { name: 'x', scope: 'read' } })
  expect(mint.status()).toBe(403)

  await page.reload()
  await expect(page.getByRole('row').filter({ hasText: 'Playwright' })).not.toContainText('Nog niet gebruikt')

  await page.getByRole('button', { name: 'Token Playwright intrekken' }).click()
  await page.getByRole('dialog').getByRole('button', { name: 'Intrekken' }).click()
  await expect(page.getByRole('dialog')).toHaveCount(0)
  await expect(page.getByRole('row').filter({ hasText: 'Playwright' })).toHaveCount(0)
  expect((await request.get('/api/v1/me', { headers: auth })).status()).toBe(401)
  expect(violations).toEqual([])
})

test('webhook to an internal address is rejected, a public one is created', async ({ page }) => {
  test.skip(!process.env.ECHOO_E2E_OWNER_PASSWORD, 'ECHOO_E2E_OWNER_PASSWORD is not set')
  const violations = watchCSP(page)
  const path = `/echoo-${Date.now()}`
  await signIn(page)

  await page.goto('/instellingen/webhooks')
  await expect(page.getByRole('heading', { name: 'Webhooks' })).toBeVisible()
  await page.getByRole('button', { name: 'Webhook toevoegen' }).first().click()
  const dialog = page.getByRole('dialog', { name: 'Webhook toevoegen' })
  await dialog.getByLabel('Bericht ontvangen').check()

  for (const internal of ['https://127.0.0.1/hook', 'https://localhost/hook', 'https://169.254.169.254/latest']) {
    await dialog.getByLabel('Webadres').fill(internal)
    await dialog.getByRole('button', { name: 'Opslaan' }).click()
    await expect(dialog).toContainText('verwijst naar een intern netwerk')
  }
  await dialog.getByLabel('Webadres').fill(`http://hooks.example.com${path}`)
  await dialog.getByRole('button', { name: 'Opslaan' }).click()
  await expect(dialog).toContainText('Gebruik een https-adres')

  await dialog.getByLabel('Webadres').fill(`https://hooks.example.com${path}`)
  await dialog.getByRole('button', { name: 'Opslaan' }).click()
  const secret = page.getByRole('dialog', { name: 'Webhook toegevoegd' })
  await expect(secret.locator('code')).toContainText(/^whsec_/)
  await secret.getByRole('button', { name: 'Klaar' }).click()

  const row = page.getByRole('row').filter({ hasText: `hooks.example.com${path}` })
  await expect(row).toContainText('Actief')
  await row.getByRole('button', { name: /^Acties voor/ }).click()
  await page.getByRole('menuitem', { name: 'Testbericht versturen' }).click()
  await expect(page.getByText(/staat in de wachtrij/)).toBeVisible()

  await row.getByRole('button', { name: /^Berichten van/ }).click()
  const log = page.getByRole('dialog', { name: 'Berichten' })
  await expect(log.getByRole('row').filter({ hasText: 'Testbericht' })).toBeVisible()
  await expect(log.getByRole('button', { name: /opnieuw versturen$/ })).toBeVisible()
  await page.keyboard.press('Escape')

  await row.getByRole('button', { name: /^Acties voor/ }).click()
  await page.getByRole('menuitem', { name: 'Verwijderen' }).click()
  await page.getByRole('dialog').getByRole('button', { name: 'Verwijderen' }).click()
  await expect(page.getByRole('dialog')).toHaveCount(0)
  await expect(page.getByRole('row').filter({ hasText: `hooks.example.com${path}` })).toHaveCount(0)
  expect(violations).toEqual([])
})

test('owner opens the jobs and audit log pages', async ({ page }) => {
  test.skip(!process.env.ECHOO_E2E_OWNER_PASSWORD, 'ECHOO_E2E_OWNER_PASSWORD is not set')
  const violations = watchCSP(page)
  await signIn(page)

  await page.getByRole('link', { name: 'Instellingen' }).click()
  await page.getByRole('link', { name: 'Taken' }).click()
  await expect(page.getByRole('heading', { name: 'Taken', level: 1 })).toBeVisible()
  await expect(page.getByRole('heading', { name: 'Taken met fouten' })).toBeVisible()
  await expect(page.getByRole('heading', { name: 'Mail die niet is verwerkt' })).toBeVisible()
  await expect(page.getByRole('alert')).toHaveCount(0)

  await page.getByRole('link', { name: 'Auditlog' }).click()
  await expect(page.getByRole('heading', { name: 'Auditlog', level: 1 })).toBeVisible()
  await expect(page.getByRole('row').filter({ hasText: 'Ingelogd' }).first()).toBeVisible()
  await expect(page.getByRole('button', { name: 'Exporteren als CSV' })).toBeVisible()

  await page.getByLabel('Actie').selectOption({ label: 'API-tokens' })
  await expect(page.getByRole('row').filter({ hasText: 'API-token aangemaakt' }).first()).toBeVisible()
  await expect(page.getByRole('row').filter({ hasText: 'Ingelogd' })).toHaveCount(0)

  await page.getByLabel('Van').fill('2999-01-01')
  await expect(page.getByText('Geen regels gevonden')).toBeVisible()
  expect(violations).toEqual([])
})
