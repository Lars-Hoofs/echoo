import { expect, type Page, test } from '@playwright/test'

import { dockerPsql } from './compose'
import { signInAsOwner } from './session'

// Runs after auth.spec.ts, which replaces the owner's temporary password. Conversations and their
// messages are seeded with SQL, because mail arrives by IMAP.
const stamp = Date.now()
const phrase = `zwaluwstaartverbinding ${stamp}`
const urgentSubject = `Retour ${stamp}`
const otherSubject = `Levering ${stamp}`
const viewName = `Hoog ${stamp}`

function psql(sql: string) {
  dockerPsql(['-v', 'ON_ERROR_STOP=1', '-c', sql])
}

// One sign-in for the whole file: the login endpoint is rate limited per address.
let page: Page

test.beforeAll(async ({ browser }) => {
  psql(`INSERT INTO mailboxes (name, email_address) VALUES ('Zoeken ${stamp}', 'zoeken-${stamp}@example.com')`)
  psql(`INSERT INTO conversations (mailbox_id, subject, preview, priority, last_message_at)
        SELECT id, '${urgentSubject}', '${urgentSubject}', 'high', now() FROM mailboxes WHERE email_address = 'zoeken-${stamp}@example.com'`)
  psql(`INSERT INTO conversations (mailbox_id, subject, preview, last_message_at)
        SELECT id, '${otherSubject}', '${otherSubject}', now() - interval '1 minute' FROM mailboxes WHERE email_address = 'zoeken-${stamp}@example.com'`)
  psql(`INSERT INTO messages (conversation_id, mailbox_id, kind, direction, from_addr, from_name, subject, body_text, received_at)
        SELECT id, mailbox_id, 'email', 'in', 'klant@example.org', 'Klant', subject,
               CASE WHEN subject = '${urgentSubject}' THEN 'Goedemiddag, de ${phrase} van mijn pakket is los gekomen.' ELSE 'Goedemiddag, wanneer komt mijn bestelling?' END,
               now()
        FROM conversations WHERE subject IN ('${urgentSubject}', '${otherSubject}')`)
  page = await (await browser.newContext({ viewport: { width: 1600, height: 900 } })).newPage()
  await signInAsOwner(page)
  await expect(page.getByRole('heading', { name: 'Gesprekken' })).toBeVisible()
})

test.afterAll(async () => {
  await page.context().close()
})

test.describe.configure({ mode: 'serial' })

test('searching for a phrase from a message body finds that conversation and opens it', async () => {
  await page.goto('/inbox/alle')
  const field = page.getByRole('search').getByRole('searchbox', { name: 'Zoeken' })
  await field.fill(`"${phrase}"`)
  await field.press('Enter')

  await expect(page).toHaveURL(/\/zoeken\?q=/)
  const results = page.getByRole('list', { name: 'Zoekresultaten' })
  await expect(results.getByRole('link')).toHaveCount(1)
  await expect(results.getByRole('link', { name: new RegExp(urgentSubject) })).toBeVisible()
  await expect(results.getByRole('link', { name: new RegExp(otherSubject) })).toHaveCount(0)
  await expect(results.locator('mark').first()).toContainText(/zwaluwstaartverbinding/)

  await results.getByRole('link', { name: new RegExp(urgentSubject) }).click()
  await expect(page.getByRole('heading', { level: 2, name: urgentSubject })).toBeVisible()
})

test('a search without matches says so', async () => {
  await page.goto(`/zoeken?q=${encodeURIComponent(`bestaatniet${stamp}`)}`)
  await expect(page.getByText(`Geen gesprekken gevonden voor “bestaatniet${stamp}”.`)).toBeVisible()
})

