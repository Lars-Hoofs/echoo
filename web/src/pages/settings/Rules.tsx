import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Link } from '@tanstack/react-router'
import { ArrowDown, ArrowUp, History, Pencil, Plus, Trash2, Workflow } from 'lucide-react'
import { type SubmitEvent, useState } from 'react'

import { ActionMenu } from '../../components/ActionMenu'
import { ActionsEditor } from '../../components/automation/ActionsEditor'
import { ConditionsEditor } from '../../components/automation/ConditionsEditor'
import { mailboxesQuery } from '../../components/automation/directory'
import { Dialog, DialogFooter } from '../../components/Dialog'
import { useToast } from '../../components/Toast'
import { Badge, Button, Card, EmptyState, ErrorNotice, Field, IconButton, Input, Page, PageHeader, Select, Skeleton, Switch, Table, TBody, Td, Th, THead, Tr } from '../../components/ui'
import { api, ApiError } from '../../lib/api'
import {
  actionLabel,
  type ConditionRow,
  conditionsFromRows,
  describeRuleAction,
  type Rule,
  type RuleAction,
  rowsFromConditions,
  ruleRunsQuery,
  rulesQuery,
  runProblem,
  type Trigger,
  triggerLabel,
  triggers,
} from '../../lib/automation'
import { errorMessage, fieldError } from '../../lib/errors'
import { formatDateTime } from '../../lib/format'

type Editing = { kind: 'new' } | { kind: 'edit' | 'delete' | 'runs'; rule: Rule }

// The rows of a list that the server rejected, read from field paths such as "actions.2.label_id".
function rejectedRows(err: unknown, prefix: string): Set<number> {
  const rows = new Set<number>()
  if (!(err instanceof ApiError)) return rows
  for (const key of Object.keys(err.fields)) {
    if (!key.startsWith(prefix + '.')) continue
    const index = Number(key.slice(prefix.length + 1).split('.')[0])
    if (Number.isInteger(index)) rows.add(index)
  }
  return rows
}

function summary(rule: Rule, mailboxName: string | undefined): string {
  const when = rule.trigger === 'customer_idle' && rule.idle_hours ? `de klant ${rule.idle_hours} uur niet reageert` : triggerLabel(rule.trigger)
  const conditions = rule.conditions.items.length
  const parts = [`Als ${when}`]
  if (conditions > 0) parts.push(conditions === 1 ? '1 voorwaarde' : `${conditions} voorwaarden`)
  parts.push(`dan ${rule.actions.map((a) => actionLabel(a.type).toLowerCase()).join(', ')}`)
  return parts.join(' · ') + (mailboxName ? ` · ${mailboxName}` : '')
}

