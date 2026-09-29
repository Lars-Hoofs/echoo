import { expect, type Page, test } from '@playwright/test'

import { loginSlot } from './session'

const ownerEmail = process.env.ECHOO_E2E_OWNER_EMAIL ?? 'owner@example.com'
const ownerPassword = 'een lange zin als wachtwoord'

// Runs after auth.spec.ts, which replaces the owner's temporary password.
test('signed-in owner sees the shell and the conversation list', async ({ page }) => {
  test.skip(!process.env.ECHOO_E2E_OWNER_PASSWORD, 'ECHOO_E2E_OWNER_PASSWORD is not set')
  const violations = watchCSP(page)

  await page.goto('/')
  await loginSlot()
  await page.getByLabel('E-mailadres').fill(ownerEmail)
  await page.getByLabel('Wachtwoord').fill(ownerPassword)
  await page.getByRole('button', { name: 'Inloggen' }).click()

  await expect(page).toHaveURL(/\/inbox\/alle$/)
  await expect(page.getByRole('heading', { name: 'Gesprekken' })).toBeVisible()
  const nav = page.getByRole('navigation', { name: 'Inbox' })
  await expect(nav.getByRole('link', { name: /Mijn inbox/ })).toBeVisible()
  await expect(nav.getByRole('link', { name: /Alle gesprekken/ })).toHaveAttribute('aria-current', 'page')
  await expect(nav.getByRole('link', { name: /Zonder toewijzing/ })).toBeVisible()
  await expect(page.getByRole('button', { name: /^Gebruikersmenu/ })).toContainText(ownerEmail)
  // Other specs seed conversations, so the list is either rows or the empty state. Without an
  // enabled mailbox the empty state points to mailbox setup instead.
  await expectRowsOrEmptyState(page, /^(Geen open gesprekken\.|Nog geen mailbox gekoppeld\.)$/)

  await page.getByRole('navigation', { name: 'Weergave' }).getByRole('link', { name: /^Mijn/ }).click()
  await expect(page).toHaveURL(/\/inbox\/mine$/)
  await expectRowsOrEmptyState(page, /^(Geen open gesprekken aan jou toegewezen\.|Nog geen mailbox gekoppeld\.)$/)

  await page.goto('/inbox/onbekend')
  await expect(page).toHaveURL(/\/inbox\/alle$/)

  await page.getByRole('link', { name: 'Instellingen' }).click()
  await expect(page.getByRole('link', { name: 'Terug naar inbox' })).toBeVisible()
  await page.getByRole('link', { name: 'Terug naar inbox' }).click()
  await expect(page).toHaveURL(/\/inbox\/alle$/)
  expect(violations).toEqual([])
})

async function expectRowsOrEmptyState(page: Page, empty: RegExp) {
  const rows = page.locator('[data-row]')
  const emptyState = page.getByText(empty)
  await expect(rows.first().or(emptyState)).toBeVisible()
  if ((await rows.count()) === 0) await expect(emptyState).toBeVisible()
}

function watchCSP(page: Page): string[] {
  const violations: string[] = []
  page.on('console', (msg) => {
    if (msg.text().includes('Content Security Policy')) violations.push(msg.text())
  })
  return violations
}
