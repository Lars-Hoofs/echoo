import { expect, type Page, test } from '@playwright/test'

import { dockerPsql } from './compose'
import { signInAsOwner } from './session'

// Runs after auth.spec.ts, which replaces the owner's temporary password. Conversations are
// seeded with SQL in the stack's database, because mail arrives by IMAP.
const stamp = Date.now()
const mailboxName = `Opruimtest ${stamp}`
const mailboxEmail = `trash-${stamp}@example.com`
const restoreSubject = `Terugzetten ${stamp}`
const purgeSubject = `Weggooien ${stamp}`
const spamSubject = `Blokkeren ${stamp}`
const spammer = `spammer-${stamp}@example.com`

function psql(sql: string) {
  dockerPsql(['-v', 'ON_ERROR_STOP=1', '-c', sql])
}

// One sign-in for the whole file: the login endpoint is rate limited per address.
let page: Page

test.beforeAll(async ({ browser }) => {
  psql(`INSERT INTO mailboxes (name, email_address) VALUES ('${mailboxName}', '${mailboxEmail}')`)
  psql(`INSERT INTO conversations (mailbox_id, subject, preview, last_message_at)
        SELECT id, v.subject, v.subject, now() FROM mailboxes, (VALUES ('${restoreSubject}'), ('${purgeSubject}')) AS v(subject)
        WHERE email_address = '${mailboxEmail}'`)
  psql(`WITH ct AS (INSERT INTO contacts (name) VALUES ('Spammer ${stamp}') RETURNING id),
        ad AS (INSERT INTO contact_addresses (contact_id, email, is_primary) SELECT id, '${spammer}', true FROM ct)
        INSERT INTO conversations (mailbox_id, subject, preview, last_message_at, contact_id)
        SELECT mailboxes.id, '${spamSubject}', '${spamSubject}', now(), ct.id FROM mailboxes, ct WHERE email_address = '${mailboxEmail}'`)
  page = await (await browser.newContext({ viewport: { width: 1600, height: 900 } })).newPage()
  await signInAsOwner(page)
  await expect(page.getByRole('heading', { name: 'Gesprekken' })).toBeVisible()
})

test.afterAll(async () => {
  await page.context().close()
})

test.describe.configure({ mode: 'serial' })

const inboxRow = (subject: string) => page.getByRole('link').filter({ hasText: subject })
const trashRow = (subject: string) => page.getByRole('row').filter({ hasText: subject })

async function open(subject: string) {
  await page.goto(`/inbox/alle?mailbox=${mailboxId()}`)
  await inboxRow(subject).click()
  await expect(page.getByRole('heading', { level: 2, name: subject })).toBeVisible()
}

function mailboxId(): string {
  return dockerPsql(['-At', '-c', `SELECT id FROM mailboxes WHERE email_address = '${mailboxEmail}'`])
}

async function deleteOpenConversation() {
  await page.getByRole('button', { name: 'Meer acties' }).click()
  await page.getByRole('menuitem', { name: 'Verwijderen' }).click()
  await expect(page.getByText('Gesprek naar de prullenbak verplaatst')).toBeVisible()
}

async function openTrash() {
  await page.getByRole('navigation', { name: 'Inbox' }).getByRole('link', { name: 'Prullenbak', exact: true }).click()
  await expect(page.getByRole('heading', { level: 1, name: 'Prullenbak', exact: true })).toBeVisible()
}

test('a deleted conversation goes to the trash and comes back when restored', async () => {
  await open(restoreSubject)
  await deleteOpenConversation()
  await expect(inboxRow(restoreSubject)).toHaveCount(0)

  await openTrash()
  await expect(trashRow(restoreSubject)).toBeVisible()
  await trashRow(restoreSubject).getByRole('checkbox').check()
  await page.getByRole('button', { name: 'Terugzetten' }).click()
  await expect(page.getByText('Gesprek teruggezet')).toBeVisible()
  await expect(trashRow(restoreSubject)).toHaveCount(0)

  await open(restoreSubject)
  await expect(page.getByText(/verplaatste het gesprek naar de prullenbak/)).toBeVisible()
  await expect(page.getByText(/zette het gesprek terug uit de prullenbak/)).toBeVisible()
})