export function RulesPage() {
  const rules = useQuery(rulesQuery)
  const mailboxes = useQuery(mailboxesQuery)
  const [editing, setEditing] = useState<Editing | null>(null)
  const queryClient = useQueryClient()
  const toast = useToast()
  const refresh = () => queryClient.invalidateQueries({ queryKey: ['automation', 'rules'] })

  const toggle = useMutation({
    mutationFn: (r: Rule) => api('PATCH', `/rules/${r.id}`, { enabled: !r.enabled }),
    onSuccess: refresh,
    onError: (err) => toast(errorMessage(err), { tone: 'error' }),
  })
  const reorder = useMutation({
    mutationFn: (ids: string[]) => api('PUT', '/rules/order', { ids }),
    onSettled: refresh,
    onError: (err) => toast(errorMessage(err), { tone: 'error' }),
  })
  const move = (from: number, to: number) => {
    const list = rules.data ?? []
    const ids = list.map((r) => r.id)
    const [id] = ids.splice(from, 1)
    if (id === undefined) return
    ids.splice(to, 0, id)
    reorder.mutate(ids)
  }
  const mailboxName = (id: string | null) => (id ? mailboxes.data?.mailboxes.find((m) => m.id === id)?.name : undefined)

  return (
    <Page>
      <PageHeader
        breadcrumb="Werkruimte"
        title="Regels"
        description="Regels sorteren en behandelen gesprekken automatisch. Ze draaien in de volgorde van deze lijst; een regel kan de rest overslaan."
        actions={
          <Button variant="primary" onClick={() => setEditing({ kind: 'new' })}>
            <Plus size={16} aria-hidden />
            Regel toevoegen
          </Button>
        }
      />
      {rules.isPending ? (
        <Skeleton className="h-40" />
      ) : rules.isError ? (
        <ErrorNotice>{errorMessage(rules.error)}</ErrorNotice>
      ) : rules.data.length === 0 ? (
        <Card>
          <EmptyState
            icon={<Workflow size={20} />}
            title="Nog geen regels"
            description="Maak een regel om gesprekken automatisch te labelen, toe te wijzen of te beantwoorden."
            action={
              <Button variant="primary" onClick={() => setEditing({ kind: 'new' })}>
                Maak de eerste regel aan
              </Button>
            }
          />
        </Card>
      ) : (
        <Card flush>
          <Table>
            <THead>
              <Th className="w-px">
                <span className="sr-only">Volgorde</span>
              </Th>
              <Th>Regel</Th>
              <Th className="w-px">Actief</Th>
              <Th className="w-px">
                <span className="sr-only">Acties</span>
              </Th>
            </THead>
            <TBody>
              {rules.data.map((r, i) => (
                <Tr key={r.id}>
                  <Td>
                    <div className="flex items-center">
                      <IconButton label={`${r.name} omhoog verplaatsen`} disabled={i === 0 || reorder.isPending} onClick={() => move(i, i - 1)}>
                        <ArrowUp size={16} aria-hidden />
                      </IconButton>
                      <IconButton label={`${r.name} omlaag verplaatsen`} disabled={i === rules.data.length - 1 || reorder.isPending} onClick={() => move(i, i + 1)}>
                        <ArrowDown size={16} aria-hidden />
                      </IconButton>
                    </div>
                  </Td>
                  <Td>
                    <div className="flex min-w-0 flex-col gap-1">
                      <span className="flex flex-wrap items-center gap-2 text-ink">
                        {r.name}
                        {r.stop_processing && <Badge>Stopt verdere regels</Badge>}
                      </span>
                      <span className="text-sm text-muted">{summary(r, mailboxName(r.mailbox_id))}</span>
                    </div>
                  </Td>
                  <Td>
                    <Switch checked={r.enabled} label={`${r.name} actief`} disabled={toggle.isPending} onChange={() => toggle.mutate(r)} />
                  </Td>
                  <Td>
                    <div className="flex items-center justify-end">
                      <ActionMenu
                        label={`Acties voor ${r.name}`}
                        items={[
                          { label: 'Bewerken', icon: <Pencil size={16} />, onSelect: () => setEditing({ kind: 'edit', rule: r }) },
                          { label: 'Uitvoeringen', icon: <History size={16} />, onSelect: () => setEditing({ kind: 'runs', rule: r }) },
                          { label: 'Verwijderen', icon: <Trash2 size={16} />, danger: true, separated: true, onSelect: () => setEditing({ kind: 'delete', rule: r }) },
                        ]}
                      />
                    </div>
                  </Td>
                </Tr>
              ))}
            </TBody>
          </Table>
        </Card>
      )}
      {(editing?.kind === 'new' || editing?.kind === 'edit') && (
        <RuleDialog rule={editing.kind === 'edit' ? editing.rule : null} onClose={() => setEditing(null)} />
      )}
      {editing?.kind === 'runs' && <RunsDialog rule={editing.rule} onClose={() => setEditing(null)} />}
      {editing?.kind === 'delete' && <DeleteDialog rule={editing.rule} onClose={() => setEditing(null)} />}
    </Page>
  )
}

