import { useQuery, useQueryClient } from '@tanstack/react-query'
import { getRouteApi } from '@tanstack/react-router'
import { Forward } from 'lucide-react'
import { useRef, useState } from 'react'

import { Dialog, DialogFooter } from '../../../components/Dialog'
import { type EditorValue, RichEditor } from '../../../components/editor/RichEditor'
import { useToast } from '../../../components/Toast'
import { Button, ErrorNotice } from '../../../components/ui'
import { api } from '../../../lib/api'
import { errorMessage, fieldError } from '../../../lib/errors'
import type { Address } from '../../../lib/inbox'
import { hasPermission, meQuery } from '../../../lib/session'
import { RecipientField } from './RecipientField'

const route = getRouteApi('/auth/ready/inbox/$view/$conversationId')

// Forwards one message of the open conversation. The original goes along as an .eml attachment.
export function ForwardButton({ messageId, className = '' }: { messageId: string; className?: string }) {
  const me = useQuery(meQuery)
  const [open, setOpen] = useState(false)
  if (!hasPermission(me.data, 'conversations.write')) return null
  return (
    <>
      <button
        type="button"
        onClick={() => setOpen(true)}
        className={`inline-flex items-center gap-1 rounded-full underline underline-offset-2 hover:text-ink ${className}`}
      >
        <Forward size={16} aria-hidden />
        Doorsturen
      </button>
      <Dialog open={open} onOpenChange={setOpen} title="Bericht doorsturen" description="Het originele bericht gaat mee als bijlage." size="lg">
        <ForwardForm messageId={messageId} onClose={() => setOpen(false)} />
      </Dialog>
    </>
  )
}

function ForwardForm({ messageId, onClose }: { messageId: string; onClose: () => void }) {
  const { conversationId } = route.useParams()
  const qc = useQueryClient()
  const toast = useToast()
  const [to, setTo] = useState<Address[]>([])
  const [note, setNote] = useState<EditorValue>({ html: '', empty: true })
  const [sending, setSending] = useState(false)
  const [error, setError] = useState<unknown>(null)
  const idempotencyKey = useRef(crypto.randomUUID())

  async function submit() {
    if (to.length === 0 || sending) return
    setSending(true)
    setError(null)
    try {
      await api('POST', `/conversations/${encodeURIComponent(conversationId)}/forward`, {
        idempotency_key: idempotencyKey.current,
        message_id: messageId,
        to,
        html: note.empty ? '' : note.html,
      })
      await qc.invalidateQueries({ queryKey: ['inbox'] })
      toast('Bericht wordt doorgestuurd')
      onClose()
    } catch (err) {
      setError(err)
      setSending(false)
    }
  }

  return (
    <div className="flex flex-col gap-3">
      {error !== null && <ErrorNotice>{fieldError(error, 'to') ?? errorMessage(error)}</ErrorNotice>}
      <RecipientField label="Aan" value={to} onChange={setTo} autoFocus />
      <RichEditor initialHtml="" ariaLabel="Toelichting" placeholder="Voeg eventueel een toelichting toe" onChange={setNote} onSubmit={() => void submit()} />
      <DialogFooter>
        <Button onClick={onClose}>Annuleren</Button>
        <Button variant="primary" busy={sending} disabled={to.length === 0} onClick={() => void submit()}>
          Doorsturen
        </Button>
      </DialogFooter>
    </div>
  )
}