test('a conversation in the trash opens read-only and can be restored from there', async () => {
  const body = `Inhoud van het bericht ${stamp}`
  psql(`INSERT INTO messages (conversation_id, mailbox_id, kind, direction, from_addr, body_text)
        SELECT id, mailbox_id, 'email', 'in', 'klant@example.com', '${body}' FROM conversations WHERE subject = '${restoreSubject}'`)
  await open(restoreSubject)
  await deleteOpenConversation()

  await openTrash()
  await trashRow(restoreSubject).getByRole('link', { name: restoreSubject }).click()
  await expect(page.getByRole('heading', { level: 2, name: restoreSubject })).toBeVisible()
  await expect(page.getByText(body)).toBeVisible()
  await expect(page.getByRole('note').filter({ hasText: 'In de prullenbak sinds' })).toBeVisible()
  // Read-only: no composer and nothing to forward.
  await expect(page.getByRole('button', { name: /Doorsturen/ })).toHaveCount(0)
  await expect(page.getByRole('textbox')).toHaveCount(0)

  await page.getByRole('button', { name: 'Terugzetten' }).click()
  await expect(page).toHaveURL(/\/inbox\/alle\//)
  await expect(page.getByRole('heading', { level: 2, name: restoreSubject })).toBeVisible()
})

test('a conversation deleted from the trash is gone for good', async () => {
  await open(purgeSubject)
  await deleteOpenConversation()

  await openTrash()
  await trashRow(purgeSubject).getByRole('checkbox').check()
  await page.getByRole('button', { name: 'Definitief verwijderen' }).click()
  const dialog = page.getByRole('dialog', { name: 'Definitief verwijderen' })
  await expect(dialog.getByText(/niet ongedaan maken/)).toBeVisible()
  await dialog.getByRole('button', { name: 'Definitief verwijderen' }).click()
  await expect(page.getByText('Gesprek definitief verwijderd')).toBeVisible()
  await expect(trashRow(purgeSubject)).toHaveCount(0)
  expect(dockerPsql(['-At', '-c', `SELECT count(*) FROM conversations WHERE subject = '${purgeSubject}'`])).toBe('0')
})

test('marking spam can block the sender, who then shows on the blocked senders page', async () => {
  await open(spamSubject)
  await page.getByRole('button', { name: 'Meer statussen' }).click()
  await page.getByRole('menuitem', { name: 'Spam en afzender blokkeren' }).click()
  await expect(page.getByText(`Nieuwe mail van ${spammer} gaat voortaan naar spam`)).toBeVisible()

  await page.goto('/instellingen/geblokkeerde-afzenders')
  await expect(page.getByRole('row').filter({ hasText: spammer }).filter({ hasText: mailboxName })).toBeVisible()
})

test('a domain is blocked and unblocked on the blocked senders page', async () => {
  const domain = `spam-${stamp}.example`
  await page.goto('/instellingen/geblokkeerde-afzenders')
  await page.getByRole('button', { name: 'Afzender blokkeren' }).click()
  const dialog = page.getByRole('dialog', { name: 'Afzender blokkeren' })
  await dialog.getByLabel('Mailbox').selectOption({ label: mailboxName })
  await dialog.getByLabel('E-mailadres of domein').fill(`@${domain.toUpperCase()}`)
  await expect(dialog.getByText(`Alle nieuwe mail van adressen op ${domain}`)).toBeVisible()
  await dialog.getByRole('button', { name: 'Blokkeren' }).click()

  const row = page.getByRole('row').filter({ hasText: domain })
  await expect(row).toBeVisible()
  await row.getByRole('button', { name: `Blokkering van ${domain} opheffen` }).click()
  await page.getByRole('dialog', { name: 'Blokkering opheffen' }).getByRole('button', { name: 'Opheffen' }).click()
  await expect(row).toHaveCount(0)
})
