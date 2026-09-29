import { Paperclip, ShieldAlert, TriangleAlert } from 'lucide-react'
import { useState } from 'react'

import { Dialog, DialogFooter } from '../../components/Dialog'
import { Button } from '../../components/ui'
import { formatBytes } from '../../lib/format'
import type { Attachment } from '../../lib/inbox'

const infectedText = 'De virusscanner heeft in dit bestand een virus gevonden. Downloaden is geblokkeerd.'
const unscannedText = 'Dit bestand kon niet op virussen worden gecontroleerd. Alleen openen als je de afzender vertrouwt.'
const dangerousText = 'Dit bestandstype kan software op je computer uitvoeren. Alleen openen als je de afzender vertrouwt.'

// hideInline drops images that the rendered mail already shows in place.
export function MessageAttachments({ attachments, hideInline }: { attachments: Attachment[]; hideInline: boolean }) {
  const [pending, setPending] = useState<Attachment>()
  const shown = attachments.filter((a) => !(hideInline && a.inline))
  if (shown.length === 0) return null

  const chip = 'inline-flex h-8 max-w-full items-center gap-2 rounded-full border border-line-strong bg-surface px-3 text-base'
  const sizeTone = 'text-muted'

  return (
    <>
      <ul className="mt-3 flex flex-wrap gap-2">
        {shown.map((a) => (
          <li key={a.id} className="max-w-full">
            {a.scan_status === 'infected' ? (
              <span className={`${chip} cursor-not-allowed opacity-80`} title={infectedText}>
                <ShieldAlert size={16} aria-hidden className="shrink-0 text-danger-text" />
                <span className="min-w-0 truncate line-through">{a.filename}</span>
                <span className="shrink-0 text-danger-text">Virus gevonden, geblokkeerd</span>
              </span>
            ) : a.dangerous || a.scan_status === 'error' ? (
              <button
                type="button"
                className={`${chip} hover:bg-subtle`}
                onClick={() => setPending(a)}
                title={a.dangerous ? 'Dit bestandstype kan software uitvoeren' : unscannedText}
              >
                <TriangleAlert size={16} aria-hidden className="shrink-0 text-danger-text" />
                <span className="min-w-0 truncate">{a.filename}</span>
                <span className={`shrink-0 tabular-nums ${sizeTone}`}>{formatBytes(a.size)}</span>
              </button>
            ) : (
              <a href={a.download_url} download className={`${chip} hover:bg-subtle`}>
                <Paperclip size={16} aria-hidden className="shrink-0" />
                <span className="min-w-0 truncate">{a.filename}</span>
                <span className={`shrink-0 tabular-nums ${sizeTone}`}>{formatBytes(a.size)}</span>
              </a>
            )}
          </li>
        ))}
      </ul>
      <Dialog
        open={pending !== undefined}
        onOpenChange={(open) => {
          if (!open) setPending(undefined)
        }}
        title="Bijlage downloaden?"
        description={pending?.dangerous ? dangerousText : unscannedText}
        size="sm"
      >
        <p className="text-base wrap-anywhere text-ink">{pending?.filename}</p>
        <DialogFooter>
          <Button onClick={() => setPending(undefined)}>Annuleren</Button>
          <Button
            variant="primary"
            onClick={() => {
              if (pending) window.location.assign(pending.download_url)
              setPending(undefined)
            }}
          >
            Toch downloaden
          </Button>
        </DialogFooter>
      </Dialog>
    </>
  )
}
