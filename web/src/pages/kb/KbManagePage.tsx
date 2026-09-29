import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Link } from '@tanstack/react-router'
import { FolderTree, Pencil, Plus, Trash2 } from 'lucide-react'
import { type SubmitEvent, useState } from 'react'

import { ActionMenu } from '../../components/ActionMenu'
import { Dialog, DialogFooter } from '../../components/Dialog'
import { useToast } from '../../components/Toast'
import { Button, Card, EmptyState, ErrorNotice, Field, Input, PageHeader, Select, Skeleton, Table, TBody, Td, Textarea, Th, THead, Tr } from '../../components/ui'
import { api, ApiError } from '../../lib/api'
import { errorMessage } from '../../lib/errors'
import { type KbCategory, kbCategoriesQuery, kbFieldError, type KbPortal, kbPortalQuery, slugify } from '../../lib/kb'

type Editing = { kind: 'new' } | { kind: 'edit' | 'delete'; category: KbCategory }

export function KbManagePage() {
  return (
    <div className="relative h-full overflow-y-auto px-4 py-8 md:px-8">
      <div className="mx-auto flex w-full max-w-4xl flex-col gap-6">
        <PageHeader
          title="Beheer kennisbank"
          breadcrumb={
            <Link to="/kennisbank" className="hover:text-ink">
              Terug naar de kennisbank
            </Link>
          }
          description="Stel de openbare site in en beheer de categorieën. Alleen beheerders kunnen dit."
        />
        <PortalCard />
        <CategoriesCard />
      </div>
    </div>
  )
}

function PortalCard() {
  const portal = useQuery(kbPortalQuery)
  if (portal.isPending) return <Skeleton className="h-64" />
  if (portal.isError) return <ErrorNotice>{errorMessage(portal.error)}</ErrorNotice>
  return <PortalForm portal={portal.data} />
}

function PortalForm({ portal }: { portal: KbPortal }) {
  const qc = useQueryClient()
  const toast = useToast()
  const [form, setForm] = useState({
    name: portal.name,
    title: portal.title,
    intro: portal.intro,
    logo_text: portal.logo_text,
    custom_domain: portal.custom_domain,
  })
  const save = useMutation({
    mutationFn: () => api<KbPortal>('PUT', '/kb/portal', form),
    onSuccess: (p) => {
      qc.setQueryData(kbPortalQuery.queryKey, p)
      toast('Opgeslagen')
    },
  })
  const set = (key: keyof typeof form) => (e: { target: { value: string } }) => setForm((f) => ({ ...f, [key]: e.target.value }))

  function submit(e: SubmitEvent) {
    e.preventDefault()
    save.mutate()
  }

  return (
    <form onSubmit={submit} noValidate>
      <Card
        title="Openbare site"
        description={
          <>
            Te bereiken op{' '}
            <a href={portal.public_url} target="_blank" rel="noreferrer" className="text-ink hover:underline">
              {portal.public_url}
            </a>
          </>
        }
        footer={
          <Button variant="primary" type="submit" busy={save.isPending}>
            Opslaan
          </Button>
        }
      >
        <div className="flex flex-col gap-4">
          {save.isError && !(save.error instanceof ApiError && save.error.status === 422) && <ErrorNotice>{errorMessage(save.error)}</ErrorNotice>}
          <Field label="Naam" error={kbFieldError(save.error, 'name')}>
            {(props) => <Input {...props} value={form.name} maxLength={100} onChange={set('name')} />}
          </Field>
          <Field label="Kop op de startpagina" error={kbFieldError(save.error, 'title', 'title_portal')}>
            {(props) => <Input {...props} value={form.title} maxLength={150} onChange={set('title')} />}
          </Field>
          <Field label="Introductie" help="Een korte zin onder de kop. Optioneel." error={kbFieldError(save.error, 'intro')}>
            {(props) => <Textarea {...props} value={form.intro} maxLength={500} rows={2} onChange={set('intro')} />}
          </Field>
          <Field label="Tekst in de balk" help="Staat linksboven op elke pagina. Leeg: de naam." error={kbFieldError(save.error, 'logo_text')}>
            {(props) => <Input {...props} value={form.logo_text} maxLength={40} onChange={set('logo_text')} />}
          </Field>
          <Field
            label="Eigen domein"
            help="Alleen invullen als een reverse proxy de kennisbank onder een eigen domein toont, met dezelfde paden (/hulp/…). Het bepaalt de adressen in de sitemap en in links; leeg is het adres van Echoo."
            error={kbFieldError(save.error, 'custom_domain')}
          >
            {(props) => <Input {...props} value={form.custom_domain} placeholder="hulp.voorbeeld.nl" spellCheck={false} onChange={set('custom_domain')} />}
          </Field>
        </div>
      </Card>
    </form>
  )
}

