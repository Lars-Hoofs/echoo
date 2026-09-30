import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Plus, ShieldBan } from 'lucide-react'
import { type SubmitEvent, useState } from 'react'

import { Dialog, DialogFooter } from '../../components/Dialog'
import { Button, Card, EmptyState, ErrorNotice, Field, Input, Page, PageHeader, Select, Skeleton, Table, TBody, Td, Th, THead, Tr } from '../../components/ui'
import { api } from '../../lib/api'
import { type BlockedSender, blockedSendersQuery, blockSender, describePattern, normalizePattern } from '../../lib/blocklist'
import { errorMessage, fieldError } from '../../lib/errors'
import { formatDateTime } from '../../lib/format'
import type { Ref } from '../../lib/inbox'

export function BlockedSendersPage() {
  const list = useQuery(blockedSendersQuery)
  const [adding, setAdding] = useState(false)
  const [removing, setRemoving] = useState<BlockedSender | null>(null)

  return (
    <Page>
      <PageHeader
        breadcrumb="Werkruimte"
        title="Geblokkeerde afzenders"
        description="Nieuwe gesprekken van een geblokkeerd adres of domein komen direct in spam, zonder toewijzing, automatisch antwoord of melding. Antwoorden in bestaande gesprekken komen gewoon binnen."
        actions={
          <Button variant="primary" onClick={() => setAdding(true)}>
            <Plus size={16} aria-hidden />
            Afzender blokkeren
          </Button>
        }
      />
      {list.isPending ? (
        <Skeleton className="h-40" />
      ) : list.isError ? (
        <ErrorNotice>{errorMessage(list.error)}</ErrorNotice>
      ) : list.data.blocked_senders.length === 0 ? (
        <Card>
          <EmptyState
            icon={<ShieldBan size={20} />}
            title="Nog niemand geblokkeerd"
            description="Blokkeer een afzender hier, of kies in een gesprek 'Spam en afzender blokkeren'."
          />
        </Card>
      ) : (
        <Card flush>
          <Table>
            <THead>
              <Th>Afzender</Th>
              <Th className="hidden md:table-cell">Mailbox</Th>
              <Th className="hidden sm:table-cell">Geblokkeerd</Th>
              <Th className="w-px">
                <span className="sr-only">Acties</span>
              </Th>
            </THead>
            <TBody>
              {list.data.blocked_senders.map((b) => (
                <Tr key={b.id}>
                  <Td>
                    <div className="flex min-w-0 flex-col">
                      <span className="truncate text-ink">{b.pattern}</span>
                      <span className="truncate text-sm text-muted md:hidden">{b.mailbox.name}</span>
                    </div>
                  </Td>
                  <Td className="hidden text-muted md:table-cell">{b.mailbox.name}</Td>
                  <Td className="hidden text-muted sm:table-cell">
                    {formatDateTime(b.created_at)}
                    {b.created_by && <span className="block text-sm">door {b.created_by.name}</span>}
                  </Td>
                  <Td>
                    <Button size="sm" variant="ghost" onClick={() => setRemoving(b)} aria-label={`Blokkering van ${b.pattern} opheffen`}>
                      Opheffen
                    </Button>
                  </Td>
                </Tr>
              ))}
            </TBody>
          </Table>
        </Card>
      )}
      {adding && list.data && <AddDialog mailboxes={list.data.mailboxes} onClose={() => setAdding(false)} />}
      {removing && <RemoveDialog entry={removing} onClose={() => setRemoving(null)} />}
    </Page>
  )
}

function AddDialog({ mailboxes, onClose }: { mailboxes: Ref[]; onClose: () => void }) {
  const [mailboxChoice, setMailboxChoice] = useState('')
  const [pattern, setPattern] = useState('')
  const mailboxId = mailboxChoice || mailboxes[0]?.id || ''
  const normalized = normalizePattern(pattern)
  const qc = useQueryClient()
  const add = useMutation({
    mutationFn: () => blockSender(mailboxId, pattern),
    onSuccess: async () => {
      await qc.invalidateQueries({ queryKey: blockedSendersQuery.queryKey })
      onClose()
    },
  })
  const submit = (e: SubmitEvent) => {
    e.preventDefault()
    add.mutate()
  }
  const patternError = fieldError(add.error, 'pattern')
  const mailboxError = fieldError(add.error, 'mailbox_id')
  return (
    <Dialog open onOpenChange={(o) => !o && onClose()} title="Afzender blokkeren">
      <form onSubmit={submit} className="flex flex-col gap-4" noValidate>
        {add.isError && !patternError && !mailboxError && <ErrorNotice>{errorMessage(add.error)}</ErrorNotice>}
        <Field label="Mailbox" error={mailboxError}>
          {(p) => (
            <Select {...p} value={mailboxId} onChange={(e) => setMailboxChoice(e.target.value)}>
              {mailboxes.map((m) => (
                <option key={m.id} value={m.id}>
                  {m.name}
                </option>
              ))}
            </Select>
          )}
        </Field>
        <Field
          label="E-mailadres of domein"
          help={normalized ? describePattern(normalized) : 'Bijvoorbeeld naam@voorbeeld.nl, of voorbeeld.nl voor het hele domein.'}
          error={patternError}
        >
          {(p) => <Input {...p} autoFocus maxLength={320} value={pattern} onChange={(e) => setPattern(e.target.value)} />}
        </Field>
        <DialogFooter>
          <Button onClick={onClose}>Annuleren</Button>
          <Button type="submit" variant="primary" busy={add.isPending} disabled={!normalized || !mailboxId}>
            Blokkeren
          </Button>
        </DialogFooter>
      </form>
    </Dialog>
  )
}

function RemoveDialog({ entry, onClose }: { entry: BlockedSender; onClose: () => void }) {
  const qc = useQueryClient()
  const remove = useMutation({
    mutationFn: () => api('DELETE', `/blocked-senders/${entry.id}`),
    onSuccess: async () => {
      await qc.invalidateQueries({ queryKey: blockedSendersQuery.queryKey })
      onClose()
    },
  })
  return (
    <Dialog open onOpenChange={(o) => !o && onClose()} title="Blokkering opheffen">
      <div className="flex flex-col gap-4">
        <p className="text-base text-muted">
          Nieuwe mail van {entry.pattern} komt weer gewoon binnen in {entry.mailbox.name}. Gesprekken die al in spam staan, blijven daar.
        </p>
        {remove.isError && <ErrorNotice>{errorMessage(remove.error)}</ErrorNotice>}
        <DialogFooter>
          <Button onClick={onClose}>Annuleren</Button>
          <Button variant="primary" busy={remove.isPending} onClick={() => remove.mutate()}>
            Opheffen
          </Button>
        </DialogFooter>
      </div>
    </Dialog>
  )
}
