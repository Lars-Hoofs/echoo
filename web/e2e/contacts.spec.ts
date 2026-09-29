import { expect, type Page, test } from '@playwright/test'

import { signInAsOwner } from './session'

const ownerTempPassword = process.env.ECHOO_E2E_OWNER_PASSWORD ?? ''

async function login(page: Page) {
  await signInAsOwner(page)
  await expect(page.getByRole('link', { name: 'Instellingen' })).toBeVisible()
}

test('owner creates a contact with a note and a custom field, imports a CSV and merges contacts', async ({ page }) => {
  test.skip(!ownerTempPassword, 'ECHOO_E2E_OWNER_PASSWORD is not set')
  const stamp = Date.now()
  const name = `Jansen ${stamp}`
  const field = `Klantnummer ${stamp}`
  const email = `jansen-${stamp}@example.com`
  await login(page)

  // A custom field for contacts.
  await page.getByRole('link', { name: 'Instellingen' }).click()
  await page.getByRole('link', { name: 'Aangepaste velden' }).click()
  await expect(page.getByRole('heading', { name: 'Aangepaste velden' })).toBeVisible()
  await page.getByRole('button', { name: 'Veld toevoegen' }).first().click()
  await page.getByLabel('Naam', { exact: true }).fill(field)
  await page.getByRole('button', { name: 'Opslaan' }).click()
  await expect(page.getByRole('row').filter({ hasText: field })).toBeVisible()

  // Create a contact with that field.
  await page.goto('/contacten')
  await expect(page.getByRole('heading', { name: 'Contacten' })).toBeVisible()
  await page.getByRole('button', { name: 'Contact toevoegen' }).click()
  await page.getByLabel('Naam', { exact: true }).fill(name)
  await page.getByRole('textbox', { name: 'E-mailadres 1' }).fill(email)
  await page.getByLabel(field).fill('K-4711')
  await page.getByRole('button', { name: 'Opslaan' }).click()
  await expect(page.getByRole('heading', { name })).toBeVisible()

  // A note.
  await page.getByRole('tab', { name: 'Notities' }).click()
  await page.getByLabel('Nieuwe notitie').fill('Belt liever na drieën')
  await page.getByRole('button', { name: 'Notitie toevoegen' }).click()
  await expect(page.getByText('Belt liever na drieën')).toBeVisible()

  // The custom attribute is shown.
  await page.getByRole('tab', { name: 'Attributen' }).click()
  await expect(page.getByText('K-4711')).toBeVisible()

  // Import two contacts from a CSV file.
  const first = { name: `Import Een ${stamp}`, email: `een-${stamp}@example.com` }
  const second = { name: `Import Twee ${stamp}`, email: `twee-${stamp}@example.com` }
  await page.goto('/contacten')
  await page.getByRole('button', { name: 'Importeren' }).click()
  await page.locator('input[type=file]').setInputFiles({
    name: 'contacten.csv',
    mimeType: 'text/csv',
    buffer: Buffer.from(`Naam;E-mail\n${first.name};${first.email}\n${second.name};${second.email}\n`),
  })
  await expect(page.getByText('2 rijen gevonden')).toBeVisible()
  await page.getByRole('dialog').getByRole('button', { name: 'Importeren' }).click()
  await expect(page.getByRole('status').filter({ hasText: 'De import is klaar.' })).toBeVisible({ timeout: 30_000 })
  await expect(page.getByText('Nieuw', { exact: true }).locator('..')).toContainText('2')
  await page.getByRole('button', { name: 'Sluiten' }).last().click()
  await page.getByLabel('Zoeken in contacten').fill(`Import`)
  await expect(page.getByRole('link', { name: first.name })).toBeVisible()
  await expect(page.getByRole('link', { name: second.name })).toBeVisible()

  // Merge the second contact into the first.
  await page.getByRole('link', { name: first.name }).click()
  await expect(page.getByRole('heading', { name: first.name })).toBeVisible()
  await page.getByRole('button', { name: 'Meer acties' }).click()
  await page.getByRole('menuitem', { name: 'Samenvoegen met…' }).click()
  await page.getByLabel('Zoek een contact').fill(second.name)
  await page.getByRole('radio').first().check()
  await page.getByRole('button', { name: 'Verder' }).click()
  await page.getByRole('button', { name: 'Definitief samenvoegen' }).click()
  await expect(page.getByText(second.email)).toBeVisible()
  await expect(page.getByText(first.email)).toBeVisible()

  // The merged contact is gone.
  await page.goto('/contacten')
  await page.getByLabel('Zoeken in contacten').fill(second.name)
  await expect(page.getByText('Geen contacten gevonden')).toBeVisible()
})
