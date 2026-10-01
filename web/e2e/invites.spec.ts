// Invitations, password reset by email, notification settings and the OAuth mailbox form.
//
// Needs a stack with system mail and a Google client configured, and the SMTP sink that captures
// what Echoo sends (see e2e/compose.smtp.yml and e2e/smtp-sink.ts):
//
//   node e2e/smtp-sink.ts 18107 18108 .e2e/smtp-cert.pem &
//   COMPOSE_PROJECT_NAME=echoo-oauth ECHOO_PORT=18106 ECHOO_BASE_URL=http://localhost:18106 \
//     docker compose -f docker-compose.yml -f web/e2e/compose.smtp.yml up -d --build --wait
//   docker compose -p echoo-oauth exec echoo /echoo admin create-owner --email owner@example.com --name Owner
//   ECHOO_E2E_URL=http://localhost:18106 ECHOO_E2E_OWNER_PASSWORD=<temporary password> \
//     ECHOO_E2E_SMTP_SINK=http://127.0.0.1:18108 npx playwright test auth.spec.ts -g "temporary password" invites.spec.ts
//
// The auth spec's first test replaces the owner's temporary password; this spec signs in with it.
import { expect, type Page, test } from '@playwright/test'

import { loginSlot } from './session'

const ownerEmail = process.env.ECHOO_E2E_OWNER_EMAIL ?? 'owner@example.com'
const ownerPassword = 'een lange zin als wachtwoord'
const sinkURL = process.env.ECHOO_E2E_SMTP_SINK ?? ''
const inviteePassword = 'een zin voor de uitgenodigde'
const newPassword = 'weer een heel andere zin'

test.describe.configure({ mode: 'serial' })
test.skip(!process.env.ECHOO_E2E_OWNER_PASSWORD || !sinkURL, 'ECHOO_E2E_OWNER_PASSWORD and ECHOO_E2E_SMTP_SINK are not set')

interface Captured {
  from: string
  to: string[]
  raw: string
}

async function capturedMail(): Promise<Captured[]> {
  const res = await fetch(`${sinkURL}/messages`)
  return (await res.json()) as Captured[]
}

// The message body is quoted-printable, which splits long lines; undo that before looking for links.
function decodeQuotedPrintable(raw: string): string {
  return raw.replace(/=\r?\n/g, '').replace(/=([0-9A-F]{2})/g, (_, hex: string) => String.fromCharCode(parseInt(hex, 16)))
}

// Waits for a message to the address whose subject matches, and returns the path of the link in it.
async function linkFromMail(to: string, subject: RegExp, linkPrefix: string): Promise<string> {
  let path = ''
  await expect
    .poll(
      async () => {
        const mail = (await capturedMail()).find((m) => m.to.includes(to) && subject.test(m.raw))
        const match = mail && new RegExp(`https?://[^\\s"<>]+${linkPrefix}/[A-Za-z0-9_-]+`).exec(decodeQuotedPrintable(mail.raw))
        path = match ? new URL(match[0]).pathname : ''
        return path
      },
      { timeout: 20_000, message: `mail to ${to}` },
    )
    .not.toBe('')
  return path
}

function watchCSP(page: Page): string[] {
  const violations: string[] = []
  page.on('console', (msg) => {
    if (msg.text().includes('Content Security Policy')) violations.push(msg.text())
  })
  return violations
}

async function signIn(page: Page, email: string, password: string) {
  await loginSlot()
  await page.goto('/inloggen')
  await page.getByLabel('E-mailadres').fill(email)
  await page.getByLabel('Wachtwoord').fill(password)
  await page.getByRole('button', { name: 'Inloggen' }).click()
}

// Signs in and waits until the app is shown, so a following goto does not cancel the login.
async function signInReady(page: Page, email: string, password: string) {
  await signIn(page, email, password)
  await expect(page.getByRole('heading', { name: 'Gesprekken' })).toBeVisible()
}

const inviteeEmail = `sanne-${Date.now()}@example.com`

