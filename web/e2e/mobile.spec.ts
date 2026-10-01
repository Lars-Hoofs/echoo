import { expect, type Page, test } from '@playwright/test'

import { signInAsOwner } from './session'

// The phone layout and push settings of the web app. Runs after auth.spec.ts.
test.use({ viewport: { width: 390, height: 844 }, isMobile: true, hasTouch: true })

const baseURL = process.env.ECHOO_E2E_URL ?? 'http://localhost:8080'

// A made-up but well-formed browser subscription: a P-256 public key and a 16-byte auth secret.
const subscription = {
  kind: 'webpush',
  endpoint: 'https://fcm.googleapis.com/fcm/send/e2e-mobile-spec',
  keys: {
    p256dh: 'BCVxsr7N_eNgVRqvHtD0zTZsEc6-VV-JvLexhqUzORcxaOzi6-AYWXvTBHm4bjyPjs7Vd8pZGH6SRpkNtoIAiw4',
    auth: 'BTBZMqHH6r4Tts7J_aSIgg',
  },
  label: 'Pixel · Chrome',
}

async function csrf(page: Page): Promise<string> {
  const me = await page.request.get('/api/v1/me')
  return ((await me.json()) as { csrf_token: string }).csrf_token
}

test('the web app is installable: manifest, icons and service worker', async ({ page }) => {
  const manifest = await page.request.get('/manifest.webmanifest')
  expect(manifest.headers()['content-type']).toBe('application/manifest+json')
  const m = (await manifest.json()) as { name: string; display: string; icons: { src: string }[] }
  expect(m.name).toBe('Echoo')
  expect(m.display).toBe('standalone')
  for (const icon of m.icons) expect((await page.request.get(icon.src)).status()).toBe(200)

  const sw = await page.request.get('/sw.js')
  expect(sw.status()).toBe(200)
  expect(sw.headers()['content-security-policy']).toContain("default-src 'self'")

  await signInAsOwner(page)
  await expect(page.locator('link[rel="manifest"]')).toHaveAttribute('href', '/manifest.webmanifest')
  await expect.poll(() => page.evaluate(async () => (await navigator.serviceWorker.getRegistration('/'))?.active?.scriptURL ?? '')).toContain('/sw.js')
})

test('a phone gets the tab bar, which reaches every part of the app', async ({ page }) => {
  await signInAsOwner(page)
  const tabs = page.getByRole('navigation', { name: 'Snelmenu' })
  await expect(tabs).toBeVisible()
  await tabs.getByRole('link', { name: 'Zoeken' }).click()
  await expect(page).toHaveURL(/\/zoeken/)
  await tabs.getByRole('link', { name: /Mijn/ }).click()
  await expect(page).toHaveURL(/\/inbox\/mine/)
  await tabs.getByRole('button', { name: 'Meer' }).click()
  const sheet = page.getByRole('dialog', { name: 'Navigatie' })
  await expect(sheet).toBeVisible()
  await sheet.getByRole('link', { name: /Zonder toewijzing/ }).click()
  await expect(page).toHaveURL(/\/inbox\/zonder-toewijzing/)
  await expect(sheet).toBeHidden()
})

test('push preferences are kept and registered devices can be removed', async ({ page }) => {
  await signInAsOwner(page)
  const token = await csrf(page)
  const registered = await page.request.post('/api/v1/push/devices', { data: subscription, headers: { 'X-CSRF-Token': token, Origin: baseURL } })
  expect(registered.status()).toBe(201)

  await page.goto('/instellingen/meldingen')
  const replies = page.getByRole('switch', { name: 'Push: als een klant antwoordt' })
  await expect(replies).toHaveAttribute('aria-checked', 'true')
  await replies.click()
  await expect(replies).toHaveAttribute('aria-checked', 'false')
  await page.reload()
  await expect(page.getByRole('switch', { name: 'Push: als een klant antwoordt' })).toHaveAttribute('aria-checked', 'false')
  await page.getByRole('switch', { name: 'Push: als een klant antwoordt' }).click()

  const devices = page.getByRole('list', { name: 'Aangemelde apparaten' })
  await expect(devices.getByText('Pixel · Chrome')).toBeVisible()
  await devices.getByRole('button', { name: 'Pixel · Chrome afmelden' }).click()
  await expect(devices).toHaveCount(0)
  await expect(page.getByText('Nog geen apparaten')).toBeVisible()
})
