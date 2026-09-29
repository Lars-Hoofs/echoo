import { expect, type Page, test } from '@playwright/test'

import { dockerPsql } from './compose'
import { signInAsOwner } from './session'

// Runs after auth.spec.ts, which replaces the owner's temporary password. Conversations are
// seeded with SQL in the stack's database, because mail arrives by IMAP.
const stamp = Date.now()
const subjects = ['Sluiten', 'Toewijzen', 'Splitsen', 'Bulk een', 'Bulk twee'].map((s) => `${s} ${stamp}`)
const [closeSubject, assignSubject, snoozeSubject, bulkOne, bulkTwo] = subjects as [string, string, string, string, string]
const labelName = `Factuur ${stamp}`

function psql(sql: string) {
  dockerPsql(['-v', 'ON_ERROR_STOP=1', '-c', sql])
}

// One sign-in for the whole file: the login endpoint is rate limited per address.
let page: Page

test.beforeAll(async ({ browser }) => {
  psql(`INSERT INTO mailboxes (name, email_address) VALUES ('Acties ${stamp}', 'acties-${stamp}@example.com')`)
  const values = subjects.map((s, i) => `('${s}', now() - interval '${i} minutes')`).join(', ')
  psql(`INSERT INTO conversations (mailbox_id, subject, preview, last_message_at)
        SELECT mailboxes.id, v.subject, v.subject, v.at FROM mailboxes, (VALUES ${values}) AS v(subject, at)
        WHERE mailboxes.email_address = 'acties-${stamp}@example.com'`)
  page = await (await browser.newContext({ viewport: { width: 1600, height: 900 } })).newPage()
  await signIn(page)
})

test.afterAll(async () => {
  await page.context().close()
})

// The contact panel is docked from 1440 px wide, hence the large viewport above.
async function signIn(page: Page) {
  await signInAsOwner(page)
  await expect(page.getByRole('heading', { name: 'Gesprekken' })).toBeVisible()
}

const row = (subject: string) => page.getByRole('link').filter({ hasText: subject })

async function open(subject: string) {
  await row(subject).click()
  await expect(page.getByRole('heading', { level: 2, name: subject })).toBeVisible()
}

test.describe.configure({ mode: 'serial' })

test('owner closes a conversation and reopens it from the closed list', async () => {
  await page.goto('/inbox/alle')
  await open(closeSubject)

  await page.getByRole('button', { name: 'Sluiten', exact: true }).click()
  await expect(page.getByText('Gesprek gesloten')).toBeVisible()
  await expect(row(closeSubject)).toHaveCount(0)
  await expect(page.getByText(/sloot het gesprek/)).toBeVisible()

  await page.getByRole('button', { name: /status filter/ }).click()
  await page.getByRole('menuitemradio', { name: 'Gesloten' }).click()
  await open(closeSubject)
  await page.getByRole('button', { name: 'Heropenen', exact: true }).click()
  await expect(page.getByText('Gesprek heropend')).toBeVisible()
  await expect(row(closeSubject)).toHaveCount(0)
  await expect(page.getByText(/heropende het gesprek/)).toBeVisible()
})

test('undo brings a closed conversation back', async () => {
  await page.goto('/inbox/alle')
  await open(closeSubject)
  await page.getByRole('button', { name: 'Sluiten', exact: true }).click()
  await page.getByRole('button', { name: 'Ongedaan maken' }).click()
  await expect(page.getByText('Ongedaan gemaakt')).toBeVisible()
  await expect(row(closeSubject)).toBeVisible()
})