function CategoriesCard() {
  const categories = useQuery(kbCategoriesQuery)
  const [editing, setEditing] = useState<Editing | null>(null)
  const list = categories.data ?? []
  const byId = new Map(list.map((c) => [c.id, c]))

  return (
    <>
      <Card
        title="Categorieën"
        description="Artikelen staan in een categorie. Een categorie kan één niveau subcategorieën hebben."
        actions={
          <Button variant="primary" onClick={() => setEditing({ kind: 'new' })}>
            <Plus size={16} aria-hidden />
            Categorie toevoegen
          </Button>
        }
        flush
      >
        {categories.isPending ? (
          <Skeleton className="h-32" />
        ) : categories.isError ? (
          <div className="p-4">
            <ErrorNotice>{errorMessage(categories.error)}</ErrorNotice>
          </div>
        ) : list.length === 0 ? (
          <EmptyState icon={<FolderTree size={20} />} title="Nog geen categorieën" description="Maak een categorie aan voordat je artikelen publiceert." />
        ) : (
          <Table>
            <THead>
              <Th>Naam</Th>
              <Th className="hidden sm:table-cell">Adres</Th>
              <Th numeric>Artikelen</Th>
              <Th className="w-px">
                <span className="sr-only">Acties</span>
              </Th>
            </THead>
            <TBody>
              {list.map((c) => (
                <Tr key={c.id}>
                  <Td>
                    <span className={c.parent_id ? 'pl-4' : ''}>
                      {c.parent_id && <span aria-hidden>↳ </span>}
                      {c.name}
                    </span>
                    {c.parent_id && <span className="sr-only"> in {byId.get(c.parent_id)?.name}</span>}
                  </Td>
                  <Td className="hidden text-muted sm:table-cell">/hulp/c/{c.slug}</Td>
                  <Td numeric>
                    {c.published_count} / {c.article_count}
                  </Td>
                  <Td>
                    <div className="flex justify-end">
                      <ActionMenu
                        label={`Acties voor ${c.name}`}
                        items={[
                          { label: 'Bewerken', icon: <Pencil size={16} />, onSelect: () => setEditing({ kind: 'edit', category: c }) },
                          { label: 'Verwijderen', icon: <Trash2 size={16} />, danger: true, separated: true, onSelect: () => setEditing({ kind: 'delete', category: c }) },
                        ]}
                      />
                    </div>
                  </Td>
                </Tr>
              ))}
            </TBody>
          </Table>
        )}
        {list.length > 0 && <p className="t-label border-t border-line px-4 py-4 sm:px-6">Artikelen: gepubliceerd / totaal.</p>}
      </Card>
      {(editing?.kind === 'new' || editing?.kind === 'edit') && (
        <CategoryDialog category={editing.kind === 'edit' ? editing.category : null} all={list} onClose={() => setEditing(null)} />
      )}
      {editing?.kind === 'delete' && <DeleteCategoryDialog category={editing.category} onClose={() => setEditing(null)} />}
    </>
  )
}

