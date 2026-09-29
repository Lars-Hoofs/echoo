import { expect, type Page, test } from '@playwright/test'

import { dockerPsql } from './compose'
import { signInAsOwner } from './session'

const ownerEmail = process.env.ECHOO_E2E_OWNER_EMAIL ?? 'owner@example.com'

// Runs after auth.spec.ts, which replaces the owner's temporary password. Mail arrives by IMAP in
// production, so the inbound message is stored with SQL and the rule evaluation is queued the way
// the ingest worker queues it: as a rules.evaluate job in the job table.
const stamp = Date.now()
const invoiceSubject = `Factuur ${stamp}`
const otherSubject = `Openingstijden ${stamp}`
const slaSubject = `Snel antwoord ${stamp}`
const labelName = `Facturen ${stamp}`
const ruleName = `Facturen labelen ${stamp}`
const macroName = `Prioriteit ${stamp}`

function psql(sql: string): string {
  return dockerPsql(['-t', '-A', '-v', 'ON_ERROR_STOP=1', '-c', sql])
}

function seedConversation(subject: string, extraColumns = '', extraValues = '') {
  psql(`WITH c AS (
          INSERT INTO conversations (mailbox_id, subject, preview, last_message_at ${extraColumns})
          SELECT id, '${subject}', '${subject}', now() ${extraValues} FROM mailboxes WHERE email_address = 'regels-${stamp}@example.com'
          RETURNING id, mailbox_id)
        INSERT INTO messages (conversation_id, mailbox_id, kind, direction, from_addr, from_name, subject, body_text, received_at)
        SELECT id, mailbox_id, 'email', 'in', 'klant@example.org', 'Klant', '${subject}', 'Goedemiddag, graag een reactie.', now() FROM c`)
}

function queueEvaluation(subject: string) {
  psql(`INSERT INTO river_job (state, max_attempts, kind, args, queue, priority)
        SELECT 'available', 25, 'rules.evaluate',
               jsonb_build_object('conversation_id', id::text, 'trigger', 'conversation_created'), 'default', 1
        FROM conversations WHERE subject = '${subject}'`)
}

let page: Page

test.beforeAll(async ({ browser }) => {
  // Earlier rules would label the seeded conversations too; the stack is only used for tests.
  psql('DELETE FROM rules')
  psql(`INSERT INTO mailboxes (name, email_address) VALUES ('Regels ${stamp}', 'regels-${stamp}@example.com')`)
  psql(`INSERT INTO labels (name, color_token) VALUES ('${labelName}', 'blue')`)
  psql(`INSERT INTO sla_policies (name, first_response_minutes, resolution_minutes) VALUES ('Snel ${stamp}', 60, 480)`)
  seedConversation(invoiceSubject)
  seedConversation(otherSubject)
  seedConversation(
    slaSubject,
    ', sla_policy_id, sla_state, first_response_due_at, resolution_due_at',
    `, (SELECT id FROM sla_policies WHERE name = 'Snel ${stamp}'), 'at_risk', now() + interval '10 minutes 30 seconds', now() + interval '7 hours'`,
  )
  page = await (await browser.newContext({ viewport: { width: 1600, height: 900 } })).newPage()
  await signInAsOwner(page)
  await expect(page.getByRole('heading', { name: 'Gesprekken' })).toBeVisible()
})

test.afterAll(async () => {
  await page.context().close()
})

test.describe.configure({ mode: 'serial' })

const row = (subject: string) => page.getByRole('link').filter({ hasText: subject })

test('a rule on the subject labels a new conversation', async () => {
  await page.goto('/instellingen/regels')
  await expect(page.getByRole('heading', { name: 'Regels' })).toBeVisible()
  await page.getByRole('button', { name: 'Regel toevoegen' }).first().click()

  const dialog = page.getByRole('dialog', { name: 'Regel toevoegen' })
  await dialog.getByLabel('Naam').fill(ruleName)
  await dialog.getByRole('button', { name: '+ Voorwaarde' }).click()
  await dialog.getByLabel('Vergelijking van voorwaarde 1').selectOption({ label: 'bevat' })
  await dialog.getByLabel('Waarde van voorwaarde 1').fill('factuur')
  await dialog.getByRole('button', { name: '+ Actie' }).click()
  await dialog.getByLabel('Label van actie 1').selectOption({ label: labelName })
  await dialog.getByRole('button', { name: 'Opslaan' }).click()

  await expect(dialog).toBeHidden()
  await expect(page.getByText(ruleName)).toBeVisible()
  await expect(page.getByRole('row').filter({ hasText: ruleName }).getByText(/Als een nieuw gesprek binnenkomt · 1 voorwaarde · dan label toevoegen/)).toBeVisible()
})

test('an invalid rule shows what is wrong and stores nothing', async () => {
  await page.goto('/instellingen/regels')
  await page.getByRole('button', { name: 'Regel toevoegen' }).first().click()
  const dialog = page.getByRole('dialog', { name: 'Regel toevoegen' })
  await dialog.getByLabel('Naam').fill(`Onvolledig ${stamp}`)
  await dialog.getByRole('button', { name: '+ Actie' }).click()
  await dialog.getByRole('button', { name: 'Opslaan' }).click()
  await expect(dialog.getByRole('alert')).toContainText('Controleer de gemarkeerde velden.')
  await dialog.getByRole('button', { name: 'Annuleren' }).click()
  await expect(page.getByText(`Onvolledig ${stamp}`)).toHaveCount(0)
})

