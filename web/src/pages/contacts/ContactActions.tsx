import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useNavigate } from '@tanstack/react-router'
import { useEffect, useState } from 'react'

import { Avatar } from '../../components/Avatar'
import { Dialog, DialogFooter } from '../../components/Dialog'
import { Button, buttonClass, ErrorNotice, Field, Input } from '../../components/ui'
import { api } from '../../lib/api'
import { type ContactFull, type ContactItem, contactDisplayName } from '../../lib/contacts'
import { errorMessage, fieldError } from '../../lib/errors'

export function MergeDialog({ primary, onClose }: { primary: ContactFull; onClose: () => void }) {
  const [term, setTerm] = useState('')
  const [debounced, setDebounced] = useState('')
  const [source, setSource] = useState<ContactItem | null>(null)
  const [confirming, setConfirming] = useState(false)
  useEffect(() => {
    const t = setTimeout(() => setDebounced(term.trim()), 300)
    return () => clearTimeout(t)
  }, [term])
  const found = useQuery({
    queryKey: ['contacts', 'merge-search', debounced],
    enabled: debounced.length >= 2,
    queryFn: () => api<{ contacts: ContactItem[] }>('GET', `/contacts?limit=8&sort=name&q=${encodeURIComponent(debounced)}`),
  })
  const queryClient = useQueryClient()
  const merge = useMutation({
    mutationFn: () => {
      if (!source) throw new Error('no source')
      return api('POST', `/contacts/${primary.id}/merge`, {
        source_id: source.id,
      })
    },
    onSuccess: async () => {
      await queryClient.invalidateQueries()
      onClose()
    },
  })
  const primaryName = contactDisplayName(primary)
  const results = (found.data?.contacts ?? []).filter((c) => c.id !== primary.id)

  if (confirming && source) {
    const sourceName = contactDisplayName(source)
    return (
      <Dialog open onOpenChange={(o) => !o && onClose()} title="Contacten samenvoegen">
        <div className="flex flex-col gap-4">
          <p className="text-base text-ink">
            {sourceName} wordt samengevoegd met {primaryName}. Alle e-mailadressen, gesprekken en notities van {sourceName} gaan naar{' '}
            {primaryName}. Bij dubbele gegevens wint {primaryName}.
          </p>
          <p className="text-base text-danger-text">
            Dit kan niet ongedaan worden gemaakt. {sourceName} bestaat daarna niet meer.
          </p>
          {merge.isError && <ErrorNotice>{fieldError(merge.error, 'source_id') ?? errorMessage(merge.error)}</ErrorNotice>}
          <DialogFooter>
            <Button onClick={() => setConfirming(false)}>Terug</Button>
            <Button variant="danger" busy={merge.isPending} onClick={() => merge.mutate()}>
              Definitief samenvoegen
            </Button>
          </DialogFooter>
        </div>
      </Dialog>
    )
  }
  return (
    <Dialog
      open
      onOpenChange={(o) => !o && onClose()}
      title="Samenvoegen met…"
      description={`Zoek het contact dat in ${primaryName} opgaat.`}
    >
      <div className="flex flex-col gap-4">
        <Field label="Zoek een contact">
          {(p) => (
            <Input {...p} autoFocus placeholder="Naam, e-mailadres of organisatie" value={term} onChange={(e) => setTerm(e.target.value)} />
          )}
        </Field>
        {found.isError && <ErrorNotice>{errorMessage(found.error)}</ErrorNotice>}
        {debounced.length >= 2 && found.data && results.length === 0 && <p className="text-base text-muted">Geen contacten gevonden.</p>}
        {results.length > 0 && (
          <fieldset className="flex flex-col gap-1">
            <legend className="sr-only">Zoekresultaten</legend>
            {results.map((c) => (
              <label
                key={c.id}
                className="flex cursor-pointer items-center gap-3 rounded-md px-3 py-2 hover:bg-subtle has-checked:bg-subtle"
              >
                <input type="radio" name="merge-source" className="accent-ink" checked={source?.id === c.id} onChange={() => setSource(c)} />
                <Avatar name={contactDisplayName(c)} />
                <span className="min-w-0">
                  <span className="block truncate text-base text-ink">{contactDisplayName(c)}</span>
                  {c.name && <span className="block truncate text-sm text-muted">{c.email}</span>}
                </span>
              </label>
            ))}
          </fieldset>
        )}
        <DialogFooter>
          <Button onClick={onClose}>Annuleren</Button>
          <Button variant="primary" disabled={!source} onClick={() => setConfirming(true)}>
            Verder
          </Button>
        </DialogFooter>
      </div>
    </Dialog>
  )
}