function RuleDialog({ rule, onClose }: { rule: Rule | null; onClose: () => void }) {
  const initial = rowsFromConditions(rule?.conditions ?? { match: 'all', items: [] })
  const [name, setName] = useState(rule?.name ?? '')
  const [mailboxId, setMailboxId] = useState(rule?.mailbox_id ?? '')
  const [trigger, setTrigger] = useState<Trigger>(rule?.trigger ?? 'conversation_created')
  const [idleHours, setIdleHours] = useState(String(rule?.idle_hours ?? 48))
  const [match, setMatch] = useState(initial.match)
  const [rows, setRows] = useState<ConditionRow[]>(initial.rows)
  const [actions, setActions] = useState<RuleAction[]>(rule?.actions ?? [])
  const [stop, setStop] = useState(rule?.stop_processing ?? false)
  const mailboxes = useQuery(mailboxesQuery)
  const queryClient = useQueryClient()

  const save = useMutation({
    mutationFn: () => {
      const body = {
        name,
        mailbox_id: mailboxId || null,
        trigger,
        idle_hours: trigger === 'customer_idle' ? Number(idleHours) : null,
        conditions: conditionsFromRows(match, rows),
        actions,
        stop_processing: stop,
      }
      return rule ? api('PATCH', `/rules/${rule.id}`, body) : api('POST', '/rules', body)
    },
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: ['automation', 'rules'] })
      onClose()
    },
  })
  const submit = (e: SubmitEvent) => {
    e.preventDefault()
    save.mutate()
  }
  const conditionErrors = rejectedRows(save.error, 'conditions.items')
  const actionErrors = rejectedRows(save.error, 'actions')
  const listError = fieldError(save.error, 'actions') ?? fieldError(save.error, 'conditions')

  return (
    <Dialog open onOpenChange={(o) => !o && onClose()} title={rule ? 'Regel bewerken' : 'Regel toevoegen'} size="xl">
      <form onSubmit={submit} className="flex flex-col gap-5" noValidate>
        {save.isError && <ErrorNotice>{errorMessage(save.error)}</ErrorNotice>}
        <Field label="Naam" error={fieldError(save.error, 'name')}>
          {(p) => <Input {...p} autoFocus maxLength={100} value={name} onChange={(e) => setName(e.target.value)} />}
        </Field>

        <section aria-labelledby="rule-when" className="flex flex-col gap-2">
          <h3 id="rule-when" className="text-base font-semibold text-ink">
            Als
          </h3>
          <div className="flex flex-wrap items-center gap-2">
            <Select aria-label="Wanneer de regel start" className="w-full max-w-md" value={trigger} onChange={(e) => setTrigger(e.target.value as Trigger)}>
              {triggers.map((t) => (
                <option key={t.value} value={t.value}>
                  {t.label}
                </option>
              ))}
            </Select>
            {trigger === 'customer_idle' && (
              <label className="flex items-center gap-2 text-base text-muted">
                na
                <Input aria-label="Aantal uur zonder reactie" type="number" min={1} max={720} className="w-24" value={idleHours} onChange={(e) => setIdleHours(e.target.value)} />
                uur
              </label>
            )}
          </div>
          {fieldError(save.error, 'idle_hours') && <p className="text-sm text-danger-text">{fieldError(save.error, 'idle_hours')}</p>}
          <label className="flex flex-wrap items-center gap-2 text-base text-muted">
            in
            <Select aria-label="Mailbox" className="w-64 max-w-full" value={mailboxId} onChange={(e) => setMailboxId(e.target.value)}>
              <option value="">alle mailboxen</option>
              {mailboxes.data?.mailboxes.map((m) => (
                <option key={m.id} value={m.id}>
                  {m.name}
                </option>
              ))}
            </Select>
          </label>
        </section>

        <section aria-labelledby="rule-if" className="flex flex-col gap-2">
          <h3 id="rule-if" className="text-base font-semibold text-ink">
            En
          </h3>
          <ConditionsEditor
            match={match}
            rows={rows}
            directory
            errors={conditionErrors}
            onChange={(m, r) => {
              setMatch(m)
              setRows(r)
            }}
          />
        </section>

        <section aria-labelledby="rule-then" className="flex flex-col gap-2">
          <h3 id="rule-then" className="text-base font-semibold text-ink">
            Dan
          </h3>
          <ActionsEditor actions={actions} onChange={setActions} directory allowRulesOnly errors={actionErrors} />
          {listError && <p className="text-sm text-danger-text">{listError}</p>}
        </section>

        <div className="flex items-center gap-3">
          <Switch checked={stop} onChange={setStop} label="Verdere regels overslaan als deze regel past" />
          <span className="text-base text-ink">Verdere regels overslaan als deze regel past</span>
        </div>

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

function RunsDialog({ rule, onClose }: { rule: Rule; onClose: () => void }) {
  const runs = useQuery(ruleRunsQuery(rule.id))
  return (
    <Dialog
      open
      onOpenChange={(o) => !o && onClose()}
      title={`Uitvoeringen van ${rule.name}`}
      description="Elke keer dat de regel is bekeken, ook als hij niet paste. Uitvoeringen worden 30 dagen bewaard."
      size="xl"
    >
      {runs.isPending ? (
        <Skeleton className="h-32" />
      ) : runs.isError ? (
        <ErrorNotice>{errorMessage(runs.error)}</ErrorNotice>
      ) : runs.data.length === 0 ? (
        <p className="text-base text-muted">Deze regel is nog niet gebruikt.</p>
      ) : (
        <Table>
          <THead>
            <Th>Tijdstip</Th>
            <Th>Gesprek</Th>
            <Th>Uitkomst</Th>
          </THead>
          <TBody>
            {runs.data.map((run) => (
              <Tr key={run.id}>
                <Td className="whitespace-nowrap text-muted tabular-nums">{formatDateTime(run.created_at)}</Td>
                <Td>
                  <Link to="/inbox/$view/$conversationId" params={{ view: 'alle', conversationId: run.conversation_id }} className="text-ink underline-offset-2 hover:underline">
                    <span className="font-mono">#{run.conversation_number}</span>
                  </Link>
                </Td>
                <Td>
                  <div className="flex flex-col gap-1">
                    <span>{run.matched ? <Badge dot>Paste</Badge> : <Badge>Paste niet</Badge>}</span>
                    {run.actions.map((a, i) => (
                      <span key={i} className={`text-sm ${a.result === 'failed' ? 'text-danger-text' : 'text-muted'}`}>
                        {describeRuleAction(a)}
                      </span>
                    ))}
                    {runProblem(run) && <span className="text-sm text-danger-text">{runProblem(run)}</span>}
                  </div>
                </Td>
              </Tr>
            ))}
          </TBody>
        </Table>
      )}
    </Dialog>
  )
}

function DeleteDialog({ rule, onClose }: { rule: Rule; onClose: () => void }) {
  const queryClient = useQueryClient()
  const del = useMutation({
    mutationFn: () => api('DELETE', `/rules/${rule.id}`),
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: ['automation', 'rules'] })
      onClose()
    },
  })
  return (
    <Dialog open onOpenChange={(o) => !o && onClose()} title="Regel verwijderen">
      <div className="flex flex-col gap-4">
        <p className="text-base text-muted">De regel {rule.name} en zijn uitvoeringen worden verwijderd. Gesprekken die de regel al heeft aangepast blijven zoals ze zijn.</p>
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
