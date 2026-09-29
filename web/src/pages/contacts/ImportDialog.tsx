import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { type CSSProperties, useState } from 'react'

import { Dialog, DialogFooter } from '../../components/Dialog'
import { Num } from '../../components/Num'
import { Button, ErrorNotice, Select, Table, TBody, Td, Th, THead, Tr } from '../../components/ui'
import { api, apiForm } from '../../lib/api'
import {
  attributeDefsQuery,
  buildImportOptions,
  type ContactImport,
  CsvError,
  csvProblemText,
  type CsvPreview,
  type DedupeMode,
  type ImportTarget,
  mappingProblem,
  MAX_IMPORT_BYTES,
  previewCsv,
  suggestMapping,
} from '../../lib/contacts'
import { errorMessage, fieldError } from '../../lib/errors'

interface Loaded {
  file: File
  preview: CsvPreview
}

export function ImportDialog({ onClose }: { onClose: () => void }) {
  const defs = useQuery(attributeDefsQuery('contact'))
  const [loaded, setLoaded] = useState<Loaded | null>(null)
  const [targets, setTargets] = useState<ImportTarget[]>([])
  const [dedupe, setDedupe] = useState<DedupeMode>('update')
  const [fileError, setFileError] = useState('')
  const [importId, setImportId] = useState('')

  const choose = async (file: File | undefined) => {
    setFileError('')
    setLoaded(null)
    if (!file) return
    if (file.size > MAX_IMPORT_BYTES) {
      setFileError(csvProblemText.too_large)
      return
    }
    try {
      const preview = previewCsv(await file.text())
      setLoaded({ file, preview })
      setTargets(suggestMapping(preview.header, defs.data ?? []))
    } catch (err) {
      setFileError(err instanceof CsvError ? csvProblemText[err.problem] : 'Het bestand kon niet worden gelezen.')
    }
  }

  const queryClient = useQueryClient()
  const start = useMutation({
    mutationFn: () => {
      if (!loaded) throw new Error('no file')
      const form = new FormData()
      form.append('file', loaded.file)
      form.append('options', JSON.stringify(buildImportOptions(loaded.preview.delimiter, dedupe, targets)))
      return apiForm<{ import: ContactImport }>('/contact-imports', form)
    },
    onSuccess: (r) => setImportId(r.import.id),
  })
  const problem = mappingProblem(targets)
  const setTarget = (i: number, t: ImportTarget) => setTargets((cur) => cur.map((x, j) => (j === i ? t : x)))

  if (importId) {
    return (
      <Dialog open onOpenChange={(o) => !o && onClose()} title="Contacten importeren">
        <ImportProgress
          id={importId}
          onClose={() => {
            void queryClient.invalidateQueries({ queryKey: ['contacts'] })
            onClose()
          }}
        />
      </Dialog>
    )
  }

  return (
    <Dialog
      open
      onOpenChange={(o) => !o && onClose()}
      size="xl"
      title="Contacten importeren"
      description="Kies een CSV-bestand (UTF-8, komma of puntkomma, met een kopregel). Maximaal 50.000 rijen."
    >
      <div className="flex flex-col gap-4">
        <label className="t-label flex flex-col gap-2">
          CSV-bestand
          <input
            type="file"
            accept=".csv,text/csv"
            onChange={(e) => void choose(e.target.files?.[0])}
            className="text-base font-normal text-ink file:mr-3 file:h-8 file:rounded-full file:border file:border-line-strong file:bg-transparent file:px-4 file:text-base file:text-ink"
          />
        </label>
        {fileError && <ErrorNotice>{fileError}</ErrorNotice>}
        {start.isError && (
          <ErrorNotice>{fieldError(start.error, 'file') ?? fieldError(start.error, 'options') ?? errorMessage(start.error)}</ErrorNotice>
        )}
        {loaded && (
          <>
            <p className="text-base text-muted">
              {loaded.preview.rowCount} {loaded.preview.rowCount === 1 ? 'rij' : 'rijen'} gevonden. Koppel de kolommen aan velden.
            </p>
            <Table>
              <THead>
                {loaded.preview.header.map((h, i) => (
                  <Th key={i} className="min-w-44 align-bottom">
                    <span className="mb-1 block truncate text-ink">{h || `Kolom ${i + 1}`}</span>
                    <Select
                      aria-label={`Koppeling voor kolom ${h || i + 1}`}
                      value={targets[i] ?? ''}
                      onChange={(e) => setTarget(i, e.target.value as ImportTarget)}
                    >
                      <option value="">Overslaan</option>
                      <option value="email">E-mail</option>
                      <option value="name">Naam</option>
                      <option value="phone">Telefoon</option>
                      <option value="organization">Organisatie</option>
                      {defs.data?.map((d) => (
                        <option key={d.id} value={`attribute:${d.key}`}>
                          Veld: {d.label}
                        </option>
                      ))}
                    </Select>
                  </Th>
                ))}
              </THead>
              <TBody>
                {loaded.preview.preview.map((row, r) => (
                  <Tr key={r}>
                    {loaded.preview.header.map((_, i) => (
                      <Td key={i} className="max-w-56 truncate text-muted">
                        {row[i] ?? ''}
                      </Td>
                    ))}
                  </Tr>
                ))}
              </TBody>
            </Table>
            <fieldset className="flex flex-col gap-2">
              <legend className="t-label mb-2">Bestaat het e-mailadres al?</legend>
              <label className="flex items-center gap-2 text-base text-ink">
                <input type="radio" className="accent-ink" name="dedupe" checked={dedupe === 'update'} onChange={() => setDedupe('update')} />
                Bestaand contact bijwerken met de gekoppelde velden
              </label>
              <label className="flex items-center gap-2 text-base text-ink">
                <input type="radio" className="accent-ink" name="dedupe" checked={dedupe === 'skip'} onChange={() => setDedupe('skip')} />
                Bestaand contact overslaan
              </label>
            </fieldset>
            {problem && <p className="text-sm text-danger-text">{problem}</p>}
          </>
        )}
        <DialogFooter>
          <Button onClick={onClose}>Annuleren</Button>
          <Button variant="primary" busy={start.isPending} disabled={!loaded || problem !== null} onClick={() => start.mutate()}>
            Importeren
          </Button>
        </DialogFooter>
      </div>
    </Dialog>
  )
}

