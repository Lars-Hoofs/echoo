import './styles.css'

import { MutationCache, QueryCache, QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { RouterProvider } from '@tanstack/react-router'
import { LucideProvider } from 'lucide-react'
import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'

import { ApiError } from './lib/api'
import { initMotion } from './lib/motion'
import { createAppRouter } from './router'

// Radix injects a few <style> elements (scroll locking). The server puts a per-request CSP
// nonce in a meta tag; get-nonce reads it from this global.
const nonce = document.querySelector<HTMLMetaElement>('meta[name="csp-nonce"]')?.content
if (nonce && nonce !== '__CSP_NONCE__') {
  ;(window as unknown as { __webpack_nonce__?: string }).__webpack_nonce__ = nonce
}

function onApiError(err: unknown) {
  if (err instanceof ApiError && err.status === 401 && router.state.location.pathname !== '/inloggen') {
    queryClient.clear()
    void router.navigate({ to: '/inloggen' })
  }
}

initMotion()

const queryClient = new QueryClient({
  queryCache: new QueryCache({ onError: onApiError }),
  mutationCache: new MutationCache({ onError: onApiError }),
  defaultOptions: { queries: { retry: (count, err) => !(err instanceof ApiError && err.status < 500) && count < 2 } },
})
const router = createAppRouter(queryClient)

const root = document.getElementById('root')
if (!root) throw new Error('missing #root')
createRoot(root).render(
  <StrictMode>
    <LucideProvider size={20} strokeWidth={1.5}>
      <QueryClientProvider client={queryClient}>
        <RouterProvider router={router} />
      </QueryClientProvider>
    </LucideProvider>
  </StrictMode>,
)
