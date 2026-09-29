import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { PenLine } from 'lucide-react'
import { useState } from 'react'

import { type EditorValue, RichEditor } from '../../components/editor/RichEditor'
import { Button, Card, ErrorNotice, Field, Select, Skeleton } from '../../components/ui'
import { api } from '../../lib/api'
import type { Signature } from '../../lib/composer'
import { errorMessage } from '../../lib/errors'
import { summaryQuery } from '../../lib/inbox'

const signaturesQuery = { queryKey: ['me', 'signatures'], queryFn: () => api<{ signatures: Signature[] }>('GET', '/me/signatures') }

// Writes one signature. Saving an empty editor removes the signature.
function SignatureEditor({ initialHtml, label, save, saving, error }: { initialHtml: string; label: string; save: (html: string) => void; saving: boolean; error: unknown }) {
  const [value, setValue] = useState<EditorValue>({ html: initialHtml, empty: initialHtml === '' })
  const [changed, setChanged] = useState(false)
  return (
    <div className="flex flex-col gap-3">
      {error !== null && error !== undefined && <ErrorNotice>{errorMessage(error)}</ErrorNotice>}
      <RichEditor
        initialHtml={initialHtml}
        ariaLabel={label}
        placeholder="Bijvoorbeeld je naam en functie"
        onChange={(v) => {
          setValue(v)
          setChanged(true)
        }}
      />
      <div>
        <Button variant="primary" busy={saving} disabled={!changed} onClick={() => save(value.empty ? '' : value.html)}>
          Opslaan
        </Button>
      </div>
    </div>
  )
}

// A personal signature per mailbox, or one default for every mailbox without its own.
export function SignatureCard() {
  const qc = useQueryClient()
  const summary = useQuery(summaryQuery)
  const signatures = useQuery(signaturesQuery)
  const [mailboxId, setMailboxId] = useState('')
  const save = useMutation({
    mutationFn: (html: string) => api('PUT', '/me/signatures', { mailbox_id: mailboxId || null, body_html: html }),
    onSuccess: async () => {
      await qc.invalidateQueries({ queryKey: ['me', 'signatures'] })
      await qc.invalidateQueries({ queryKey: ['composer', 'signature'] })
    },
  })
  const current = signatures.data?.signatures.find((s) => (s.mailbox_id ?? '') === mailboxId)?.body_html ?? ''

  return (
    <div id="handtekening">
      <Card title="Handtekening" icon={<PenLine size={16} />} description="Wordt onder je antwoorden gezet. Een handtekening voor een mailbox gaat voor je standaardhandtekening, en die gaat voor die van de mailbox.">
        {signatures.isPending ? (
          <Skeleton className="h-32" />
        ) : signatures.isError ? (
          <ErrorNotice>{errorMessage(signatures.error)}</ErrorNotice>
        ) : (
          <div className="flex flex-col gap-4">
            <div className="max-w-sm">
              <Field label="Voor">
                {(p) => (
                  <Select {...p} value={mailboxId} onChange={(e) => setMailboxId(e.target.value)}>
                    <option value="">Alle mailboxen (standaard)</option>
                    {summary.data?.mailboxes.map((m) => (
                      <option key={m.id} value={m.id}>
                        {m.name}
                      </option>
                    ))}
                  </Select>
                )}
              </Field>
            </div>
            <SignatureEditor
              key={`${mailboxId}:${current}`}
              initialHtml={current}
              label="Handtekening"
              saving={save.isPending}
              error={save.error}
              save={(html) => save.mutate(html)}
            />
          </div>
        )}
      </Card>
    </div>
  )
}

// The signature of a mailbox, used for everyone who has no signature of their own.
export function MailboxSignatureCard({ mailboxId }: { mailboxId: string }) {
  const qc = useQueryClient()
  const signature = useQuery({
    queryKey: ['mailboxes', mailboxId, 'signature'],
    queryFn: () => api<{ body_html: string }>('GET', `/mailboxes/${mailboxId}/signature`),
  })
  const save = useMutation({
    mutationFn: (html: string) => api<{ body_html: string }>('PUT', `/mailboxes/${mailboxId}/signature`, { body_html: html }),
    onSuccess: async () => {
      await qc.invalidateQueries({ queryKey: ['mailboxes', mailboxId, 'signature'] })
      await qc.invalidateQueries({ queryKey: ['composer', 'signature'] })
    },
  })
  return (
    <Card title="Handtekening" icon={<PenLine size={16} />} description="Voor iedereen die geen eigen handtekening heeft ingesteld.">
      {signature.isPending ? (
        <Skeleton className="h-32" />
      ) : signature.isError ? (
        <ErrorNotice>{errorMessage(signature.error)}</ErrorNotice>
      ) : (
        <SignatureEditor
          key={signature.data.body_html}
          initialHtml={signature.data.body_html}
          label="Handtekening van de mailbox"
          saving={save.isPending}
          error={save.error}
          save={(html) => save.mutate(html)}
        />
      )}
    </Card>
  )
}
