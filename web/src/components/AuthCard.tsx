import type { ReactNode } from 'react'

import { Logo } from './Logo'
import { Reveal } from './Reveal'

// Text links on the pages shown before the app. Ink with a hairline underline: a link is not "the customer waits".
export const authLinkClass = 'w-fit rounded-sm text-base text-ink underline decoration-line-strong underline-offset-4 hover:decoration-ink'

// Centered card for the pages shown before the app: login, account setup and the mail links.
export function AuthCard({ title, intro, children }: { title: string; intro?: string; children: ReactNode }) {
  return (
    <main className="flex min-h-full flex-col items-center justify-center gap-6 px-4 py-8">
      <Logo />
      <Reveal className="w-full max-w-100">
        <div className="card p-6 sm:p-8">
          <h1 className="t-h3 text-ink">{title}</h1>
          {intro && <p className="t-body mt-2">{intro}</p>}
          <div className="mt-6">{children}</div>
        </div>
      </Reveal>
    </main>
  )
}
