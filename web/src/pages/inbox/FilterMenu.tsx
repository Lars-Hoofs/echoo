import { useQuery } from '@tanstack/react-query'
import { Command } from 'cmdk'
import { CalendarDays, Check, ChevronLeft, Flag, Mail, Paperclip, Tag, UserRound, UsersRound } from 'lucide-react'
import { type ReactNode, useState } from 'react'

import { Dialog, DialogFooter } from '../../components/Dialog'
import { LabelDot } from '../../components/LabelChip'
import { StatusGlyph } from '../../components/StatusBadge'
import { Button, Field, Input } from '../../components/ui'
import { labelsQuery } from '../../lib/actions'
import { exclusiveEnd, inclusiveEnd, splitList, toggleInList, withFilter } from '../../lib/filters'
import { type InboxSearch, listStatusLabel, listStatusOrder, type Priority, priorityLabel, type Ref, summaryQuery } from '../../lib/inbox'

type Step = 'status' | 'mailbox' | 'assignee' | 'team' | 'label' | 'priority' | 'period'

const properties: { step: Step | 'attachment'; label: string; icon: ReactNode }[] = [
  { step: 'status', label: 'Status', icon: <StatusGlyph status="open" size={20} /> },
  { step: 'mailbox', label: 'Mailbox', icon: <Mail aria-hidden /> },
  { step: 'assignee', label: 'Toegewezen aan', icon: <UserRound aria-hidden /> },
  { step: 'team', label: 'Team', icon: <UsersRound aria-hidden /> },
  { step: 'label', label: 'Label', icon: <Tag aria-hidden /> },
  { step: 'priority', label: 'Prioriteit', icon: <Flag aria-hidden /> },
  { step: 'attachment', label: 'Heeft bijlage', icon: <Paperclip aria-hidden /> },
  { step: 'period', label: 'Periode', icon: <CalendarDays aria-hidden /> },
]

const priorities: Priority[] = ['urgent', 'high', 'normal', 'low', 'none']

const itemClass =
  'flex cursor-default items-center gap-3 rounded-full px-4 py-2 text-base text-ink outline-none data-[selected=true]:bg-selected max-md:py-3'

function Choice({ value, selected, onSelect, children, count }: { value: string; selected?: boolean; onSelect: () => void; children: ReactNode; count?: number }) {
  return (
    <Command.Item value={value} onSelect={onSelect} className={itemClass}>
      {children}
      <span className="ml-auto flex items-center gap-2">
        {count !== undefined && <span className="t-label tabular-nums">{count}</span>}
        {selected && (
          <>
            <Check aria-hidden />
            <span className="sr-only">gekozen</span>
          </>
        )}
      </span>
    </Command.Item>
  )
}

function PeriodForm({ search, apply }: { search: InboxSearch; apply: (after: string | undefined, before: string | undefined) => void }) {
  const [from, setFrom] = useState(search.after ?? '')
  const [to, setTo] = useState(search.before ? inclusiveEnd(search.before) : '')
  const invalid = from !== '' && to !== '' && from > to
  return (
    <form
      className="flex flex-col gap-3"
      onSubmit={(e) => {
        e.preventDefault()
        if (!invalid) apply(from || undefined, to ? exclusiveEnd(to) : undefined)
      }}
    >
      <Field label="Van" help="Eerste dag die meetelt.">
        {(p) => <Input {...p} type="date" autoFocus value={from} max={to || undefined} onChange={(e) => setFrom(e.target.value)} />}
      </Field>
      <Field label="Tot en met" error={invalid ? 'Kies een datum na de begindatum.' : undefined}>
        {(p) => <Input {...p} type="date" value={to} min={from || undefined} onChange={(e) => setTo(e.target.value)} />}
      </Field>
      <DialogFooter>
        <Button type="submit" variant="primary" disabled={(from === '' && to === '') || invalid}>
          Toepassen
        </Button>
      </DialogFooter>
    </form>
  )
}

interface Props {
  open: boolean
  onOpenChange: (open: boolean) => void
  search: InboxSearch
  users: Ref[]
  onChange: (next: InboxSearch) => void
}

