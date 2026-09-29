import { Check, Copy } from 'lucide-react'
import { useState } from 'react'

import { Button } from './ui'

// Copies text to the clipboard. The clipboard is unavailable on plain-http origins other than
// localhost, so a failure says so instead of pretending it worked.
export function CopyButton({ text, label = 'Kopiëren' }: { text: string; label?: string }) {
  const [state, setState] = useState<'idle' | 'copied' | 'failed'>('idle')
  const copy = () => {
    navigator.clipboard.writeText(text).then(
      () => setState('copied'),
      () => setState('failed'),
    )
  }
  return (
    <span className="inline-flex items-center gap-2">
      <Button onClick={copy}>
        {state === 'copied' ? <Check size={16} aria-hidden /> : <Copy size={16} aria-hidden />}
        {state === 'copied' ? 'Gekopieerd' : label}
      </Button>
      {state === 'failed' && (
        <span role="status" className="text-sm text-danger-text">
          Kopiëren lukt hier niet. Selecteer de tekst en kopieer die zelf.
        </span>
      )}
    </span>
  )
}
