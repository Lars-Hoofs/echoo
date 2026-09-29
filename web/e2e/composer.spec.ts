import { expect, type Page, test } from '@playwright/test'

import { dockerPsql } from './compose'
import { signInAsOwner } from './session'

// Runs after auth.spec.ts, which replaces the owner's temporary password. The mailbox points at an
// SMTP host that does not resolve and keeps a 30 second undo window, so a sent reply stays
// queued long enough to look at it and take it back. Mail arrives by IMAP in real life, so the
// conversation is seeded with SQL.
const stamp = Date.now()
const mailboxAddress = `composer-${stamp}@example.com`
const customerAddress = `jan-${stamp}@customer.example`
const subject = `Vraag over factuur ${stamp}`
const colleague = `Sanne Bakker ${stamp}`
const templateName = `Groet ${stamp}`

function psql(sql: string): string {
  return dockerPsql(['-t', '-A', '-v', 'ON_ERROR_STOP=1', '-c', sql])
}

let page: Page

test.beforeAll(async ({ browser }) => {
  psql(`
    INSERT INTO mailboxes (name, email_address, display_name, smtp_host, smtp_port, smtp_username, send_delay_seconds)
      VALUES ('Composer ${stamp}', '${mailboxAddress}', 'Composer Support', 'smtp.example.invalid', 465, '${mailboxAddress}', 30);
    INSERT INTO teams (name) VALUES ('Composer team ${stamp}');
    INSERT INTO users (email, name, role, password_hash) VALUES ('sanne-${stamp}@example.com', '${colleague}', 'agent', 'not-a-hash');
    INSERT INTO team_members (team_id, user_id)
      SELECT teams.id, users.id FROM teams, users WHERE teams.name = 'Composer team ${stamp}' AND users.email = 'sanne-${stamp}@example.com';
    INSERT INTO mailbox_access (mailbox_id, team_id, level)
      SELECT mailboxes.id, teams.id, 'write' FROM mailboxes, teams WHERE mailboxes.email_address = '${mailboxAddress}' AND teams.name = 'Composer team ${stamp}';
    WITH mb AS (SELECT id FROM mailboxes WHERE email_address = '${mailboxAddress}'),
         c AS (INSERT INTO contacts (name) VALUES ('Jan de Vries') RETURNING id),
         ca AS (INSERT INTO contact_addresses (contact_id, email, is_primary) SELECT id, '${customerAddress}', true FROM c),
         conv AS (INSERT INTO conversations (mailbox_id, subject, contact_id, preview, message_count)
                  SELECT mb.id, '${subject}', c.id, '${subject}', 1 FROM mb, c RETURNING id, mailbox_id)
    INSERT INTO messages (conversation_id, mailbox_id, kind, direction, message_id_header, from_addr, from_name, to_addrs, subject, body_text, received_at)
      SELECT conv.id, conv.mailbox_id, 'email', 'in', 'abc-${stamp}@customer.example', '${customerAddress}', 'Jan de Vries',
             '[{"name":"","address":"${mailboxAddress}"}]', '${subject}', 'Klopt mijn factuur wel?', now()
      FROM conv;
  `)
  page = await (await browser.newContext({ viewport: { width: 1600, height: 1000 } })).newPage()
  await signInAsOwner(page)
  await expect(page.getByRole('heading', { name: 'Gesprekken' })).toBeVisible()
})

test.afterAll(async () => {
  await page.context().close()
})

test.describe.configure({ mode: 'serial' })

async function openConversation() {
  await page.goto('/inbox/alle')
  await page.getByRole('link').filter({ hasText: subject }).click()
  await expect(page.getByRole('heading', { level: 2, name: subject })).toBeVisible()
}

const replyEditor = () => page.getByRole('textbox', { name: 'Antwoord', exact: true })
const thread = () => page.getByRole('list').filter({ has: page.getByText('Klopt mijn factuur wel?') })

test('the composer proposes the customer as recipient', async () => {
  const csp: string[] = []
  page.on('console', (msg) => {
    if (msg.text().includes('Content Security Policy')) csp.push(msg.text())
  })
  await openConversation()
  await expect(page.getByRole('tab', { name: 'Antwoorden' })).toHaveAttribute('aria-selected', 'true')
  await expect(page.getByRole('button', { name: `${customerAddress} verwijderen` })).toBeVisible()
  await expect(page.getByRole('button', { name: 'Versturen', exact: true })).toBeDisabled()
  await expect(page.getByText('Handtekening wijzigen')).toHaveCount(0)
  expect(csp).toEqual([])
})

test('a reply is queued with a clock and can be undone within the window', async () => {
  await openConversation()
  await replyEditor().click()
  await page.keyboard.type('Ik kijk er meteen naar.')

  await page.getByRole('button', { name: 'Versturen', exact: true }).click()
  await expect(page.getByText('Verstuurd', { exact: true })).toBeVisible()
  const sent = thread().getByRole('listitem').filter({ hasText: 'Ik kijk er meteen naar.' })
  await expect(sent).toBeVisible()
  await expect(sent.getByTitle('Wordt verzonden')).toBeVisible()
  expect(psql(`SELECT status FROM outbound o JOIN messages m ON m.id = o.message_id WHERE m.body_text LIKE '%Ik kijk er meteen naar.%'`)).toBe('queued')
  await expect(replyEditor()).toHaveText('')

  await page.getByRole('button', { name: 'Ongedaan maken' }).click()
  await expect(page.getByText(/Verzenden ongedaan gemaakt/)).toBeVisible()
  await expect(sent).toHaveCount(0)
  await expect(replyEditor()).toHaveText('Ik kijk er meteen naar.')
  expect(psql(`SELECT status FROM outbound o JOIN messages m ON m.id = o.message_id WHERE m.body_text LIKE '%Ik kijk er meteen naar.%'`)).toBe('cancelled')
})

