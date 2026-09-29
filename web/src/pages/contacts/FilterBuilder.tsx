import { Plus, X } from 'lucide-react'

import { Button, IconButton, Input, Select } from '../../components/ui'
import {
  type AttributeDef,
  type ContactFilter,
  defaultCondition,
  type FilterCondition,
  type FilterField,
  fieldLabel,
  isAttributeField,
  opLabel,
  opsFor,
  valueKind,
} from '../../lib/contacts'

const fields = Object.keys(fieldLabel) as FilterField[]

export function FilterBuilder({
  filter,
  defs,
  onChange,
}: {
  filter: ContactFilter
  defs: AttributeDef[]
  onChange: (f: ContactFilter) => void
}) {
  const setCondition = (i: number, c: FilterCondition) =>
    onChange({
      ...filter,
      conditions: filter.conditions.map((x, j) => (j === i ? c : x)),
    })
  const available = fields.filter(
    (f) => !isAttributeField(f) || defs.some((d) => d.entity === (f === 'attribute' ? 'contact' : 'conversation')),
  )
  return (
    <div className="flex flex-col gap-2" role="group" aria-label="Filters">
      {filter.conditions.length > 1 && (
        <Select
          className="w-64"
          aria-label="Combinatie van voorwaarden"
          value={filter.match}
          onChange={(e) =>
            onChange({
              ...filter,
              match: e.target.value === 'any' ? 'any' : 'all',
            })
          }
        >
          <option value="all">Aan alle voorwaarden voldoen</option>
          <option value="any">Aan één van de voorwaarden voldoen</option>
        </Select>
      )}
      {filter.conditions.map((c, i) => (
        <ConditionRow
          key={i}
          index={i}
          condition={c}
          defs={defs}
          fields={available}
          onChange={(next) => setCondition(i, next)}
          onRemove={() =>
            onChange({
              ...filter,
              conditions: filter.conditions.filter((_, j) => j !== i),
            })
          }
        />
      ))}
      <div>
        <Button
          size="sm"
          onClick={() =>
            onChange({
              ...filter,
              conditions: [...filter.conditions, defaultCondition(available[0] ?? 'name', defs)],
            })
          }
        >
          <Plus size={14} aria-hidden />
          Voorwaarde toevoegen
        </Button>
      </div>
    </div>
  )
}

function ConditionRow({
  index,
  condition: c,
  defs,
  fields: available,
  onChange,
  onRemove,
}: {
  index: number
  condition: FilterCondition
  defs: AttributeDef[]
  fields: FilterField[]
  onChange: (c: FilterCondition) => void
  onRemove: () => void
}) {
  const n = index + 1
  const entity = c.field === 'attribute' ? 'contact' : 'conversation'
  const attrDefs = isAttributeField(c.field) ? defs.filter((d) => d.entity === entity) : []
  const def = attrDefs.find((d) => d.key === c.key)
  const ops = opsFor(c.field, def?.type)
  const kind = valueKind(c, def)
  const changeField = (field: FilterField) => onChange(defaultCondition(field, defs))
  const changeKey = (key: string) => {
    const next = defs.find((d) => d.key === key && d.entity === entity)
    onChange({
      field: c.field,
      key,
      op: opsFor(c.field, next?.type)[0] ?? 'contains',
      value: '',
    })
  }
  return (
    <div className="flex flex-wrap items-center gap-2">
      <Select
        className="w-44"
        aria-label={`Veld van voorwaarde ${n}`}
        value={c.field}
        onChange={(e) => changeField(e.target.value as FilterField)}
      >
        {available.map((f) => (
          <option key={f} value={f}>
            {fieldLabel[f]}
          </option>
        ))}
      </Select>
      {isAttributeField(c.field) && (
        <Select className="w-44" aria-label={`Welk veld, voorwaarde ${n}`} value={c.key ?? ''} onChange={(e) => changeKey(e.target.value)}>
          {attrDefs.map((d) => (
            <option key={d.id} value={d.key}>
              {d.label}
            </option>
          ))}
        </Select>
      )}
      <Select
        className="w-48"
        aria-label={`Vergelijking van voorwaarde ${n}`}
        value={c.op}
        onChange={(e) => onChange({ ...c, op: e.target.value, value: '' })}
      >
        {ops.map((o) => (
          <option key={o} value={o}>
            {opLabel[o] ?? o}
          </option>
        ))}
      </Select>
      {kind === 'list' && def ? (
        <Select
          className="w-44"
          aria-label={`Waarde van voorwaarde ${n}`}
          value={c.value ?? ''}
          onChange={(e) => onChange({ ...c, value: e.target.value })}
        >
          <option value="">Kies…</option>
          {def.options.map((o) => (
            <option key={o} value={o}>
              {o}
            </option>
          ))}
        </Select>
      ) : kind !== 'none' ? (
        <Input
          className="w-44"
          aria-label={`Waarde van voorwaarde ${n}`}
          type={kind === 'date' ? 'date' : kind === 'days' || kind === 'number' ? 'number' : 'text'}
          maxLength={200}
          value={c.value ?? ''}
          onChange={(e) => onChange({ ...c, value: e.target.value })}
        />
      ) : null}
      <IconButton label={`Voorwaarde ${n} verwijderen`} onClick={onRemove}>
        <X size={16} aria-hidden />
      </IconButton>
    </div>
  )
}
