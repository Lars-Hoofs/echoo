export interface BodySegment {
  kind: 'text' | 'quote'
  text: string
}

const quoted = (line: string) => line.startsWith('>')
const trailerEnd = /(schreef.*|wrote):\s*$/i
const trailerStart = /^(op|on)\s/i

// Index of the last line of an attribution line such as "Op ma 1 jan 2026 om 10:00 schreef X <x@y>:"
// or "On Mon, Jan 1, 2026, X wrote:". Mail clients wrap these over up to two lines.
function trailerLength(lines: string[], i: number): 0 | 1 | 2 {
  const line = lines[i] ?? ''
  if (!trailerStart.test(line)) return 0
  if (trailerEnd.test(line)) return 1
  const next = lines[i + 1]
  return next !== undefined && trailerEnd.test(`${line} ${next}`) ? 2 : 0
}

function nextNonBlank(lines: string[], from: number): number {
  let j = from
  while (j < lines.length && (lines[j] ?? '').trim() === '') j++
  return j
}

// Splits a plain-text mail body into normal text and quoted blocks. A quoted block is a run of
// lines starting with ">" (blank lines inside the run stay in it), including a directly preceding
// attribution line. An attribution line without quoted text after it is left as normal text.
export function splitQuotes(body: string): BodySegment[] {
  const lines = body.replace(/\r\n?/g, '\n').split('\n')
  const segments: BodySegment[] = []
  let text: string[] = []

  const flush = () => {
    const joined = text.join('\n').replace(/^\n+|\s+$/g, '')
    if (joined) segments.push({ kind: 'text', text: joined })
    text = []
  }

  let i = 0
  while (i < lines.length) {
    const line = lines[i] ?? ''
    const tl = trailerLength(lines, i)
    const startsQuote = quoted(line) || (tl > 0 && quoted(lines[nextNonBlank(lines, i + tl)] ?? ''))
    if (!startsQuote) {
      text.push(line)
      i++
      continue
    }
    flush()
    const block: string[] = []
    if (tl > 0) {
      block.push(...lines.slice(i, i + tl))
      i += tl
    }
    while (i < lines.length) {
      const l = lines[i] ?? ''
      if (quoted(l)) {
        block.push(l)
        i++
      } else if (l.trim() === '' && quoted(lines[nextNonBlank(lines, i)] ?? '')) {
        block.push(l)
        i++
      } else break
    }
    segments.push({ kind: 'quote', text: block.join('\n') })
  }
  flush()
  return segments
}
