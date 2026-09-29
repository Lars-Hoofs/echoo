// Shared sign-in helpers, so one run over every spec stays under the login rate limit (10 per
// minute per address) without touching the limiter.
//
// signInAsOwner signs in through the API once, keeps the session in .e2e/owner-session.json and
// hands the same session to every later spec that only needs "an owner who is signed in". A
// cached session that was revoked meanwhile (password change, sign-out of all sessions) is
// detected and replaced. Specs that test the login page itself sign in through the UI and call
// loginSlot() before each attempt, which waits until the attempt fits in the limit.
import { mkdirSync, readFileSync, writeFileSync } from 'node:fs'
import { dirname } from 'node:path'

import type { Page } from '@playwright/test'

const baseURL = process.env.ECHOO_E2E_URL ?? 'http://localhost:8080'
const ownerEmail = process.env.ECHOO_E2E_OWNER_EMAIL ?? 'owner@example.com'
// Set by the first test of auth.spec.ts, which replaces the temporary password.
const ownerPassword = 'een lange zin als wachtwoord'
const stateFile = '.e2e/owner-session.json'

// The server allows 10 attempts per fixed one-minute window and per address, and counts second-
// factor attempts in the same window. Staying at 6 in any sliding minute leaves room for those.
const maxLoginsPerMinute = 6
const attempts: number[] = []

export async function loginSlot(): Promise<void> {
  for (;;) {
    const now = Date.now()
    while ((attempts[0] ?? now) <= now - 60_000) attempts.shift()
    const oldest = attempts[0]
    if (oldest === undefined || attempts.length < maxLoginsPerMinute) {
      attempts.push(now)
      return
    }
    await new Promise((resolve) => setTimeout(resolve, oldest + 60_000 - now + 100))
  }
}

interface Cached {
  name: string
  value: string
}

function readCached(): Cached | undefined {
  try {
    return JSON.parse(readFileSync(stateFile, 'utf8')) as Cached
  } catch {
    return undefined
  }
}

async function stillValid(session: Cached): Promise<boolean> {
  const res = await fetch(`${baseURL}/api/v1/me`, { headers: { Cookie: `${session.name}=${session.value}` } })
  return res.ok
}

async function apiLogin(retries = 2): Promise<Cached> {
  await loginSlot()
  const res = await fetch(`${baseURL}/api/v1/auth/login`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json', Origin: baseURL },
    body: JSON.stringify({ email: ownerEmail, password: ownerPassword }),
  })
  if (res.status === 429 && retries > 0) {
    // Attempts made outside loginSlot (second factor, a spec's own form) filled the window.
    await new Promise((resolve) => setTimeout(resolve, 61_000))
    return apiLogin(retries - 1)
  }
  if (!res.ok) throw new Error(`owner sign-in through the API failed with ${res.status}; did auth.spec.ts run first?`)
  for (const header of res.headers.getSetCookie()) {
    const match = /^((?:__Host-)?echoo_session)=([^;]+)/.exec(header)
    if (match?.[1] && match[2]) return { name: match[1], value: match[2] }
  }
  throw new Error('the sign-in response set no session cookie')
}

async function ownerSession(): Promise<Cached> {
  const cached = readCached()
  if (cached && (await stillValid(cached))) return cached
  const fresh = await apiLogin()
  mkdirSync(dirname(stateFile), { recursive: true })
  writeFileSync(stateFile, JSON.stringify(fresh), { mode: 0o600 })
  return fresh
}

// Puts the page's browser context in the owner's session and opens the app.
export async function signInAsOwner(page: Page): Promise<void> {
  const session = await ownerSession()
  await page.context().addCookies([{ name: session.name, value: session.value, url: baseURL, httpOnly: true, sameSite: 'Lax' }])
  await page.goto('/')
}
