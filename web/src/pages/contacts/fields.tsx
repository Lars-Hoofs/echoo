import { useQuery } from '@tanstack/react-query'
import { Plus, X } from 'lucide-react'

import { Button, Field, IconButton, Input, Select } from '../../components/ui'
import { api } from '../../lib/api'
import { type AttributeDef, type ContactAddress, type OrganizationItem } from '../../lib/contacts'
import { fieldError } from '../../lib/errors'

// One input per definition. Values stay strings while editing; attributesToPatch converts them.
export function AttributeFields({
  defs,
  drafts,
  onChange,
  error,
}: {
  defs: AttributeDef[]
  drafts: Record<string, string>
  onChange: (key: string, value: string) => void
  error: unknown
}) {
  return defs.map((def) => (
    <Field key={def.id} label={def.label} error={fieldError(error, `custom_attributes.${def.key}`)}>
      {(p) => <AttributeInput {...p} def={def} value={drafts[def.key] ?? ''} onChange={(v) => onChange(def.key, v)} />}
    </Field>
  ))
}

export function AttributeInput({
  def,
  value,
  onChange,
  ...aria
}: {
  def: AttributeDef
  value: string
  onChange: (v: string) => void
  id?: string
  'aria-describedby'?: string
  'aria-invalid'?: true
}) {
  switch (def.type) {
    case 'boolean':
      return (
        <Select {...aria} value={value} onChange={(e) => onChange(e.target.value)}>
          <option value="">Niet ingevuld</option>
          <option value="true">Ja</option>
          <option value="false">Nee</option>
        </Select>
      )
    case 'list':
      return (
        <Select {...aria} value={value} onChange={(e) => onChange(e.target.value)}>
          <option value="">Niet ingevuld</option>
          {def.options.map((o) => (
            <option key={o} value={o}>
              {o}
            </option>
          ))}
        </Select>
      )
    case 'date':
      return <Input {...aria} type="date" value={value} onChange={(e) => onChange(e.target.value)} />
    case 'number':
      return <Input {...aria} inputMode="decimal" value={value} onChange={(e) => onChange(e.target.value)} />
    case 'link':
      return <Input {...aria} type="url" maxLength={2000} placeholder="https://" value={value} onChange={(e) => onChange(e.target.value)} />
    default:
      return <Input {...aria} maxLength={1000} value={value} onChange={(e) => onChange(e.target.value)} />
  }
}

export interface EmailRow {
  email: string
  primary: boolean
}

export function EmailsEditor({
  rows,
  onChange,
  error,
}: {
  rows: EmailRow[]
  onChange: (rows: EmailRow[]) => void
  error?: string | undefined
}) {
  const set = (i: number, patch: Partial<EmailRow>) => onChange(rows.map((r, j) => (j === i ? { ...r, ...patch } : r)))
  const makePrimary = (i: number) => onChange(rows.map((r, j) => ({ ...r, primary: j === i })))
  const remove = (i: number) => {
    const next = rows.filter((_, j) => j !== i)
    if (next.length > 0 && !next.some((r) => r.primary) && next[0]) next[0] = { ...next[0], primary: true }
    onChange(next)
  }
  return (
    <fieldset className="flex flex-col gap-2">
      <legend className="mb-2 text-sm text-ink">E-mailadressen</legend>
      {rows.map((r, i) => (
        <div key={i} className="flex items-center gap-2">
          <Input
            type="email"
            aria-label={`E-mailadres ${i + 1}`}
            value={r.email}
            onChange={(e) => set(i, { email: e.target.value })}
            aria-invalid={error ? true : undefined}
          />
          <label className="flex shrink-0 items-center gap-2 t-label">
            <input type="radio" name="primary-email" checked={r.primary} onChange={() => makePrimary(i)} />
            Primair
          </label>
          <IconButton label={`E-mailadres ${i + 1} verwijderen`} disabled={rows.length === 1} onClick={() => remove(i)}>
            <X size={16} aria-hidden />
          </IconButton>
        </div>
      ))}
      {error && <p className="text-sm text-danger-text">{error}</p>}
      <div>
        <Button size="sm" disabled={rows.length >= 10} onClick={() => onChange([...rows, { email: '', primary: rows.length === 0 }])}>
          <Plus size={14} aria-hidden />
          E-mailadres toevoegen
        </Button>
      </div>
    </fieldset>
  )
}

export const emailRowsFrom = (emails: ContactAddress[]): EmailRow[] =>
  emails.length > 0 ? emails.map((e) => ({ ...e })) : [{ email: '', primary: true }]

export function OrganizationSelect({
  value,
  current,
  onChange,
  error,
}: {
  value: string
  current?: { id: string; name: string } | null
  onChange: (id: string) => void
  error?: string | undefined
}) {
  const orgs = useQuery({
    queryKey: ['organizations', 'select'],
    queryFn: () => api<{ organizations: OrganizationItem[] }>('GET', '/organizations?limit=100'),
  })
  const list = orgs.data?.organizations ?? []
  const options = current && !list.some((o) => o.id === current.id) ? [...list, { id: current.id, name: current.name }] : list
  return (
    <Field label="Organisatie" error={error}>
      {(p) => (
        <Select {...p} value={value} onChange={(e) => onChange(e.target.value)}>
          <option value="">Geen organisatie</option>
          {options.map((o) => (
            <option key={o.id} value={o.id}>
              {o.name}
            </option>
          ))}
        </Select>
      )}
    </Field>
  )
}
