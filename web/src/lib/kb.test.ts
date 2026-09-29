import { describe, expect, it } from 'vitest'

import { ApiError } from './api'
import { categoryChoices, isValidSlug, kbFieldError, slugify } from './kb'

describe('slugify', () => {
  it.each([
    ['Hoe betaal ik een factuur?', 'hoe-betaal-ik-een-factuur'],
    ['  Café: één ëxtra  ', 'cafe-een-extra'],
    ['Straße & Œuvre', 'strasse-oeuvre'],
    ['Wachtwoord vergeten?!', 'wachtwoord-vergeten'],
    ['a_b/c', 'a-b-c'],
    ['2026: wat verandert er', '2026-wat-verandert-er'],
    ['---', ''],
    ['', ''],
  ])('%j becomes %j', (input, want) => {
    expect(slugify(input)).toBe(want)
  })

  it('cuts at 80 characters without a trailing dash', () => {
    const slug = slugify('ab '.repeat(60))
    expect(slug.length).toBeLessThanOrEqual(80)
    expect(slug.endsWith('-')).toBe(false)
    expect(isValidSlug(slug)).toBe(true)
  })

  it('always produces a valid slug or nothing', () => {
    for (const title of ['Ünïcödé tïtel', 'x'.repeat(200), '!!! a !!!', '日本語 title']) {
      const slug = slugify(title)
      expect(slug === '' || isValidSlug(slug)).toBe(true)
    }
  })
})

describe('isValidSlug', () => {
  it('accepts the server format only', () => {
    expect(isValidSlug('een-slug-2')).toBe(true)
    for (const bad of ['', '-a', 'a-', 'a--b', 'A', 'a b', 'a/b', 'a'.repeat(81)]) expect(isValidSlug(bad)).toBe(false)
  })
})

describe('categoryChoices', () => {
  it('lists children under their parent', () => {
    const cat = (id: string, name: string, parent_id: string | null) => ({
      id, parent_id, name, slug: id, description: '', position: 0, article_count: 0, published_count: 0,
    })
    const out = categoryChoices([cat('c', 'Kind', 'p'), cat('q', 'Andere', null), cat('p', 'Ouder', null)])
    expect(out.map((o) => o.label)).toEqual(['Andere', 'Ouder', 'Ouder / Kind'])
  })
})

describe('kbFieldError', () => {
  it('uses knowledge base wording and ignores other errors', () => {
    const err = new ApiError(422, 'validation_failed', { slug: 'taken', body_html: 'unknown_image' })
    expect(kbFieldError(err, 'slug')).toMatch(/al in gebruik/)
    expect(kbFieldError(err, 'body_html')).toMatch(/afbeelding/)
    expect(kbFieldError(err, 'title')).toBeUndefined()
    expect(kbFieldError(new Error('x'), 'slug')).toBeUndefined()
  })
})
