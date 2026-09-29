import type { CSSProperties, ReactNode } from 'react'

import { useReveal } from '../lib/motion'

// Enters as a focus pull (blur and 24px low, then sharp). i staggers siblings by 70ms each.
export function Reveal({ i = 0, className = '', children }: { i?: number; className?: string; children: ReactNode }) {
  const ref = useReveal<HTMLDivElement>()
  return (
    <div ref={ref} className={`reveal ${className}`} style={{ '--i': i } as CSSProperties}>
      {children}
    </div>
  )
}