test('cmd+enter sends the restored reply', async () => {
  await openConversation()
  await expect(replyEditor()).toHaveText('Ik kijk er meteen naar.')
  await replyEditor().click()
  await page.keyboard.press('ControlOrMeta+Enter')
  await expect(thread().getByRole('listitem').filter({ hasText: 'Ik kijk er meteen naar.' }).getByTitle('Wordt verzonden')).toBeVisible()
  await expect(replyEditor()).toHaveText('')
})

test('a note mentions a colleague, who gets a notification', async () => {
  await openConversation()
  await page.getByRole('tab', { name: 'Notitie' }).click()
  await expect(page.getByText('Alleen zichtbaar voor je team')).toBeVisible()
  const note = page.getByRole('textbox', { name: 'Notitie', exact: true })
  await note.click()
  await page.keyboard.type(`Kun jij dit oppakken @Sanne`)
  const option = page.getByRole('option', { name: new RegExp(colleague) })
  await expect(option).toBeVisible()
  await page.keyboard.press('Enter')
  await expect(note).toContainText(`@${colleague}`)
  await page.getByRole('button', { name: 'Notitie toevoegen' }).click()

  const shown = thread().getByRole('listitem').filter({ hasText: `Kun jij dit oppakken @${colleague}` })
  await expect(shown).toBeVisible()
  await expect(shown).toContainText('Interne notitie')
  expect(psql(`SELECT count(*) FROM notifications n JOIN users u ON u.id = n.user_id WHERE u.email = 'sanne-${stamp}@example.com' AND n.kind = 'mention'`)).toBe('1')
})

test('the bell lists notifications and opens the conversation', async () => {
  psql(`INSERT INTO notifications (user_id, kind, conversation_id)
        SELECT users.id, 'assigned', conversations.id FROM users, conversations WHERE users.role = 'owner' AND conversations.subject = '${subject}'`)
  await page.goto('/inbox/alle')
  const bell = page.getByRole('button', { name: /^Meldingen, 1 ongelezen$/ })
  await expect(bell).toBeVisible()
  await bell.click()
  await page.getByRole('menuitem').filter({ hasText: subject }).click()
  await expect(page.getByRole('heading', { level: 2, name: subject })).toBeVisible()
  await expect(page.getByRole('button', { name: 'Meldingen', exact: true })).toBeVisible()
})

test('a canned response is created and inserted with a slash', async () => {
  await page.goto('/instellingen/standaardantwoorden')
  await page.getByRole('button', { name: 'Standaardantwoord toevoegen' }).first().click()
  const dialog = page.getByRole('dialog')
  await dialog.getByLabel('Naam', { exact: true }).fill(templateName)
  await dialog.getByLabel('Snelcode').fill('groet')
  await dialog.getByRole('textbox', { name: 'Tekst van het standaardantwoord' }).click()
  await page.keyboard.type('Hoi ')
  await dialog.getByRole('button', { name: 'Voornaam contactpersoon' }).click()
  await expect(dialog.getByRole('textbox', { name: 'Tekst van het standaardantwoord' })).toBeFocused()
  await page.keyboard.type(', bedankt voor je bericht.')
  await dialog.getByRole('button', { name: 'Opslaan' }).click()
  await expect(page.getByRole('row').filter({ hasText: templateName })).toContainText('/groet')

  await openConversation()
  await replyEditor().click()
  await page.keyboard.type('/gro')
  await expect(page.getByRole('option', { name: new RegExp(templateName) })).toBeVisible()
  await page.keyboard.press('Enter')
  await expect(replyEditor()).toHaveText('Hoi Jan, bedankt voor je bericht.')
})

test('a message is forwarded and a new conversation is started', async () => {
  await openConversation()
  await page.getByRole('button', { name: 'Doorsturen' }).first().click()
  const forward = page.getByRole('dialog')
  await forward.getByLabel('Aan').fill('collega@example.org')
  await forward.getByLabel('Aan').press('Enter')
  await forward.getByRole('button', { name: 'Doorsturen' }).click()
  await expect(page.getByText('Bericht wordt doorgestuurd')).toBeVisible()
  await expect(thread().getByText('Doorgestuurd bericht')).toBeVisible()

  await page.getByRole('button', { name: 'Nieuw gesprek' }).click()
  const dialog = page.getByRole('dialog')
  await dialog.getByLabel('Verstuur vanuit').selectOption({ label: `Composer ${stamp} (${mailboxAddress})` })
  await dialog.getByLabel('Aan').fill('nieuwe-klant@example.org')
  await dialog.getByLabel('Aan').press('Enter')
  await dialog.getByLabel('Onderwerp').fill(`Welkom ${stamp}`)
  await dialog.getByRole('textbox', { name: 'Bericht' }).click()
  await page.keyboard.type('Welkom bij ons.')
  await dialog.getByRole('button', { name: 'Versturen' }).click()
  await expect(page.getByRole('heading', { level: 2, name: `Welkom ${stamp}` })).toBeVisible()
  await expect(page.getByRole('listitem').filter({ hasText: 'Welkom bij ons.' }).getByTitle('Wordt verzonden')).toBeVisible()
})
