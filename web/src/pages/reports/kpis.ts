import { durationParts, formatPointsDelta, type Overview, slaPercent } from '../../lib/reports'
import { deltaOf, type Kpi, numParts } from './shared'

// The four figures every report page opens with. The delta is against the period of the same
// length directly before; a duration that fell or a share that rose is not colored, since neither
// direction is good or bad for every workspace.
export function overviewKpis(o: Overview): Kpi[] {
  const { current: c, previous: p } = o
  const sla = slaPercent(c)
  return [
    { label: 'Nieuwe gesprekken', parts: numParts(c.new_conversations), delta: deltaOf(c.new_conversations, p.new_conversations) },
    {
      label: 'Eerste reactie (mediaan)',
      parts: durationParts(c.first_response.median_seconds),
      delta: deltaOf(c.first_response.median_seconds, p.first_response.median_seconds),
    },
    {
      label: 'Oplostijd (mediaan)',
      parts: durationParts(c.resolution.median_seconds),
      delta: deltaOf(c.resolution.median_seconds, p.resolution.median_seconds),
    },
    sla === null
      ? { label: 'Binnen SLA', parts: [{ value: '—' }], note: 'Geen SLA-doel ingesteld' }
      : {
          label: 'Binnen SLA',
          parts: numParts(sla, 1, '%'),
          delta: formatPointsDelta(sla, slaPercent(p)) === 'n.v.t.' ? undefined : formatPointsDelta(sla, slaPercent(p)),
        },
  ]
}