function CategoryDialog({ category, all, onClose }: { category: KbCategory | null; all: KbCategory[]; onClose: () => void }) {
  const qc = useQueryClient()
  const [name, setName] = useState(category?.name ?? '')
  const [slug, setSlug] = useState(category?.slug ?? '')
  const [slugTouched, setSlugTouched] = useState(category !== null)
  const [description, setDescription] = useState(category?.description ?? '')
  const [parentId, setParentId] = useState(category?.parent_id ?? '')
  const [position, setPosition] = useState(String(category?.position ?? 0))
  // A category with subcategories cannot move under another one: the tree has one level.
  const hasChildren = category ? all.some((c) => c.parent_id === category.id) : false
  const parents = all.filter((c) => c.parent_id === null && c.id !== category?.id)
  const save = useMutation({
    mutationFn: () => {
      const body = { name, slug: slug.trim(), description, parent_id: parentId || null, position: Number(position) }
      return category ? api('PATCH', `/kb/categories/${category.id}`, body) : api('POST', '/kb/categories', body)
    },
    onSuccess: async () => {
      await qc.invalidateQueries({ queryKey: ['kb'] })
      onClose()
    },
  })

  function submit(e: SubmitEvent) {
    e.preventDefault()
    save.mutate()
  }

  return (
    <Dialog open onOpenChange={(o) => !o && onClose()} title={category ? 'Categorie bewerken' : 'Categorie toevoegen'}>
      <form onSubmit={submit} className="flex flex-col gap-4" noValidate>
        {save.isError && !(save.error instanceof ApiError && save.error.status === 422) && <ErrorNotice>{errorMessage(save.error)}</ErrorNotice>}
        <Field label="Naam" error={kbFieldError(save.error, 'name')}>
          {(props) => (
            <Input
              {...props}
              value={name}
              maxLength={100}
              autoFocus
              onChange={(e) => {
                setName(e.target.value)
                if (!slugTouched) setSlug(slugify(e.target.value))
              }}
            />
          )}
        </Field>
        <Field label="Adres" help="Het stuk na /hulp/c/." error={kbFieldError(save.error, 'slug')}>
          {(props) => (
            <Input
              {...props}
              value={slug}
              maxLength={80}
              spellCheck={false}
              onChange={(e) => {
                setSlug(e.target.value)
                setSlugTouched(true)
              }}
            />
          )}
        </Field>
        <Field label="Omschrijving" help="Optioneel, staat op de openbare site." error={kbFieldError(save.error, 'description')}>
          {(props) => <Textarea {...props} value={description} maxLength={300} rows={2} onChange={(e) => setDescription(e.target.value)} />}
        </Field>
        <Field
          label="Hoort bij"
          help={hasChildren ? 'Deze categorie heeft zelf subcategorieën en blijft daarom een hoofdcategorie.' : undefined}
          error={kbFieldError(save.error, 'parent_id')}
        >
          {(props) => (
            <Select {...props} value={parentId} disabled={hasChildren} onChange={(e) => setParentId(e.target.value)}>
              <option value="">Geen (hoofdcategorie)</option>
              {parents.map((c) => (
                <option key={c.id} value={c.id}>
                  {c.name}
                </option>
              ))}
            </Select>
          )}
        </Field>
        <Field label="Volgorde" help="Lagere getallen staan eerst." error={kbFieldError(save.error, 'position')}>
          {(props) => <Input {...props} type="number" min={0} max={10000} value={position} onChange={(e) => setPosition(e.target.value)} />}
        </Field>
        <DialogFooter>
          <Button onClick={onClose}>Annuleren</Button>
          <Button variant="primary" type="submit" busy={save.isPending} disabled={name.trim() === ''}>
            Opslaan
          </Button>
        </DialogFooter>
      </form>
    </Dialog>
  )
}

function DeleteCategoryDialog({ category, onClose }: { category: KbCategory; onClose: () => void }) {
  const qc = useQueryClient()
  const remove = useMutation({
    mutationFn: () => api('DELETE', `/kb/categories/${category.id}`),
    onSuccess: async () => {
      await qc.invalidateQueries({ queryKey: ['kb'] })
      onClose()
    },
  })
  const notEmpty = remove.error instanceof ApiError && remove.error.code === 'category_not_empty'
  return (
    <Dialog open onOpenChange={(o) => !o && onClose()} title="Categorie verwijderen" description={`“${category.name}” wordt verwijderd.`}>
      {remove.isError && (
        <ErrorNotice>{notEmpty ? 'Deze categorie heeft nog artikelen of subcategorieën. Verplaats of verwijder die eerst.' : errorMessage(remove.error)}</ErrorNotice>
      )}
      <DialogFooter>
        <Button onClick={onClose}>Annuleren</Button>
        <Button variant="danger" busy={remove.isPending} onClick={() => remove.mutate()}>
          Verwijderen
        </Button>
      </DialogFooter>
    </Dialog>
  )
}
