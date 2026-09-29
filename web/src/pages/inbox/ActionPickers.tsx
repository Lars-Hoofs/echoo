import { useQueries, useQuery } from '@tanstack/react-query'
import { Command } from 'cmdk'
import { Check, ClockArrowUp, UserRound, UsersRound } from 'lucide-react'
import { createContext, type ReactNode, useCallback, useContext, useMemo, useState } from 'react'

import { Dialog, DialogFooter } from '../../components/Dialog'
import { LabelDot } from '../../components/LabelChip'
import { Button, ErrorNotice, Input } from '../../components/ui'
import { type Assignees, assigneesQuery, labelsQuery, snoozePresets } from '../../lib/actions'
import { errorMessage } from '../../lib/errors'
import { type ConversationListItem, type Priority, priorityLabel, type Ref } from '../../lib/inbox'
import { useConversationActions } from './useConversationActions'

export type PickerKind = 'assign' | 'label' | 'priority' | 'snooze'

interface PickerApi {
  openPicker: (kind: PickerKind, targets: ConversationListItem[]) => void
}

const PickerContext = createContext<PickerApi | null>(null)

export function usePickers(): PickerApi {
  const ctx = useContext(PickerContext)
  if (!ctx) throw new Error('usePickers needs a PickerProvider')
  return ctx
}

const titles: Record<PickerKind, string> = {
  assign: 'Toewijzen aan',
  label: 'Labels',
  priority: 'Prioriteit',
  snooze: 'Uitstellen tot',
}

const itemClass =
  'flex cursor-default items-center gap-3 rounded-full px-4 py-2 text-base text-ink outline-none data-[selected=true]:bg-selected max-md:py-3'
const groupClass =
  '[&_[cmdk-group-heading]]:px-4 [&_[cmdk-group-heading]]:pt-3 [&_[cmdk-group-heading]]:pb-1 [&_[cmdk-group-heading]]:text-xs [&_[cmdk-group-heading]]:text-muted'

// One dialog serves every picker, so the keyboard shortcuts and the contact panel share it.
export function PickerProvider({ children }: { children: ReactNode }) {
  const [open, setOpen] = useState<{ kind: PickerKind; targets: ConversationListItem[] } | null>(null)
  const openPicker = useCallback((kind: PickerKind, targets: ConversationListItem[]) => {
    if (targets.length > 0) setOpen({ kind, targets })
  }, [])
  const api = useMemo(() => ({ openPicker }), [openPicker])
  const close = () => setOpen(null)

  return (
    <PickerContext.Provider value={api}>
      {children}
      {open && (
        <Dialog open onOpenChange={(o) => !o && close()} title={titles[open.kind]} size="sm">
          {open.kind === 'assign' && <AssignPicker targets={open.targets} onDone={close} />}
          {open.kind === 'label' && <LabelPicker targets={open.targets} onDone={close} />}
          {open.kind === 'priority' && <PriorityPicker targets={open.targets} onDone={close} />}
          {open.kind === 'snooze' && <SnoozePicker targets={open.targets} onDone={close} />}
        </Dialog>
      )}
    </PickerContext.Provider>
  )
}

function Palette({ placeholder, label, children }: { placeholder: string; label: string; children: ReactNode }) {
  return (
    <Command label={label} className="flex flex-col gap-2">
      <Command.Input
        autoFocus
        placeholder={placeholder}
        aria-label={label}
        className="h-11 w-full rounded-full border border-line-input bg-transparent px-4 text-base text-ink placeholder:text-faint focus-visible:border-ink focus-visible:outline-offset-0 max-md:text-lg"
      />
      <Command.List className="max-h-72 overflow-y-auto">
        <Command.Empty className="px-4 py-3 text-base text-muted">Niets gevonden.</Command.Empty>
        {children}
      </Command.List>
    </Command>
  )
}

