import { type APIRequestContext, expect, type Page, test } from '@playwright/test'

import { dockerPsql } from './compose'
import { loginSlot } from './session'

const ownerEmail = process.env.ECHOO_E2E_OWNER_EMAIL ?? 'owner@example.com'
const ownerTempPassword = process.env.ECHOO_E2E_OWNER_PASSWORD ?? ''
// Set by the first test of auth.spec.ts, which runs before this file.
const ownerPassword = 'een lange zin als wachtwoord'
const agentPassword = 'nog een lange zin voor de agent'

test.describe.configure({ mode: 'serial' })

function sql(statement: string): string {
  return dockerPsql(['-At', '-c', statement])
}

function notifyConversation(id: string, mailbox: string): string {
  return `SELECT pg_notify('echoo_events', json_build_object('type', 'conversation.updated', 'conversation_id', '${id}', 'mailbox_id', '${mailbox}')::text)`
}

function insertConversation(mailbox: string, subject: string): string {
  const out = sql(
    `WITH c AS (INSERT INTO conversations (mailbox_id, subject, status, last_message_at) VALUES ('${mailbox}', '${subject}', 'open', now()) RETURNING id) ` +
      `SELECT id FROM c`,
  )
  const id = out.split('\n')[0] ?? ''
  sql(notifyConversation(id, mailbox))
  return id
}

async function login(page: Page, email: string, password: string) {
  await loginSlot()
  await page.goto('/')
  await page.getByLabel('E-mailadres').fill(email)
  await page.getByLabel('Wachtwoord').fill(password)
  await page.getByRole('button', { name: 'Inloggen' }).click()
}

async function post<T>(request: APIRequestContext, origin: string, csrf: string, method: 'POST' | 'PUT', path: string, data: unknown): Promise<T> {
  const res = await request.fetch(`${origin}/api/v1${path}`, {
    method,
    data,
    headers: { Origin: origin, 'X-CSRF-Token': csrf },
  })
  expect(res.ok(), `${method} ${path}: ${res.status()}`).toBe(true)
  return (res.status() === 204 ? undefined : await res.json()) as T
}