test('the evaluation labels the matching conversation only, and the run log says why', async () => {
  queueEvaluation(invoiceSubject)
  queueEvaluation(otherSubject)
  await expect
    .poll(() => psql(`SELECT count(*) FROM rule_runs r JOIN rules ON rules.id = r.rule_id WHERE rules.name = '${ruleName}'`), { timeout: 30_000 })
    .toBe('2')

  await page.goto('/inbox/alle')
  await row(invoiceSubject).click()
  await expect(page.getByRole('heading', { level: 2, name: invoiceSubject })).toBeVisible()
  await expect(page.getByText(`voegde label ${labelName} toe`)).toBeVisible()
  await expect(row(invoiceSubject).getByText(labelName)).toBeVisible()
  await expect(row(otherSubject).getByText(labelName)).toHaveCount(0)

  await page.goto('/instellingen/regels')
  await page.getByRole('button', { name: `Acties voor ${ruleName}` }).click()
  await page.getByRole('menuitem', { name: 'Uitvoeringen' }).click()
  const log = page.getByRole('dialog', { name: `Uitvoeringen van ${ruleName}` })
  await expect(log.getByText('Paste', { exact: true })).toHaveCount(1)
  await expect(log.getByText('Paste niet')).toHaveCount(1)
})

test('rules can be switched off and reordered', async () => {
  await page.goto('/instellingen/regels')
  const toggle = page.getByRole('switch', { name: `${ruleName} actief` })
  await expect(toggle).toBeChecked()
  await toggle.click()
  await expect(toggle).not.toBeChecked()
  await toggle.click()
  await expect(toggle).toBeChecked()
  await expect(page.getByRole('button', { name: `${ruleName} omhoog verplaatsen` })).toBeVisible()
})

test('a conversation with a deadline in reach shows a timer', async () => {
  await page.goto('/inbox/alle')
  const timer = row(slaSubject).getByText(/^Nog \d+ min$/)
  await expect(timer).toBeVisible()
  await expect(row(slaSubject).getByText(/Eerste reactie over \d+ min/)).toBeAttached()

  await row(slaSubject).click()
  await expect(page.getByRole('heading', { level: 2, name: slaSubject })).toBeVisible()
  await expect(page.getByRole('region', { name: /^Gesprek \d+$/ }).getByText(/^Eerste reactie over \d+ min$/).last()).toBeVisible()
})

test('a timer turns red and says how late it is once the deadline passed', async () => {
  psql(`UPDATE conversations SET first_response_due_at = now() - interval '12 minutes' WHERE subject = '${slaSubject}'`)
  await page.goto('/inbox/alle')
  await expect(row(slaSubject).getByText(/^\d+ min te laat$/)).toBeVisible()
})

test('an agent-friendly macro runs from the conversation header', async () => {
  await page.goto('/instellingen/macros')
  await page.getByRole('button', { name: 'Macro toevoegen' }).first().click()
  const dialog = page.getByRole('dialog', { name: 'Macro toevoegen' })
  await dialog.getByLabel('Naam').fill(macroName)
  await dialog.getByRole('button', { name: '+ Actie' }).click()
  await dialog.getByLabel('Actie 1', { exact: true }).selectOption({ label: 'Prioriteit instellen' })
  await dialog.getByLabel('Prioriteit van actie 1').selectOption({ label: 'Urgent' })
  await dialog.getByRole('button', { name: 'Opslaan' }).click()
  await expect(dialog).toBeHidden()
  await expect(page.getByText(macroName)).toBeVisible()

  await page.goto('/inbox/alle')
  await row(otherSubject).click()
  await expect(page.getByRole('heading', { level: 2, name: otherSubject })).toBeVisible()
  await page.getByRole('button', { name: /^Macro/ }).click()
  await page.getByRole('menuitem', { name: macroName }).click()
  await expect(page.getByText(`Macro ${macroName} uitgevoerd`)).toBeVisible()
  await expect(page.getByText(/zette de prioriteit op urgent via een macro/)).toBeVisible()
})

test('the user menu sets availability', async () => {
  await page.goto('/inbox/alle')
  await page.getByRole('button', { name: /^Gebruikersmenu/ }).click()
  await page.getByRole('menuitem', { name: /^Beschikbaarheid/ }).click()
  await page.getByRole('menuitemradio', { name: 'Bezet' }).click()
  await expect.poll(() => psql(`SELECT availability FROM users WHERE email = '${ownerEmail}'`)).toBe('busy')
  await page.getByRole('button', { name: /^Gebruikersmenu/ }).click()
  await page.getByRole('menuitem', { name: /^Beschikbaarheid/ }).click()
  await page.getByRole('menuitemradio', { name: 'Online' }).click()
  await expect.poll(() => psql(`SELECT availability FROM users WHERE email = '${ownerEmail}'`)).toBe('online')
})

test('SLA and assignment settings save', async () => {
  await page.goto('/instellingen/sla')
  await expect(page.getByRole('heading', { name: 'SLA', exact: true })).toBeVisible()
  await expect(page.getByText(`Snel ${stamp}`)).toBeVisible()
  await page.getByRole('button', { name: 'Schema toevoegen' }).click()
  const dialog = page.getByRole('dialog', { name: 'Schema toevoegen' })
  await dialog.getByLabel('Naam').fill(`Kantoor ${stamp}`)
  await dialog.getByLabel('Tijdzone').selectOption('Europe/Amsterdam')
  await dialog.getByRole('button', { name: 'Opslaan' }).click()
  await expect(dialog).toBeHidden()
  await expect(page.getByText(`Kantoor ${stamp}`)).toBeVisible()

  await page.goto('/instellingen/toewijzing')
  await page.getByLabel(`Automatisch toewijzen voor Regels ${stamp}`).selectOption({ label: 'Om de beurt' })
  await expect.poll(() => psql(`SELECT auto_assign_mode FROM mailboxes WHERE email_address = 'regels-${stamp}@example.com'`)).toBe('round_robin')
})