test('owner assigns a conversation to themselves and adds a label', async () => {
  await page.goto('/inbox/alle')
  const ownerName = /^Gebruikersmenu voor (.+)$/.exec((await page.getByRole('button', { name: /^Gebruikersmenu/ }).getAttribute('aria-label')) ?? '')?.[1]
  expect(ownerName).toBeTruthy()

  await page.goto('/instellingen/labels')
  await page.getByRole('button', { name: 'Label toevoegen' }).first().click()
  await page.getByLabel('Naam').fill(labelName)
  await page.getByText('Groen', { exact: true }).click()
  await page.getByRole('button', { name: 'Opslaan' }).click()
  await expect(page.getByRole('row').filter({ hasText: labelName })).toBeVisible()

  await page.goto('/inbox/alle')
  await open(assignSubject)
  const panel = page.getByRole('complementary', { name: 'Contact' })
  await panel.getByRole('button', { name: /Niemand/ }).click()
  await page.getByRole('option', { name: ownerName ?? '' }).click()
  await expect(page.getByText(`toegewezen aan ${ownerName}`)).toBeVisible()
  await expect(panel.getByRole('button', { name: new RegExp(ownerName ?? '') })).toBeVisible()
  await expect(page.getByText(`${ownerName} wees het gesprek toe aan ${ownerName}`).or(page.getByText(`${ownerName} nam het gesprek op`))).toBeVisible()

  await panel.getByRole('button', { name: 'Label toevoegen' }).click()
  await page.getByRole('option', { name: labelName }).click()
  await page.getByRole('button', { name: 'Toepassen' }).click()
  await expect(panel.getByText(labelName)).toBeVisible()
  await expect(row(assignSubject).getByText(labelName)).toBeVisible()
  await expect(page.getByText(`voegde label ${labelName} toe`)).toBeVisible()

  // The sidebar lists the label and filters on it.
  await page.getByRole('navigation', { name: 'Inbox' }).getByRole('link', { name: labelName }).click()
  await expect(page).toHaveURL(/label=/)
  await expect(row(assignSubject)).toBeVisible()
  await expect(row(snoozeSubject)).toHaveCount(0)
})

test('owner snoozes a conversation from the menu', async () => {
  await page.goto('/inbox/alle')
  await open(snoozeSubject)
  await page.getByRole('button', { name: 'Uitstellen', exact: true }).click()
  await page.getByRole('menuitem', { name: 'Morgen 09:00' }).click()
  await expect(page.getByText(/uitgesteld tot/)).toBeVisible()
  await expect(row(snoozeSubject)).toHaveCount(0)

  await page.getByRole('button', { name: /status filter/ }).click()
  await page.getByRole('menuitemradio', { name: 'Uitgesteld' }).click()
  await expect(row(snoozeSubject)).toBeVisible()
})

test('owner closes two conversations at once from the bulk bar', async () => {
  await page.goto('/inbox/alle')
  await row(bulkOne).hover()
  await page.locator('li', { has: row(bulkOne) }).getByRole('checkbox').check()
  await page.locator('li', { has: row(bulkTwo) }).getByRole('checkbox').check()
  await expect(page.getByText('2 geselecteerd')).toBeVisible()

  await page.getByRole('toolbar', { name: 'Acties voor selectie' }).getByRole('button', { name: 'Sluiten', exact: true }).click()
  await expect(page.getByText('2 gesprekken gesloten')).toBeVisible()
  await expect(row(bulkOne)).toHaveCount(0)
  await expect(row(bulkTwo)).toHaveCount(0)
})

test('keyboard shortcut e closes the focused conversation', async () => {
  await page.goto('/inbox/alle')
  await open(closeSubject)
  await page.keyboard.press('e')
  await expect(page.getByText('Gesprek gesloten')).toBeVisible()
})

test('shortcuts n and r switch the composer to a note or a reply and focus the editor', async () => {
  await page.goto('/inbox/alle')
  await open(assignSubject)

  await page.keyboard.press('n')
  await expect(page.getByRole('tab', { name: 'Notitie' })).toHaveAttribute('aria-selected', 'true')
  await expect(page.getByRole('textbox', { name: 'Notitie' })).toBeFocused()

  // Typing in the editor must not trigger the shortcut again or leak the letter elsewhere.
  await page.keyboard.type('r')
  await expect(page.getByRole('tab', { name: 'Notitie' })).toHaveAttribute('aria-selected', 'true')

  await page.getByRole('heading', { level: 2, name: assignSubject }).click()
  await page.keyboard.press('r')
  await expect(page.getByRole('tab', { name: 'Antwoorden' })).toHaveAttribute('aria-selected', 'true')
  await expect(page.getByRole('textbox', { name: 'Antwoord' })).toBeFocused()

  await page.getByRole('heading', { level: 2, name: assignSubject }).click()
  await page.keyboard.press('ControlOrMeta+k')
  await page.getByRole('dialog').getByRole('option', { name: /Notitie schrijven/ }).click()
  await expect(page.getByRole('textbox', { name: 'Notitie' })).toBeFocused()
})
