import { TriangleAlert } from 'lucide-react'
import { createContext, type ReactNode, useCallback, useContext, useEffect, useMemo, useRef, useState } from 'react'

const DURATION_MS = 5000

interface ToastOptions {
  // Shown as a button; the toast closes when it is pressed.
  action?: { label: string; onClick: () => void }
  tone?: 'default' | 'error'
  // How long the toast stays; defaults to 5 seconds.
  durationMs?: number
}

interface ToastItem extends ToastOptions {
  id: number
  message: string
}

interface ToastApi {
  toast: (message: string, options?: ToastOptions) => void
}

const ToastContext = createContext<ToastApi | null>(null)

export function useToast(): ToastApi['toast'] {
  const ctx = useContext(ToastContext)
  if (!ctx) throw new Error('useToast needs a ToastProvider')
  return ctx.toast
}

function Toast({ item, close }: { item: ToastItem; close: (id: number) => void }) {
  const [paused, setPaused] = useState(false)
  const { id, durationMs = DURATION_MS } = item
  useEffect(() => {
    if (paused) return
    const t = setTimeout(() => {
      close(id)
    }, durationMs)
    return () => {
      clearTimeout(t)
    }
  }, [paused, close, id, durationMs])
  return (
    <li
      role={item.tone === 'error' ? 'alert' : undefined}
      onMouseEnter={() => setPaused(true)}
      onMouseLeave={() => setPaused(false)}
      onFocus={() => setPaused(true)}
      onBlur={() => setPaused(false)}
      className={`pointer-events-auto flex min-h-12 items-center gap-3 rounded-lg border bg-float py-2 pr-2 pl-4 text-base text-ink shadow-float animate-[echoo-fade-in_var(--dur-move)_var(--ease-enter)] ${item.tone === 'error' ? 'border-danger-text/40' : 'border-line'}`}
    >
      {item.tone === 'error' && <TriangleAlert size={20} aria-hidden className="shrink-0 text-danger-text" />}
      <span className="min-w-0 flex-1">{item.message}</span>
      {item.action && (
        <button
          type="button"
          onClick={() => {
            item.action?.onClick()
            close(id)
          }}
          className="btn btn-s shrink-0 max-md:h-11"
        >
          {item.action.label}
        </button>
      )}
    </li>
  )
}

// Toasts stack at the bottom left in a labelled live region. The container has no status role,
// so it never competes with status messages in the page; errors announce themselves as alerts.
export function ToastProvider({ children }: { children: ReactNode }) {
  const [items, setItems] = useState<ToastItem[]>([])
  const nextId = useRef(0)
  const toast = useCallback((message: string, options: ToastOptions = {}) => {
    setItems((list) => [...list.slice(-2), { ...options, id: nextId.current++, message }])
  }, [])
  const api = useMemo(() => ({ toast }), [toast])
  const close = useCallback((id: number) => {
    setItems((list) => list.filter((t) => t.id !== id))
  }, [])

  return (
    <ToastContext.Provider value={api}>
      {children}
      <div role="region" aria-label="Meldingen" className="pointer-events-none fixed bottom-4 left-4 z-[60] w-[calc(100vw-32px)] max-w-sm">
        <ul aria-live="polite" className="flex flex-col gap-2">
          {items.map((t) => (
            <Toast key={t.id} item={t} close={close} />
          ))}
        </ul>
      </div>
    </ToastContext.Provider>
  )
}
