import { describe, expect, it } from 'vitest'

import { collectMentions, composerModeForKey, filterTemplates, findVariables, mergeAddresses, parseRecipients, pendingSuggestions, type Template, undoWindowMs } from './composer'

describe('parseRecipients', () => {
  it('parses plain and named addresses, lower-casing the address', () => {
    expect(parseRecipients('Jan@Example.com, Kim de Boer <kim@example.org>')).toEqual({
      valid: [
        { name: '', address: 'jan@example.com' },
        { name: 'Kim de Boer', address: 'kim@example.org' },
      ],
      invalid: [],
    })
  })

  it('keeps a comma inside quotes and strips the quotes', () => {
    expect(parseRecipients('"Vries, Jan" <jan@example.com>').valid).toEqual([{ name: 'Vries, Jan', address: 'jan@example.com' }])
  })

  it('splits on semicolons, line breaks and spaces', () => {
    expect(parseRecipients('a@example.com;b@example.com\nc@example.com d@example.com').valid.map((a) => a.address)).toEqual([
      'a@example.com',
      'b@example.com',
      'c@example.com',
      'd@example.com',
    ])
  })

  it('reports what is not an address', () => {
    expect(parseRecipients('jan, jan@, @example.com, ok@example.com, a@b.c')).toEqual({
      valid: [{ name: '', address: 'ok@example.com' }],
      invalid: ['jan', 'jan@', '@example.com', 'a@b.c'],
    })
  })

  it('rejects header injection and stray angle brackets', () => {
    expect(parseRecipients('a@example.com\r\nBcc: x@example.com').valid.map((a) => a.address)).toEqual(['a@example.com'])
    expect(parseRecipients('<a@example.com><b@example.com>').valid).toEqual([])
  })

  it('returns nothing for empty input', () => {
    expect(parseRecipients('  ')).toEqual({ valid: [], invalid: [] })
  })
})

describe('mergeAddresses', () => {
  it('skips addresses that are already present', () => {
    const list = [{ name: 'Jan', address: 'jan@example.com' }]
    expect(mergeAddresses(list, [{ name: '', address: 'jan@example.com' }, { name: '', address: 'kim@example.com' }])).toEqual([
      { name: 'Jan', address: 'jan@example.com' },
      { name: '', address: 'kim@example.com' },
    ])
  })
})

describe('findVariables', () => {
  it('finds known and unknown variables with their positions', () => {
    const text = 'Hoi {{contact.first_name}}, {{ agent.name }} {{secret.key}}'
    const found = findVariables(text)
    expect(found.map((v) => [v.name, v.known])).toEqual([
      ['contact.first_name', true],
      ['agent.name', true],
      ['secret.key', false],
    ])
    const [first] = found
    expect(first && text.slice(first.from, first.to)).toBe('{{contact.first_name}}')
  })

  it('ignores braces that are not variables', () => {
    expect(findVariables('{{ }} {single} {{Contact.name}} {{contact}}')).toEqual([])
  })
})

const tpl = (name: string, shortcode: string): Template => ({
  id: name,
  name,
  shortcode,
  scope: 'personal',
  team_id: null,
  mailbox_id: null,
  subject: '',
  body_html: '',
  editable: true,
  updated_at: '',
})

describe('filterTemplates', () => {
  const list = [tpl('Retourbeleid', 'retour'), tpl('Openingstijden', 'tijden'), tpl('Terugbetaling na retour', '')]

  it('returns everything for an empty query', () => {
    expect(filterTemplates(list, '')).toHaveLength(3)
  })

  it('ranks shortcode, then name prefix, then name substring', () => {
    expect(filterTemplates(list, 'retour').map((t) => t.name)).toEqual(['Retourbeleid', 'Terugbetaling na retour'])
    expect(filterTemplates(list, 'ope').map((t) => t.name)).toEqual(['Openingstijden'])
  })

  it('finds nothing for an unknown query', () => {
    expect(filterTemplates(list, 'zzz')).toEqual([])
  })
})

describe('collectMentions', () => {
  it('collects each mentioned user once, including nested ones', () => {
    const doc = {
      type: 'doc',
      content: [
        { type: 'paragraph', content: [{ type: 'mention', attrs: { id: 'a', label: 'Sanne' } }, { type: 'text', text: ' en ' }, { type: 'mention', attrs: { id: 'b' } }] },
        { type: 'bulletList', content: [{ type: 'listItem', content: [{ type: 'paragraph', content: [{ type: 'mention', attrs: { id: 'a' } }] }] }] },
      ],
    }
    expect(collectMentions(doc)).toEqual(['a', 'b'])
  })

  it('returns nothing for a document without mentions', () => {
    expect(collectMentions({ type: 'doc', content: [{ type: 'paragraph' }] })).toEqual([])
  })
})

describe('undoWindowMs', () => {
  const now = Date.parse('2026-09-28T12:00:00Z')

  it('is the time left until the send is due', () => {
    expect(undoWindowMs('2026-09-28T12:00:05Z', now)).toBe(5000)
  })

  it('is zero without a delay or when the window has passed', () => {
    expect(undoWindowMs(null, now)).toBe(0)
    expect(undoWindowMs('2026-09-28T11:59:59Z', now)).toBe(0)
  })
})

describe('pendingSuggestions', () => {
  const stranger = { name: 'Eve', address: 'eve@evil.example' }
  it('offers suggestions that are not recipients yet, ignoring case', () => {
    expect(pendingSuggestions([stranger], [{ name: '', address: 'jan@example.com' }], [])).toEqual([stranger])
    expect(pendingSuggestions([stranger], [], [{ name: '', address: 'EVE@evil.example' }])).toEqual([])
  })
})

describe('composer shortcuts', () => {
  it('maps r to reply and n to note and ignores every other key', () => {
    expect(composerModeForKey('r')).toBe('reply')
    expect(composerModeForKey('n')).toBe('note')
    for (const key of ['R', 'N', 'e', 'a', 'Enter', 'constructor', '']) expect(composerModeForKey(key)).toBeUndefined()
  })
})
