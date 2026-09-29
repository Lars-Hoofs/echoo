import { expect, type Page, test } from '@playwright/test'

import { signInAsOwner } from './session'

// Runs after auth.spec.ts, which replaces the owner's temporary password. The public help center
// is opened in contexts without a session, as a customer would see it.
const stamp = Date.now()
const letters = String(stamp).replace(/\d/g, (d) => 'abcdefghij'.charAt(Number(d)))
const word = `zoekwoord${letters}`
const categoryName = `Facturen ${stamp}`
const title = `Een factuur betalen ${stamp}`
const slug = `factuur-betalen-${stamp}`
const newSlug = `betalen-van-facturen-${stamp}`

test.describe.configure({ mode: 'serial' })

let agent: Page

test.beforeAll(async ({ browser }) => {
  agent = await (await browser.newContext({ viewport: { width: 1600, height: 1000 } })).newPage()
  await signInAsOwner(agent)
  await expect(agent.getByRole('heading', { name: 'Gesprekken' })).toBeVisible()
})

test.afterAll(async () => {
  await agent.context().close()
})

// A visitor without a session, who also reports every Content Security Policy violation.
async function visitor(browser: import('@playwright/test').Browser) {
  const context = await browser.newContext()
  const page = await context.newPage()
  const violations: string[] = []
  page.on('console', (msg) => {
    if (msg.text().includes('Content Security Policy')) violations.push(msg.text())
  })
  return { context, page, violations }
}

test('an admin creates a category and an article and publishes it', async () => {
  await agent.goto('/kennisbank/beheer')
  await agent.getByRole('button', { name: 'Categorie toevoegen' }).click()
  const dialog = agent.getByRole('dialog')
  await dialog.getByLabel('Naam').fill(categoryName)
  await dialog.getByRole('button', { name: 'Opslaan' }).click()
  await expect(agent.getByRole('cell', { name: categoryName, exact: true })).toBeVisible()

  await agent.goto('/kennisbank/nieuw')
  await agent.getByLabel('Titel').fill(title)
  await agent.getByLabel('Adres').fill(slug)
  await agent.getByLabel('Categorie').selectOption({ label: categoryName })
  await agent.getByRole('textbox', { name: 'Tekst van het artikel' }).click()
  await agent.keyboard.type(`Open de link in de mail en kies betalen. Het ${word} staat bovenaan.`)
  await agent.getByRole('button', { name: 'Opslaan' }).click()
  await expect(agent).toHaveURL(/\/kennisbank\/[0-9a-f-]{36}$/)
  await expect(agent.getByText('Concept', { exact: true })).toBeVisible()

  await agent.getByRole('button', { name: 'Voorbeeld' }).click()
  await expect(agent.getByRole('heading', { name: title })).toBeVisible()
  await agent.getByRole('button', { name: 'Bewerken' }).click()

  await agent.getByRole('button', { name: 'Publiceren' }).click()
  await expect(agent.getByText('Gepubliceerd', { exact: true }).first()).toBeVisible()
})

test('a visitor without a session finds the article and gives feedback', async ({ browser }) => {
  const { context, page, violations } = await visitor(browser)
  await page.goto('/hulp')
  await expect(page.getByRole('heading', { level: 1 })).toBeVisible()
  await expect(page.getByRole('link', { name: new RegExp(categoryName) })).toBeVisible()

  await page.getByRole('searchbox').fill(word)
  await page.getByRole('button', { name: 'Zoeken' }).click()
  await expect(page).toHaveURL(/\/hulp\/zoeken\?q=/)
  await page.getByRole('link', { name: new RegExp(title) }).click()
  await expect(page).toHaveURL(new RegExp(`/hulp/a/${slug}$`))
  await expect(page.getByRole('heading', { level: 1, name: title })).toBeVisible()
  expect(await page.evaluate(() => document.scripts.length)).toBe(0)
  expect(await page.evaluate(() => document.cookie)).toBe('')

  await page.getByRole('button', { name: 'Ja' }).click()
  await expect(page.getByText('Bedankt voor je reactie')).toBeVisible()
  expect(violations).toEqual([])
  await context.close()

  // The vote shows up for agents.
  await agent.goto('/kennisbank')
  await agent.getByLabel('Zoek in artikelen').fill(title)
  await expect(agent.getByRole('row', { name: new RegExp(`${title}.*1 ja`) })).toBeVisible()
})

test('changing the address keeps the old one working', async ({ browser }) => {
  await agent.goto('/kennisbank')
  await agent.getByRole('link', { name: title }).click()
  await agent.getByLabel('Adres').fill(newSlug)
  await agent.getByRole('button', { name: 'Opslaan' }).click()
  await expect(agent.getByText('Opgeslagen')).toBeVisible()

  const { context, page } = await visitor(browser)
  // Navigation redirect metadata is not reliable across browsers, so the 301 is asserted on a
  // request that does not follow it.
  const redirect = await page.request.get(`/hulp/a/${slug}`, { maxRedirects: 0 })
  expect(redirect.status()).toBe(301)
  expect(redirect.headers()['location']).toContain(`/hulp/a/${newSlug}`)
  await page.goto(`/hulp/a/${slug}`)
  await expect(page).toHaveURL(new RegExp(`/hulp/a/${newSlug}$`))
  await expect(page.getByRole('heading', { level: 1, name: title })).toBeVisible()

  const missing = await page.goto('/hulp/a/bestaat-niet')
  expect(missing?.status()).toBe(404)
  await expect(page.getByRole('heading', { name: 'Pagina niet gevonden' })).toBeVisible()
  await context.close()
})

test('an unpublished article is gone from the public site', async ({ browser }) => {
  await agent.getByRole('button', { name: 'Depubliceren' }).click()
  await expect(agent.getByText('Teruggezet naar concept')).toBeVisible()
  const { context, page } = await visitor(browser)
  const response = await page.goto(`/hulp/a/${newSlug}`)
  expect(response?.status()).toBe(404)
  await context.close()
})