test('the command bar navigates, searches live and lists the shortcuts', async () => {
  await page.goto('/inbox/mine')
  await expect(page.getByRole('heading', { name: 'Gesprekken' })).toBeVisible()
  await page.keyboard.press('Control+k')
  const palette = page.getByRole('dialog', { name: 'Opdrachtenbalk' })
  await expect(palette).toBeVisible()
  await expect(palette.getByText('Navigatie', { exact: true })).toBeVisible()

  await palette.getByRole('combobox').fill('alle gesp')
  await palette.getByRole('option', { name: /Alle gesprekken/ }).click()
  await expect(palette).toHaveCount(0)
  await expect(page).toHaveURL(/\/inbox\/alle$/)

  await page.keyboard.press('Control+k')
  await palette.getByRole('combobox').fill(phrase)
  await expect(palette.getByText('Gesprekken', { exact: true })).toBeVisible()
  await palette.getByRole('option', { name: new RegExp(urgentSubject) }).click()
  await expect(page.getByRole('heading', { level: 2, name: urgentSubject })).toBeVisible()

  // With a conversation open, its actions come first and a shortcut is shown next to them.
  await page.keyboard.press('Control+k')
  await expect(palette.getByText('Acties', { exact: true })).toBeVisible()
  await expect(palette.getByRole('option', { name: /Toewijzen aan/ })).toBeVisible()
  await expect(palette.getByRole('option', { name: /Prioriteit/ })).toBeVisible()
  await page.keyboard.press('Escape')
  await expect(palette).toHaveCount(0)

  // The conversation was remembered.
  await page.keyboard.press('Control+k')
  await expect(palette.getByText('Recent', { exact: true })).toBeVisible()
  await expect(palette.getByRole('option', { name: new RegExp(urgentSubject) }).first()).toBeVisible()
  await page.keyboard.press('Escape')

  await page.goto('/inbox/alle')
  await page.getByRole('heading', { name: 'Gesprekken' }).click()
  await page.keyboard.press('?')
  await expect(page.getByRole('dialog', { name: 'Sneltoetsen' })).toBeVisible()
  await expect(page.getByText('Naar alle gesprekken')).toBeVisible()
  await page.keyboard.press('Escape')

  await page.keyboard.press('g')
  await page.keyboard.press('i')
  await expect(page).toHaveURL(/\/inbox\/mine$/)
  await page.keyboard.press('g')
  await page.keyboard.press('a')
  await expect(page).toHaveURL(/\/inbox\/alle$/)
  // The second key of a chord must not also act as the assign shortcut.
  await expect(page.getByRole('dialog')).toHaveCount(0)

  await page.keyboard.press('/')
  await expect(page.getByRole('searchbox', { name: 'Zoeken' })).toBeFocused()
})

test('a filtered list can be saved as a view, shows in the sidebar, and modifying it offers to save or discard', async () => {
  await page.goto('/inbox/alle')
  await page.getByRole('button', { name: 'Filter', exact: true }).click()
  await page.getByRole('dialog', { name: 'Filter toevoegen' }).getByRole('option', { name: 'Prioriteit' }).click()
  await page.getByRole('dialog', { name: 'Prioriteit' }).getByRole('option', { name: 'Hoog' }).click()

  await expect(page).toHaveURL(/priority=high/)
  await expect(page.getByText('Prioriteit is Hoog', { exact: true })).toBeVisible()
  await expect(page.getByRole('link').filter({ hasText: urgentSubject })).toBeVisible()
  await expect(page.getByRole('link').filter({ hasText: otherSubject })).toHaveCount(0)

  await page.getByRole('button', { name: 'Opslaan als weergave' }).click()
  await page.getByLabel('Naam').fill(viewName)
  await page.getByRole('button', { name: 'Opslaan', exact: true }).click()

  const views = page.getByRole('navigation', { name: 'Inbox' })
  const link = views.getByRole('link', { name: new RegExp(viewName) })
  await expect(link).toBeVisible()
  await expect(link).toHaveAttribute('aria-current', 'page')
  await expect(link.locator('.tabular-nums')).toHaveText(/^[1-9]\d*\+?$/)
  await expect(page.getByRole('heading', { level: 1, name: viewName })).toBeVisible()

  // Removing the chip modifies the view.
  await page.getByRole('button', { name: 'Filter verwijderen: Prioriteit is Hoog' }).click()
  await expect(page.getByRole('button', { name: 'Wijzigingen verwerpen' })).toBeVisible()
  await expect(page.getByRole('button', { name: 'Opslaan als nieuwe weergave' })).toBeVisible()
  await page.getByRole('button', { name: 'Wijzigingen verwerpen' }).click()
  await expect(page.getByText('Prioriteit is Hoog', { exact: true })).toBeVisible()
  await expect(page.getByRole('button', { name: 'Wijzigingen verwerpen' })).toHaveCount(0)

  // The view opens from the sidebar after a reload.
  await page.goto('/inbox/alle')
  await views.getByRole('link', { name: new RegExp(viewName) }).click()
  await expect(page.getByText('Prioriteit is Hoog', { exact: true })).toBeVisible()
  await expect(page.getByRole('link').filter({ hasText: urgentSubject })).toBeVisible()
})
