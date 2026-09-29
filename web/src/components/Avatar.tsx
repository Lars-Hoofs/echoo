import type { CSSProperties } from 'react'

import { initials } from '../lib/avatar'

// ron avatars are neutral: initials on a grey circle. Size is data, so it goes in as a style.
export function Avatar({ name, size = 32 }: { name: string; size?: number }) {
  const style = size === 32 ? undefined : ({ width: size, height: size, fontSize: Math.max(12, Math.round(size * 0.4)) } satisfies CSSProperties)
  return (
    <span aria-hidden className="avatar select-none" style={style}>
      {initials(name)}
    </span>
  )
}