test('owner invites a colleague who accepts and signs in', async ({ page, browser }) => {
  const csp = watchCSP(page)
  await signInReady(page, ownerEmail, ownerPassword)
  await page.goto('/instellingen/gebruikers')
  await page.getByRole('button', { name: 'Gebruiker toevoegen' }).click()
  const dialog = page.getByRole('dialog', { name: 'Gebruiker uitnodigen' })
  await dialog.getByLabel('Naam').fill('Sanne de Vries')
  await dialog.getByLabel('E-mailadres').fill(inviteeEmail)
  await expect(dialog.getByRole('button', { name: 'Tijdelijk wachtwoord tonen' })).toBeVisible()
  await dialog.getByRole('button', { name: 'Uitnodigen' }).click()
  await expect(page.getByRole('dialog', { name: 'Uitnodiging verstuurd' })).toContainText(inviteeEmail)
  await page.getByRole('button', { name: 'Klaar' }).click()
  await expect(page.getByRole('row').filter({ hasText: inviteeEmail })).toContainText('Uitgenodigd')

  const link = await linkFromMail(inviteeEmail, /Subject: Uitnodiging voor Echoo/, '/uitnodiging')

  // The invited account cannot sign in before it accepted.
  const guest = await (await browser.newContext()).newPage()
  const guestCsp = watchCSP(guest)
  await signIn(guest, inviteeEmail, inviteePassword)
  await expect(guest.getByRole('alert')).toContainText('Inloggen mislukt')

  await guest.goto(link)
  await expect(guest.getByRole('heading', { name: 'Welkom bij Echoo' })).toBeVisible()
  await expect(guest.getByLabel('Naam')).toHaveValue('Sanne de Vries')
  await guest.getByLabel('Wachtwoord').fill('kort')
  await guest.getByRole('button', { name: 'Account activeren' }).click()
  await expect(guest.getByText('Gebruik minimaal 12 tekens.')).toBeVisible()
  await guest.getByLabel('Wachtwoord').fill(inviteePassword)
  await guest.getByRole('button', { name: 'Account activeren' }).click()
  await expect(guest.getByRole('heading', { name: 'Gesprekken' })).toBeVisible()

  // The link works once.
  await guest.goto(link)
  await expect(guest.getByRole('heading', { name: 'Deze link werkt niet meer' })).toBeVisible()

  await page.reload()
  await expect(page.getByRole('row').filter({ hasText: inviteeEmail })).not.toContainText('Uitgenodigd')
  expect(csp).toEqual([])
  expect(guestCsp).toEqual([])
  await guest.context().close()
})

test('owner resends and revokes an invitation', async ({ page }) => {
  const email = `revoked-${Date.now()}@example.com`
  await signInReady(page, ownerEmail, ownerPassword)
  await page.goto('/instellingen/gebruikers')
  await page.getByRole('button', { name: 'Gebruiker toevoegen' }).click()
  const dialog = page.getByRole('dialog', { name: 'Gebruiker uitnodigen' })
  await dialog.getByLabel('Naam').fill('Piet Jansen')
  await dialog.getByLabel('E-mailadres').fill(email)
  await dialog.getByRole('button', { name: 'Uitnodigen' }).click()
  await page.getByRole('button', { name: 'Klaar' }).click()
  const first = await linkFromMail(email, /Subject: Uitnodiging voor Echoo/, '/uitnodiging')

  const row = page.getByRole('row').filter({ hasText: email })
  await row.getByRole('button', { name: 'Acties voor Piet Jansen' }).click()
  await page.getByRole('menuitem', { name: 'Uitnodiging opnieuw versturen' }).click()
  await page.getByRole('dialog').getByRole('button', { name: 'Versturen' }).click()
  await expect(page.getByRole('dialog')).toHaveCount(0)
  await expect
    .poll(async () => (await capturedMail()).filter((m) => m.to.includes(email)).length, { timeout: 20_000 })
    .toBe(2)

  await row.getByRole('button', { name: 'Acties voor Piet Jansen' }).click()
  await page.getByRole('menuitem', { name: 'Uitnodiging intrekken' }).click()
  await page.getByRole('dialog').getByRole('button', { name: 'Intrekken' }).click()
  await expect(page.getByRole('row').filter({ hasText: email })).toHaveCount(0)

  await page.goto(first)
  await expect(page.getByRole('heading', { name: 'Deze link werkt niet meer' })).toBeVisible()
})

