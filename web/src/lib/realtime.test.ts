import { QueryClient } from '@tanstack/react-query'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { backoffDelay, invalidationKeys, isRealtimeConnected, parseEvent, RealtimeClient, type SourceLike } from './realtime'

describe('invalidationKeys', () => {
  it.each(['message.created', 'message.updated'])('%s refreshes that conversation, the lists and the summary', (type) => {
    expect(invalidationKeys({ type, conversation_id: 'c1' })).toEqual([
      ['inbox', 'conversations'],
      ['inbox', 'summary'],
      ['inbox', 'conversation', 'c1'],
    ])
  })

  it('conversation.updated also refreshes the trash', () => {
    expect(invalidationKeys({ type: 'conversation.updated', conversation_id: 'c1' })).toEqual([
      ['inbox', 'conversations'],
      ['inbox', 'summary'],
      ['inbox', 'trash'],
      ['inbox', 'conversation', 'c1'],
    ])
  })

  it('skips the detail query when the event names no conversation', () => {
    expect(invalidationKeys({ type: 'message.created' })).toEqual([['inbox', 'conversations'], ['inbox', 'summary']])
  })

  it('maps notifications and resync', () => {
    expect(invalidationKeys({ type: 'notification' })).toEqual([['notifications']])
    expect(invalidationKeys({ type: 'resync' })).toEqual([['inbox'], ['notifications']])
  })

  it('leaves presence and unknown types alone', () => {
    expect(invalidationKeys({ type: 'presence', conversation_id: 'c1' })).toEqual([])
    expect(invalidationKeys({ type: 'something.new' })).toEqual([])
  })
})

describe('parseEvent', () => {
  it('accepts an object with a type and rejects everything else', () => {
    expect(parseEvent('{"type":"resync"}')).toEqual({ type: 'resync' })
    expect(parseEvent('not json')).toBeNull()
    expect(parseEvent('[]')).toBeNull()
    expect(parseEvent('{"type":5}')).toBeNull()
    expect(parseEvent('null')).toBeNull()
  })
})

describe('backoffDelay', () => {
  it('doubles from one second and stops at thirty', () => {
    const full = () => 1
    expect([0, 1, 2, 3, 4, 5, 6, 10].map((n) => backoffDelay(n, full))).toEqual([1000, 2000, 4000, 8000, 16000, 30000, 30000, 30000])
  })

  it('jitters between half and all of the step', () => {
    expect(backoffDelay(2, () => 0)).toBe(2000)
    expect(backoffDelay(2, () => 0.5)).toBe(3000)
  })
})

class FakeSource implements SourceLike {
  static all: FakeSource[] = []
  readyState = 0
  onopen: EventSource['onopen'] = null
  onmessage: EventSource['onmessage'] = null
  onerror: EventSource['onerror'] = null
  closed = false
  constructor(readonly url: string) {
    FakeSource.all.push(this)
  }
  close() {
    this.closed = true
    this.readyState = 2
  }
  open() {
    this.readyState = 1
    this.onopen?.call(this as unknown as EventSource, new Event('open'))
  }
  emit(data: object) {
    this.onmessage?.call(this as unknown as EventSource, { data: JSON.stringify(data) } as MessageEvent)
  }
  fail(fatal: boolean) {
    this.readyState = fatal ? 2 : 0
    this.onerror?.call(this as unknown as EventSource, new Event('error'))
  }
}

