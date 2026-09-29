import { createHmac, randomUUID } from 'node:crypto'

import { expect, type Page, test } from '@playwright/test'

import { dockerPsql } from './compose'
import { signInAsOwner } from './session'

const ownerEmail = process.env.ECHOO_E2E_OWNER_EMAIL ?? 'owner@example.com'

// Runs after auth.spec.ts. The mailbox points at an SMTP host that does not resolve, so every
// message of the campaign stays in the send queue and the counts can be looked at. The rate is
// one message per tick, which leaves time to pause between two of them. Contacts and the segment
// are stored with SQL; the segment only matches the contacts of this run.
const stamp = Date.now()
const domain = `campagne-${stamp}.example`
const mailboxName = `Campagne ${stamp}`
const mailboxAddress = `campagne-${stamp}@example.com`
const segmentName = `Segment ${stamp}`
const campaignName = `Herfstactie ${stamp}`

function psql(sql: string): string {
  return dockerPsql(['-t', '-A', '-v', 'ON_ERROR_STOP=1', '-c', sql])
}

test.describe.configure({ mode: 'serial' })

let page: Page

test.beforeAll(async ({ browser }) => {
  psql(`
    INSERT INTO mailboxes (name, email_address, display_name, smtp_host, smtp_port, smtp_username, send_delay_seconds)
      VALUES ('${mailboxName}', '${mailboxAddress}', 'Campagne Shop', 'smtp.example.invalid', 465, '${mailboxAddress}', 0);
    WITH c AS (INSERT INTO contacts (name) SELECT 'Klant ' || g FROM generate_series(1, 12) g RETURNING id, name)
      INSERT INTO contact_addresses (contact_id, email, is_primary)
      SELECT id, lower(replace(name, ' ', '.')) || '@${domain}', true FROM c;
    UPDATE contacts SET unsubscribed_at = now()
      WHERE id = (SELECT contact_id FROM contact_addresses WHERE email = 'klant.12@${domain}');
    INSERT INTO contact_segments (name, owner_user_id, shared, filter)
      SELECT '${segmentName}', id, true, '{"match":"all","conditions":[{"field":"domain","op":"equals","value":"${domain}"}]}'
      FROM users WHERE email = '${ownerEmail}';
  `)
  page = await (await browser.newContext({ viewport: { width: 1400, height: 1000 } })).newPage()
  await signInAsOwner(page)
})

test.afterAll(async () => {
  await page.context().close()
})

const count = (label: string) => page.locator('dt', { hasText: new RegExp(`^${label}$`) }).locator('xpath=following-sibling::dd[1]')
const numberOf = async (label: string) => Number((await count(label).innerText()).replace(/\D/g, ''))

test('an owner builds a campaign in the wizard and starts it', async () => {
  test.setTimeout(120_000)
  await page.goto('/campagnes')
  await expect(page.getByRole('heading', { name: 'Campagnes' })).toBeVisible()
  await expect(page.getByRole('link', { name: 'Campagnes' }).first()).toBeVisible()
  await page.getByRole('link', { name: 'Nieuwe campagne' }).click()

  // Step 1: mailbox
  await page.getByRole('button', { name: 'Volgende' }).click()
  await expect(page.getByText('Geef de campagne een naam.')).toBeVisible()
  await expect(page.getByText('Kies een mailbox.')).toBeVisible()
  await page.getByLabel('Naam van de campagne').fill(campaignName)
  await page.getByLabel('Mailbox', { exact: true }).selectOption({ label: `${mailboxName} (${mailboxAddress})` })
  await page.getByLabel('Verzendsnelheid (berichten per minuut)').fill('500')
  await page.getByRole('button', { name: 'Volgende' }).click()
  await expect(page.getByText('Vul een aantal in van 1 tot 120.')).toBeVisible()
  await page.getByLabel('Verzendsnelheid (berichten per minuut)').fill('12')
  await page.getByRole('button', { name: 'Volgende' }).click()

  // Step 2: segment with a recipient count
  await expect(page).toHaveURL(/\/campagnes\/[0-9a-f-]{36}\/bewerken\?stap=segment/)
  await page.getByRole('button', { name: 'Volgende' }).click()
  await expect(page.getByText('Kies een segment.')).toBeVisible()
  await page.getByLabel('Segment', { exact: true }).selectOption({ label: segmentName })
  await expect(page.getByText('11 ontvangers')).toBeVisible()
  await expect(page.getByText('Wordt overgeslagen: 1 afgemeld.')).toBeVisible()
  await page.getByRole('button', { name: 'Volgende' }).click()

  // Step 3: content, with a test mail to myself that cannot be delivered
  await page.getByRole('button', { name: 'Volgende' }).click()
  await expect(page.getByText('Vul een onderwerp in.')).toBeVisible()
  await page.getByLabel('Onderwerp').fill('Herfstactie voor {{contact.first_name}}')
  const editor = page.getByRole('textbox', { name: 'Tekst van de e-mail' })
  await editor.click()
  await page.keyboard.type('Hallo ')
  await page.getByRole('button', { name: 'Voornaam contactpersoon' }).click()
  await page.keyboard.type(', bekijk onze herfstaanbieding.')
  await page.getByRole('button', { name: 'Testmail naar mezelf' }).click()
  await expect(page.getByRole('alert').filter({ hasText: 'De testmail kon niet worden verstuurd.' })).toBeVisible({ timeout: 40_000 })
  await page.getByRole('button', { name: 'Volgende' }).click()

  // Step 4: planning, start now
  await expect(page.getByRole('heading', { name: 'Planning' })).toBeVisible()
  await expect(page.getByText('12 per minuut')).toBeVisible()
  await page.getByText('Inplannen', { exact: true }).click()
  await page.getByRole('button', { name: 'Inplannen' }).click()
  await expect(page.getByText('Kies een datum en tijd.')).toBeVisible()
  await page.getByText('Nu versturen', { exact: true }).click()
  await page.getByRole('button', { name: 'Versturen' }).click()
  await page.getByRole('dialog').getByRole('button', { name: 'Nu versturen' }).click()

  await expect(page.getByRole('heading', { name: campaignName })).toBeVisible()
  await expect(page.getByText('Bezig met verzenden')).toBeVisible()
})