test('a colleague resets a forgotten password with the emailed link', async ({ page }) => {
  const csp = watchCSP(page)
  await page.goto('/inloggen')
  await page.getByRole('link', { name: 'Wachtwoord vergeten?' }).click()
  await expect(page.getByRole('heading', { name: 'Wachtwoord vergeten' })).toBeVisible()

  // The answer is the same for an address nobody owns.
  await page.getByLabel('E-mailadres').fill(`nobody-${Date.now()}@example.com`)
  await page.getByRole('button', { name: 'Link versturen' }).click()
  await expect(page.getByRole('heading', { name: 'Controleer je e-mail' })).toBeVisible()

  await page.goto('/wachtwoord-vergeten')
  await page.getByLabel('E-mailadres').fill(inviteeEmail)
  await page.getByRole('button', { name: 'Link versturen' }).click()
  await expect(page.getByRole('heading', { name: 'Controleer je e-mail' })).toBeVisible()

  const link = await linkFromMail(inviteeEmail, /Subject: Wachtwoord herstellen voor Echoo/, '/wachtwoord-herstellen')
  await page.goto(link)
  await expect(page.getByRole('heading', { name: 'Nieuw wachtwoord kiezen' })).toBeVisible()
  await page.getByLabel('Nieuw wachtwoord').fill('kort')
  await page.getByRole('button', { name: 'Wachtwoord opslaan' }).click()
  await expect(page.getByText('Gebruik minimaal 12 tekens.')).toBeVisible()
  await page.getByLabel('Nieuw wachtwoord').fill(newPassword)
  await page.getByRole('button', { name: 'Wachtwoord opslaan' }).click()
  await expect(page.getByRole('heading', { name: 'Wachtwoord gewijzigd' })).toBeVisible()

  await page.goto(link)
  await expect(page.getByRole('heading', { name: 'Deze link werkt niet meer' })).toBeVisible()

  await signIn(page, inviteeEmail, inviteePassword)
  await expect(page.getByRole('alert')).toContainText('Inloggen mislukt')
  await page.getByLabel('Wachtwoord').fill(newPassword)
  await loginSlot()
  await page.getByRole('button', { name: 'Inloggen' }).click()
  await expect(page.getByRole('heading', { name: 'Gesprekken' })).toBeVisible()
  expect(csp).toEqual([])
})

test('a colleague opts in to email notifications', async ({ page }) => {
  await signInReady(page, inviteeEmail, newPassword)
  await page.goto('/instellingen/meldingen')
  const mentions = page.getByRole('switch', { name: 'E-mail bij een vermelding' })
  await expect(mentions).toHaveAttribute('aria-checked', 'false')
  await mentions.click()
  await expect(mentions).toHaveAttribute('aria-checked', 'true')
  await page.reload()
  await expect(page.getByRole('switch', { name: 'E-mail bij een vermelding' })).toHaveAttribute('aria-checked', 'true')
  await expect(page.getByRole('switch', { name: 'E-mail bij een toewijzing' })).toHaveAttribute('aria-checked', 'false')
})

test('the mailbox form offers Google and sends the browser to the provider with PKCE', async ({ page }) => {
  const csp = watchCSP(page)
  await signInReady(page, ownerEmail, ownerPassword)
  await page.goto('/instellingen/mailboxen')
  await page.getByRole('button', { name: 'Mailbox toevoegen' }).click()
  await expect(page.getByRole('radio', { name: 'Microsoft 365' })).toHaveCount(0)
  await page.getByRole('radio', { name: 'Google' }).check({ force: true })
  const redirect = new URL('/oauth/callback/google', page.url()).toString()
  await expect(page.getByText(redirect)).toBeVisible()

  let providerURL = ''
  await page.route('https://accounts.google.com/**', async (route) => {
    providerURL = route.request().url()
    await route.fulfill({ contentType: 'text/html', body: '<title>Google</title>' })
  })
  await page.getByLabel('Naam').fill('Klantenservice Google')
  await page.getByRole('button', { name: 'Verbinden met Google' }).click()
  await expect.poll(() => providerURL).not.toBe('')
  const params = new URL(providerURL).searchParams
  expect(params.get('client_id')).toBe('e2e-client')
  expect(params.get('redirect_uri')).toBe(redirect)
  expect(params.get('code_challenge_method')).toBe('S256')
  expect(params.get('scope')).toContain('https://mail.google.com/')
  expect(params.get('code_challenge')).toMatch(/^[A-Za-z0-9_-]{43}$/)
  const state = params.get('state') ?? ''
  expect(state.length).toBeGreaterThan(40)

  // Back from the provider with a code the provider will not honour, and with a forged state.
  await page.goto(`/oauth/callback/google?code=fake&state=${encodeURIComponent(state)}`)
  await expect(page).toHaveURL(/oauth_error=exchange_failed/)
  await expect(page.getByRole('alert')).toContainText('De aanbieder wees de koppeling af')
  await page.goto(`/oauth/callback/google?code=fake&state=${encodeURIComponent(state.slice(0, -4) + 'AAAA')}`)
  await expect(page).toHaveURL(/oauth_error=state_invalid/)
  await expect(page.getByRole('alert')).toContainText('De koppeling kon niet worden bevestigd')
  await expect(page.getByRole('row').filter({ hasText: 'Klantenservice Google' })).toHaveCount(0)
  expect(csp).toEqual([])
})
