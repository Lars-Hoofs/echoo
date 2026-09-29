import { expect, type Page, test } from '@playwright/test'

import { signInAsOwner } from './session'

const ownerTempPassword = process.env.ECHOO_E2E_OWNER_PASSWORD ?? ''

test('owner adds a mailbox and sees it in the list', async ({ page }) => {
  test.skip(!ownerTempPassword, 'ECHOO_E2E_OWNER_PASSWORD is not set')
  const violations = watchCSP(page)
  const address = `support-${Date.now()}@example.com`

  await signInAsOwner(page)
  await page.getByRole('link', { name: 'Instellingen' }).click()
  await page.getByRole('link', { name: 'Mailboxen' }).click()
  await expect(page.getByRole('heading', { name: 'Mailboxen' })).toBeVisible()

  await page.getByRole('button', { name: 'Mailbox toevoegen' }).click()
  await page.getByLabel('Naam', { exact: true }).fill('Klantenservice')
  await page.getByLabel('E-mailadres').fill(address)
  await page.getByLabel('IMAP-server').fill('imap.example.invalid')
  await page.getByLabel('IMAP-gebruikersnaam').fill(address)
  await page.getByLabel('IMAP-wachtwoord').fill('geheim-imap-wachtwoord')
  await page.getByLabel('SMTP-server').fill('smtp.example.invalid')
  await page.getByLabel('SMTP-gebruikersnaam').fill(address)
  await page.getByLabel('SMTP-wachtwoord').fill('geheim-smtp-wachtwoord')

  await page.getByRole('button', { name: 'Verbinding testen' }).click()
  await expect(page.getByRole('status')).toContainText('IMAP: geen verbinding')
  await expect(page.getByRole('status')).toContainText('SMTP: geen verbinding')

  await page.getByRole('button', { name: 'Opslaan' }).click()
  const row = page.getByRole('row').filter({ hasText: address })
  await expect(row).toContainText('Klantenservice')

  // Passwords are write-only: editing shows only that they are set.
  await row.getByRole('button', { name: 'Klantenservice bewerken' }).click()
  await expect(page.getByLabel('IMAP-wachtwoord')).toHaveValue('')
  await expect(page.getByLabel('IMAP-wachtwoord')).toHaveAttribute('placeholder', 'Ingesteld')
  await page.getByRole('button', { name: 'Annuleren' }).click()

  await row.getByRole('button', { name: 'Klantenservice uitschakelen' }).click()
  await page.getByRole('dialog', { name: 'Mailbox uitschakelen' }).getByRole('button', { name: 'Uitschakelen' }).click()
  await expect(row).toContainText('Uitgeschakeld')
  expect(violations).toEqual([])
})

function watchCSP(page: Page): string[] {
  const violations: string[] = []
  page.on('console', (msg) => {
    if (msg.text().includes('Content Security Policy')) violations.push(msg.text())
  })
  return violations
}