// Only people and teams that every selected conversation's mailbox accepts can be offered.
function useCommonAssignees(targets: ConversationListItem[]) {
  const mailboxIds = [...new Set(targets.map((t) => t.mailbox.id))]
  const results = useQueries({ queries: mailboxIds.map(assigneesQuery) })
  const loading = results.some((r) => r.isPending)
  const error = results.find((r) => r.isError)?.error
  const loaded = results.flatMap((r) => (r.data ? [r.data] : []))
  const common = (pick: (a: Assignees) => Ref[]): Ref[] => {
    const [first, ...rest] = loaded
    if (!first) return []
    return pick(first).filter((x) => rest.every((a) => pick(a).some((y) => y.id === x.id)))
  }
  return { loading, error, users: common((a) => a.users), teams: common((a) => a.teams) }
}

function AssignPicker({ targets, onDone }: { targets: ConversationListItem[]; onDone: () => void }) {
  const apply = useConversationActions()
  const { loading, error, users, teams } = useCommonAssignees(targets)
  if (error) return <ErrorNotice>{errorMessage(error)}</ErrorNotice>
  if (loading) return <p className="py-6 text-center text-base text-muted">Laden</p>
  const choose = (change: Parameters<typeof apply>[1]) => {
    onDone()
    void apply(targets, change)
  }
  return (
    <Palette placeholder="Zoek een collega of team" label="Toewijzen aan">
      <Command.Group className={groupClass}>
        <Command.Item value="niemand toewijzing verwijderen" onSelect={() => choose({ assignee: null })} className={itemClass}>
          <UserRound aria-hidden className="text-muted" />
          Niemand
        </Command.Item>
      </Command.Group>
      {users.length > 0 && (
        <Command.Group heading="Collega's" className={groupClass}>
          {users.map((u) => (
            <Command.Item key={u.id} value={`${u.name} ${u.id}`} onSelect={() => choose({ assignee: u })} className={itemClass}>
              <UserRound aria-hidden className="text-muted" />
              {u.name}
            </Command.Item>
          ))}
        </Command.Group>
      )}
      <Command.Group heading="Teams" className={groupClass}>
        <Command.Item value="geen team verwijderen" onSelect={() => choose({ team: null })} className={itemClass}>
          <UsersRound aria-hidden className="text-muted" />
          Geen team
        </Command.Item>
        {teams.map((t) => (
          <Command.Item key={t.id} value={`team ${t.name} ${t.id}`} onSelect={() => choose({ team: t })} className={itemClass}>
            <UsersRound aria-hidden className="text-muted" />
            {t.name}
          </Command.Item>
        ))}
      </Command.Group>
    </Palette>
  )
}

function LabelPicker({ targets, onDone }: { targets: ConversationListItem[]; onDone: () => void }) {
  const apply = useConversationActions()
  const labels = useQuery(labelsQuery)
  // A label counts as set when every selected conversation has it.
  const initial = useMemo(
    () => new Set(targets[0]?.labels.map((l) => l.id).filter((id) => targets.every((t) => t.labels.some((l) => l.id === id)))),
    [targets],
  )
  const [selected, setSelected] = useState(initial)
  if (labels.isError) return <ErrorNotice>{errorMessage(labels.error)}</ErrorNotice>
  if (labels.isPending) return <p className="py-6 text-center text-base text-muted">Laden</p>

  const toggle = (id: string) => {
    const next = new Set(selected)
    if (next.has(id)) next.delete(id)
    else next.add(id)
    setSelected(next)
  }
  const save = () => {
    const add = labels.data.filter((l) => selected.has(l.id) && !initial.has(l.id))
    const remove = [...initial].filter((id) => !selected.has(id))
    onDone()
    if (add.length > 0) void apply(targets, { addLabels: add })
    if (remove.length > 0) void apply(targets, { removeLabelIds: remove })
  }
  const changed = labels.data.some((l) => selected.has(l.id) !== initial.has(l.id))

  return (
    <div className="flex flex-col gap-3">
      {labels.data.length === 0 ? (
        <p className="py-4 text-base text-muted">Er zijn nog geen labels. Een beheerder maakt ze aan bij Instellingen.</p>
      ) : (
        <Palette placeholder="Zoek een label" label="Labels">
          {labels.data.map((l) => (
            <Command.Item key={l.id} value={`${l.name} ${l.id}`} onSelect={() => toggle(l.id)} className={itemClass}>
              <LabelDot color={l.color_token} />
              <span className="min-w-0 flex-1 truncate">{l.name}</span>
              {selected.has(l.id) && (
                <>
                  <Check aria-hidden />
                  <span className="sr-only">geselecteerd</span>
                </>
              )}
            </Command.Item>
          ))}
        </Palette>
      )}
      <DialogFooter>
        <Button onClick={onDone}>Annuleren</Button>
        <Button variant="primary" disabled={!changed} onClick={save}>
          Toepassen
        </Button>
      </DialogFooter>
    </div>
  )
}

