import { expect, type Locator, type Page, test } from '@playwright/test'

import { dockerPsql } from './compose'
import { signInAsOwner } from './session'

const ownerEmail = process.env.ECHOO_E2E_OWNER_EMAIL ?? 'owner@example.com'

function sql(statement: string): string {
  return dockerPsql(['-At', '-c', statement])
}

// A conversation assigned to the owner with one customer message that arrived just now, so it
// is unread for the owner.
function seedUnreadConversation(stamp: number): string {
  const out = sql(
    `WITH mb AS (INSERT INTO mailboxes (name, email_address) VALUES ('Ongelezen ${stamp}', 'unread-${stamp}@example.com') RETURNING id), ` +
      `ct AS (INSERT INTO contacts (name) VALUES ('Lezer ${stamp}') RETURNING id), ` +
      `cv AS (INSERT INTO conversations (mailbox_id, subject, status, last_message_at, contact_id, assignee_user_id, message_count, preview) ` +
      `SELECT mb.id, 'Onderwerp ${stamp}', 'open', now(), ct.id, (SELECT id FROM users WHERE email = '${ownerEmail}'), 1, 'Hallo ${stamp}' FROM mb, ct RETURNING id, mailbox_id), ` +
      `m AS (INSERT INTO messages (conversation_id, mailbox_id, kind, direction, from_addr, from_name, subject, body_text) ` +
      `SELECT id, mailbox_id, 'email', 'in', 'klant@example.com', 'Lezer', 'Onderwerp ${stamp}', 'Hallo ${stamp}' FROM cv RETURNING id) ` +
      `SELECT id FROM cv`,
  )
  return out.split('\n')[0] ?? ''
}

async function login(page: Page) {
  await signInAsOwner(page)
  await expect(page.getByRole('heading', { name: 'Gesprekken' })).toBeVisible()
}

async function unreadInSidebar(page: Page): Promise<number> {
  const text = await page.getByRole('navigation', { name: 'Inbox' }).getByRole('link', { name: /Mijn inbox/ }).innerText()
  return Number(/(\d+)\s*ongelezen/.exec(text)?.[1] ?? 0)
}

async function expectBold(name: Locator, bold: boolean) {
  if (bold) await expect(name).toHaveClass(/font-bold/)
  else await expect(name).not.toHaveClass(/font-bold/)
}

test('an unread conversation is bold with a count, reading clears it and marking unread brings it back', async ({ page }) => {
  test.skip(!process.env.ECHOO_E2E_OWNER_PASSWORD, 'ECHOO_E2E_OWNER_PASSWORD is not set')
  const stamp = Date.now()
  const id = seedUnreadConversation(stamp)
  await login(page)
  await page.goto('/inbox/alle')

  const row = page.locator(`[data-row="${id}"]`)
  const name = row.getByText(`Lezer ${stamp}`, { exact: true })
  await expect(row).toHaveAttribute('data-unread', 'true')
  await expectBold(name, true)
  await expect(row).toContainText('Ongelezen:')
  const unreadBefore = await unreadInSidebar(page)
  expect(unreadBefore).toBeGreaterThanOrEqual(1)

  await row.click()
  await expect(page.getByRole('heading', { name: `Lezer ${stamp}` })).toBeVisible()
  await expect(row).toHaveAttribute('data-unread', 'false')
  await expectBold(name, false)
  await expect.poll(() => unreadInSidebar(page)).toBe(unreadBefore - 1)

  // The state is stored, not only cached.
  await page.reload()
  await expect(page.locator(`[data-row="${id}"]`)).toHaveAttribute('data-unread', 'false')

  await page.getByRole('button', { name: 'Meer acties' }).click()
  await page.getByRole('menuitem', { name: 'Markeren als ongelezen' }).click()
  await expect(page).toHaveURL(/\/inbox\/alle$/)
  await expect(row).toHaveAttribute('data-unread', 'true')
  await expectBold(name, true)
  await expect.poll(() => unreadInSidebar(page)).toBe(unreadBefore)
})
