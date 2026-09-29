import type { Editor } from '@tiptap/core'
import { useEditorState } from '@tiptap/react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Link, useNavigate, useParams } from '@tanstack/react-router'
import { Archive, Code, Eye, EyeOff, ExternalLink, FileCode, Heading2, Heading3, History, ImagePlus, Trash2 } from 'lucide-react'
import { useCallback, useEffect, useRef, useState } from 'react'

import { ActionMenu } from '../../components/ActionMenu'
import { Dialog, DialogFooter } from '../../components/Dialog'
import { ArticleImage } from '../../components/editor/articleImage'
import { ReadOnlyHtml } from '../../components/editor/ReadOnlyHtml'
import { type EditorValue, RichEditor } from '../../components/editor/RichEditor'
import { useToast } from '../../components/Toast'
import { Badge, Button, Card, ErrorNotice, Field, IconButton, Input, PageHeader, Select, Skeleton, Textarea } from '../../components/ui'
import { api, ApiError, apiUpload } from '../../lib/api'
import { errorMessage } from '../../lib/errors'
import { formatDateTime } from '../../lib/format'
import {
  type ArticleStatus,
  categoryChoices,
  isVersionConflict,
  type KbArticleDetail,
  kbArticleQuery,
  kbCategoriesQuery,
  kbFieldError,
  kbRevisionsQuery,
  slugify,
  statusLabel,
} from '../../lib/kb'
import { hasPermission, meQuery } from '../../lib/session'

const articleExtensions = [ArticleImage]

export function KbNewPage() {
  const me = useQuery(meQuery)
  if (!me.data) return <Skeleton className="m-8 h-64" />
  return <ArticleEditor article={null} canEdit={hasPermission(me.data, 'kb.write')} />
}

export function KbArticlePage() {
  const { id } = useParams({ from: '/auth/ready/kennisbank/$id' })
  const me = useQuery(meQuery)
  const article = useQuery({ ...kbArticleQuery(id), retry: false })
  if (article.isError) {
    return (
      <div className="relative h-full overflow-y-auto px-4 py-8 md:px-8">
        <div className="mx-auto flex max-w-3xl flex-col gap-4">
          <Link to="/kennisbank" className="t-label w-fit hover:text-ink">
            Terug naar de kennisbank
          </Link>
          <ErrorNotice>{article.error instanceof ApiError && article.error.status === 404 ? 'Dit artikel bestaat niet (meer).' : errorMessage(article.error)}</ErrorNotice>
        </div>
      </div>
    )
  }
  if (!article.data || !me.data) return <Skeleton className="m-8 h-64" />
  return <ArticleEditor article={article.data} canEdit={hasPermission(me.data, 'kb.write')} />
}