const priorities: Priority[] = ['none', 'low', 'normal', 'high', 'urgent']

function PriorityPicker({ targets, onDone }: { targets: ConversationListItem[]; onDone: () => void }) {
  const apply = useConversationActions()
  const current = targets.length === 1 ? targets[0]?.priority : undefined
  return (
    <Palette placeholder="Kies een prioriteit" label="Prioriteit">
      {priorities.map((p) => (
        <Command.Item
          key={p}
          value={priorityLabel[p]}
          onSelect={() => {
            onDone()
            void apply(targets, { priority: p })
          }}
          className={itemClass}
        >
          <span className="flex-1">{priorityLabel[p]}</span>
          {current === p && (
            <>
              <Check aria-hidden />
              <span className="sr-only">huidige prioriteit</span>
            </>
          )}
        </Command.Item>
      ))}
    </Palette>
  )
}

// datetime-local wants "YYYY-MM-DDTHH:mm" in local time.
function toLocalInput(d: Date): string {
  const pad = (n: number) => String(n).padStart(2, '0')
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}T${pad(d.getHours())}:${pad(d.getMinutes())}`
}

function SnoozePicker({ targets, onDone }: { targets: ConversationListItem[]; onDone: () => void }) {
  const apply = useConversationActions()
  const now = new Date()
  const [custom, setCustom] = useState(() => toLocalInput(new Date(now.getTime() + 60 * 60_000)))
  const at = new Date(custom)
  const valid = !Number.isNaN(at.getTime()) && at.getTime() > now.getTime()
  const snooze = (d: Date) => {
    onDone()
    void apply(targets, { snoozedUntil: d.toISOString() })
  }
  return (
    <div className="flex flex-col gap-3">
      <Command label="Uitstellen tot" className="flex flex-col">
        <Command.List>
          {snoozePresets(now).map((p) => (
            <Command.Item key={p.key} value={p.label} onSelect={() => snooze(p.at)} className={itemClass}>
              <ClockArrowUp aria-hidden className="text-muted" />
              {p.label}
            </Command.Item>
          ))}
          {targets.some((t) => t.snoozed_until) && (
            <Command.Item
              value="uitstel opheffen"
              onSelect={() => {
                onDone()
                void apply(targets, { snoozedUntil: null })
              }}
              className={itemClass}
            >
              Uitstel opheffen
            </Command.Item>
          )}
        </Command.List>
      </Command>
      <div className="flex items-end gap-2 border-t border-line pt-3">
        <label className="t-label flex min-w-0 flex-1 flex-col gap-2">
          Kies datum en tijd
          <Input type="datetime-local" value={custom} min={toLocalInput(now)} onChange={(e) => setCustom(e.target.value)} />
        </label>
        <Button variant="primary" disabled={!valid} onClick={() => snooze(at)}>
          Uitstellen
        </Button>
      </div>
    </div>
  )
}
