import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { CalendarClock, Pencil, Plus, Timer, Trash2, X } from 'lucide-react'
import { type SubmitEvent, useState } from 'react'

import { ActionMenu } from '../../components/ActionMenu'
import { Dialog, DialogFooter } from '../../components/Dialog'
import { Badge, Button, Card, EmptyState, ErrorNotice, Field, IconButton, Input, Page, PageHeader, Select, Skeleton, Switch, Table, TBody, Td, Th, THead, Tr } from '../../components/ui'
import { api, ApiError } from '../../lib/api'
import {
  type BusinessHours,
  businessHoursQuery,
  type BusinessHoursRange,
  defaultWeek,
  formatMinutes,
  type SlaPolicy,
  slaPoliciesQuery,
  splitMinutes,
  timeUnitLabel,
  type TimeUnit,
  timezones,
  toMinutes,
  weekdays,
  type WeekdayKey,
} from '../../lib/automation'
import { errorMessage, fieldError } from '../../lib/errors'

type Editing =
  | { kind: 'new-policy' }
  | { kind: 'policy' | 'delete-policy'; policy: SlaPolicy }
  | { kind: 'new-hours' }
  | { kind: 'hours' | 'delete-hours'; hours: BusinessHours }

export function SlaPage() {
  const policies = useQuery(slaPoliciesQuery)
  const hours = useQuery(businessHoursQuery)
  const [editing, setEditing] = useState<Editing | null>(null)
  const hoursName = (id: string | null) => (id ? (hours.data?.find((h) => h.id === id)?.name ?? '—') : 'Kalendertijd (24/7)')

  return (
    <Page>
      <PageHeader
        breadcrumb="Werkruimte"
        title="SLA"
        description="Een SLA-beleid stelt deadlines voor de eerste reactie en de oplossing. Ze tellen alleen binnen werktijden als je die koppelt. Wachtend zet de oplostijd stil."
      />
      <Card
        title="SLA-beleid"
        icon={<Timer size={16} />}
        description="Koppel een beleid aan een mailbox (bij Toewijzing) of pas het toe met een regel."
        actions={
          <Button variant="primary" size="sm" onClick={() => setEditing({ kind: 'new-policy' })}>
            <Plus size={14} aria-hidden />
            Beleid toevoegen
          </Button>
        }
        flush
      >
        {policies.isPending ? (
          <div className="p-4">
            <Skeleton className="h-24" />
          </div>
        ) : policies.isError ? (
          <div className="p-4">
            <ErrorNotice>{errorMessage(policies.error)}</ErrorNotice>
          </div>
        ) : policies.data.length === 0 ? (
          <EmptyState icon={<Timer size={20} />} title="Nog geen SLA-beleid" description="Maak een beleid aan om deadlines te tonen bij gesprekken." />
        ) : (
          <Table>
            <THead>
              <Th>Naam</Th>
              <Th>Eerste reactie</Th>
              <Th>Oplossing</Th>
              <Th className="hidden md:table-cell">Werktijden</Th>
              <Th className="w-px">
                <span className="sr-only">Acties</span>
              </Th>
            </THead>
            <TBody>
              {policies.data.map((p) => (
                <Tr key={p.id}>
                  <Td className="text-ink">{p.name}</Td>
                  <Td>{p.first_response_minutes ? formatMinutes(p.first_response_minutes) : '—'}</Td>
                  <Td>{p.resolution_minutes ? formatMinutes(p.resolution_minutes) : '—'}</Td>
                  <Td className="hidden text-muted md:table-cell">{hoursName(p.business_hours_id)}</Td>
                  <Td>
                    <div className="flex justify-end">
                      <ActionMenu
                        label={`Acties voor ${p.name}`}
                        items={[
                          { label: 'Bewerken', icon: <Pencil size={16} />, onSelect: () => setEditing({ kind: 'policy', policy: p }) },
                          { label: 'Verwijderen', icon: <Trash2 size={16} />, danger: true, separated: true, onSelect: () => setEditing({ kind: 'delete-policy', policy: p }) },
                        ]}
                      />
                    </div>
                  </Td>
                </Tr>
              ))}
            </TBody>
          </Table>
        )}
      </Card>

      <Card
        title="Werktijden"
        icon={<CalendarClock size={16} />}
        description="Schema's met openingstijden, tijdzone en feestdagen. Het standaardschema geldt voor mailboxen zonder eigen schema."
        actions={
          <Button size="sm" onClick={() => setEditing({ kind: 'new-hours' })}>
            <Plus size={14} aria-hidden />
            Schema toevoegen
          </Button>
        }
        flush
      >
        {hours.isPending ? (
          <div className="p-4">
            <Skeleton className="h-24" />
          </div>
        ) : hours.isError ? (
          <div className="p-4">
            <ErrorNotice>{errorMessage(hours.error)}</ErrorNotice>
          </div>
        ) : hours.data.length === 0 ? (
          <EmptyState icon={<CalendarClock size={20} />} title="Nog geen werktijden" description="Zonder schema tellen SLA-deadlines alle uren van de dag." />
        ) : (
          <Table>
            <THead>
              <Th>Naam</Th>
              <Th>Tijdzone</Th>
              <Th className="hidden md:table-cell">Feestdagen</Th>
              <Th className="w-px">
                <span className="sr-only">Acties</span>
              </Th>
            </THead>
            <TBody>
              {hours.data.map((h) => (
                <Tr key={h.id}>
                  <Td>
                    <span className="flex flex-wrap items-center gap-2 text-ink">
                      {h.name}
                      {h.is_default && <Badge>Standaard</Badge>}
                    </span>
                  </Td>
                  <Td className="text-muted">{h.timezone}</Td>
                  <Td className="hidden text-muted md:table-cell">{h.holidays.length}</Td>
                  <Td>
                    <div className="flex justify-end">
                      <ActionMenu
                        label={`Acties voor ${h.name}`}
                        items={[
                          { label: 'Bewerken', icon: <Pencil size={16} />, onSelect: () => setEditing({ kind: 'hours', hours: h }) },
                          ...(h.is_default ? [] : [{ label: 'Verwijderen', icon: <Trash2 size={16} />, danger: true, separated: true, onSelect: () => setEditing({ kind: 'delete-hours', hours: h }) }]),
                        ]}
                      />
                    </div>
                  </Td>
                </Tr>
              ))}
            </TBody>
          </Table>
        )}
      </Card>

      {(editing?.kind === 'new-policy' || editing?.kind === 'policy') && (
        <PolicyDialog policy={editing.kind === 'policy' ? editing.policy : null} hours={hours.data ?? []} onClose={() => setEditing(null)} />
      )}
      {(editing?.kind === 'new-hours' || editing?.kind === 'hours') && (
        <HoursDialog hours={editing.kind === 'hours' ? editing.hours : null} onClose={() => setEditing(null)} />
      )}
      {editing?.kind === 'delete-policy' && (
        <DeleteDialog
          title="SLA-beleid verwijderen"
          text={`Gesprekken onder ${editing.policy.name} worden niet meer bewaakt. Bestaande gesprekken blijven zoals ze zijn.`}
          path={`/sla-policies/${editing.policy.id}`}
          keys={[['automation', 'sla-policies']]}
          onClose={() => setEditing(null)}
        />
      )}
      {editing?.kind === 'delete-hours' && (
        <DeleteDialog
          title="Schema verwijderen"
          text={`Het schema ${editing.hours.name} wordt verwijderd. Mailboxen die het gebruiken vallen terug op het standaardschema. Een schema dat een SLA-beleid gebruikt kun je niet verwijderen.`}
          path={`/business-hours/${editing.hours.id}`}
          keys={[['automation', 'business-hours']]}
          onClose={() => setEditing(null)}
        />
      )}
    </Page>
  )
}

