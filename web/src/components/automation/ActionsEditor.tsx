import { useQuery } from '@tanstack/react-query'
import { X } from 'lucide-react'

import { Button, IconButton, Input, Select, Textarea } from '../ui'
import { labelsQuery } from '../../lib/actions'
import { actionLabel, actionTypes, type RuleAction, settableStatuses, slaPoliciesQuery, type ActionType } from '../../lib/automation'
import { templatesQuery } from '../../lib/composer'
import { priorityLabel, type Priority } from '../../lib/inbox'
import { teamsQuery, usersQuery } from './directory'

const MAX_ACTIONS = 10

function freshAction(type: ActionType): RuleAction {
  switch (type) {
    case 'set_priority':
      return { type, priority: 'high' }
    case 'set_status':
      return { type, status: 'waiting' }
    case 'snooze':
      return { type, hours: 24 }
    case 'add_note':
    case 'auto_reply':
      return { type, text: '' }
    default:
      return { type }
  }
}

function without(a: RuleAction, key: string): RuleAction {
  return Object.fromEntries(Object.entries(a).filter(([k]) => k !== key)) as unknown as RuleAction
}

// The "dan ..." part of a rule or macro: a list of actions, one per row. directory says whether
// the caller may list users, teams and SLA policies (admins); the actions that need them are
// hidden otherwise. rulesOnly actions (automatic replies) are hidden for macros.
export function ActionsEditor({
  actions,
  onChange,
  directory,
  allowRulesOnly,
  errors,
}: {
  actions: RuleAction[]
  onChange: (actions: RuleAction[]) => void
  directory: boolean
  allowRulesOnly: boolean
  // Per row index: whether the server rejected it.
  errors: Set<number>
}) {
  const labels = useQuery(labelsQuery)
  const users = useQuery({ ...usersQuery, enabled: directory })
  const teams = useQuery({ ...teamsQuery, enabled: directory })
  const policies = useQuery({ ...slaPoliciesQuery, enabled: directory })
  const templates = useQuery({ ...templatesQuery(), enabled: allowRulesOnly })
  const available = actionTypes.filter((t) => (directory || !t.needsDirectory) && (allowRulesOnly || !t.rulesOnly))

  const update = (i: number, next: RuleAction) => {
    onChange(actions.map((a, j) => (j === i ? next : a)))
  }

  return (
    <div className="flex flex-col gap-2">
      {actions.length > 0 && (
        <ol className="flex flex-col gap-2">
          {actions.map((a, i) => {
            const n = i + 1
            const bad = errors.has(i)
            return (
              <li key={i} className={`flex flex-wrap items-start gap-2 rounded-md border bg-surface p-2 ${bad ? 'border-danger-text' : 'border-line'}`}>
                <Select
                  aria-label={`Actie ${n}`}
                  className="w-64 max-w-full"
                  value={a.type}
                  onChange={(e) => update(i, freshAction(e.target.value as ActionType))}
                >
                  {!available.some((t) => t.value === a.type) && <option value={a.type}>{actionLabel(a.type)}</option>}
                  {available.map((t) => (
                    <option key={t.value} value={t.value}>
                      {t.label}
                    </option>
                  ))}
                </Select>

                {(a.type === 'add_label' || a.type === 'remove_label') && (
                  <Select aria-label={`Label van actie ${n}`} className="w-52 max-w-full" value={a.label_id ?? ''} onChange={(e) => update(i, { ...a, label_id: e.target.value })}>
                    <option value="">Kies een label</option>
                    {labels.data?.map((l) => (
                      <option key={l.id} value={l.id}>
                        {l.name}
                      </option>
                    ))}
                  </Select>
                )}
                {a.type === 'set_priority' && (
                  <Select aria-label={`Prioriteit van actie ${n}`} className="w-40" value={a.priority ?? 'high'} onChange={(e) => update(i, { ...a, priority: e.target.value })}>
                    {(Object.keys(priorityLabel) as Priority[]).map((p) => (
                      <option key={p} value={p}>
                        {priorityLabel[p]}
                      </option>
                    ))}
                  </Select>
                )}
                {a.type === 'set_status' && (
                  <Select aria-label={`Status van actie ${n}`} className="w-40" value={a.status ?? 'waiting'} onChange={(e) => update(i, { ...a, status: e.target.value })}>
                    {settableStatuses.map((s) => (
                      <option key={s.value} value={s.value}>
                        {s.label}
                      </option>
                    ))}
                  </Select>
                )}
                {a.type === 'assign_agent' && (
                  <Select aria-label={`Agent van actie ${n}`} className="w-52 max-w-full" value={a.user_id ?? ''} onChange={(e) => update(i, { ...a, user_id: e.target.value })}>
                    <option value="">Kies een agent</option>
                    {users.data?.map((u) => (
                      <option key={u.id} value={u.id}>
                        {u.name}
                      </option>
                    ))}
                  </Select>
                )}
                {(a.type === 'assign_team' || a.type === 'assign_round_robin') && directory && (
                  <Select
                    aria-label={`Team van actie ${n}`}
                    className="w-52 max-w-full"
                    value={a.team_id ?? ''}
                    onChange={(e) => update(i, e.target.value ? { ...a, team_id: e.target.value } : without(a, 'team_id'))}
                  >
                    <option value="">{a.type === 'assign_team' ? 'Kies een team' : 'Alle agenten van de mailbox'}</option>
                    {teams.data?.teams.map((t) => (
                      <option key={t.id} value={t.id}>
                        {t.name}
                      </option>
                    ))}
                  </Select>
                )}
                {a.type === 'snooze' && (
                  <label className="flex items-center gap-2 text-base text-muted">
                    voor
                    <Input
                      aria-label={`Aantal uur van actie ${n}`}
                      type="number"
                      min={1}
                      max={8760}
                      className="w-20"
                      value={a.hours ?? ''}
                      onChange={(e) => update(i, { ...a, hours: Number(e.target.value) })}
                    />
                    uur
                  </label>
                )}
                {a.type === 'apply_sla' && (
                  <Select aria-label={`SLA-beleid van actie ${n}`} className="w-52 max-w-full" value={a.policy_id ?? ''} onChange={(e) => update(i, { ...a, policy_id: e.target.value })}>
                    <option value="">Kies een beleid</option>
                    {policies.data?.map((p) => (
                      <option key={p.id} value={p.id}>
                        {p.name}
                      </option>
                    ))}
                  </Select>
                )}
                {a.type === 'add_note' && (
                  <Textarea
                    aria-label={`Tekst van notitie ${n}`}
                    className="min-h-16 min-w-64 flex-1"
                    maxLength={5000}
                    value={a.text ?? ''}
                    onChange={(e) => update(i, { ...a, text: e.target.value })}
                  />
                )}
                {a.type === 'auto_reply' && (
                  <div className="flex min-w-64 flex-1 flex-col gap-2">
                    <Select
                      aria-label={`Bron van antwoord ${n}`}
                      value={a.template_id !== undefined ? 'template' : 'text'}
                      onChange={(e) => update(i, e.target.value === 'template' ? { type: 'auto_reply', template_id: '' } : { type: 'auto_reply', text: '' })}
                    >
                      <option value="template">Uit een standaardantwoord</option>
                      <option value="text">Eigen tekst</option>
                    </Select>
                    {a.template_id !== undefined ? (
                      <Select aria-label={`Standaardantwoord van actie ${n}`} value={a.template_id} onChange={(e) => update(i, { type: 'auto_reply', template_id: e.target.value })}>
                        <option value="">Kies een standaardantwoord</option>
                        {templates.data?.templates.map((t) => (
                          <option key={t.id} value={t.id}>
                            {t.name}
                          </option>
                        ))}
                      </Select>
                    ) : (
                      <>
                        <Input
                          aria-label={`Onderwerp van antwoord ${n}`}
                          placeholder="Onderwerp (optioneel, anders Re: onderwerp)"
                          maxLength={200}
                          value={a.subject ?? ''}
                          onChange={(e) => update(i, e.target.value ? { ...a, subject: e.target.value } : without(a, 'subject'))}
                        />
                        <Textarea
                          aria-label={`Tekst van antwoord ${n}`}
                          maxLength={5000}
                          value={a.text ?? ''}
                          onChange={(e) => update(i, { ...a, text: e.target.value })}
                        />
                      </>
                    )}
                    <p className="text-sm text-faint">Wordt nooit verstuurd naar automatische mail, nieuwsbrieven of no-reply-adressen, en maximaal één keer per 24 uur per afzender.</p>
                  </div>
                )}
                {a.type === 'send_webhook' && <span className="self-center text-sm text-faint">Meldt het gesprek aan webhooks met de gebeurtenis Regel: webhook-actie.</span>}

                <IconButton label={`Actie ${n} verwijderen`} onClick={() => onChange(actions.filter((_, j) => j !== i))} className="ml-auto">
                  <X size={16} aria-hidden />
                </IconButton>
              </li>
            )
          })}
        </ol>
      )}
      <div>
        <Button
          size="sm"
          disabled={actions.length >= MAX_ACTIONS}
          onClick={() => onChange([...actions, freshAction(available[0]?.value ?? 'add_label')])}
        >
          + Actie
        </Button>
      </div>
    </div>
  )
}