// A type-to-filter list of properties, then the values of the chosen one.
export function FilterMenu({ open, onOpenChange, search, users, onChange }: Props) {
  const [step, setStep] = useState<Step | null>(null)
  const summary = useQuery(summaryQuery)
  const labels = useQuery(labelsQuery)

  const done = (next: InboxSearch) => {
    onChange(next)
    onOpenChange(false)
    setStep(null)
  }
  const close = (o: boolean) => {
    onOpenChange(o)
    if (!o) setStep(null)
  }
  const title = step ? (properties.find((p) => p.step === step)?.label ?? 'Filter') : 'Filter toevoegen'

  return (
    <Dialog open={open} onOpenChange={close} title={title} size="sm">
      {step === 'period' ? (
        <div className="flex flex-col gap-3">
          <BackButton onClick={() => setStep(null)} />
          <PeriodForm
            search={search}
            apply={(after, before) => {
              done(withFilter(withFilter(search, 'after', after), 'before', before))
            }}
          />
        </div>
      ) : (
        <Command
          label={title}
          className="flex flex-col gap-2"
          onKeyDown={(e) => {
            if (e.key === 'Backspace' && step && e.currentTarget.querySelector('input')?.value === '') setStep(null)
          }}
        >
          {step && <BackButton onClick={() => setStep(null)} />}
          <Command.Input
            key={step ?? 'properties'}
            autoFocus
            placeholder={step ? 'Zoek een waarde' : 'Zoek een eigenschap'}
            aria-label={step ? 'Zoek een waarde' : 'Zoek een eigenschap'}
            className="h-11 w-full rounded-full border border-line-input bg-transparent px-4 text-base text-ink placeholder:text-faint focus-visible:border-ink focus-visible:outline-offset-0 max-md:text-lg"
          />
          <Command.List className="max-h-72 overflow-y-auto">
            <Command.Empty className="px-4 py-3 text-base text-muted">Niets gevonden.</Command.Empty>
            {step === null &&
              properties.map((p) => (
                <Command.Item
                  key={p.step}
                  value={p.label}
                  onSelect={() => {
                    if (p.step === 'attachment') done({ ...search, attachment: true })
                    else setStep(p.step)
                  }}
                  className={itemClass}
                >
                  <span className="text-muted">{p.icon}</span>
                  {p.label}
                </Command.Item>
              ))}
            {step === 'status' &&
              listStatusOrder.map((s) => (
                <Choice key={s} value={listStatusLabel[s]} selected={(search.status ?? 'open') === s} onSelect={() => done(withFilter(search, 'status', s === 'open' ? undefined : s))}>
                  <StatusGlyph status={s} size={20} />
                  {listStatusLabel[s]}
                </Choice>
              ))}
            {step === 'mailbox' &&
              summary.data?.mailboxes.map((m) => (
                <Choice key={m.id} value={m.name} count={m.open_count} selected={splitList(search.mailbox).includes(m.id)} onSelect={() => done(withFilter(search, 'mailbox', toggleInList(search.mailbox, m.id)))}>
                  <Mail aria-hidden className="text-muted" />
                  {m.name}
                </Choice>
              ))}
            {step === 'team' &&
              summary.data?.teams.map((t) => (
                <Choice key={t.id} value={t.name} count={t.open_count} selected={splitList(search.team).includes(t.id)} onSelect={() => done(withFilter(search, 'team', toggleInList(search.team, t.id)))}>
                  <UsersRound aria-hidden className="text-muted" />
                  {t.name}
                </Choice>
              ))}
            {step === 'label' &&
              labels.data?.map((l) => (
                <Choice key={l.id} value={l.name} selected={splitList(search.label).includes(l.id)} onSelect={() => done(withFilter(search, 'label', toggleInList(search.label, l.id)))}>
                  <LabelDot color={l.color_token} />
                  {l.name}
                </Choice>
              ))}
            {step === 'priority' &&
              priorities.map((p) => (
                <Choice key={p} value={priorityLabel[p]} selected={splitList(search.priority).includes(p)} onSelect={() => done(withFilter(search, 'priority', toggleInList(search.priority, p)))}>
                  {priorityLabel[p]}
                </Choice>
              ))}
            {step === 'assignee' && (
              <>
                <Choice value="mij" selected={search.assignee === 'me'} onSelect={() => done(withFilter(search, 'assignee', search.assignee === 'me' ? undefined : 'me'))}>
                  <UserRound aria-hidden className="text-muted" />
                  Mij
                </Choice>
                <Choice value="niemand zonder toewijzing" selected={search.assignee === 'none'} onSelect={() => done(withFilter(search, 'assignee', search.assignee === 'none' ? undefined : 'none'))}>
                  <UserRound aria-hidden className="text-muted" />
                  Niemand
                </Choice>
                {users.map((u) => (
                  <Choice key={u.id} value={`${u.name} ${u.id}`} selected={search.assignee === u.id} onSelect={() => done(withFilter(search, 'assignee', search.assignee === u.id ? undefined : u.id))}>
                    <UserRound aria-hidden className="text-muted" />
                    {u.name}
                  </Choice>
                ))}
              </>
            )}
          </Command.List>
        </Command>
      )}
    </Dialog>
  )
}

function BackButton({ onClick }: { onClick: () => void }) {
  return (
    <button type="button" onClick={onClick} className="btn btn-s w-fit border-transparent px-3 text-muted hover:text-ink">
      <ChevronLeft size={16} aria-hidden />
      Alle eigenschappen
    </button>
  )
}