function DurationField({ label, value, unit, onChange, error }: { label: string; value: string; unit: TimeUnit; onChange: (value: string, unit: TimeUnit) => void; error: string | undefined }) {
  return (
    <Field label={label} error={error}>
      {(p) => (
        <div className="flex gap-2">
          <Input {...p} type="number" min={1} className="w-28" value={value} onChange={(e) => onChange(e.target.value, unit)} />
          <Select aria-label={`Eenheid van ${label.toLowerCase()}`} className="w-32" value={unit} onChange={(e) => onChange(value, e.target.value as TimeUnit)}>
            {(Object.keys(timeUnitLabel) as TimeUnit[]).map((u) => (
              <option key={u} value={u}>
                {timeUnitLabel[u]}
              </option>
            ))}
          </Select>
        </div>
      )}
    </Field>
  )
}

function PolicyDialog({ policy, hours, onClose }: { policy: SlaPolicy | null; hours: BusinessHours[]; onClose: () => void }) {
  const first = splitMinutes(policy?.first_response_minutes ?? 60)
  const resolution = splitMinutes(policy?.resolution_minutes ?? 480)
  const [name, setName] = useState(policy?.name ?? '')
  const [useFirst, setUseFirst] = useState(policy ? policy.first_response_minutes !== null : true)
  const [firstValue, setFirstValue] = useState(String(first.value))
  const [firstUnit, setFirstUnit] = useState<TimeUnit>(first.unit)
  const [useResolution, setUseResolution] = useState(policy ? policy.resolution_minutes !== null : true)
  const [resolutionValue, setResolutionValue] = useState(String(resolution.value))
  const [resolutionUnit, setResolutionUnit] = useState<TimeUnit>(resolution.unit)
  const [percent, setPercent] = useState(String(policy?.at_risk_percent ?? 80))
  const [hoursId, setHoursId] = useState(policy?.business_hours_id ?? '')
  const queryClient = useQueryClient()
  const save = useMutation({
    mutationFn: () => {
      const body = {
        name,
        first_response_minutes: useFirst ? toMinutes(Number(firstValue), firstUnit) : null,
        resolution_minutes: useResolution ? toMinutes(Number(resolutionValue), resolutionUnit) : null,
        at_risk_percent: Number(percent),
        business_hours_id: hoursId || null,
      }
      return policy ? api('PATCH', `/sla-policies/${policy.id}`, body) : api('POST', '/sla-policies', body)
    },
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: ['automation', 'sla-policies'] })
      onClose()
    },
  })
  const submit = (e: SubmitEvent) => {
    e.preventDefault()
    save.mutate()
  }
  const nameCode = save.error instanceof ApiError ? save.error.fields.name : undefined
  return (
    <Dialog open onOpenChange={(o) => !o && onClose()} title={policy ? 'SLA-beleid bewerken' : 'SLA-beleid toevoegen'}>
      <form onSubmit={submit} className="flex flex-col gap-4" noValidate>
        {save.isError && <ErrorNotice>{errorMessage(save.error)}</ErrorNotice>}
        <Field label="Naam" error={nameCode === 'taken' ? 'Er bestaat al een beleid met deze naam.' : fieldError(save.error, 'name')}>
          {(p) => <Input {...p} autoFocus maxLength={100} value={name} onChange={(e) => setName(e.target.value)} />}
        </Field>
        <div className="flex items-center gap-3">
          <Switch checked={useFirst} onChange={setUseFirst} label="Doel voor de eerste reactie" />
          <span className="text-base text-ink">Doel voor de eerste reactie</span>
        </div>
        {useFirst && (
          <DurationField
            label="Eerste reactie binnen"
            value={firstValue}
            unit={firstUnit}
            onChange={(v, u) => {
              setFirstValue(v)
              setFirstUnit(u)
            }}
            error={fieldError(save.error, 'first_response_minutes')}
          />
        )}
        <div className="flex items-center gap-3">
          <Switch checked={useResolution} onChange={setUseResolution} label="Doel voor de oplossing" />
          <span className="text-base text-ink">Doel voor de oplossing</span>
        </div>
        {useResolution && (
          <DurationField
            label="Oplossing binnen"
            value={resolutionValue}
            unit={resolutionUnit}
            onChange={(v, u) => {
              setResolutionValue(v)
              setResolutionUnit(u)
            }}
            error={fieldError(save.error, 'resolution_minutes')}
          />
        )}
        <Field label="Werktijden" help="Zonder schema tellen alle uren van de dag mee.">
          {(p) => (
            <Select {...p} value={hoursId} onChange={(e) => setHoursId(e.target.value)}>
              <option value="">Kalendertijd (24/7)</option>
              {hours.map((h) => (
                <option key={h.id} value={h.id}>
                  {h.name}
                </option>
              ))}
            </Select>
          )}
        </Field>
        <Field label="Risico vanaf (%)" help="Vanaf dit percentage van de tijd toont Echoo de deadline als dreigend." error={fieldError(save.error, 'at_risk_percent')}>
          {(p) => <Input {...p} type="number" min={1} max={99} className="w-28" value={percent} onChange={(e) => setPercent(e.target.value)} />}
        </Field>
        <DialogFooter>
          <Button onClick={onClose}>Annuleren</Button>
          <Button type="submit" variant="primary" busy={save.isPending} disabled={!name.trim()}>
            Opslaan
          </Button>
        </DialogFooter>
      </form>
    </Dialog>
  )
}

