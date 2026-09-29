import { queryOptions } from '@tanstack/react-query'

import { api, ApiError } from './api'

export type ArticleStatus = 'draft' | 'published' | 'archived'

export interface KbArticle {
  id: string
  title: string
  slug: string
  excerpt: string
  status: ArticleStatus
  category_id: string | null
  category_name: string | null
  version: number
  view_count: number
  helpful_yes: number
  helpful_no: number
  updated_at: string
  updated_by_name: string | null
  published_at: string | null
  public_url: string | null
}

export interface KbArticleDetail extends KbArticle {
  body_html: string
  created_at: string
}

export interface KbCategory {
  id: string
  parent_id: string | null
  name: string
  slug: string
  description: string
  position: number
  article_count: number
  published_count: number
}

export interface KbPortal {
  name: string
  title: string
  intro: string
  logo_text: string
  custom_domain: string
  public_url: string
  updated_at: string
}

export interface KbRevision {
  id: string
  title: string
  created_at: string
  edited_by_name: string | null
}

export const statusLabel: Record<ArticleStatus, string> = {
  draft: 'Concept',
  published: 'Gepubliceerd',
  archived: 'Gearchiveerd',
}

const folds: Record<string, string> = { ß: 'ss', æ: 'ae', œ: 'oe', ø: 'o' }

const MAX_SLUG = 80

// Same rules as the server: lowercase, accents folded, everything else collapsed into single
// dashes. The server checks the result again; this only proposes an address while typing.
export function slugify(title: string): string {
  const slug = title
    .toLowerCase()
    .replace(/[ßæœø]/g, (c) => folds[c] ?? c)
    .normalize('NFD')
    .replace(/\p{M}/gu, '')
    .replace(/[^a-z0-9]+/g, '-')
    .replace(/^-+|-+$/g, '')
  return slug.length > MAX_SLUG ? slug.slice(0, MAX_SLUG).replace(/-+$/, '') : slug
}

export const isValidSlug = (slug: string) => slug.length <= MAX_SLUG && /^[a-z0-9]+(-[a-z0-9]+)*$/.test(slug)

export const kbPortalQuery = queryOptions({
  queryKey: ['kb', 'portal'],
  queryFn: () => api<KbPortal>('GET', '/kb/portal'),
  staleTime: 60_000,
})

export const kbCategoriesQuery = queryOptions({
  queryKey: ['kb', 'categories'],
  queryFn: async () => (await api<{ categories: KbCategory[] }>('GET', '/kb/categories')).categories,
  staleTime: 30_000,
})

export interface ArticleFilter {
  status: ArticleStatus | ''
  categoryId: string
  q: string
}

export const kbArticlesQuery = (f: ArticleFilter) => {
  const params = new URLSearchParams()
  if (f.status) params.set('status', f.status)
  if (f.categoryId) params.set('category_id', f.categoryId)
  if (f.q) params.set('q', f.q)
  const qs = params.toString()
  return queryOptions({
    queryKey: ['kb', 'articles', qs],
    queryFn: async () => (await api<{ articles: KbArticle[] }>('GET', `/kb/articles${qs ? `?${qs}` : ''}`)).articles,
  })
}

export const kbArticleQuery = (id: string) =>
  queryOptions({
    queryKey: ['kb', 'article', id],
    queryFn: () => api<KbArticleDetail>('GET', `/kb/articles/${encodeURIComponent(id)}`),
  })

export const kbRevisionsQuery = (id: string) =>
  queryOptions({
    queryKey: ['kb', 'revisions', id],
    queryFn: async () => (await api<{ revisions: KbRevision[] }>('GET', `/kb/articles/${encodeURIComponent(id)}/revisions`)).revisions,
  })

// Categories in tree order for a picker: each top-level category followed by its children.
export function categoryChoices(categories: KbCategory[]): { id: string; label: string }[] {
  const top = categories.filter((c) => c.parent_id === null)
  return top.flatMap((parent) => [
    { id: parent.id, label: parent.name },
    ...categories.filter((c) => c.parent_id === parent.id).map((c) => ({ id: c.id, label: `${parent.name} / ${c.name}` })),
  ])
}

const fieldMessages: Record<string, Record<string, string>> = {
  title: { invalid: 'Vul een titel in (maximaal 200 tekens).' },
  slug: { invalid: 'Gebruik kleine letters, cijfers en streepjes (maximaal 80 tekens).', taken: 'Dit adres is al in gebruik door een ander artikel.' },
  category_id: {
    invalid: 'Kies een categorie.',
    unknown: 'Deze categorie bestaat niet meer.',
    required_when_published: 'Een gepubliceerd artikel heeft een categorie nodig.',
  },
  excerpt: { invalid: 'Gebruik maximaal 300 tekens, zonder regeleinden.' },
  body_html: {
    too_large: 'Dit artikel is te groot.',
    unknown_image: 'Een afbeelding in dit artikel bestaat niet meer. Verwijder hem en voeg hem opnieuw toe.',
    required_when_published: 'Schrijf eerst de tekst voordat je publiceert.',
  },
  status: { invalid: 'Kies een geldige status.' },
  file: { required: 'Kies een afbeelding.', empty: 'Dit bestand is leeg.', not_an_image: 'Gebruik een PNG-, JPEG-, GIF- of WebP-afbeelding.' },
  name: { invalid: 'Vul een naam in (maximaal 100 tekens).' },
  title_portal: { invalid: 'Vul een titel in (maximaal 150 tekens).' },
  intro: { invalid: 'Gebruik maximaal 500 tekens, zonder regeleinden.' },
  logo_text: { invalid: 'Gebruik maximaal 40 tekens.' },
  custom_domain: { invalid: 'Vul alleen een domeinnaam in, zoals hulp.voorbeeld.nl, zonder https:// of pad.' },
  description: { invalid: 'Gebruik maximaal 300 tekens, zonder regeleinden.' },
  parent_id: { invalid: 'Kies een andere hoofdcategorie.', unknown: 'Deze hoofdcategorie bestaat niet meer.', too_deep: 'Categorieën kunnen maar één niveau diep.' },
  position: { invalid: 'Vul een getal van 0 tot 10.000 in.' },
}

// The knowledge base reuses field names that mean something else elsewhere (name, description,
// body_html), so it keeps its own messages instead of extending the shared table.
export function kbFieldError(err: unknown, field: string, table = field): string | undefined {
  if (!(err instanceof ApiError)) return undefined
  const code = err.fields[field]
  if (!code) return undefined
  return fieldMessages[table]?.[code] ?? 'Ongeldige waarde.'
}

export const isVersionConflict = (err: unknown) => err instanceof ApiError && err.status === 409 && err.code === 'version_conflict'
