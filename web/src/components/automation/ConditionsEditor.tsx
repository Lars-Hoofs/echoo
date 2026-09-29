import { useQuery } from '@tanstack/react-query'
import { X } from 'lucide-react'

import { Button, IconButton, Input, Select } from '../ui'
import { labelsQuery } from '../../lib/actions'
import { type ConditionRow, conditionFields, type FieldKey, fieldByKey, newConditionRow, opsFor } from '../../lib/automation'
import { priorityLabel, type Priority, statusLabel, type ConversationStatus } from '../../lib/inbox'
import { mailboxesQuery } from './directory'

const MAX_CONDITIONS = 20

// The "en ..." part of a rule: ALL/ANY over typed condition rows. Groups that were created
// through the API are shown as one line and kept as they are.
export function ConditionsEditor({
  match,
  rows,
  onChange,
  directory,
  errors,
}: {
  match: 'all' | 'any'
  rows: ConditionRow[]
  onChange: (match: 'all' | 'any', rows: ConditionRow[]) => void
  directory: boolean
  errors: Set<number>
}) {
  const labels = useQuery(labelsQuery)
  const mailboxes = useQuery({ ...mailboxesQuery, enabled: directory })
  const update = (i: number, next: ConditionRow) => {
    onChange(match, rows.map((r, j) => (j === i ? next : r)))
  }

  return (
    <div className="flex flex-col gap-2">
      {rows.length > 1 && (
        <label className="flex flex-wrap items-center gap-2 text-base text-muted">
          <Select aria-label="Hoeveel voorwaarden moeten kloppen" className="w-44" value={match} onChange={(e) => onChange(e.target.value as 'all' | 'any', rows)}>
            <option value="all">Alle</option>
            <option value="any">Een van de</option>
          </Select>
          voorwaarden moeten kloppen
        </label>
      )}
      {rows.length > 0 && (
        <ol className="flex flex-col gap-2">
          {rows.map((r, i) => {
            const n = i + 1
            const bad = errors.has(i)
            const frame = `flex flex-wrap items-center gap-2 rounded-md border bg-surface p-2 ${bad ? 'border-danger-text' : 'border-line'}`
            if (r.kind === 'group') {
              return (
                <li key={i} className={frame}>
                  <span className="min-w-0 flex-1 text-base text-muted">
                    Een groep met {r.node.items.length} voorwaarden ({r.node.match === 'all' ? 'alle' : 'een van de'} moeten kloppen), aangemaakt via de API.
                  </span>
                  <IconButton label={`Voorwaarde ${n} verwijderen`} onClick={() => onChange(match, rows.filter((_, j) => j !== i))}>
                    <X size={16} aria-hidden />
                  </IconButton>
                </li>
              )
            }
            const kind = fieldByKey(r.field)?.kind
            return (
              <li key={i} className={frame}>
                <Select
                  aria-label={`Voorwaarde ${n}`}
                  className="w-64 max-w-full"
                  value={r.field}
                  onChange={(e) => update(i, newConditionRow(e.target.value as FieldKey))}
                >
                  {conditionFields.map((f) => (
                    <option key={f.key} value={f.key}>
                      {f.label}
                    </option>
                  ))}
                </Select>
                {kind !== 'bool' && (
                  <Select aria-label={`Vergelijking van voorwaarde ${n}`} className="w-44" value={r.op} onChange={(e) => update(i, { ...r, op: e.target.value })}>
                    {opsFor(r.field).map((o) => (
                      <option key={o.value} value={o.value}>
                        {o.label}
                      </option>
                    ))}
                  </Select>
                )}
                {kind === 'text' && (
                  <Input
                    aria-label={`Waarde van voorwaarde ${n}`}
                    className="min-w-40 flex-1"
                    maxLength={200}
                    value={r.text}
                    onChange={(e) => update(i, { ...r, text: e.target.value })}
                  />
                )}
                {kind === 'bool' && (
                  <Select aria-label={`Waarde van voorwaarde ${n}`} className="w-28" value={r.flag ? 'yes' : 'no'} onChange={(e) => update(i, { ...r, flag: e.target.value === 'yes' })}>
                    <option value="yes">Ja</option>
                    <option value="no">Nee</option>
                  </Select>
                )}
                {kind === 'ids' && (
                  <Select
                    aria-label={`Waarde van voorwaarde ${n}`}
                    className="w-52 max-w-full"
                    value={r.ids[0] ?? ''}
                    onChange={(e) => update(i, { ...r, ids: e.target.value ? [e.target.value] : [] })}
                  >
                    <option value="">Kies…</option>
                    {r.field === 'status' && (Object.keys(statusLabel) as ConversationStatus[]).map((s) => (
                      <option key={s} value={s}>
                        {statusLabel[s]}
                      </option>
                    ))}
                    {r.field === 'priority' && (Object.keys(priorityLabel) as Priority[]).map((p) => (
                      <option key={p} value={p}>
                        {priorityLabel[p]}
                      </option>
                    ))}
                    {r.field === 'label' && labels.data?.map((l) => (
                      <option key={l.id} value={l.id}>
                        {l.name}
                      </option>
                    ))}
                    {r.field === 'mailbox' && mailboxes.data?.mailboxes.map((m) => (
                      <option key={m.id} value={m.id}>
                        {m.name}
                      </option>
                    ))}
                  </Select>
                )}
                <IconButton label={`Voorwaarde ${n} verwijderen`} onClick={() => onChange(match, rows.filter((_, j) => j !== i))} className="ml-auto">
                  <X size={16} aria-hidden />
                </IconButton>
              </li>
            )
          })}
        </ol>
      )}
      {rows.length === 0 && <p className="text-base text-muted">Zonder voorwaarden geldt de regel voor elk gesprek.</p>}
      <div>
        <Button size="sm" disabled={rows.length >= MAX_CONDITIONS} onClick={() => onChange(match, [...rows, newConditionRow()])}>
          + Voorwaarde
        </Button>
      </div>
    </div>
  )
}
