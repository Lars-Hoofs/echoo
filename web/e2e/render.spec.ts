import { expect, type Page, test } from '@playwright/test'

import { dockerPsql } from './compose'
import { signInAsOwner } from './session'

const ownerTempPassword = process.env.ECHOO_E2E_OWNER_PASSWORD ?? ''

function sql(statement: string): string {
  return dockerPsql(['-At', '-c', statement])
}

const hostileHtml = `<p>Hallo klant</p>
<script>parent.document.title = 'script-ran'; document.body.setAttribute('data-ran', '1')</script>
<img src="x" onerror="parent.document.title = 'onerror-ran'">
<img src="https://images.example.net/track.gif" width="1" height="1">
<a href="https://evil.example/login">https://bank.example.com/login</a>`

function seedMessage(mailbox: string, subject: string, from: string): string {
  const out = sql(
    `WITH c AS (INSERT INTO conversations (mailbox_id, subject, status, last_message_at) VALUES ('${mailbox}', '${subject}', 'open', now()) RETURNING id), ` +
      `m AS (INSERT INTO messages (conversation_id, mailbox_id, kind, direction, from_addr, from_name, to_addrs, subject, body_text, body_html, received_at) ` +
      `SELECT c.id, '${mailbox}', 'email', 'in', '${from}', 'Klant', '[]', '${subject}', 'Hallo klant', $html$${hostileHtml}$html$, now() FROM c RETURNING id) ` +
      `SELECT c.id FROM c`,
  )
  return out.split('\n')[0] ?? ''
}

async function login(page: Page) {
  await signInAsOwner(page)
  await expect(page.getByRole('heading', { name: 'Gesprekken' })).toBeVisible()
}

test('HTML mail renders in a sandboxed frame, scripts do not run and remote images are blocked', async ({ page }) => {
  test.skip(!ownerTempPassword, 'ECHOO_E2E_OWNER_PASSWORD is not set')
  test.setTimeout(90_000)
  const stamp = Date.now()
  const violations: string[] = []
  page.on('console', (msg) => {
    if (msg.text().includes('Content Security Policy')) violations.push(msg.text())
  })

  await login(page)
  const origin = new URL(page.url()).origin
  const me = (await (await page.request.get(`${origin}/api/v1/me`)).json()) as { csrf_token: string }
  const created = await page.request.fetch(`${origin}/api/v1/mailboxes`, {
    method: 'POST',
    headers: { Origin: origin, 'X-CSRF-Token': me.csrf_token },
    data: {
      name: 'Render',
      email_address: `render-${stamp}@example.com`,
      display_name: 'Render',
      imap_host: 'imap.example.invalid',
      imap_port: 993,
      imap_tls: 'implicit',
      imap_username: 'render',
      imap_password: 'geheim-imap-wachtwoord',
      smtp_host: 'smtp.example.invalid',
      smtp_port: 465,
      smtp_tls: 'implicit',
      smtp_username: 'render',
      smtp_password: 'geheim-smtp-wachtwoord',
      sent_folder: 'Sent',
      send_delay_seconds: 5,
    },
  })
  expect(created.ok(), `create mailbox: ${created.status()}`).toBe(true)
  const mailbox = ((await created.json()) as { mailbox: { id: string } }).mailbox.id

  const first = seedMessage(mailbox, `Render eenmalig ${stamp}`, 'once@sender.example')
  await page.goto(`/inbox/alle/${first}`)
  await expect(page.getByRole('heading', { name: `Render eenmalig ${stamp}` })).toBeVisible()
  const titleBefore = await page.title()

  const frame = page.frameLocator('iframe[title="Inhoud van het bericht"]')
  await expect(frame.getByText('Hallo klant')).toBeVisible()
  await expect(page.getByText('Externe afbeeldingen zijn geblokkeerd.')).toBeVisible()
  await expect(page.getByText('Een link toont bank.example.com, maar leidt naar evil.example.')).toBeVisible()

  // Neither the script nor the onerror handler ran, inside the frame or against the parent.
  await expect(frame.locator('body')).not.toHaveAttribute('data-ran', '1')
  await expect(frame.locator('script')).toHaveCount(1)
  await page.waitForTimeout(500)
  expect(await page.title()).toBe(titleBefore)

  // The frame has no origin of its own to script the app with, and its height follows its content.
  const iframe = page.locator('iframe[title="Inhoud van het bericht"]')
  await expect(iframe).toHaveAttribute('sandbox', 'allow-scripts allow-popups allow-popups-to-escape-sandbox')
  const height = (await iframe.boundingBox())?.height ?? 0
  expect(height).toBeGreaterThan(40)
  expect(height).toBeLessThan(600)

  await page.getByRole('button', { name: 'Eenmalig tonen' }).click()
  await expect(page.getByText('Externe afbeeldingen zijn geblokkeerd.')).toBeHidden()

  // Allowing a sender persists: a second message from the same address shows no banner.
  const second = seedMessage(mailbox, `Render altijd ${stamp}`, 'always@sender.example')
  await page.goto(`/inbox/alle/${second}`)
  await expect(page.getByText('Externe afbeeldingen zijn geblokkeerd.')).toBeVisible()
  await page.getByRole('button', { name: 'Altijd tonen van dit adres' }).click()
  await expect(page.getByText('Externe afbeeldingen zijn geblokkeerd.')).toBeHidden()
  await page.reload()
  await expect(page.getByRole('heading', { name: `Render altijd ${stamp}` })).toBeVisible()
  await expect(frame.getByText('Hallo klant')).toBeVisible()
  await expect(page.getByText('Externe afbeeldingen zijn geblokkeerd.')).toBeHidden()

  expect(violations).toEqual([])
})

test('long pages scroll inside the shell, never the document', async ({ page }) => {
  test.skip(!ownerTempPassword, 'ECHOO_E2E_OWNER_PASSWORD is not set')
  await page.setViewportSize({ width: 1440, height: 700 })
  await login(page)
  for (const path of ['/instellingen/profiel', '/rapportage', '/contacten']) {
    await page.goto(path)
    await expect(page.locator('main')).toBeVisible()
    const overflow = await page.evaluate(() => document.documentElement.scrollHeight - window.innerHeight)
    expect(overflow, path).toBeLessThanOrEqual(0)
  }
})
