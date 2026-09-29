import { useNavigate } from '@tanstack/react-router'
import { useEffect } from 'react'

import { isTypingTarget } from '../../lib/keys'

const CHORD_MS = 1000

// The search field that is on screen: the page's own, else the sidebar's.
function visibleSearchInput(): HTMLInputElement | undefined {
  return Array.from(document.querySelectorAll<HTMLInputElement>('[data-search-input]'))
    .filter((el) => el.offsetParent !== null)
    .at(-1)
}

// ⌘K or Ctrl+K toggles the command bar, "/" searches, "?" lists the shortcuts and "g" followed
// by i, a or s navigates. The listener sits in the capture phase so the second key of a chord
// never also reaches the single-key shortcuts ("g a" must not open the assign picker).
export function useGlobalShortcuts({ onPalette, onHelp }: { onPalette: () => void; onHelp: () => void }) {
  const navigate = useNavigate()

  useEffect(() => {
    let chordStartedAt = 0
    const consume = (e: KeyboardEvent) => {
      e.preventDefault()
      e.stopImmediatePropagation()
    }
    const onKey = (e: KeyboardEvent) => {
      const mod = e.metaKey || e.ctrlKey
      if (mod && !e.altKey && !e.shiftKey && e.key.toLowerCase() === 'k') {
        consume(e)
        onPalette()
        return
      }
      if (mod || e.altKey || isTypingTarget(e.target)) {
        chordStartedAt = 0
        return
      }
      if (chordStartedAt > 0 && Date.now() - chordStartedAt < CHORD_MS) {
        chordStartedAt = 0
        if (e.key === 'i') {
          consume(e)
          void navigate({ to: '/inbox/$view', params: { view: 'mine' } })
          return
        }
        if (e.key === 'a') {
          consume(e)
          void navigate({ to: '/inbox/$view', params: { view: 'alle' } })
          return
        }
        if (e.key === 's') {
          consume(e)
          void navigate({ to: '/instellingen/profiel' })
          return
        }
      }
      chordStartedAt = 0
      if (e.key === 'g' && !e.shiftKey) {
        chordStartedAt = Date.now()
      } else if (e.key === '/') {
        consume(e)
        const input = visibleSearchInput()
        if (input) input.focus()
        else void navigate({ to: '/zoeken' })
      } else if (e.key === '?') {
        consume(e)
        onHelp()
      }
    }
    window.addEventListener('keydown', onKey, true)
    return () => {
      window.removeEventListener('keydown', onKey, true)
    }
  }, [navigate, onPalette, onHelp])
}
