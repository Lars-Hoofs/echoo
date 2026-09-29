import { createHash, createHmac } from 'node:crypto'
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'

import { expect, type Locator, type Page, test } from '@playwright/test'

import { dockerPsql } from './compose'
import { signInAsOwner } from './session'

const ownerEmail = process.env.ECHOO_E2E_OWNER_EMAIL ?? 'owner@example.com'
const repoRoot = resolve(process.cwd(), '..')

// Runs after auth.spec.ts. Conversations are stored with SQL, the way mail ingest stores them,
// relative to local midnight in the workspace timezone so a run near midnight counts the same.
const stamp = Date.now()
const mailboxEmail = `rapport-${stamp}@example.com`
const mailboxName = `Rapport ${stamp}`

function psql(sql: string): string {
  return dockerPsql(['-t', '-A', '-v', 'ON_ERROR_STOP=1', '-c', sql])
}

const mailboxId = () => psql(`SELECT id FROM mailboxes WHERE email_address = '${mailboxEmail}'`)

// Twelve conversations in the last seven days (three today), four older ones. Even numbers are
// answered after 30 minutes and resolved after two hours, by the owner.
function seed() {
  psql(`INSERT INTO mailboxes (name, email_address) VALUES ('${mailboxName}', '${mailboxEmail}')`)
  psql(`
    INSERT INTO conversations (mailbox_id, subject, status, assignee_user_id, created_at, last_message_at, first_responded_at, first_response_met_at, resolved_at)
    SELECT mb.id, 'Rapport ${stamp} ' || d.n, CASE WHEN d.n % 2 = 0 THEN 'closed' ELSE 'open' END, me.id,
           t.at, t.at, t.at + interval '30 minutes', t.at + interval '30 minutes',
           CASE WHEN d.n % 2 = 0 THEN t.at + interval '2 hours' END
    FROM mailboxes mb
    JOIN users me ON me.email = '${ownerEmail}'
    CROSS JOIN (VALUES (1, 0), (2, 0), (3, 0), (4, 1), (5, 2), (6, 3), (7, 3), (8, 4), (9, 5), (10, 6), (11, 6), (12, 6),
                       (13, 12), (14, 15), (15, 18), (16, 20)) d(n, ago)
    CROSS JOIN LATERAL (SELECT (date_trunc('day', now() AT TIME ZONE 'Europe/Amsterdam') AT TIME ZONE 'Europe/Amsterdam')
                               - d.ago * interval '1 day' + interval '10 hours' AS at) t
    WHERE mb.email_address = '${mailboxEmail}'`)
  psql(`
    INSERT INTO conversation_events (conversation_id, mailbox_id, type, data, created_at)
    SELECT id, mailbox_id, 'created', '{"reason":"new_conversation"}', created_at FROM conversations WHERE mailbox_id = '${mailboxId()}'`)
  psql(`
    INSERT INTO conversation_events (conversation_id, mailbox_id, actor_user_id, type, data, created_at)
    SELECT id, mailbox_id, assignee_user_id, 'resolved', '{"from":"open"}', resolved_at FROM conversations WHERE mailbox_id = '${mailboxId()}' AND resolved_at IS NOT NULL`)
  psql(`
    INSERT INTO messages (conversation_id, mailbox_id, kind, direction, message_id_header, from_addr, subject, received_at)
    SELECT id, mailbox_id, 'email', 'in', 'in-' || id::text || '@example.org', 'klant@example.org', subject, created_at FROM conversations WHERE mailbox_id = '${mailboxId()}'`)
  psql(`
    INSERT INTO messages (conversation_id, mailbox_id, kind, direction, message_id_header, from_addr, subject, received_at, sent_at, author_user_id)
    SELECT id, mailbox_id, 'email', 'out', 'out-' || id::text || '@example.org', '${mailboxEmail}', subject, first_responded_at, first_responded_at, assignee_user_id
    FROM conversations WHERE mailbox_id = '${mailboxId()}'`)
}

// Survey links are signed with a key derived from the encryption keyring (internal/csat), so the
// test derives the same key from the stack's key file and issues a token the way the server does.
function surveyToken(conversationId: string, expires: Date): string {
  const keys = readFileSync(resolve(repoRoot, 'conf/encryption_keys'), 'utf8').trim().split(/[\s,]+/)
  const active = Buffer.from((keys[0] ?? '').split(':')[1] ?? '', 'base64')
  const key = createHmac('sha256', active).update('echoo/derive/csat-token').digest()
  const body = Buffer.concat([Buffer.from(conversationId.replaceAll('-', ''), 'hex'), Buffer.alloc(8)])
  body.writeBigUInt64BE(BigInt(Math.floor(expires.getTime() / 1000)), 16)
  const mac = createHmac('sha256', key).update(Buffer.concat([Buffer.from('token\0'), body])).digest()
  return Buffer.concat([body, mac]).toString('base64url')
}

