import { describe, expect, it } from 'vitest'

import { addRecent, isSearchable, matchesQuery, parseRecent, searchParams, splitSnippet } from './search'

describe('splitSnippet', () => {
  it('turns markers into match segments', () => {
    expect(splitSnippet('Mijn ⟦bestelling⟧ is nog niet ⟦aangekomen⟧.')).toEqual([
      { text: 'Mijn ', match: false },
      { text: 'bestelling', match: true },
      { text: ' is nog niet ', match: false },
      { text: 'aangekomen', match: true },
      { text: '.', match: false },
    ])
  })

  it('collapses whitespace and newlines', () => {
    expect(splitSnippet('  een\n\n  twee\t⟦drie⟧ ')).toEqual([
      { text: 'een twee ', match: false },
      { text: 'drie', match: true },
    ])
  })

  it('never produces markup: angle brackets stay literal text', () => {
    const segments = splitSnippet('<img src=x onerror=alert(1)> ⟦<b>vet</b>⟧')
    expect(segments.map((s) => s.text).join('')).toBe('<img src=x onerror=alert(1)> <b>vet</b>')
    expect(segments.at(-1)).toEqual({ text: '<b>vet</b>', match: true })
  })

  it('copes with unbalanced markers', () => {
    expect(splitSnippet('⟦open zonder einde')).toEqual([{ text: 'open zonder einde', match: true }])
    expect(splitSnippet('einde zonder begin⟧ rest')).toEqual([
      { text: 'einde zonder begin', match: false },
      { text: ' rest', match: false },
    ])
  })

  it('returns nothing for an empty snippet', () => {
    expect(splitSnippet('')).toEqual([])
    expect(splitSnippet('⟦⟧')).toEqual([])
  })
})

describe('searchParams', () => {
  it('encodes the query, including filter syntax and quotes', () => {
    const qs = searchParams('status:open van:@bedrijf.nl "exacte zin"')
    expect(new URLSearchParams(qs).get('q')).toBe('status:open van:@bedrijf.nl "exacte zin"')
  })

  it('adds cursor and limit only when given', () => {
    expect(searchParams('a')).toBe('q=a')
    const qs = new URLSearchParams(searchParams('a', 'abc', 6))
    expect(qs.get('cursor')).toBe('abc')
    expect(qs.get('limit')).toBe('6')
  })
})

describe('isSearchable', () => {
  it('needs two characters after trimming', () => {
    expect(isSearchable('a')).toBe(false)
    expect(isSearchable('  a ')).toBe(false)
    expect(isSearchable('ab')).toBe(true)
  })
})

describe('matchesQuery', () => {
  it('matches every word, ignoring case and accents', () => {
    expect(matchesQuery('Toewijzen aan…', 'toew aan')).toBe(true)
    expect(matchesQuery('Café Zürich', 'cafe zurich')).toBe(true)
    expect(matchesQuery('Alle gesprekken', 'alle inbox')).toBe(false)
  })

  it('matches everything for an empty query', () => {
    expect(matchesQuery('iets', '   ')).toBe(true)
  })
})

describe('recent conversations', () => {
  const a = { id: 'a', number: 1, subject: 'Een' }
  const b = { id: 'b', number: 2, subject: 'Twee' }

  it('puts the newest first and moves a repeat to the front', () => {
    expect(addRecent([a, b], b)).toEqual([b, a])
    expect(addRecent([a], b)).toEqual([b, a])
  })

  it('keeps five', () => {
    const many = Array.from({ length: 5 }, (_, i) => ({ id: String(i), number: i, subject: 's' }))
    const next = addRecent(many, a)
    expect(next).toHaveLength(5)
    expect(next[0]).toEqual(a)
    expect(next.at(-1)?.id).toBe('3')
  })

  it('reads stored JSON defensively', () => {
    expect(parseRecent(null)).toEqual([])
    expect(parseRecent('{not json')).toEqual([])
    expect(parseRecent('{"id":"a"}')).toEqual([])
    expect(parseRecent(JSON.stringify([a, { id: 5 }, 'x']))).toEqual([a])
  })
})