describe('RealtimeClient', () => {
  let queryClient: QueryClient
  let invalidated: unknown[][]
  let hidden: boolean
  let visibility: (() => void) | undefined
  let presence: unknown[]
  let client: RealtimeClient

  beforeEach(() => {
    vi.useFakeTimers()
    FakeSource.all = []
    invalidated = []
    hidden = false
    visibility = undefined
    presence = []
    queryClient = new QueryClient()
    vi.spyOn(queryClient, 'invalidateQueries').mockImplementation((filters) => {
      invalidated.push(filters?.queryKey as unknown[])
      return Promise.resolve()
    })
    client = new RealtimeClient({
      queryClient,
      createSource: (url) => new FakeSource(url),
      isHidden: () => hidden,
      onVisibilityChange: (cb) => {
        visibility = cb
        return () => {
          visibility = undefined
        }
      },
      onPresence: (ev) => presence.push(ev),
    })
  })

  afterEach(() => {
    client.stop()
    vi.useRealTimers()
    vi.restoreAllMocks()
  })

  const current = () => FakeSource.all[FakeSource.all.length - 1] as FakeSource

  it('opens one stream and reports the connection state', () => {
    client.start()
    expect(FakeSource.all).toHaveLength(1)
    expect(current().url).toBe('/api/v1/events')
    expect(isRealtimeConnected()).toBe(false)
    current().open()
    expect(isRealtimeConnected()).toBe(true)
  })

  it('debounces bursts into one invalidation per query', () => {
    client.start()
    current().open()
    current().emit({ type: 'message.created', conversation_id: 'c1' })
    current().emit({ type: 'message.created', conversation_id: 'c1' })
    current().emit({ type: 'conversation.updated', conversation_id: 'c2' })
    vi.advanceTimersByTime(299)
    expect(invalidated).toHaveLength(0)
    vi.advanceTimersByTime(2)
    expect(invalidated).toEqual([
      ['inbox', 'conversations'],
      ['inbox', 'summary'],
      ['inbox', 'conversation', 'c1'],
      ['inbox', 'trash'],
      ['inbox', 'conversation', 'c2'],
    ])
  })

  it('routes presence to the presence store instead of the query cache', () => {
    client.start()
    current().open()
    current().emit({ type: 'presence', conversation_id: 'c1', user_id: 'u1', state: 'viewing', name: 'Sanne' })
    vi.advanceTimersByTime(1000)
    expect(presence).toHaveLength(1)
    expect(invalidated).toHaveLength(0)
  })

  it('refetches the inbox once when the stream drops, so polling can take over', () => {
    client.start()
    current().open()
    current().fail(false)
    vi.advanceTimersByTime(400)
    expect(invalidated).toEqual([['inbox']])
    expect(isRealtimeConnected()).toBe(false)
  })

  it('reconnects with backoff after a fatal error and resyncs on open', () => {
    vi.spyOn(Math, 'random').mockReturnValue(1)
    client.start()
    current().open()
    current().fail(true)
    expect(FakeSource.all).toHaveLength(1)
    vi.advanceTimersByTime(1000)
    expect(FakeSource.all).toHaveLength(2)

    current().fail(true)
    vi.advanceTimersByTime(1999)
    expect(FakeSource.all).toHaveLength(2)
    vi.advanceTimersByTime(1001)
    expect(FakeSource.all).toHaveLength(3)

    invalidated.length = 0
    current().open()
    vi.advanceTimersByTime(300)
    expect(invalidated).toEqual([['inbox'], ['notifications']])
  })

  it('pauses after five minutes hidden and resyncs when the tab returns', () => {
    client.start()
    current().open()
    hidden = true
    visibility?.()
    vi.advanceTimersByTime(4 * 60_000)
    expect(FakeSource.all[0]?.closed).toBe(false)
    vi.advanceTimersByTime(61_000)
    expect(FakeSource.all[0]?.closed).toBe(true)
    expect(isRealtimeConnected()).toBe(false)

    hidden = false
    visibility?.()
    expect(FakeSource.all).toHaveLength(2)
    invalidated.length = 0
    current().open()
    vi.advanceTimersByTime(300)
    expect(invalidated).toEqual([['inbox'], ['notifications']])
  })

  it('keeps the stream when the tab is hidden only briefly', () => {
    client.start()
    current().open()
    hidden = true
    visibility?.()
    vi.advanceTimersByTime(2 * 60_000)
    hidden = false
    visibility?.()
    vi.advanceTimersByTime(10 * 60_000)
    expect(FakeSource.all).toHaveLength(1)
    expect(FakeSource.all[0]?.closed).toBe(false)
  })

  it('closes the stream and drops pending work on stop', () => {
    client.start()
    current().open()
    current().emit({ type: 'message.created', conversation_id: 'c1' })
    client.stop()
    vi.advanceTimersByTime(5000)
    expect(current().closed).toBe(true)
    expect(invalidated).toHaveLength(0)
  })
})