function ArticleEditor({ article, canEdit }: { article: KbArticleDetail | null; canEdit: boolean }) {
  const qc = useQueryClient()
  const navigate = useNavigate()
  const toast = useToast()
  const categories = useQuery(kbCategoriesQuery)
  const [title, setTitle] = useState(article?.title ?? '')
  const [slug, setSlug] = useState(article?.slug ?? '')
  const [slugTouched, setSlugTouched] = useState(article !== null)
  const [categoryId, setCategoryId] = useState(article?.category_id ?? '')
  const [excerpt, setExcerpt] = useState(article?.excerpt ?? '')
  const [body, setBody] = useState<EditorValue>({ html: article?.body_html ?? '', empty: !article?.body_html })
  const [dirty, setDirty] = useState(false)
  const [error, setError] = useState<unknown>(null)
  const [conflict, setConflict] = useState(false)
  const [editor, setEditor] = useState<Editor | null>(null)
  const [editorKey, setEditorKey] = useState(0)
  const [preview, setPreview] = useState(false)
  const [dialog, setDialog] = useState<'delete' | 'revisions' | null>(null)
  const fileInput = useRef<HTMLInputElement>(null)

  useEffect(() => {
    if (!dirty) return
    const warn = (e: BeforeUnloadEvent) => e.preventDefault()
    window.addEventListener('beforeunload', warn)
    return () => window.removeEventListener('beforeunload', warn)
  }, [dirty])

  const apply = useCallback(
    (d: KbArticleDetail) => {
      qc.setQueryData(kbArticleQuery(d.id).queryKey, d)
      void qc.invalidateQueries({ queryKey: ['kb', 'articles'] })
      void qc.invalidateQueries({ queryKey: ['kb', 'categories'] })
      void qc.invalidateQueries({ queryKey: ['kb', 'revisions', d.id] })
    },
    [qc],
  )

  // Replaces everything in the form by what the server has, for a restore or a conflict.
  const reload = useCallback(
    (d: KbArticleDetail) => {
      apply(d)
      setTitle(d.title)
      setSlug(d.slug)
      setCategoryId(d.category_id ?? '')
      setExcerpt(d.excerpt)
      setBody({ html: d.body_html, empty: d.body_html === '' })
      setEditorKey((k) => k + 1)
      setDirty(false)
      setError(null)
      setConflict(false)
    },
    [apply],
  )

  const save = useMutation({
    mutationFn: () => {
      const payload = { title, slug: slug.trim(), category_id: categoryId || null, body_html: body.empty ? '' : body.html, excerpt }
      return article
        ? api<KbArticleDetail>('PATCH', `/kb/articles/${article.id}`, { ...payload, version: article.version })
        : api<KbArticleDetail>('POST', '/kb/articles', payload)
    },
    onMutate: () => setError(null),
    onError: (err) => {
      if (isVersionConflict(err)) setConflict(true)
      else setError(err)
    },
  })

  async function saveNow(): Promise<KbArticleDetail | null> {
    try {
      const saved = await save.mutateAsync()
      apply(saved)
      setDirty(false)
      if (!article) await navigate({ to: '/kennisbank/$id', params: { id: saved.id }, replace: true })
      else toast('Opgeslagen')
      return saved
    } catch {
      return null
    }
  }

  const setStatus = useMutation({
    mutationFn: async (status: ArticleStatus) => {
      if (!article) return null
      const saved = dirty ? await saveNow() : article
      if (!saved) return null
      return api<KbArticleDetail>('POST', `/kb/articles/${saved.id}/status`, { status })
    },
    onSuccess: (d, status) => {
      if (!d) return
      apply(d)
      toast(status === 'published' ? 'Gepubliceerd' : status === 'archived' ? 'Gearchiveerd' : 'Teruggezet naar concept')
    },
    onError: (err) => setError(err),
  })

  const remove = useMutation({
    mutationFn: () => api('DELETE', `/kb/articles/${article?.id ?? ''}`),
    onSuccess: async () => {
      setDirty(false)
      await qc.invalidateQueries({ queryKey: ['kb'] })
      toast('Artikel verwijderd')
      await navigate({ to: '/kennisbank' })
    },
    onError: (err) => {
      setDialog(null)
      setError(err)
    },
  })

  async function upload(file: File) {
    if (!editor) return
    try {
      const img = await apiUpload<{ url: string; filename: string }>('/kb/images', file, () => undefined)
      editor
        .chain()
        .focus()
        .insertContent({ type: 'articleImage', attrs: { src: img.url, alt: img.filename.replace(/\.[^.]+$/, '') } })
        .run()
      setDirty(true)
    } catch (err) {
      toast(kbFieldError(err, 'file') ?? errorMessage(err), { tone: 'error' })
    }
  }

  const touch = () => setDirty(true)
  const status = article?.status ?? 'draft'
  const busy = save.isPending || setStatus.isPending
  // Problems with title, address, category and summary show at their field; the rest shows here.
  const topError =
    error === null ? null : (kbFieldError(error, 'body_html') ?? (error instanceof ApiError && error.status === 422 ? null : errorMessage(error)))

  if (!canEdit) {
    return (
      <Shell>
        <PageHeader title={title} breadcrumb={<BackLink />} {...(article?.category_name ? { description: article.category_name } : {})} />
        <Card>
          <ReadOnlyHtml html={body.html} ariaLabel="Artikel" extensions={articleExtensions} />
        </Card>
      </Shell>
    )
  }

  return (
    <Shell>
      <PageHeader
        title={article ? 'Artikel bewerken' : 'Nieuw artikel'}
        breadcrumb={<BackLink />}
        actions={
          <>
            <Button onClick={() => setPreview((p) => !p)} aria-pressed={preview}>
              {preview ? <EyeOff size={16} aria-hidden /> : <Eye size={16} aria-hidden />}
              {preview ? 'Bewerken' : 'Voorbeeld'}
            </Button>
            <Button variant="primary" busy={save.isPending} disabled={busy || (!dirty && article !== null) || title.trim() === ''} onClick={() => void saveNow()}>
              Opslaan
            </Button>
            {article && status === 'published' && (
              <Button busy={setStatus.isPending} disabled={busy} onClick={() => setStatus.mutate('draft')}>
                Depubliceren
              </Button>
            )}
            {article && status !== 'published' && (
              <Button busy={setStatus.isPending} disabled={busy || title.trim() === '' || body.empty || categoryId === ''} onClick={() => setStatus.mutate('published')}>
                Publiceren
              </Button>
            )}
            {article && (
              <ActionMenu
                label="Meer acties"
                items={[
                  { label: 'Versies bekijken', icon: <History size={16} />, onSelect: () => setDialog('revisions') },
                  ...(status !== 'archived' ? [{ label: 'Archiveren', icon: <Archive size={16} />, onSelect: () => setStatus.mutate('archived') }] : []),
                  { label: 'Verwijderen', icon: <Trash2 size={16} />, danger: true, separated: true, onSelect: () => setDialog('delete') },
                ]}
              />
            )}
          </>
        }
      />

      {conflict && (
        <ErrorNotice>
          <p>Iemand anders heeft dit artikel intussen gewijzigd. Je wijzigingen zijn nog niet opgeslagen.</p>
          <div className="mt-2">
            <Button
              size="sm"
              onClick={() => {
                if (!article) return
                void api<KbArticleDetail>('GET', `/kb/articles/${article.id}`).then(reload, (err: unknown) => setError(err))
              }}
            >
              Nieuwste versie laden
            </Button>
          </div>
        </ErrorNotice>
      )}
      {topError && <ErrorNotice>{topError}</ErrorNotice>}

      <div className="grid gap-6 lg:grid-cols-[minmax(0,1fr)_288px]">
        <div className="flex min-w-0 flex-col gap-4">
          <div className="flex items-center gap-2">
            <Badge dot>{statusLabel[status]}</Badge>
            {dirty && <span className="t-label">Niet-opgeslagen wijzigingen</span>}
          </div>
          <Field label="Titel" error={kbFieldError(error, 'title')}>
            {(props) => (
              <Input
                {...props}
                value={title}
                maxLength={200}
                placeholder="Bijvoorbeeld: Hoe betaal ik een factuur?"
                onChange={(e) => {
                  setTitle(e.target.value)
                  if (!slugTouched) setSlug(slugify(e.target.value))
                  touch()
                }}
              />
            )}
          </Field>
          <div className={preview ? 'hidden' : ''}>
            <RichEditor
              key={editorKey}
              initialHtml={body.html}
              ariaLabel="Tekst van het artikel"
              placeholder="Schrijf het artikel. Gebruik koppen om het overzichtelijk te houden."
              extensions={articleExtensions}
              onChange={(v) => {
                setBody(v)
                touch()
              }}
              onSubmit={() => void saveNow()}
              onEditor={setEditor}
              className="[&_.echoo-prose]:max-h-[60vh] [&_.echoo-prose]:min-h-64 [&_.echoo-prose_img]:max-w-full"
              toolbarExtra={editor && <ArticleTools editor={editor} onImage={() => fileInput.current?.click()} />}
            />
            <input
              ref={fileInput}
              type="file"
              hidden
              accept="image/png,image/jpeg,image/gif,image/webp"
              aria-label="Afbeelding kiezen"
              onChange={(e) => {
                const file = e.target.files?.[0]
                e.target.value = ''
                if (file) void upload(file)
              }}
            />
          </div>
          {preview && (
            <Card>
              <h2 className="t-h3 mb-4 text-ink">{title || 'Zonder titel'}</h2>
              <ReadOnlyHtml html={body.html} ariaLabel="Voorbeeld van het artikel" extensions={articleExtensions} />
            </Card>
          )}
        </div>

        <div className="flex flex-col gap-4">
          <Card title="Instellingen">
            <div className="flex flex-col gap-4">
              <Field label="Categorie" error={kbFieldError(error, 'category_id')} help={categories.data?.length === 0 ? 'Een beheerder maakt eerst een categorie aan.' : undefined}>
                {(props) => (
                  <Select
                    {...props}
                    value={categoryId}
                    onChange={(e) => {
                      setCategoryId(e.target.value)
                      touch()
                    }}
                  >
                    <option value="">Kies een categorie</option>
                    {categoryChoices(categories.data ?? []).map((c) => (
                      <option key={c.id} value={c.id}>
                        {c.label}
                      </option>
                    ))}
                  </Select>
                )}
              </Field>
              <Field
                label="Adres"
                error={kbFieldError(error, 'slug')}
                help={article?.published_at ? 'Verandert het adres, dan blijft het oude adres doorverwijzen.' : 'Het stuk na /hulp/a/.'}
              >
                {(props) => (
                  <Input
                    {...props}
                    value={slug}
                    maxLength={80}
                    spellCheck={false}
                    onChange={(e) => {
                      setSlug(e.target.value)
                      setSlugTouched(true)
                      touch()
                    }}
                  />
                )}
              </Field>
              <Field label="Samenvatting" error={kbFieldError(error, 'excerpt')} help="Staat onder de titel in lijsten en zoekresultaten. Leeg: het begin van de tekst.">
                {(props) => (
                  <Textarea
                    {...props}
                    value={excerpt}
                    maxLength={300}
                    rows={3}
                    onChange={(e) => {
                      setExcerpt(e.target.value)
                      touch()
                    }}
                  />
                )}
              </Field>
            </div>
          </Card>
          {article && (
            <Card title="Gebruik">
              <dl className="grid grid-cols-2 gap-y-2 text-base">
                <dt className="t-label">Gelezen</dt>
                <dd className="text-right tabular-nums">{article.view_count}</dd>
                <dt className="t-label">Nuttig: ja</dt>
                <dd className="text-right tabular-nums">{article.helpful_yes}</dd>
                <dt className="t-label">Nuttig: nee</dt>
                <dd className="text-right tabular-nums">{article.helpful_no}</dd>
                <dt className="t-label">Bijgewerkt</dt>
                <dd className="text-right">{formatDateTime(article.updated_at)}</dd>
              </dl>
              {article.public_url && (
                <a href={article.public_url} target="_blank" rel="noreferrer" className="mt-3 inline-flex items-center gap-2 text-base text-ink hover:underline">
                  <ExternalLink size={14} aria-hidden />
                  Bekijk op de openbare site
                </a>
              )}
            </Card>
          )}
        </div>
      </div>

      {dialog === 'delete' && article && (
        <Dialog open onOpenChange={(o) => !o && setDialog(null)} title="Artikel verwijderen" description={`“${article.title}” en alle eerdere versies worden verwijderd. Dit kan niet ongedaan worden gemaakt.`}>
          <DialogFooter>
            <Button onClick={() => setDialog(null)}>Annuleren</Button>
            <Button variant="danger" busy={remove.isPending} onClick={() => remove.mutate()}>
              Verwijderen
            </Button>
          </DialogFooter>
        </Dialog>
      )}
      {dialog === 'revisions' && article && (
        <RevisionsDialog
          articleId={article.id}
          onClose={() => setDialog(null)}
          onRestored={(d) => {
            reload(d)
            setDialog(null)
            toast('Versie teruggezet')
          }}
        />
      )}
    </Shell>
  )
}