test('progress moves on, can be paused and resumed, and is cancelled', async () => {
  test.setTimeout(180_000)
  await expect(page.getByRole('heading', { name: campaignName })).toBeVisible()
  await expect(count('Ontvangers')).toHaveText('12', { timeout: 20_000 })
  await expect(count('Overgeslagen')).toHaveText('1')

  // One message per tick reaches the send queue, where the unreachable server keeps it.
  await expect.poll(() => numberOf('In wachtrij'), { timeout: 30_000 }).toBeGreaterThanOrEqual(2)

  await page.getByRole('button', { name: 'Pauzeren' }).click()
  await expect(page.getByText('Gepauzeerd', { exact: true })).toBeVisible()
  const paused = await numberOf('In wachtrij')
  await page.waitForTimeout(12_000)
  expect(await numberOf('In wachtrij')).toBe(paused)

  await page.getByRole('button', { name: 'Hervatten' }).click()
  await expect(page.getByText('Bezig met verzenden')).toBeVisible()
  await expect.poll(() => numberOf('In wachtrij'), { timeout: 30_000 }).toBeGreaterThan(paused)
  await expect(page.getByRole('progressbar', { name: 'Voortgang' })).toBeVisible()

  await page.getByRole('button', { name: 'Annuleren' }).click()
  await page.getByRole('dialog').getByRole('button', { name: 'Campagne annuleren' }).click()
  await expect(page.getByText('Geannuleerd', { exact: true })).toBeVisible()
  await expect.poll(() => numberOf('Nog te gaan')).toBe(0)
  await expect.poll(() => numberOf('In wachtrij'), { timeout: 30_000 }).toBe(0)
  expect(await numberOf('Overgeslagen')).toBe(12)

  // The report lists everybody, with the reason for the skipped ones.
  await page.getByRole('radio', { name: 'Overgeslagen' }).check({ force: true })
  await expect(page.getByRole('row').filter({ hasText: 'Afgemeld' })).toHaveCount(1)
  await expect(page.getByRole('row').filter({ hasText: 'Campagne geannuleerd' }).first()).toBeVisible()
  await expect(page.getByRole('link', { name: 'Exporteren' })).toHaveAttribute('href', /\/api\/v1\/campaigns\/[0-9a-f-]{36}\/report$/)
})

// The link in a campaign mail is HMAC-signed with a per-recipient secret; the spec builds one
// from a seeded recipient the way the dispatcher does.
function seededToken(): { token: string; contact: string } {
  const contact = psql(`SELECT id FROM contacts WHERE id = (SELECT contact_id FROM contact_addresses WHERE email = 'klant.1@${domain}')`)
  const campaign = psql(`SELECT id FROM campaigns WHERE name = '${campaignName}'`)
  const recipient = randomUUID()
  const secret = randomUUID()
  psql(`
    INSERT INTO campaign_recipients (id, campaign_id, contact_id, email, name, state, unsubscribe_secret)
    VALUES ('${recipient}', '${campaign}', '${contact}', 'klant.1@${domain}', 'Klant 1', 'sent', '${secret}')
    ON CONFLICT (campaign_id, contact_id) DO UPDATE SET id = EXCLUDED.id, unsubscribe_secret = EXCLUDED.unsubscribe_secret;
  `)
  const body = Buffer.concat([Buffer.from(recipient.replaceAll('-', ''), 'hex'), Buffer.alloc(8)])
  body.writeBigUInt64BE(BigInt(Math.floor(Date.now() / 1000) + 3600), 16)
  const mac = createHmac('sha256', Buffer.from(secret.replaceAll('-', ''), 'hex')).update(body).digest()
  return { token: Buffer.concat([body, mac]).toString('base64url'), contact }
}

test('a contact unsubscribes from the public page without signing in', async ({ browser }) => {
  const { token, contact } = seededToken()
  const context = await browser.newContext()
  const visitor = await context.newPage()
  const violations: string[] = []
  visitor.on('console', (msg) => {
    if (msg.text().includes('Content Security Policy')) violations.push(msg.text())
  })

  await visitor.goto(`/afmelden/${token}x`)
  await expect(visitor.getByRole('heading', { name: 'Link ongeldig' })).toBeVisible()

  await visitor.goto(`/afmelden/${token}`)
  await expect(visitor.getByRole('heading', { name: 'Afmelden' })).toBeVisible()
  await expect(visitor.getByText('k***@')).toBeVisible()
  expect(psql(`SELECT unsubscribed_at IS NOT NULL FROM contacts WHERE id = '${contact}'`)).toBe('f')

  await visitor.getByRole('button', { name: 'Afmelden' }).click()
  await expect(visitor.getByRole('heading', { name: 'Je bent afgemeld' })).toBeVisible()
  expect(psql(`SELECT unsubscribed_at IS NOT NULL FROM contacts WHERE id = '${contact}'`)).toBe('t')
  expect(violations).toEqual([])
  await context.close()
})