type Week = Partial<Record<WeekdayKey, BusinessHoursRange[]>>

function HoursDialog({ hours, onClose }: { hours: BusinessHours | null; onClose: () => void }) {
  const [name, setName] = useState(hours?.name ?? '')
  const [timezone, setTimezone] = useState(hours?.timezone ?? 'Europe/Amsterdam')
  const [week, setWeek] = useState<Week>(hours?.weekly ?? defaultWeek)
  const [holidays, setHolidays] = useState<string[]>(hours?.holidays ?? [])
  const [isDefault, setIsDefault] = useState(hours?.is_default ?? false)
  const [newHoliday, setNewHoliday] = useState('')
  const queryClient = useQueryClient()
  const save = useMutation({
    mutationFn: () => {
      const body = { name, timezone, weekly: week, holidays, is_default: isDefault }
      return hours ? api('PATCH', `/business-hours/${hours.id}`, body) : api('POST', '/business-hours', body)
    },
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: ['automation', 'business-hours'] })
      onClose()
    },
  })
  const submit = (e: SubmitEvent) => {
    e.preventDefault()
    save.mutate()
  }
  const setRanges = (day: WeekdayKey, ranges: BusinessHoursRange[]) => {
    setWeek({ ...week, [day]: ranges })
  }
  const nameCode = save.error instanceof ApiError ? save.error.fields.name : undefined
  const zones = timezones.includes(timezone) ? timezones : [timezone, ...timezones]
  const addHoliday = () => {
    if (newHoliday && !holidays.includes(newHoliday)) setHolidays([...holidays, newHoliday].sort())
    setNewHoliday('')
  }

  return (
    <Dialog open onOpenChange={(o) => !o && onClose()} title={hours ? 'Schema bewerken' : 'Schema toevoegen'} size="lg">
      <form onSubmit={submit} className="flex flex-col gap-4" noValidate>
        {save.isError && <ErrorNotice>{errorMessage(save.error)}</ErrorNotice>}
        <Field label="Naam" error={nameCode === 'taken' ? 'Er bestaat al een schema met deze naam.' : fieldError(save.error, 'name')}>
          {(p) => <Input {...p} autoFocus maxLength={100} value={name} onChange={(e) => setName(e.target.value)} />}
        </Field>
        <Field label="Tijdzone" error={fieldError(save.error, 'timezone')}>
          {(p) => (
            <Select {...p} value={timezone} onChange={(e) => setTimezone(e.target.value)}>
              {zones.map((z) => (
                <option key={z} value={z}>
                  {z}
                </option>
              ))}
            </Select>
          )}
        </Field>

        <fieldset className="flex flex-col gap-2">
          <legend className="mb-1 text-sm text-ink">Werktijden per dag</legend>
          <p className="text-sm text-faint">Een dag zonder tijdvak is gesloten. Een eindtijd voor de begintijd loopt door tot de volgende ochtend.</p>
          {fieldError(save.error, 'weekly') && <p className="text-sm text-danger-text">{fieldError(save.error, 'weekly')}</p>}
          {weekdays.map((d) => {
            const ranges = week[d.key] ?? []
            return (
              <div key={d.key} className="flex flex-wrap items-start gap-2 border-t border-line pt-2">
                <span className="w-24 pt-1.5 text-base text-ink">{d.label}</span>
                <div className="flex min-w-0 flex-1 flex-col gap-2">
                  {ranges.length === 0 && <span className="pt-1.5 text-base text-muted">Gesloten</span>}
                  {ranges.map((r, i) => (
                    <div key={i} className="flex items-center gap-2">
                      <Input
                        aria-label={`${d.label}, tijdvak ${i + 1}, van`}
                        type="time"
                        className="w-28"
                        value={r.start}
                        onChange={(e) => setRanges(d.key, ranges.map((x, j) => (j === i ? { ...x, start: e.target.value } : x)))}
                      />
                      <span className="text-muted">tot</span>
                      <Input
                        aria-label={`${d.label}, tijdvak ${i + 1}, tot`}
                        type="time"
                        className="w-28"
                        value={r.end}
                        onChange={(e) => setRanges(d.key, ranges.map((x, j) => (j === i ? { ...x, end: e.target.value } : x)))}
                      />
                      <IconButton label={`${d.label}, tijdvak ${i + 1} verwijderen`} onClick={() => setRanges(d.key, ranges.filter((_, j) => j !== i))}>
                        <X size={16} aria-hidden />
                      </IconButton>
                    </div>
                  ))}
                </div>
                <Button size="sm" onClick={() => setRanges(d.key, [...ranges, { start: '09:00', end: '17:00' }])}>
                  + Tijdvak
                </Button>
              </div>
            )
          })}
        </fieldset>

        <fieldset className="flex flex-col gap-2">
          <legend className="mb-1 text-sm text-ink">Feestdagen</legend>
          {fieldError(save.error, 'holidays') && <p className="text-sm text-danger-text">{fieldError(save.error, 'holidays')}</p>}
          {holidays.length > 0 && (
            <ul className="flex flex-wrap gap-2">
              {holidays.map((h) => (
                <li key={h} className="inline-flex h-7 items-center gap-1 rounded-full border border-line bg-subtle pr-1 pl-2 text-base tabular-nums">
                  {new Intl.DateTimeFormat('nl-NL', { day: 'numeric', month: 'long', year: 'numeric' }).format(new Date(`${h}T12:00:00`))}
                  <IconButton label={`Feestdag ${h} verwijderen`} className="size-5 max-md:size-8" onClick={() => setHolidays(holidays.filter((x) => x !== h))}>
                    <X size={12} aria-hidden />
                  </IconButton>
                </li>
              ))}
            </ul>
          )}
          <div className="flex gap-2">
            <Input aria-label="Nieuwe feestdag" type="date" className="w-44" value={newHoliday} onChange={(e) => setNewHoliday(e.target.value)} />
            <Button disabled={!newHoliday} onClick={addHoliday}>
              Toevoegen
            </Button>
          </div>
        </fieldset>

        <div className="flex items-center gap-3">
          <Switch checked={isDefault} onChange={setIsDefault} label="Standaardschema" disabled={hours?.is_default === true} />
          <span className="text-base text-ink">Standaardschema van de werkruimte</span>
        </div>
        {fieldError(save.error, 'is_default') && <p className="text-sm text-danger-text">{fieldError(save.error, 'is_default')}</p>}
        <DialogFooter>
          <Button onClick={onClose}>Annuleren</Button>
          <Button type="submit" variant="primary" busy={save.isPending} disabled={!name.trim()}>
            Opslaan
          </Button>
        </DialogFooter>
      </form>
    </Dialog>
  )
}

function DeleteDialog({ title, text, path, keys, onClose }: { title: string; text: string; path: string; keys: string[][]; onClose: () => void }) {
  const queryClient = useQueryClient()
  const del = useMutation({
    mutationFn: () => api('DELETE', path),
    onSuccess: async () => {
      await Promise.all(keys.map((queryKey) => queryClient.invalidateQueries({ queryKey })))
      onClose()
    },
  })
  return (
    <Dialog open onOpenChange={(o) => !o && onClose()} title={title}>
      <div className="flex flex-col gap-4">
        <p className="text-base text-muted">{text}</p>
        {del.isError && <ErrorNotice>{errorMessage(del.error)}</ErrorNotice>}
        <DialogFooter>
          <Button onClick={onClose}>Annuleren</Button>
          <Button variant="danger" busy={del.isPending} onClick={() => del.mutate()}>
            Verwijderen
          </Button>
        </DialogFooter>
      </div>
    </Dialog>
  )
}
