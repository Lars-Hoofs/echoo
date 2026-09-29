import { useQuery, useQueryClient } from '@tanstack/react-query'
import { useNavigate } from '@tanstack/react-router'
import { Paperclip } from 'lucide-react'
import { type SubmitEvent, useRef, useState } from 'react'

import { Dialog, DialogFooter } from '../../../components/Dialog'
import { type EditorValue, RichEditor } from '../../../components/editor/RichEditor'
import { useToast } from '../../../components/Toast'
import { Button, ErrorNotice, Field, IconButton, Input, Select } from '../../../components/ui'
import { api } from '../../../lib/api'
import type { SentMessage } from '../../../lib/composer'
import { errorMessage, fieldError } from '../../../lib/errors'
import type { Address } from '../../../lib/inbox'
import { summaryQuery } from '../../../lib/inbox'
import { AttachmentBar } from './AttachmentBar'
import { RecipientField } from './RecipientField'
import { useAttachments } from './useAttachments'

export function NewConversationDialog({ open, onOpenChange }: { open: boolean; onOpenChange: (open: boolean) => void }) {
  return (
    <Dialog open={open} onOpenChange={onOpenChange} title="Nieuw gesprek" description="Stuur een bericht naar iemand die je nog niet eerder schreef." size="lg">
      <NewConversationForm onClose={() => onOpenChange(false)} />
    </Dialog>
  )
}

function NewConversationForm({ onClose }: { onClose: () => void }) {
  const qc = useQueryClient()
  const navigate = useNavigate()
  const toast = useToast()
  const summary = useQuery(summaryQuery)
  const mailboxes = summary.data?.mailboxes ?? []
  const [mailboxChoice, setMailboxChoice] = useState('')
  const mailboxId = mailboxChoice || mailboxes[0]?.id || ''
  const [to, setTo] = useState<Address[]>([])
  const [cc, setCc] = useState<Address[]>([])
  const [bcc, setBcc] = useState<Address[]>([])
  const [showCc, setShowCc] = useState(false)
  const [subject, setSubject] = useState('')
  const [value, setValue] = useState<EditorValue>({ html: '', empty: true })
  const [sending, setSending] = useState(false)
  const [error, setError] = useState<unknown>(null)
  const att = useAttachments([])
  const fileInput = useRef<HTMLInputElement>(null)
  const idempotencyKey = useRef(crypto.randomUUID())

  const canSend = mailboxId !== '' && to.length > 0 && subject.trim() !== '' && !value.empty && !att.busy && !att.failed && !sending

  async function submit(e?: SubmitEvent) {
    e?.preventDefault()
    if (!canSend) return
    setSending(true)
    setError(null)
    try {
      const res = await api<SentMessage>('POST', '/conversations', {
        idempotency_key: idempotencyKey.current,
        mailbox_id: mailboxId,
        to,
        cc,
        bcc,
        subject,
        html: value.html,
        attachment_ids: att.uploads.map((u) => u.id),
      })
      await qc.invalidateQueries({ queryKey: ['inbox'] })
      toast('Gesprek aangemaakt. Het bericht wordt verstuurd.')
      onClose()
      if (res.conversation_id) {
        await navigate({ to: '/inbox/$view/$conversationId', params: { view: 'alle', conversationId: res.conversation_id } })
      }
    } catch (err) {
      setError(err)
      setSending(false)
    }
  }

  const fieldFailed = ['mailbox_id', 'to', 'cc', 'bcc', 'subject', 'html', 'attachment_ids'].some((f) => fieldError(error, f))

  return (
    <form onSubmit={(e) => void submit(e)} className="flex flex-col gap-3" noValidate>
      {error !== null && (
        <ErrorNotice>
          {fieldError(error, 'to') ??
            fieldError(error, 'cc') ??
            fieldError(error, 'bcc') ??
            fieldError(error, 'subject') ??
            fieldError(error, 'html') ??
            fieldError(error, 'attachment_ids') ??
            fieldError(error, 'mailbox_id') ??
            (fieldFailed ? undefined : errorMessage(error))}
        </ErrorNotice>
      )}
      <Field label="Verstuur vanuit">
        {(p) => (
          <Select {...p} value={mailboxId} onChange={(e) => setMailboxChoice(e.target.value)}>
            {mailboxes.map((m) => (
              <option key={m.id} value={m.id}>
                {m.name} ({m.email_address})
              </option>
            ))}
          </Select>
        )}
      </Field>
      <div>
        <RecipientField label="Aan" value={to} onChange={setTo} autoFocus />
        {showCc ? (
          <>
            <RecipientField label="Cc" value={cc} onChange={setCc} />
            <RecipientField label="Bcc" value={bcc} onChange={setBcc} />
          </>
        ) : (
          <button type="button" onClick={() => setShowCc(true)} className="t-label ml-19 underline underline-offset-2 hover:text-ink">
            Cc en Bcc
          </button>
        )}
      </div>
      <Field label="Onderwerp">{(p) => <Input {...p} value={subject} onChange={(e) => setSubject(e.target.value)} maxLength={300} />}</Field>
      <div>
        <RichEditor
          initialHtml=""
          ariaLabel="Bericht"
          placeholder="Schrijf je bericht"
          onChange={setValue}
          onSubmit={() => void submit()}
          toolbarExtra={
            <IconButton label="Bijlage toevoegen" size="sm" className="border-transparent" onClick={() => fileInput.current?.click()}>
              <Paperclip aria-hidden />
            </IconButton>
          }
        />
        <input ref={fileInput} type="file" multiple hidden aria-label="Bestanden kiezen" onChange={(e) => {
          att.add(Array.from(e.target.files ?? []))
          e.target.value = ''
        }} />
        <AttachmentBar items={att.items} onRemove={att.remove} />
      </div>
      <p className="t-label">Je handtekening en de voettekst van de werkruimte worden automatisch toegevoegd.</p>
      <DialogFooter>
        <Button onClick={onClose}>Annuleren</Button>
        <Button type="submit" variant="primary" busy={sending} disabled={!canSend}>
          Versturen
        </Button>
      </DialogFooter>
    </form>
  )
}
