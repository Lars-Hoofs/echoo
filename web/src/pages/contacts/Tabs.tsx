import { type KeyboardEvent, type ReactNode, useId } from 'react'

export interface TabDef<T extends string> {
  id: T
  label: string
}

// Tab list with roving focus: arrow keys move between tabs, Home and End jump to the ends.
export function Tabs<T extends string>({
  tabs,
  value,
  onChange,
  children,
}: {
  tabs: TabDef<T>[]
  value: T
  onChange: (id: T) => void
  children: ReactNode
}) {
  const base = useId()
  const onKey = (e: KeyboardEvent<HTMLButtonElement>, i: number) => {
    const step = e.key === 'ArrowRight' ? 1 : e.key === 'ArrowLeft' ? -1 : 0
    let next = i + step
    if (e.key === 'Home') next = 0
    else if (e.key === 'End') next = tabs.length - 1
    else if (step === 0) return
    const target = tabs[(next + tabs.length) % tabs.length]
    if (!target) return
    e.preventDefault()
    onChange(target.id)
    document.getElementById(`${base}-tab-${target.id}`)?.focus()
  }
  return (
    <div>
      <div role="tablist" className="tabs max-w-full overflow-x-auto">
        {tabs.map((t, i) => (
          <button
            key={t.id}
            id={`${base}-tab-${t.id}`}
            type="button"
            role="tab"
            aria-selected={t.id === value}
            aria-controls={`${base}-panel`}
            tabIndex={t.id === value ? 0 : -1}
            onClick={() => onChange(t.id)}
            onKeyDown={(e) => onKey(e, i)}
            className="tab aria-selected:bg-ink aria-selected:text-on-ink aria-selected:hover:text-on-ink max-md:h-11"
          >
            {t.label}
          </button>
        ))}
      </div>
      <div role="tabpanel" id={`${base}-panel`} aria-labelledby={`${base}-tab-${value}`} className="pt-5">
        {children}
      </div>
    </div>
  )
}
