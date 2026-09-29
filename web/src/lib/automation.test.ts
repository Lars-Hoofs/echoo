import { describe, expect, it } from 'vitest'

import {
  actionLabel,
  conditionsFromRows,
  describeRuleAction,
  formatMinutes,
  macroToast,
  newConditionRow,
  opsFor,
  rowsFromConditions,
  runProblem,
  splitMinutes,
  toMinutes,
  triggerLabel,
  type ConditionGroup,
} from './automation'

describe('condition rows', () => {
  const stored: ConditionGroup = {
    match: 'any',
    items: [
      { field: 'subject', op: 'contains', value: 'factuur' },
      { field: 'from_domain', op: 'ends_with', value: 'acme.example', negate: true },
      { field: 'label', op: 'has_any', value: ['l1', 'l2'] },
      { field: 'has_attachment', op: 'is', value: false },
      { match: 'all', items: [{ field: 'status', op: 'is', value: ['open'] }] },
    ],
  }

  it('turns negation into an operator of its own and back', () => {
    const { match, rows } = rowsFromConditions(stored)
    expect(match).toBe('any')
    expect(rows[1]).toMatchObject({ kind: 'condition', field: 'from_domain', op: '!ends_with', text: 'acme.example' })
    expect(conditionsFromRows(match, rows)).toEqual(stored)
  })

  it('keeps nested groups untouched', () => {
    const { rows } = rowsFromConditions(stored)
    expect(rows[4]).toEqual({ kind: 'group', node: stored.items[4] })
  })

  it('sends the value in the shape of the field', () => {
    const text = { ...newConditionRow('subject'), text: '  Factuur ' }
    const bool = { ...newConditionRow('has_attachment'), flag: false }
    const ids = { ...newConditionRow('label'), ids: ['l1'] }
    expect(conditionsFromRows('all', [text, bool, ids]).items).toEqual([
      { field: 'subject', op: 'equals', value: 'Factuur' },
      { field: 'has_attachment', op: 'is', value: false },
      { field: 'label', op: 'has_any', value: ['l1'] },
    ])
  })

  it('offers the operators of the field', () => {
    expect(opsFor('subject').map((o) => o.value)).toContain('!contains')
    expect(opsFor('label').map((o) => o.value)).toEqual(['has_any', '!has_any'])
    expect(opsFor('has_assignee').map((o) => o.value)).toEqual(['is'])
  })
})

describe('labels', () => {
  it('names triggers and actions and falls back to the code', () => {
    expect(triggerLabel('message_received')).toBe('de klant een nieuw bericht stuurt')
    expect(triggerLabel('x')).toBe('x')
    expect(actionLabel('add_label')).toBe('Label toevoegen')
    expect(actionLabel('x')).toBe('x')
  })

  it('formats minutes exactly', () => {
    expect(formatMinutes(90)).toBe('1 u 30 min')
    expect(formatMinutes(1440)).toBe('1 d')
    expect(formatMinutes(60)).toBe('1 u')
    expect(formatMinutes(2885)).toBe('2 d 5 min')
    expect(formatMinutes(0)).toBe('0 min')
  })
})

describe('run log', () => {
  it('translates known reasons and never shows an unknown developer message', () => {
    expect(describeRuleAction({ type: 'add_label', result: 'applied' })).toBe('Label toevoegen')
    expect(describeRuleAction({ type: 'assign_round_robin', result: 'skipped', detail: 'no agent available' })).toBe(
      'Verdelen over beschikbare agenten: geen agent beschikbaar',
    )
    expect(describeRuleAction({ type: 'add_label', result: 'failed', detail: 'unknown label' })).toBe('Label toevoegen: het label bestaat niet meer')
    expect(describeRuleAction({ type: 'assign_agent', result: 'failed', detail: 'assign: assignee cannot write to the mailbox' })).toContain('geen schrijfrechten')
    expect(describeRuleAction({ type: 'add_note', result: 'failed', detail: 'ERROR: connection refused (SQLSTATE 08006)' })).toBe('Interne notitie toevoegen: mislukt')
    expect(describeRuleAction({ type: 'auto_reply', result: 'skipped', detail: 'something new' })).toBe('Automatisch antwoord versturen: overgeslagen')
  })

  it('reports a broken stored rule only', () => {
    const run = { id: '1', conversation_id: 'c', conversation_number: 1, trigger: 'x', matched: false, actions: [], created_at: '' }
    expect(runProblem({ ...run, error: 'stored conditions are invalid: invalid' })).toContain('ongeldig')
    expect(runProblem({ ...run, error: 'add_label: unknown label' })).toBe('')
  })
})

describe('durations', () => {
  it('shows the largest exact unit', () => {
    expect(splitMinutes(2880)).toEqual({ value: 2, unit: 'days' })
    expect(splitMinutes(120)).toEqual({ value: 2, unit: 'hours' })
    expect(splitMinutes(90)).toEqual({ value: 90, unit: 'minutes' })
    expect(splitMinutes(1441)).toEqual({ value: 1441, unit: 'minutes' })
  })

  it('converts back', () => {
    expect(toMinutes(2, 'days')).toBe(2880)
    expect(toMinutes(1.5, 'hours')).toBe(90)
    expect(toMinutes(30, 'minutes')).toBe(30)
    expect(toMinutes(splitMinutes(4320).value, splitMinutes(4320).unit)).toBe(4320)
  })
})

describe('macro toast', () => {
  it('says what happened', () => {
    expect(macroToast('VIP', 1, 0)).toBe('Macro VIP uitgevoerd')
    expect(macroToast('VIP', 5, 0)).toBe('Macro VIP uitgevoerd op 5 gesprekken')
    expect(macroToast('VIP', 2, 2)).toBe('Macro VIP is niet uitgevoerd')
    expect(macroToast('VIP', 5, 2)).toBe('Macro VIP uitgevoerd, maar 2 van 5 gesprekken zijn niet volledig bijgewerkt')
  })
})
