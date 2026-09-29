import { capitalize, countWord, formatDuration, formatNumber, type LiveReport } from '../../lib/reports'

export interface AttentionSummary {
  // What the card says, in order of urgency; "quiet" when nothing needs a reply.
  kind: 'breached' | 'due_soon' | 'unanswered' | 'unassigned' | 'quiet'
  headline: string
  body: string
  // The inbox view that lists the conversations.
  view: 'alle' | 'zonder-toewijzing'
}

const conversations = (n: number) => (n === 1 ? 'gesprek' : 'gesprekken')

function whoHolds(a: LiveReport['attention']): string {
  const named = a.holders.map((h) => `${h.name} (${formatNumber(h.count)})`)
  const parts: string[] = []
  if (named.length === 1) parts.push(`Bij ${named[0]}.`)
  else if (named.length > 1) parts.push(`Bij ${named.slice(0, -1).join(', ')} en ${named[named.length - 1]}.`)
  if (a.unassigned > 0) parts.push(`${capitalize(countWord(a.unassigned))} ${a.unassigned === 1 ? 'is' : 'zijn'} nog niet toegewezen.`)
  return parts.join(' ')
}

// What needs a reply now, from the live numbers: conversations past their first-response time,
// those about to be, those waiting without a target, open ones nobody holds, or nothing.
export function attentionSummary(live: LiveReport): AttentionSummary {
  const a = live.attention
  const minutes = Math.max(1, Math.ceil(a.next_due_seconds / 60))
  const holders = whoHolds(a)

  if (a.breached > 0) {
    const soon = a.due_soon > 0 ? `Nog ${countWord(a.due_soon)} ${conversations(a.due_soon)} ${a.due_soon === 1 ? 'volgt' : 'volgen'} binnen ${minutes} min.` : ''
    return {
      kind: 'breached',
      headline: `${capitalize(countWord(a.breached))} ${conversations(a.breached)} ${a.breached === 1 ? 'is' : 'zijn'} de eerste-reactietijd al voorbij`,
      body: [soon, holders].filter(Boolean).join(' '),
      view: 'alle',
    }
  }
  if (a.due_soon > 0) {
    return {
      kind: 'due_soon',
      headline: `${capitalize(countWord(a.due_soon))} ${conversations(a.due_soon)} ${a.due_soon === 1 ? 'mist' : 'missen'} de eerste-reactietijd, de eerste over ${formatDuration(a.next_due_seconds)}`,
      body: holders,
      view: 'alle',
    }
  }
  if (a.unanswered > 0) {
    return {
      kind: 'unanswered',
      headline: `${capitalize(countWord(a.unanswered))} ${a.unanswered === 1 ? 'gesprek wacht' : 'gesprekken wachten'} op een eerste reactie`,
      body: `Het oudste wacht al ${formatDuration(a.oldest_seconds)}. Geen ervan heeft een eerste-reactietijd die binnen ${a.window_minutes} minuten afloopt.`,
      view: 'alle',
    }
  }
  if (live.unassigned > 0) {
    return {
      kind: 'unassigned',
      headline: `${capitalize(countWord(live.unassigned))} open ${conversations(live.unassigned)} ${live.unassigned === 1 ? 'heeft' : 'hebben'} nog geen behandelaar`,
      body: 'Ze hebben al een reactie gehad, maar niemand houdt ze bij.',
      view: 'zonder-toewijzing',
    }
  }
  return {
    kind: 'quiet',
    headline: 'Er wacht nu geen gesprek op een eerste reactie',
    body: 'Alle open gesprekken hebben al een antwoord van ons gehad, of staan uitgesteld.',
    view: 'alle',
  }
}