function seedSurvey(): { conversation: string; token: string } {
  const conversation = psql(`
    INSERT INTO conversations (mailbox_id, subject, status, assignee_user_id, resolved_at, created_at)
    SELECT mb.id, 'Tevredenheid ${stamp}', 'closed', me.id, now() - interval '2 hours', now() - interval '1 day'
    FROM mailboxes mb JOIN users me ON me.email = '${ownerEmail}' WHERE mb.email_address = '${mailboxEmail}'
    RETURNING id`).split('\n')[0] ?? ''
  const token = surveyToken(conversation, new Date(Date.now() + 30 * 24 * 3600 * 1000))
  const hash = createHash('sha256').update(token).digest('hex')
  psql(`INSERT INTO csat_requests (conversation_id, mailbox_id, token_hash, sent_at, expires_at)
        SELECT '${conversation}', mailbox_id, decode('${hash}', 'hex'), now() - interval '1 hour', now() + interval '30 days'
        FROM conversations WHERE id = '${conversation}'`)
  return { conversation, token }
}

function kpi(page: Page, label: string): Locator {
  return page.getByLabel('Kerncijfers').locator('div').filter({ has: page.getByText(label, { exact: true }) }).locator('dd').first()
}

let page: Page

test.beforeAll(async ({ browser }) => {
  seed()
  page = await (await browser.newContext({ viewport: { width: 1400, height: 1000 } })).newPage()
  await signInAsOwner(page)
  await expect(page.getByRole('heading', { name: 'Gesprekken' })).toBeVisible()
})

test.afterAll(async () => {
  await page.context().close()
})

test.describe.configure({ mode: 'serial' })

test('the overview shows KPIs and charts, follows the period and exports CSV', async () => {
  const mailbox = mailboxId()
  await page.getByRole('link', { name: 'Rapportage' }).click()
  await expect(page).toHaveURL(/\/rapportage\/overzicht/)
  await expect(page.getByRole('heading', { name: 'Rapportage' })).toBeVisible()
  await expect(page.getByRole('tablist', { name: 'Rapporten' }).getByRole('tab')).toHaveText(['Overzicht', 'Agenten', 'Teams', 'Mailboxen', 'Labels', 'Tevredenheid', 'Live'])

  await page.goto(`/rapportage/overzicht?mailbox=${mailbox}`)
  await expect(page.getByText(/^Periode .* · tijden in Europe\/Amsterdam$/)).toBeVisible()
  await expect(kpi(page, 'Nieuwe gesprekken')).toHaveText('12')
  await expect(kpi(page, 'Eerste reactie (mediaan)')).toHaveText('30 min')
  await expect(kpi(page, 'Oplostijd (mediaan)')).toHaveText('2 u')
  await expect(page.getByLabel('Aanvullende cijfers')).toContainText('Opgelost')
  const chart = page.getByRole('img', { name: 'Nieuwe gesprekken per dag' })
  await expect(chart).toBeVisible()
  await expect(chart.locator('rect, path').first()).toBeVisible()
  await expect(page.getByRole('img', { name: /^Eerste reactie Liniaal van/ })).toBeVisible()

  // The period control changes every number.
  await page.getByRole('radio', { name: '30 d' }).check({ force: true })
  await expect(kpi(page, 'Nieuwe gesprekken')).toHaveText('16')
  await page.getByRole('radio', { name: 'Vandaag' }).check({ force: true })
  await expect(kpi(page, 'Nieuwe gesprekken')).toHaveText('3')
  await page.getByRole('radio', { name: '7 d' }).check({ force: true })
  await expect(kpi(page, 'Nieuwe gesprekken')).toHaveText('12')

  // A custom period of one past day.
  await page.getByRole('radio', { name: 'Aangepast' }).check({ force: true })
  const day = await page.evaluate(() => {
    const d = new Date(Date.now() - 6 * 24 * 3600 * 1000)
    return new Intl.DateTimeFormat('sv-SE', { timeZone: 'Europe/Amsterdam' }).format(d)
  })
  await page.getByLabel('Van', { exact: true }).fill(day)
  await page.getByLabel('Tot en met').fill(day)
  await expect(kpi(page, 'Nieuwe gesprekken')).toHaveText('3')

  // Export the overview of the last seven days.
  await page.getByRole('radio', { name: '7 d' }).check({ force: true })
  await expect(kpi(page, 'Nieuwe gesprekken')).toHaveText('12')
  const [download] = await Promise.all([page.waitForEvent('download'), page.getByRole('link', { name: 'Exporteren' }).first().click()])
  expect(download.suggestedFilename()).toMatch(/^rapportage-overview-\d{4}-\d{2}-\d{2}-\d{4}-\d{2}-\d{2}\.csv$/)
  const csv = readFileSync(await download.path(), 'utf8')
  const lines = csv.trim().split('\n')
  expect(lines[0]).toMatch(/^datum,nieuwe_gesprekken,klantberichten,antwoorden,opgelost,heropend,/)
  expect(lines).toHaveLength(8)
  const total = lines.slice(1).reduce((sum, l) => sum + Number(l.split(',')[1]), 0)
  expect(total).toBe(12)
})

