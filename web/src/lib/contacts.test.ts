import { describe, expect, it } from 'vitest'

import {
  type AttributeDef,
  buildImportOptions,
  cleanFilter,
  contactQueryString,
  CsvError,
  detectDelimiter,
  emptyFilter,
  formatAttribute,
  mappingProblem,
  opsFor,
  parseCsv,
  previewCsv,
  slugFromLabel,
  suggestMapping,
} from './contacts'

const def = (key: string, label: string): AttributeDef => ({ id: key, entity: 'contact', key, label, type: 'text', options: [], created_at: '' })

describe('detectDelimiter', () => {
  it('picks semicolons and commas from the first line', () => {
    expect(detectDelimiter('naam;email\na;b')).toBe(';')
    expect(detectDelimiter('naam,email\na,b')).toBe(',')
    expect(detectDelimiter('naam')).toBe(',')
  })
  it('ignores delimiters inside quotes and later lines', () => {
    expect(detectDelimiter('"a,b,c";d\n1,2,3')).toBe(';')
  })
})

describe('parseCsv', () => {
  it('strips a BOM and reads quoted fields with delimiters, quotes and line breaks', () => {
    const { records } = parseCsv('﻿naam;notitie\r\n"Jansen; Piet";"zei ""hoi""\nen ging"\r\n')
    expect(records).toEqual([
      ['naam', 'notitie'],
      ['Jansen; Piet', 'zei "hoi"\nen ging'],
    ])
  })
  it('skips blank lines', () => {
    expect(parseCsv('a,b\n\n1,2\n , \n3,4').records).toEqual([
      ['a', 'b'],
      ['1', '2'],
      ['3', '4'],
    ])
  })
  it('rejects an unterminated quote', () => {
    expect(() => parseCsv('a,b\n"1,2')).toThrow(CsvError)
  })
})

describe('previewCsv', () => {
  it('returns the header, five preview rows and the row count', () => {
    const rows = Array.from({ length: 8 }, (_, i) => `n${i};n${i}@example.com`).join('\n')
    const p = previewCsv(`naam;e-mail\n${rows}`)
    expect(p.delimiter).toBe(';')
    expect(p.header).toEqual(['naam', 'e-mail'])
    expect(p.preview).toHaveLength(5)
    expect(p.rowCount).toBe(8)
  })
  it('treats a header without data as empty', () => {
    expect(() => previewCsv('naam,email\n')).toThrow(CsvError)
    expect(() => previewCsv('')).toThrow(CsvError)
  })
})

describe('suggestMapping', () => {
  it('maps Dutch and English headers and custom attributes, once each', () => {
    const t = suggestMapping(['Naam', 'E-mailadres', 'Telefoon', 'Bedrijf', 'Klantnummer', 'Email', 'Overig'], [def('klantnummer', 'Klantnummer')])
    expect(t).toEqual(['name', 'email', 'phone', 'organization', 'attribute:klantnummer', '', ''])
  })
})

describe('import options', () => {
  it('skips unmapped columns', () => {
    expect(buildImportOptions(';', 'skip', ['email', '', 'name'])).toEqual({
      delimiter: ';',
      dedupe: 'skip',
      columns: [
        { index: 0, target: 'email' },
        { index: 2, target: 'name' },
      ],
    })
  })
  it('requires an email column and unique targets', () => {
    expect(mappingProblem(['name', ''])).toMatch(/E-mail/)
    expect(mappingProblem(['email', 'email'])).toMatch(/één kolom/)
    expect(mappingProblem(['email', 'name'])).toBeNull()
  })
})

describe('filters', () => {
  it('drops incomplete conditions and builds the query string', () => {
    const f = {
      ...emptyFilter,
      conditions: [
        { field: 'name' as const, op: 'contains', value: 'jan' },
        { field: 'email' as const, op: 'contains', value: '' },
        { field: 'attribute' as const, op: 'is_set', key: '' },
      ],
    }
    expect(cleanFilter(f).conditions).toHaveLength(1)
    const qs = new URLSearchParams(contactQueryString({ q: ' x ', sort: 'name', dir: 'asc', segmentId: '', filter: f }))
    expect(qs.get('q')).toBe('x')
    expect(JSON.parse(qs.get('filter') ?? '{}')).toEqual({ match: 'all', conditions: [{ field: 'name', op: 'contains', value: 'jan' }] })
  })
  it('sends a segment instead of an ad-hoc filter', () => {
    const f = { ...emptyFilter, conditions: [{ field: 'name' as const, op: 'contains', value: 'jan' }] }
    const qs = new URLSearchParams(contactQueryString({ q: '', sort: 'last_activity', dir: 'desc', segmentId: 's1', filter: f }))
    expect(qs.get('segment_id')).toBe('s1')
    expect(qs.has('filter')).toBe(false)
  })
  it('offers operators per attribute type', () => {
    expect(opsFor('attribute', 'number')).toContain('gt')
    expect(opsFor('attribute', 'boolean')).toContain('is_true')
    expect(opsFor('has_open_conversation')).toEqual(['is_true', 'is_false'])
  })
})

describe('slugFromLabel', () => {
  it('makes a valid key', () => {
    expect(slugFromLabel('Klantnummer')).toBe('klantnummer')
    expect(slugFromLabel('Café  Nr. 2')).toBe('cafe_nr_2')
    expect(slugFromLabel('2e adres')).toBe('veld_2e_adres')
    expect(slugFromLabel('***')).toBe('')
  })
})

describe('formatAttribute', () => {
  it('writes dates as a Dutch calendar day', () => {
    expect(formatAttribute({ ...def('sinds', 'Klant sinds'), type: 'date' }, '2024-03-15')).toBe('15 maart 2024')
  })
  it('shows booleans as Ja and Nee and leaves other values alone', () => {
    expect(formatAttribute({ ...def('vip', 'VIP'), type: 'boolean' }, true)).toBe('Ja')
    expect(formatAttribute(def('nr', 'Nummer'), 'K-1')).toBe('K-1')
    expect(formatAttribute(def('nr', 'Nummer'), undefined)).toBe('—')
  })
})
