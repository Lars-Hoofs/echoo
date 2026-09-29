import { describe, expect, it } from 'vitest'

import { splitQuotes } from './quotes'

describe('splitQuotes', () => {
  it('keeps a body without quotes as one text segment', () => {
    expect(splitQuotes('Hallo,\n\nDank voor je bericht.\n')).toEqual([{ kind: 'text', text: 'Hallo,\n\nDank voor je bericht.' }])
  })

  it('collapses consecutive > lines, including blank lines between them', () => {
    expect(splitQuotes('Akkoord.\n\n> eerste\n>\n> tweede\n\n> derde\n\nGroet')).toEqual([
      { kind: 'text', text: 'Akkoord.' },
      { kind: 'quote', text: '> eerste\n>\n> tweede\n\n> derde' },
      { kind: 'text', text: 'Groet' },
    ])
  })

  it('includes a Dutch attribution line in the quote', () => {
    expect(splitQuotes('Top.\n\nOp ma 1 jan 2026 om 10:00 schreef Jan <jan@example.com>:\n> Hoi\n> Kan dit?')).toEqual([
      { kind: 'text', text: 'Top.' },
      { kind: 'quote', text: 'Op ma 1 jan 2026 om 10:00 schreef Jan <jan@example.com>:\n> Hoi\n> Kan dit?' },
    ])
  })

  it('includes an English attribution line, also when wrapped over two lines', () => {
    expect(splitQuotes('Thanks\n\nOn Mon, Jan 1, 2026 at 10:00 AM Jan Jansen <jan@example.com>\nwrote:\n> Hi')).toEqual([
      { kind: 'text', text: 'Thanks' },
      { kind: 'quote', text: 'On Mon, Jan 1, 2026 at 10:00 AM Jan Jansen <jan@example.com>\nwrote:\n> Hi' },
    ])
  })

  it('leaves an attribution-like line alone when no quote follows', () => {
    const body = 'Op maandag schreef ik je al:\nzie bijlage.'
    expect(splitQuotes(body)).toEqual([{ kind: 'text', text: body }])
  })

  it('handles CRLF and a body that is only a quote', () => {
    expect(splitQuotes('> a\r\n> b')).toEqual([{ kind: 'quote', text: '> a\n> b' }])
  })

  it('returns nothing for an empty body', () => {
    expect(splitQuotes('')).toEqual([])
  })
})
