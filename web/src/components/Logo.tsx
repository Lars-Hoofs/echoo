import { Wordmark } from './ui'

// ron's logo tile: an accent square with the initial, and the wordmark beside it. sm is for the
// phone top bar.
export function Logo({ size = 'md', className = '' }: { size?: 'sm' | 'md'; className?: string }) {
  return (
    <span className={`inline-flex items-center gap-3 ${className}`}>
      <span aria-hidden className={`logo ${size === 'sm' ? 'size-8 text-base' : ''}`}>
        e
      </span>
      <Wordmark className="text-xl" />
    </span>
  )
}
