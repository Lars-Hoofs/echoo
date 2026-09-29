import { X } from 'lucide-react'
import { type ClipboardEvent, type KeyboardEvent, useId, useState } from 'react'

import { formatAddress, mergeAddresses, parseRecipients } from '../../../lib/composer'
import type { Address } from '../../../lib/inbox'

// Recipients as chips. Typed text becomes a chip on Enter, comma, semicolon, Tab or when the
// field loses focus; pasted lists are split into chips at once.
export function RecipientField({
  label,
  value,
  onChange,
  autoFocus = false,
}: {
  label: string
  value: Address[]
  onChange: (next: Address[]) => void
  autoFocus?: boolean
}) {
  const id = useId()
  const [text, setText] = useState('')
  const [invalid, setInvalid] = useState<string[]>([])

  function commit(input: string) {
    if (!input.trim()) return
    const parsed = parseRecipients(input)
    if (parsed.valid.length > 0) onChange(mergeAddresses(value, parsed.valid))
    setText(parsed.invalid.join(', '))
    setInvalid(parsed.invalid)
  }

  function onKeyDown(e: KeyboardEvent<HTMLInputElement>) {
    if (e.key === 'Enter' || e.key === ',' || e.key === ';' || (e.key === 'Tab' && text.trim() !== '')) {
      if (text.trim() === '') return
      e.preventDefault()
      commit(text)
    } else if (e.key === 'Backspace' && text === '' && value.length > 0) {
      onChange(value.slice(0, -1))
    }
  }

  function onPaste(e: ClipboardEvent<HTMLInputElement>) {
    const pasted = e.clipboardData.getData('text')
    if (!/[,;\n\s]/.test(pasted.trim())) return
    e.preventDefault()
    commit(pasted)
  }

  return (
    <div className="flex items-start gap-3 py-2">
      <label htmlFor={id} className="t-label w-16 shrink-0 pt-2">
        {label}
      </label>
      <div className="min-w-0 flex-1">
        <div className="flex flex-wrap items-center gap-2 rounded-full border border-transparent focus-within:border-line-strong">
          {value.map((a) => (
            <span key={a.address} className="inline-flex h-8 max-w-full items-center gap-1 rounded-full bg-subtle pr-1 pl-3 text-base text-ink">
              <span className="truncate" title={formatAddress(a)}>
                {a.name || a.address}
              </span>
              <button
                type="button"
                aria-label={`${a.address} verwijderen`}
                onClick={() => onChange(value.filter((x) => x.address !== a.address))}
                className="inline-flex size-6 shrink-0 items-center justify-center rounded-full text-muted hover:bg-active hover:text-ink"
              >
                <X size={16} aria-hidden />
              </button>
            </span>
          ))}
          <input
            id={id}
            value={text}
            autoFocus={autoFocus}
            onChange={(e) => {
              setText(e.target.value)
              setInvalid([])
            }}
            onKeyDown={onKeyDown}
            onPaste={onPaste}
            onBlur={() => commit(text)}
            aria-invalid={invalid.length > 0 || undefined}
            aria-describedby={invalid.length > 0 ? `${id}-error` : undefined}
            className="h-8 min-w-32 flex-1 bg-transparent px-2 text-base text-ink placeholder:text-faint focus-visible:outline-none"
            autoComplete="off"
            spellCheck={false}
          />
        </div>
        {invalid.length > 0 && (
          <p id={`${id}-error`} className="mt-1 text-base text-danger-text">
            {invalid.length === 1 ? `“${invalid[0] ?? ''}” is geen geldig e-mailadres.` : 'Sommige adressen zijn ongeldig.'}
          </p>
        )}
      </div>
    </div>
  )
}
