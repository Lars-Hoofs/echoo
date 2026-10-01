import { describe, expect, it } from 'vitest'

import { pushData, routeForPush } from './routes'

describe('routeForPush', () => {
  it('opens the conversation of a push', () => {
    expect(routeForPush({ url: '/inbox/alle/01a0f457-fb38-7726-b6d2-652adfcaafdc' })).toBe('/gesprek/01a0f457-fb38-7726-b6d2-652adfcaafdc')
    expect(routeForPush({ url: '/instellingen/meldingen' })).toBe('/ik')
  })
  it('ignores anything else', () => {
    expect(routeForPush({ url: 'https://evil.example.com/inbox/alle/x' })).toBeNull()
    expect(routeForPush({ url: '/inbox/alle/../../etc' })).toBeNull()
    expect(routeForPush({})).toBeNull()
    expect(routeForPush(undefined)).toBeNull()
  })
})

describe('pushData', () => {
  const url = '/inbox/alle/01a0f457-fb38-7726-b6d2-652adfcaafdc'
  it('reads the APNs payload, the FCM data and plain data', () => {
    expect(pushData({ content: { data: null }, trigger: { type: 'push', payload: { aps: {}, url } } })?.url).toBe(url)
    expect(pushData({ content: { data: null }, trigger: { type: 'push', remoteMessage: { data: { url } } } })?.url).toBe(url)
    expect(pushData({ content: { data: { url } }, trigger: null })?.url).toBe(url)
    expect(pushData({ content: { data: null }, trigger: null })).toBeUndefined()
  })
})
