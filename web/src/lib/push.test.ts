import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { deviceLabel, isIOS, onNotificationOpen, pushSupport, urlBase64ToBytes } from './push'

const iPhoneSafari = 'Mozilla/5.0 (iPhone; CPU iPhone OS 18_5 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.5 Mobile/15E148 Safari/604.1'
const androidChrome = 'Mozilla/5.0 (Linux; Android 15; Pixel 9) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/141.0.0.0 Mobile Safari/537.36'
const iPadDesktop = 'Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.5 Safari/605.1.15'

function stubBrowser({ standalone = false, webPush = true }: { standalone?: boolean; webPush?: boolean } = {}) {
  const win: Record<string, unknown> = {
    matchMedia: () => ({ matches: standalone }),
    location: { origin: 'https://support.example.com' },
  }
  if (webPush) Object.assign(win, { PushManager: function PushManager() {}, Notification: function Notification() {} })
  vi.stubGlobal('window', win)
  vi.stubGlobal('navigator', { userAgent: iPhoneSafari, maxTouchPoints: 5, ...(webPush ? { serviceWorker: {} } : {}) })
}

beforeEach(() => stubBrowser())
afterEach(() => vi.unstubAllGlobals())

describe('isIOS', () => {
  it('knows iPhones, and iPads that pretend to be a Mac', () => {
    expect(isIOS(iPhoneSafari, 5)).toBe(true)
    expect(isIOS(iPadDesktop, 5)).toBe(true)
    expect(isIOS(iPadDesktop, 0)).toBe(false)
    expect(isIOS(androidChrome, 5)).toBe(false)
  })
})

describe('pushSupport', () => {
  it('uses Web Push where the browser has it', () => {
    expect(pushSupport()).toBe('webpush')
  })

  it('asks iOS Safari users to add the app to the home screen first', () => {
    stubBrowser({ webPush: false })
    expect(pushSupport()).toBe('install')
  })

  it('reports browsers without push', () => {
    stubBrowser({ webPush: false })
    vi.stubGlobal('navigator', { userAgent: androidChrome, maxTouchPoints: 0 })
    expect(pushSupport()).toBe('unsupported')
  })
})

describe('deviceLabel', () => {
  it('names the device and browser', () => {
    expect(deviceLabel(iPhoneSafari)).toBe('iPhone · Safari')
    expect(deviceLabel(androidChrome)).toBe('Android · Chrome')
  })

  it('marks the installed web app', () => {
    stubBrowser({ standalone: true })
    expect(deviceLabel(iPhoneSafari)).toBe('iPhone · Safari (app)')
  })
})

describe('urlBase64ToBytes', () => {
  it('decodes unpadded base64url', () => {
    expect(Array.from(urlBase64ToBytes('_-8'))).toEqual([0xff, 0xef])
    expect(Array.from(urlBase64ToBytes('AQID'))).toEqual([1, 2, 3])
  })
})

describe('onNotificationOpen', () => {
  it('follows same-origin links from the service worker and ignores the rest', () => {
    let handler: ((e: { data: unknown }) => void) | undefined
    vi.stubGlobal('navigator', {
      userAgent: androidChrome,
      serviceWorker: {
        addEventListener: (_: string, h: (e: { data: unknown }) => void) => (handler = h),
        removeEventListener: vi.fn(),
      },
    })
    const navigate = vi.fn()
    const stop = onNotificationOpen(navigate)
    handler?.({ data: { type: 'echoo:navigate', url: 'https://support.example.com/inbox/alle/c1' } })
    handler?.({ data: { type: 'echoo:navigate', url: 'https://evil.example.com/phish' } })
    handler?.({ data: { type: 'other', url: '/inbox/alle/c2' } })
    expect(navigate.mock.calls).toEqual([['/inbox/alle/c1']])
    stop()
  })
})