function ImportProgress({ id, onClose }: { id: string; onClose: () => void }) {
  const imp = useQuery({
    queryKey: ['contact-import', id],
    queryFn: async () => (await api<{ import: ContactImport }>('GET', `/contact-imports/${id}`)).import,
    refetchInterval: (q) => (q.state.data && (q.state.data.status === 'done' || q.state.data.status === 'failed') ? false : 1000),
  })
  if (imp.isError) return <ErrorNotice>{errorMessage(imp.error)}</ErrorNotice>
  const d = imp.data
  const finished = d?.status === 'done' || d?.status === 'failed'
  const pct = d && d.total_rows > 0 ? Math.round((d.processed_rows / d.total_rows) * 100) : 0
  return (
    <div className="flex flex-col gap-4">
      <div>
        <div
          role="progressbar"
          aria-label="Voortgang van de import"
          aria-valuemin={0}
          aria-valuemax={100}
          aria-valuenow={finished && d.status === 'done' ? 100 : pct}
          className="progress progress-ink"
        >
          <i style={{ '--w': `${finished && d.status === 'done' ? 100 : pct}%` } as CSSProperties} />
        </div>
        <p role="status" className="mt-2 text-base text-muted">
          {!d || d.status === 'queued'
            ? 'De import staat in de wachtrij.'
            : d.status === 'running'
              ? `Bezig: ${d.processed_rows} van ${d.total_rows} rijen verwerkt.`
              : d.status === 'done'
                ? 'De import is klaar.'
                : 'De import is mislukt. Controleer het bestand en probeer het opnieuw.'}
        </p>
      </div>
      {d && (
        <dl className="grid grid-cols-2 gap-2 text-base sm:grid-cols-4">
          <Stat label="Nieuw" value={d.created_count} />
          <Stat label="Bijgewerkt" value={d.updated_count} />
          <Stat label="Overgeslagen" value={d.skipped_count} />
          <Stat label="Mislukt" value={d.failed_count} />
        </dl>
      )}
      {finished && d.has_errors && (
        <a href={`/api/v1/contact-imports/${id}/errors`} className="text-base text-ink hover:underline">
          Foutrapport downloaden (CSV)
        </a>
      )}
      <DialogFooter>
        <Button variant="primary" onClick={onClose}>
          {finished ? 'Sluiten' : 'Op de achtergrond doorgaan'}
        </Button>
      </DialogFooter>
    </div>
  )
}

function Stat({ label, value }: { label: string; value: number }) {
  return (
    <div className="card card-line card-s flex flex-col-reverse justify-end gap-1">
      <dt className="t-label">{label}</dt>
      <dd>
        <Num value={value} size="s" />
      </dd>
    </div>
  )
}