test('a colleague sees new conversations and who else has one open, without reloading', async ({ browser }) => {
  test.skip(!ownerTempPassword, 'ECHOO_E2E_OWNER_PASSWORD is not set')
  test.setTimeout(120_000)
  const stamp = Date.now()
  const agentEmail = `live-${stamp}@example.com`

  const ownerContext = await browser.newContext()
  const owner = await ownerContext.newPage()
  await login(owner, ownerEmail, ownerPassword)
  await expect(owner.getByRole('heading', { name: 'Gesprekken' })).toBeVisible()
  const origin = new URL(owner.url()).origin
  const me = (await (await owner.request.get(`${origin}/api/v1/me`)).json()) as { user: { name: string }; csrf_token: string }
  const csrf = me.csrf_token

  const created = await post<{ user: { id: string }; temporary_password: string }>(owner.request, origin, csrf, 'POST', '/users', {
    email: agentEmail,
    name: 'Sanne de Vries',
    role: 'agent',
  })
  const team = await post<{ team: { id: string } }>(owner.request, origin, csrf, 'POST', '/teams', { name: `Live ${stamp}` })
  await post(owner.request, origin, csrf, 'PUT', `/teams/${team.team.id}/members`, { user_ids: [created.user.id] })
  const address = `live-${stamp}@example.com`
  const mailbox = await post<{ mailbox: { id: string } }>(owner.request, origin, csrf, 'POST', '/mailboxes', {
    name: 'Live',
    email_address: address,
    display_name: 'Live',
    imap_host: 'imap.example.invalid',
    imap_port: 993,
    imap_tls: 'implicit',
    imap_username: address,
    imap_password: 'geheim-imap-wachtwoord',
    smtp_host: 'smtp.example.invalid',
    smtp_port: 465,
    smtp_tls: 'implicit',
    smtp_username: address,
    smtp_password: 'geheim-smtp-wachtwoord',
    sent_folder: 'Sent',
    send_delay_seconds: 5,
  })
  const mailboxId = mailbox.mailbox.id
  await post(owner.request, origin, csrf, 'PUT', `/mailboxes/${mailboxId}/access`, {
    access: [{ team_id: team.team.id, level: 'write' }],
  })

  const agentContext = await browser.newContext()
  const agent = await agentContext.newPage()
  await login(agent, agentEmail, created.temporary_password)
  await agent.getByLabel('Tijdelijk wachtwoord').fill(created.temporary_password)
  await agent.getByLabel('Nieuw wachtwoord').fill(agentPassword)
  await agent.getByRole('button', { name: 'Wachtwoord wijzigen' }).click()
  await expect(agent.getByRole('heading', { name: 'Gesprekken' })).toBeVisible()
  await agent.goto('/inbox/alle')

  // Pushed over the event stream: the list polls only every 30 s, so this proves the stream works.
  const first = insertConversation(mailboxId, `Live gesprek ${stamp}`)
  await expect(agent.getByText(`Live gesprek ${stamp}`)).toBeVisible({ timeout: 10_000 })

  await agent.getByText(`Live gesprek ${stamp}`).click()
  await expect(agent.getByRole('heading', { name: `Live gesprek ${stamp}` })).toBeVisible()
  await expect(agent.getByText('Ook geopend door')).toHaveCount(0)

  await owner.goto(`/inbox/alle/${first}`)
  await expect(agent.getByText(`Ook geopend door ${me.user.name}`)).toBeVisible({ timeout: 10_000 })

  // Leaving inside the app reports "left"; a closed tab is only forgotten after the 30 s TTL.
  await owner.getByRole('navigation', { name: 'Inbox' }).getByRole('link', { name: /Alle gesprekken/ }).click()
  await expect(agent.getByText('Ook geopend door')).toHaveCount(0, { timeout: 10_000 })

  // Someone outside the mailbox's teams never hears about it: the owner sees everything, so
  // the reverse check uses a second mailbox the agent has no access to.
  const other = await post<{ mailbox: { id: string } }>(owner.request, origin, csrf, 'POST', '/mailboxes', {
    name: 'Verborgen',
    email_address: `hidden-${stamp}@example.com`,
    display_name: 'Verborgen',
    imap_host: 'imap.example.invalid',
    imap_port: 993,
    imap_tls: 'implicit',
    imap_username: 'hidden',
    imap_password: 'geheim-imap-wachtwoord',
    smtp_host: 'smtp.example.invalid',
    smtp_port: 465,
    smtp_tls: 'implicit',
    smtp_username: 'hidden',
    smtp_password: 'geheim-smtp-wachtwoord',
    sent_folder: 'Sent',
    send_delay_seconds: 5,
  })
  const hiddenSubject = `Verborgen gesprek ${stamp}`
  insertConversation(other.mailbox.id, hiddenSubject)
  await expect(owner.getByText(hiddenSubject)).toBeVisible({ timeout: 10_000 })
  await agent.waitForTimeout(1500)
  await expect(agent.getByText(hiddenSubject)).toHaveCount(0)

  await ownerContext.close()
  await agentContext.close()
})

test('a new conversation waits behind a pill while the list is scrolled down', async ({ browser }) => {
  test.skip(!ownerTempPassword, 'ECHOO_E2E_OWNER_PASSWORD is not set')
  test.setTimeout(60_000)
  const stamp = Date.now()
  const context = await browser.newContext()
  const page = await context.newPage()
  await login(page, ownerEmail, ownerPassword)
  await expect(page.getByRole('heading', { name: 'Gesprekken' })).toBeVisible()
  const mailboxId = sql(`SELECT id FROM mailboxes WHERE name = 'Live' ORDER BY created_at DESC LIMIT 1`)
  expect(mailboxId).not.toBe('')

  for (let i = 0; i < 25; i++) sql(`INSERT INTO conversations (mailbox_id, subject, status, last_message_at) VALUES ('${mailboxId}', 'Vulling ${stamp} ${i}', 'open', now() - interval '1 hour' - ${i} * interval '1 minute')`)
  // Other specs leave newer conversations behind, so the list is limited to this mailbox: the
  // first filler must be on the first page and the list must overflow on its own.
  await page.goto(`/inbox/alle?mailbox=${mailboxId}`)
  await expect(page.getByText(`Vulling ${stamp} 0`)).toBeVisible()

  const list = page.getByRole('region', { name: 'Gesprekken' }).locator('.overflow-y-auto')
  await list.evaluate((el) => {
    el.scrollTop = el.scrollHeight
  })

  const subject = `Nieuw binnen ${stamp}`
  insertConversation(mailboxId, subject)
  const pill = page.getByRole('button', { name: '1 nieuw gesprek' })
  await expect(pill).toBeVisible({ timeout: 10_000 })
  await expect(page.getByText(subject)).toHaveCount(0)
  await expect(page.getByRole('status').filter({ hasText: '1 nieuw gesprek' })).toHaveCount(1)

  await pill.click()
  await expect(page.getByText(subject)).toBeVisible()
  await expect(pill).toHaveCount(0)
  await context.close()
})