function Shell({ children }: { children: React.ReactNode }) {
  return (
    <div className="relative h-full overflow-y-auto px-4 py-8 md:px-8">
      <div className="mx-auto flex w-full max-w-5xl flex-col gap-6">{children}</div>
    </div>
  )
}

function BackLink() {
  return (
    <Link to="/kennisbank" className="hover:text-ink">
      Terug naar de kennisbank
    </Link>
  )
}

function ArticleTools({ editor, onImage }: { editor: Editor; onImage: () => void }) {
  const active = useEditorState({
    editor,
    selector: (ctx) => ({
      h2: ctx.editor.isActive('heading', { level: 2 }),
      h3: ctx.editor.isActive('heading', { level: 3 }),
      code: ctx.editor.isActive('code'),
      codeBlock: ctx.editor.isActive('codeBlock'),
    }),
  })
  const tool = (on: boolean) => (on ? 'bg-active text-ink' : '')
  return (
    <>
      <IconButton label="Kop" aria-pressed={active.h2} className={tool(active.h2)} onClick={() => editor.chain().focus().toggleHeading({ level: 2 }).run()}>
        <Heading2 size={16} aria-hidden />
      </IconButton>
      <IconButton label="Subkop" aria-pressed={active.h3} className={tool(active.h3)} onClick={() => editor.chain().focus().toggleHeading({ level: 3 }).run()}>
        <Heading3 size={16} aria-hidden />
      </IconButton>
      <IconButton label="Code in de regel" aria-pressed={active.code} className={tool(active.code)} onClick={() => editor.chain().focus().toggleCode().run()}>
        <Code size={16} aria-hidden />
      </IconButton>
      <IconButton label="Codeblok" aria-pressed={active.codeBlock} className={tool(active.codeBlock)} onClick={() => editor.chain().focus().toggleCodeBlock().run()}>
        <FileCode size={16} aria-hidden />
      </IconButton>
      <IconButton label="Afbeelding invoegen" onClick={onImage}>
        <ImagePlus size={16} aria-hidden />
      </IconButton>
    </>
  )
}

