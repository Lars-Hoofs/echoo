import { useMutation, useQueryClient } from '@tanstack/react-query'
import { MailX } from 'lucide-react'
import { useState } from 'react'

import { Dialog, DialogFooter } from '../../components/Dialog'
import { useToast } from '../../components/Toast'
import { Button, ErrorNotice } from '../../components/ui'
import { api } from '../../lib/api'
import { type ContactFull } from '../../lib/contacts'
import { errorMessage } from '../../lib/errors'
import { formatDateTime } from '../../lib/format'

type Undo = { kind: 'unsubscribe' } | { kind: 'bounce'; email: string }

// Says why campaigns skip this contact, and lets someone with campaigns.manage undo it. Nothing
// shows for a contact that is reachable.
export function ConsentStatus({ contact, canUndo }: { contact: ContactFull; canUndo: boolean }) {
  const [undo, setUndo] = useState<Undo | null>(null)
  const bouncing = contact.emails.filter((e) => e.bounced_at)
  if (!contact.unsubscribed_at && bouncing.length === 0) return null
  return (
    <div className="card card-line card-s flex flex-col gap-3">
      {contact.unsubscribed_at && (
        <div className="flex flex-wrap items-center justify-between gap-2">
          <p className="flex items-center gap-2 text-base text-ink">
            <MailX size={16} className="shrink-0 text-muted" aria-hidden />
            Afgemeld voor campagnes sinds {formatDateTime(contact.unsubscribed_at)}. Campagnes slaan dit contact over.
          </p>
          {canUndo && (
            <Button size="sm" onClick={() => setUndo({ kind: 'unsubscribe' })}>
              Afmelding opheffen
            </Button>
          )}
        </div>
      )}
      {bouncing.map((e) => (
        <div key={e.email} className="flex flex-wrap items-center justify-between gap-2">
          <p className="flex items-center gap-2 text-base text-ink">
            <MailX size={16} className="shrink-0 text-danger-text" aria-hidden />
            <span className="wrap-anywhere">
              {e.email} is onbereikbaar sinds {formatDateTime(e.bounced_at ?? null)}. Campagnes gebruiken dit adres niet meer.
            </span>
          </p>
          {canUndo && (
            <Button size="sm" onClick={() => setUndo({ kind: 'bounce', email: e.email })}>
              Adres weer gebruiken
            </Button>
          )}
        </div>
      ))}
      {undo && <UndoDialog contact={contact} undo={undo} onClose={() => setUndo(null)} />}
    </div>
  )
}

function UndoDialog({ contact, undo, onClose }: { contact: ContactFull; undo: Undo; onClose: () => void }) {
  const queryClient = useQueryClient()
  const toast = useToast()
  const unsubscribe = undo.kind === 'unsubscribe'
  const run = useMutation({
    mutationFn: () =>
      unsubscribe
        ? api('POST', `/contacts/${contact.id}/resubscribe`)
        : api('POST', `/contacts/${contact.id}/reactivate-address`, {
            email: undo.email,
          }),
    onSuccess: async () => {
      await queryClient.invalidateQueries({
        queryKey: ['contact', contact.id],
      })
      toast(unsubscribe ? 'Afmelding opgeheven' : 'Adres wordt weer gebruikt')
      onClose()
    },
  })
  return (
    <Dialog open onOpenChange={(o) => !o && onClose()} title={unsubscribe ? 'Afmelding opheffen' : 'Adres weer gebruiken'}>
      <div className="flex flex-col gap-4">
        <p className="text-base text-ink">
          {unsubscribe
            ? 'Alleen doen als het contact zelf heeft gevraagd om weer mail te ontvangen. Campagnes sturen dan weer naar dit contact.'
            : `Alleen doen als het probleem met ${undo.email} is opgelost. Campagnes sturen dan weer naar dit adres; bij een nieuwe fout wordt het opnieuw gemarkeerd.`}
        </p>
        {run.isError && <ErrorNotice>{errorMessage(run.error)}</ErrorNotice>}
        <DialogFooter>
          <Button onClick={onClose}>Annuleren</Button>
          <Button variant="primary" busy={run.isPending} onClick={() => run.mutate()}>
            {unsubscribe ? 'Afmelding opheffen' : 'Adres weer gebruiken'}
          </Button>
        </DialogFooter>
      </div>
    </Dialog>
  )
}