interface DataExport {
  id: string
  status: 'queued' | 'ready' | 'failed' | 'downloaded' | 'expired'
}

export function DataExportDialog({ contact, onClose }: { contact: ContactFull; onClose: () => void }) {
  const [exportId, setExportId] = useState('')
  const start = useMutation({
    mutationFn: () => api<{ export: DataExport }>('POST', `/contacts/${contact.id}/data-export`),
    onSuccess: (r) => setExportId(r.export.id),
  })
  const status = useQuery({
    queryKey: ['data-export', exportId],
    enabled: exportId !== '',
    queryFn: async () => (await api<{ export: DataExport }>('GET', `/data-exports/${exportId}`)).export,
    refetchInterval: (q) => (q.state.data?.status === 'queued' ? 1500 : false),
  })
  const state = status.data?.status
  return (
    <Dialog
      open
      onOpenChange={(o) => !o && onClose()}
      title="Gegevens exporteren"
      description="Een ZIP met het profiel, de notities, alle gesprekken met berichttekst en de bijlagen van dit contact."
    >
      <div className="flex flex-col gap-4">
        {start.isError && <ErrorNotice>{errorMessage(start.error)}</ErrorNotice>}
        {status.isError && <ErrorNotice>{errorMessage(status.error)}</ErrorNotice>}
        {!exportId && (
          <p className="text-base text-muted">De export wordt op de achtergrond gemaakt. Je kunt hem daarna één keer downloaden.</p>
        )}
        {exportId && state === 'queued' && (
          <p role="status" className="text-base text-muted">
            De export wordt gemaakt…
          </p>
        )}
        {state === 'failed' && <ErrorNotice>De export is mislukt. Probeer het opnieuw.</ErrorNotice>}
        {state === 'ready' && (
          <div className="flex flex-col gap-2">
            <p role="status" className="text-base text-ink">
              De export is klaar. Je kunt hem maar één keer downloaden en hij blijft 24 uur beschikbaar.
            </p>
            <a href={`/api/v1/data-exports/${exportId}/download`} className={`${buttonClass({ variant: 'primary' })} w-fit`}>
              ZIP downloaden
            </a>
          </div>
        )}
        {(state === 'downloaded' || state === 'expired') && (
          <p className="text-base text-muted">Deze export is al gedownload of verlopen.</p>
        )}
        <DialogFooter>
          <Button onClick={onClose}>Sluiten</Button>
          {!exportId && (
            <Button variant="primary" busy={start.isPending} onClick={() => start.mutate()}>
              Export maken
            </Button>
          )}
        </DialogFooter>
      </div>
    </Dialog>
  )
}

export function EraseDialog({ contact, onClose }: { contact: ContactFull; onClose: () => void }) {
  const navigate = useNavigate()
  const queryClient = useQueryClient()
  const primary = contact.emails.find((e) => e.primary)?.email ?? contact.emails[0]?.email ?? ''
  const [typed, setTyped] = useState('')
  const erase = useMutation({
    mutationFn: () => api('POST', `/contacts/${contact.id}/erase`, { confirmation: typed }),
    onSuccess: async () => {
      queryClient.removeQueries({ queryKey: ['contact', contact.id] })
      await queryClient.invalidateQueries()
      onClose()
      await navigate({ to: '/contacten' })
    },
  })
  const confirmError = fieldError(erase.error, 'confirmation')
  return (
    <Dialog open onOpenChange={(o) => !o && onClose()} title="Contact wissen">
      <div className="flex flex-col gap-4">
        <p className="text-base text-ink">
          Alle gegevens van {contactDisplayName(contact)} worden gewist: het profiel, de notities en de e-mailadressen. Berichten van dit
          contact worden geanonimiseerd. Gesprekken waarin alleen dit contact zit, verliezen hun berichten en bijlagen.
        </p>
        <p className="text-base text-danger-text">
          Dit kan niet ongedaan worden gemaakt. Bestaande back-ups worden niet aangepast.
        </p>
        <Field label={`Typ ${primary} om te bevestigen`} error={confirmError}>
          {(p) => <Input {...p} autoFocus autoComplete="off" value={typed} onChange={(e) => setTyped(e.target.value)} />}
        </Field>
        {erase.isError && !confirmError && <ErrorNotice>{errorMessage(erase.error)}</ErrorNotice>}
        <DialogFooter>
          <Button onClick={onClose}>Annuleren</Button>
          <Button
            variant="danger"
            busy={erase.isPending}
            disabled={typed.trim().toLowerCase() !== primary.toLowerCase()}
            onClick={() => erase.mutate()}
          >
            Contact definitief wissen
          </Button>
        </DialogFooter>
      </div>
    </Dialog>
  )
}
