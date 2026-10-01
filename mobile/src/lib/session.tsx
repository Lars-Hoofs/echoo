import { useQueryClient } from '@tanstack/react-query'
import * as SecureStore from 'expo-secure-store'
import { createContext, type ReactNode, useCallback, useContext, useEffect, useMemo, useState } from 'react'

import { api, ApiError, setCsrfToken, setServer, setUnauthenticatedHandler } from './api'
import type { Me } from './types'

const serverKey = 'echoo.server'

export type SessionState =
  | { status: 'loading' }
  | { status: 'no-server' }
  | { status: 'signed-out'; server: string }
  // Signed in, but the account must first change its password or set up two-factor
  // authentication, which happens in the web app.
  | { status: 'unfinished'; server: string; me: Me }
  | { status: 'signed-in'; server: string; me: Me }

interface Session {
  state: SessionState
  connect(server: string): Promise<void>
  forgetServer(): Promise<void>
  login(email: string, password: string): Promise<{ mfaRequired: boolean }>
  verifyMfa(code: string): Promise<void>
  logout(): Promise<void>
  refresh(): Promise<void>
}

const SessionContext = createContext<Session | null>(null)

export function useSession(): Session {
  const s = useContext(SessionContext)
  if (!s) throw new Error('useSession outside SessionProvider')
  return s
}

// The signed-in user; only for screens behind the sign-in guard.
export function useMe(): Me {
  const { state } = useSession()
  if (state.status !== 'signed-in') throw new Error('useMe while not signed in')
  return state.me
}

export function hasPermission(me: Me, permission: string): boolean {
  return me.permissions.includes(permission)
}

export function SessionProvider({ children }: { children: ReactNode }) {
  const [state, setState] = useState<SessionState>({ status: 'loading' })
  const queryClient = useQueryClient()

  const loadMe = useCallback(async (server: string) => {
    try {
      const me = await api<Me>('GET', '/me')
      setCsrfToken(me.csrf_token)
      setState(me.must_change_password || me.mfa_enrollment_required ? { status: 'unfinished', server, me } : { status: 'signed-in', server, me })
    } catch (err) {
      if (err instanceof ApiError && err.status === 401) {
        setState({ status: 'signed-out', server })
        return
      }
      throw err
    }
  }, [])

  useEffect(() => {
    setUnauthenticatedHandler(() => {
      queryClient.clear()
      setState((s) => ('server' in s ? { status: 'signed-out', server: s.server } : s))
    })
  }, [queryClient])

  useEffect(() => {
    void (async () => {
      let server: string | null = null
      try {
        server = await SecureStore.getItemAsync(serverKey)
      } catch (err) {
        // A keychain that cannot be read is treated as a fresh install: the agent connects again.
        console.warn('stored server address unreadable', err)
      }
      if (!server) {
        setState({ status: 'no-server' })
        return
      }
      setServer(server)
      try {
        await loadMe(server)
      } catch {
        // Offline at start: the sign-in screen retries and says what is wrong.
        setState({ status: 'signed-out', server })
      }
    })()
  }, [loadMe])

  const session = useMemo<Session>(
    () => ({
      state,
      async connect(server) {
        // An Echoo installation answers this public route; anything else is not one.
        const res = await fetch(`${server}/api/v1/auth/sso`, { headers: { Accept: 'application/json' } }).catch(() => null)
        if (!res?.ok) throw new ApiError(res?.status ?? 0, res ? 'not_echoo' : 'network', 'not an Echoo server')
        await SecureStore.setItemAsync(serverKey, server)
        setServer(server)
        setState({ status: 'signed-out', server })
      },
      async forgetServer() {
        await SecureStore.deleteItemAsync(serverKey)
        setServer('')
        queryClient.clear()
        setState({ status: 'no-server' })
      },
      async login(email, password) {
        const r = await api<{ mfa_required: boolean; csrf_token: string }>('POST', '/auth/login', { email, password })
        setCsrfToken(r.csrf_token)
        if (!r.mfa_required && 'server' in state) await loadMe(state.server)
        return { mfaRequired: r.mfa_required }
      },
      async verifyMfa(code) {
        const r = await api<{ csrf_token: string }>('POST', '/auth/mfa', { code })
        setCsrfToken(r.csrf_token)
        if ('server' in state) await loadMe(state.server)
      },
      async logout() {
        // Revoking the session also unregisters this phone for pushes (a database trigger).
        await api('POST', '/auth/logout').catch(() => undefined)
        queryClient.clear()
        if ('server' in state) setState({ status: 'signed-out', server: state.server })
      },
      async refresh() {
        if ('server' in state) await loadMe(state.server)
      },
    }),
    [state, loadMe, queryClient],
  )

  return <SessionContext.Provider value={session}>{children}</SessionContext.Provider>
}