test('the agent table shows the owner and the live tab shows the state now', async () => {
  const mailbox = mailboxId()
  await page.goto(`/rapportage/agenten?mailbox=${mailbox}`)
  const ownerName = psql(`SELECT name FROM users WHERE email = '${ownerEmail}'`)
  const row = page.getByRole('row').filter({ hasText: ownerName })
  await expect(row).toContainText('12')

  await page.goto(`/rapportage/live?mailbox=${mailbox}`)
  const now = page.locator('dl[aria-label="Nu"]')
  await expect(now).toContainText('Open')
  await expect(now).toContainText('Wachtend')
  await expect(page.getByText(/ververst elke 30 seconden/)).toBeVisible()
  await expect(page.getByRole('row').filter({ hasText: 'In Echoo' })).toBeVisible()
})

test('a customer rates a conversation on the public page and the rating shows in the reports', async ({ browser, baseURL }) => {
  const { conversation, token } = seedSurvey()
  const mailbox = mailboxId()

  // No session, no app chrome: a fresh browser context.
  const customer = await browser.newContext(baseURL ? { baseURL } : {})
  const survey = await customer.newPage()
  await survey.goto(`/tevredenheid/${token}?r=2`)
  await expect(survey.getByRole('heading', { name: 'Uw mening' })).toBeVisible()
  await expect(survey.getByRole('link')).toHaveCount(0)
  await expect(survey.getByRole('radio', { name: '2' })).toBeChecked()
  expect(await survey.evaluate(() => document.cookie)).toBe('')

  await survey.locator('label[for="r5"]').click()
  await survey.getByLabel(/Wilt u iets toelichten/).fill('Snel en vriendelijk geholpen')
  await survey.getByRole('button', { name: 'Verstuur' }).click()
  await expect(survey.getByText('Uw beoordeling is opgeslagen.')).toBeVisible()
  await expect(survey.getByRole('radio', { name: '5' })).toBeChecked()
  expect(psql(`SELECT rating || ':' || comment FROM csat_responses WHERE conversation_id = '${conversation}'`)).toBe('5:Snel en vriendelijk geholpen')

  // A change within the week replaces the answer instead of adding one.
  await survey.locator('label[for="r4"]').click()
  await survey.getByRole('button', { name: 'Wijzig mijn beoordeling' }).click()
  await expect(survey.getByText('Uw beoordeling is opgeslagen.')).toBeVisible()
  expect(psql(`SELECT count(*) || ':' || max(rating) FROM csat_responses WHERE conversation_id = '${conversation}'`)).toBe('1:4')

  // A tampered link is refused.
  const bad = await survey.goto(`/tevredenheid/${token.slice(0, -2)}xx`)
  expect(bad?.status()).toBe(404)
  await customer.close()

  await page.goto(`/rapportage/tevredenheid?mailbox=${mailbox}`)
  await expect(page.locator('dl[aria-label="Tevredenheid"]')).toContainText('4,0 van 5')
  await expect(page.getByText('Snel en vriendelijk geholpen')).toBeVisible()

  // The rating is also shown with the conversation.
  await page.goto(`/inbox/alle/${conversation}`)
  await expect(page.getByText(/Tevredenheid.*4 van 5/)).toBeVisible()
})
