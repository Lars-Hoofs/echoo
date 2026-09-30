import { useInfiniteQuery, useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Link } from '@tanstack/react-router'
import { RotateCcw, Trash2 } from 'lucide-react'
import { useState } from 'react'

import { Dialog, DialogFooter } from '../../components/Dialog'
import { useToast } from '../../components/Toast'
import { Button, Card, EmptyState, ErrorNotice, PageHeader, Select, Skeleton, Table, TBody, Td, Th, THead, Tr } from '../../components/ui'
import { errorMessage } from '../../lib/errors'
import { formatDateTime, formatRelative } from '../../lib/format'
import { summaryQuery } from '../../lib/inbox'
import { emptyTrash, trashQuery } from '../../lib/trash'
import { useTrashActions } from './useTrashActions'

type Confirm = { kind: 'purge'; ids: string[] } | { kind: 'empty' }

export function TrashPage() {
  const [mailboxId, setMailboxId] = useState('')
  const list = useInfiniteQuery(trashQuery(mailboxId))
  const summary = useQuery(summaryQuery)
  const run = useTrashActions()
  const [selected, setSelected] = useState<ReadonlySet<string>>(new Set())
  const [confirm, setConfirm] = useState<Confirm | null>(null)

  const rows = list.data?.pages.flatMap((p) => p.conversations) ?? []
  // Rows that left the list (restored elsewhere, emptied) drop out of the selection.
  const picked = rows.filter((r) => selected.has(r.id)).map((r) => r.id)
  const allPicked = rows.length > 0 && picked.length === rows.length
  const mailboxes = summary.data?.mailboxes ?? []

  const toggle = (id: string) => {
    const next = new Set(selected)
    if (next.has(id)) next.delete(id)
    else next.add(id)
    setSelected(next)
  }

  const restore = async () => {
    await run('restore', picked)
    setSelected(new Set())
  }

  return (
    <div className="relative h-full overflow-y-auto px-4 py-8 md:px-8">
      <div className="mx-auto flex w-full max-w-5xl flex-col gap-6">
        <PageHeader
          breadcrumb="Gesprekken"
          title="Prullenbak"
          description="Verwijderde gesprekken staan hier tot de bewaartermijn van de prullenbak voorbij is; daarna verwijdert Echoo ze definitief. Zet een gesprek terug om het weer in de inbox te zien."
          actions={
            <Button variant="danger" disabled={rows.length === 0} onClick={() => setConfirm({ kind: 'empty' })}>
              <Trash2 size={16} aria-hidden />
              Prullenbak legen
            </Button>
          }
        />
        {mailboxes.length > 1 && (
          <Select
            className="w-64"
            aria-label="Mailbox"
            value={mailboxId}
            onChange={(e) => {
              setMailboxId(e.target.value)
              setSelected(new Set())
            }}
          >
            <option value="">Alle mailboxen</option>
            {mailboxes.map((m) => (
              <option key={m.id} value={m.id}>
                {m.name}
              </option>
            ))}
          </Select>
        )}
        {list.isPending ? (
          <Skeleton className="h-64" />
        ) : list.isError ? (
          <ErrorNotice>{errorMessage(list.error)}</ErrorNotice>
        ) : rows.length === 0 ? (
          <Card>
            <EmptyState icon={<Trash2 size={20} />} title="De prullenbak is leeg" description="Gesprekken die je verwijdert, komen hier eerst terecht." />
          </Card>
        ) : (
          <>
            {picked.length > 0 && (
              <div role="toolbar" aria-label="Acties voor selectie" className="flex flex-wrap items-center gap-2">
                <span role="status" className="t-title mr-2">
                  {picked.length === 1 ? '1 geselecteerd' : `${picked.length} geselecteerd`}
                </span>
                <Button size="sm" variant="primary" onClick={() => void restore()}>
                  <RotateCcw size={16} aria-hidden />
                  Terugzetten
                </Button>
                <Button size="sm" variant="danger" onClick={() => setConfirm({ kind: 'purge', ids: picked })}>
                  Definitief verwijderen
                </Button>
              </div>
            )}
            <Card flush>
              <Table>
                <THead>
                  <Th className="w-px">
                    <input
                      type="checkbox"
                      checked={allPicked}
                      onChange={() => setSelected(allPicked ? new Set() : new Set(rows.map((r) => r.id)))}
                      aria-label="Alles selecteren"
                      className="size-4 accent-(--ink)"
                    />
                  </Th>
                  <Th>Gesprek</Th>
                  <Th className="hidden md:table-cell">Mailbox</Th>
                  <Th className="hidden sm:table-cell">Verwijderd</Th>
                </THead>
                <TBody>
                  {rows.map((c) => {
                    const name = c.contact?.name || c.contact?.email || 'Onbekende afzender'
                    return (
                      <Tr key={c.id}>
                        <Td>
                          <input
                            type="checkbox"
                            checked={selected.has(c.id)}
                            onChange={() => toggle(c.id)}
                            aria-label={`Selecteer gesprek van ${name}`}
                            className="size-4 accent-(--ink)"
                          />
                        </Td>
                        <Td>
                          <div className="flex min-w-0 flex-col">
                            <Link
                              to="/prullenbak/$conversationId"
                              params={{ conversationId: c.id }}
                              className="truncate text-ink underline-offset-4 hover:underline"
                            >
                              {c.subject || '(geen onderwerp)'}
                            </Link>
                            <span className="truncate text-sm text-muted">
                              {name} · #{c.number}
                            </span>
                          </div>
                        </Td>
                        <Td className="hidden text-muted md:table-cell">{c.mailbox.name}</Td>
                        <Td className="hidden text-muted sm:table-cell">
                          <span title={formatDateTime(c.deleted_at)}>{formatRelative(c.deleted_at)}</span>
                          {c.deleted_by && <span className="block text-sm">door {c.deleted_by.name}</span>}
                        </Td>
                      </Tr>
                    )
                  })}
                </TBody>
              </Table>
            </Card>
            {list.hasNextPage && (
              <div className="flex justify-center">
                <Button busy={list.isFetchingNextPage} onClick={() => void list.fetchNextPage()}>
                  Meer laden
                </Button>
              </div>
            )}
          </>
        )}
        {confirm?.kind === 'purge' && (
          <PurgeDialog
            ids={confirm.ids}
            onClose={() => setConfirm(null)}
            onDone={() => {
              setConfirm(null)
              setSelected(new Set())
            }}
          />
        )}
        {confirm?.kind === 'empty' && (
          <EmptyDialog
            mailboxId={mailboxId}
            mailboxName={mailboxes.find((m) => m.id === mailboxId)?.name}
            onClose={() => setConfirm(null)}
            onDone={() => {
              setConfirm(null)
              setSelected(new Set())
            }}
          />
        )}
      </div>
    </div>
  )
}

