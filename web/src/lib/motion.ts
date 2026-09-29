import { type RefObject, useEffect, useRef } from 'react'

const reducedMotion = window.matchMedia('(prefers-reduced-motion: reduce)')

// ron.css only animates under html.motion. Without it (reduced motion, no IntersectionObserver)
// everything is complete and still.
export const motionEnabled = !reducedMotion.matches && 'IntersectionObserver' in window

export function initMotion(): void {
  if (motionEnabled) document.documentElement.classList.add('motion')
}

// Adds is-in to the element once it scrolls into view, which plays the .reveal focus pull.
// Elements mount and unmount with the route, so every navigation runs the reveal again.
export function useReveal<T extends HTMLElement>(): RefObject<T | null> {
  const ref = useRef<T | null>(null)
  useEffect(() => {
    const el = ref.current
    if (!el) return
    if (!motionEnabled) {
      el.classList.add('is-in')
      return
    }
    const observer = new IntersectionObserver(
      ([entry]) => {
        if (!entry?.isIntersecting) return
        el.classList.add('is-in')
        observer.disconnect()
      },
      { threshold: 0.15 },
    )
    observer.observe(el)
    return () => {
      observer.disconnect()
    }
  }, [])
  return ref
}
