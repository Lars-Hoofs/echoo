import { expect, test } from '@playwright/test'

import { signInAsOwner } from './session'

// Runs after auth.spec.ts, which replaces the owner's temporary password. Only the spam period is
// touched, and it is cleared again at the end, so the conversations other specs seed stay.
test.describe.configure({ mode: 'serial' })

test('the owner previews and saves a spam period, and sees which key protects the secrets', async ({ page }) => {
  test.skip(!process.env.ECHOO_E2E_OWNER_PASSWORD, 'ECHOO_E2E_OWNER_PASSWORD is not set')
  await signInAsOwner(page)
  await page.getByRole('link', { name: 'Instellingen' }).click()
  await page.getByRole('link', { name: 'Privacy en retentie' }).click()
  await expect(page.getByRole('heading', { name: 'Privacy en retentie' })).toBeVisible()

  const spam = page.getByLabel('Spam verwijderen na (dagen)').first()
  await spam.fill('30')
  await page.getByRole('button', { name: 'Voorbeeld bekijken' }).click()
  await expect(page.getByText('Als je dit nu opslaat, verwijdert de eerstvolgende opruiming:')).toBeVisible()
  await expect(page.getByText(/^Spam: 0 gesprekken/)).toBeVisible()

  await page.getByRole('button', { name: 'Opslaan' }).click()
  await expect(page.getByText('Opgeslagen.')).toBeVisible()
  await page.reload()
  await expect(page.getByLabel('Spam verwijderen na (dagen)').first()).toHaveValue('30')

  await expect(page.getByText('Sleutel k1')).toBeVisible()
})

test('an invalid period is refused before anything is sent, and the period can be cleared again', async ({ page }) => {
  test.skip(!process.env.ECHOO_E2E_OWNER_PASSWORD, 'ECHOO_E2E_OWNER_PASSWORD is not set')
  await signInAsOwner(page)
  await page.goto('/instellingen/privacy')
  const spam = page.getByLabel('Spam verwijderen na (dagen)').first()
  await spam.fill('0')
  await expect(page.getByText('Vul een heel getal vanaf 1 in.')).toBeVisible()
  await expect(page.getByRole('button', { name: 'Opslaan' })).toBeDisabled()

  await spam.fill('')
  await page.getByRole('button', { name: 'Opslaan' }).click()
  await expect(page.getByText('Opgeslagen.')).toBeVisible()
  await page.reload()
  await expect(page.getByLabel('Spam verwijderen na (dagen)').first()).toHaveValue('')
})