export function PurgeDialog({ ids, onClose, onDone }: { ids: string[]; onClose: () => void; onDone: () => void }) {
  const run = useTrashActions()
  const [busy, setBusy] = useState(false)
  const what = ids.length === 1 ? 'dit gesprek' : `deze ${ids.length} gesprekken`
  return (
    <Dialog open onOpenChange={(o) => !o && onClose()} title="Definitief verwijderen">
      <div className="flex flex-col gap-4">
        <p className="text-base text-muted">
          Je verwijdert {what} met alle berichten, notities en bijlagen. Dit kun je niet ongedaan maken.
        </p>
        <DialogFooter>
          <Button onClick={onClose}>Annuleren</Button>
          <Button
            variant="danger"
            busy={busy}
            onClick={() => {
              setBusy(true)
              void run('purge', ids).then(onDone)
            }}
          >
            Definitief verwijderen
          </Button>
        </DialogFooter>
      </div>
    </Dialog>
  )
}

function EmptyDialog({
  mailboxId,
  mailboxName,
  onClose,
  onDone,
}: {
  mailboxId: string
  mailboxName: string | undefined
  onClose: () => void
  onDone: () => void
}) {
  const qc = useQueryClient()
  const toast = useToast()
  const empty = useMutation({
    mutationFn: () => emptyTrash(mailboxId),
    onSuccess: (deleted) => {
      void qc.invalidateQueries({ queryKey: ['inbox'] })
      toast(deleted === 1 ? '1 gesprek definitief verwijderd' : `${deleted} gesprekken definitief verwijderd`)
      onDone()
    },
  })
  return (
    <Dialog open onOpenChange={(o) => !o && onClose()} title="Prullenbak legen">
      <div className="flex flex-col gap-4">
        <p className="text-base text-muted">
          {mailboxName
            ? `Alle gesprekken in de prullenbak van ${mailboxName} worden definitief verwijderd, met hun berichten, notities en bijlagen.`
            : 'Alle gesprekken in de prullenbak worden definitief verwijderd, met hun berichten, notities en bijlagen.'}{' '}
          Dit kun je niet ongedaan maken.
        </p>
        {empty.isError && <ErrorNotice>{errorMessage(empty.error)}</ErrorNotice>}
        <DialogFooter>
          <Button onClick={onClose}>Annuleren</Button>
          <Button variant="danger" busy={empty.isPending} onClick={() => empty.mutate()}>
            Prullenbak legen
          </Button>
        </DialogFooter>
      </div>
    </Dialog>
  )
}
