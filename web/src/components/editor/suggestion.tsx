import type { Editor, Range } from '@tiptap/core'
import type { SuggestionOptions } from '@tiptap/suggestion'
import { type ReactNode, useEffect, useMemo, useRef, useState } from 'react'

export interface SuggestionView<T> {
  items: T[]
  index: number
  query: string
  rect: DOMRect | null
  pick: (item: T) => void
}

interface SuggestionSource<S> {
  query: string
  clientRect?: (() => DOMRect | null) | null | undefined
  command: (selected: S) => void
}

export type SuggestionHandlers<T, S> = Required<Pick<SuggestionOptions<T, S>, 'items' | 'render' | 'command'>>

interface Config<T, S> {
  // The full list; the popup shows what `filter` keeps for the typed query.
  source: T[]
  filter: (source: T[], query: string) => T[]
  toSelected: (item: T) => S
  // Replaces the default insertion when an item is picked (the "/" picker inserts rendered HTML).
  onPick?: (ctx: { editor: Editor; range: Range; props: S }) => void
}

// Bridges a TipTap suggestion (a plugin that lives outside React) to React state. `options` goes
// into the suggestion configuration; `view` is what the popup component shows. Enter and Tab pick
// the highlighted item and the arrow keys move it; Escape is left to the plugin, which closes itself.
export function useSuggestion<T, S>(config: Config<T, S>) {
  const [view, setView] = useState<SuggestionView<T> | null>(null)
  const current = useRef<SuggestionView<T> | null>(null)
  const latest = useRef(config)
  useEffect(() => {
    latest.current = config
  })

  const options = useMemo(() => {
    const publish = (next: SuggestionView<T> | null) => {
      current.current = next
      setView(next)
    }
    const show = (props: SuggestionSource<S> & { items: T[] }, keepIndex: boolean) => {
      const last = Math.max(props.items.length - 1, 0)
      publish({
        items: props.items,
        query: props.query,
        rect: props.clientRect?.() ?? null,
        index: keepIndex ? Math.min(current.current?.index ?? 0, last) : 0,
        pick: (item) => {
          props.command(latest.current.toSelected(item))
        },
      })
    }
    const suggestion: SuggestionHandlers<T, S> = {
      items: ({ query }) => latest.current.filter(latest.current.source, query),
      command: (ctx) => {
        latest.current.onPick?.(ctx)
      },
      render: () => ({
        onStart: (props) => {
          show(props, false)
        },
        onUpdate: (props) => {
          show(props, true)
        },
        onExit: () => {
          publish(null)
        },
        onKeyDown: ({ event }): boolean => {
          const v = current.current
          if (!v || v.items.length === 0) return false
          switch (event.key) {
            case 'ArrowDown':
              publish({ ...v, index: (v.index + 1) % v.items.length })
              return true
            case 'ArrowUp':
              publish({ ...v, index: (v.index - 1 + v.items.length) % v.items.length })
              return true
            case 'Enter':
            case 'Tab': {
              const item = v.items[v.index]
              if (!item) return false
              v.pick(item)
              return true
            }
            default:
              return false
          }
        },
      }),
    }
    return suggestion
  }, [])
  return { view, options }
}

// A panel anchored above the caret; the composer sits at the bottom of the screen, so opening
// upwards keeps it on screen. mousedown is cancelled so the editor keeps its focus and selection.
export function FloatingPanel({ rect, label, width = 360, children }: { rect: DOMRect | null; label: string; width?: number; children: ReactNode }) {
  if (!rect) return null
  return (
    <div
      role="presentation"
      aria-label={label}
      onMouseDown={(e) => {
        e.preventDefault()
      }}
      style={{
        position: 'fixed',
        left: Math.max(8, Math.min(rect.left, window.innerWidth - width - 8)),
        bottom: window.innerHeight - rect.top + 6,
        width: Math.min(width, window.innerWidth - 16),
      }}
      className="z-50 overflow-hidden rounded-lg border border-line bg-float shadow-float"
    >
      {children}
    </div>
  )
}