function RevisionsDialog({ articleId, onClose, onRestored }: { articleId: string; onClose: () => void; onRestored: (d: KbArticleDetail) => void }) {
  const revisions = useQuery(kbRevisionsQuery(articleId))
  const restore = useMutation({
    mutationFn: (revisionId: string) => api<KbArticleDetail>('POST', `/kb/articles/${articleId}/revisions/${revisionId}/restore`),
    onSuccess: onRestored,
  })
  return (
    <Dialog open onOpenChange={(o) => !o && onClose()} title="Eerdere versies" description="Bij elke wijziging bewaren we de vorige tekst. De laatste 20 blijven beschikbaar. Terugzetten bewaart de huidige tekst ook als versie." size="lg">
      {restore.isError && (
        <div className="mb-3">
          <ErrorNotice>{kbFieldError(restore.error, 'body_html') ?? errorMessage(restore.error)}</ErrorNotice>
        </div>
      )}
      {revisions.isPending ? (
        <Skeleton className="h-24" />
      ) : revisions.isError ? (
        <ErrorNotice>{errorMessage(revisions.error)}</ErrorNotice>
      ) : revisions.data.length === 0 ? (
        <p className="text-base text-muted">Er zijn nog geen eerdere versies.</p>
      ) : (
        <ul className="divide-y divide-line">
          {revisions.data.map((r) => (
            <li key={r.id} className="flex items-center justify-between gap-3 py-3">
              <div className="min-w-0">
                <div className="truncate text-base text-ink">{r.title}</div>
                <div className="t-label">
                  {formatDateTime(r.created_at)}
                  {r.edited_by_name ? ` · ${r.edited_by_name}` : ''}
                </div>
              </div>
              <Button size="sm" busy={restore.isPending && restore.variables === r.id} disabled={restore.isPending} onClick={() => restore.mutate(r.id)}>
                Terugzetten
              </Button>
            </li>
          ))}
        </ul>
      )}
    </Dialog>
  )
}
