import type { Editor, Range } from '@tiptap/core'
import { useQueryClient } from '@tanstack/react-query'
import { useQuery } from '@tanstack/react-query'
import { Link } from '@tanstack/react-router'
import { BookOpen, FileText, Paperclip } from 'lucide-react'
import { type DragEvent, useCallback, useEffect, useMemo, useRef, useState } from 'react'

import { SlashCommand, VariableHighlight } from '../../../components/editor/extensions'
import { ReadOnlyHtml } from '../../../components/editor/ReadOnlyHtml'
import { type EditorValue, RichEditor } from '../../../components/editor/RichEditor'
import { useReportTyping } from '../../../lib/presence'
import { useSuggestion } from '../../../components/editor/suggestion'
import { useToast } from '../../../components/Toast'
import { ErrorNotice, IconButton, Input } from '../../../components/ui'
import { api, ApiError } from '../../../lib/api'
import {
  type Draft,
  draftQuery,
  filterTemplates,
  findVariables,
  type RenderedTemplate,
  pendingSuggestions,
  replyDefaultsQuery,
  resetComposer,
  type ReplyDefaults,
  type SendPayload,
  type SentMessage,
  type Template,
  templatesQuery,
  type Upload,
  undoWindowMs,
} from '../../../lib/composer'
import { errorMessage, fieldError } from '../../../lib/errors'
import { ArticlePicker } from '../../kb/ArticlePicker'
import type { Address } from '../../../lib/inbox'
import { AttachmentBar } from './AttachmentBar'
import { TemplatePicker } from './pickers'
import { RecipientField } from './RecipientField'
import { SendButton, type StatusAfter } from './SendButton'
import { useAttachments } from './useAttachments'
import { useSignature } from './useSignature'

interface Props {
  conversationId: string
  mailboxId: string
  defaults: ReplyDefaults
  draft: Draft | null
}

interface Snapshot {
  to: Address[]
  cc: Address[]
  bcc: Address[]
  subject: string
  html: string
}

const AUTOSAVE_MS = 2000

export function ReplyForm({ conversationId, mailboxId, defaults, draft }: Props) {
  const qc = useQueryClient()
  const toast = useToast()
  const initial: Snapshot = draft ?? { to: defaults.to, cc: defaults.cc, bcc: [], subject: defaults.subject, html: '' }
  const [to, setTo] = useState(initial.to)
  const [cc, setCc] = useState(initial.cc)
  const [bcc, setBcc] = useState(initial.bcc)
  const [subject, setSubject] = useState(initial.subject)
  const [showCc, setShowCc] = useState(initial.cc.length > 0)
  const [showBcc, setShowBcc] = useState(initial.bcc.length > 0)
  const [showSubject, setShowSubject] = useState(initial.subject !== defaults.subject)
  const [value, setValue] = useState<EditorValue>({ html: initial.html, empty: initial.html === '' })
  const [editor, setEditor] = useState<Editor | null>(null)
  const [dragging, setDragging] = useState(false)
  const [articleOpen, setArticleOpen] = useState(false)
  const [sending, setSending] = useState(false)
  const [error, setError] = useState<unknown>(null)
  const [draftStatus, setDraftStatus] = useState<'idle' | 'saved' | 'failed'>('idle')
  const reportTyping = useReportTyping(conversationId)
  const att = useAttachments(draft?.attachments ?? [])
  const signature = useSignature(mailboxId)
  const fileInput = useRef<HTMLInputElement>(null)
  const idempotencyKey = useRef(crypto.randomUUID())
  const dirty = useRef(false)
  const pending = useRef(false)
  const sent = useRef(false)
  const draftPath = `/conversations/${encodeURIComponent(conversationId)}/draft`

  const templates = useQuery(templatesQuery())
  const applyTemplate = useCallback(
    async (target: Editor, range: Range, template: Template) => {
      target.chain().focus().deleteRange(range).run()
      try {
        const r = await api<RenderedTemplate>(
          'GET',
          `/templates/${template.id}/render?conversation_id=${encodeURIComponent(conversationId)}`,
        )
        target.chain().focus().insertContent(r.body_html).run()
        if (r.subject) {
          dirty.current = true
          setSubject(r.subject)
          setShowSubject(true)
        }
      } catch (err) {
        toast(errorMessage(err), { tone: 'error' })
      }
    },
    [conversationId, toast],
  )

  const slash = useSuggestion<Template, Template>({
    source: templates.data?.templates ?? [],
    filter: filterTemplates,
    toSelected: (t) => t,
    onPick: ({ editor: e, range, props }) => {
      void applyTemplate(e, range, props)
    },
  })
  const [extensions] = useState(() => [VariableHighlight, SlashCommand.configure({ suggestion: slash.options })])

  const payload = (status: StatusAfter): SendPayload => ({
    idempotency_key: idempotencyKey.current,
    to,
    cc,
    bcc,
    subject,
    html: value.html,
    attachment_ids: att.uploads.map((u) => u.id),
    status_after: status,
  })

  const latest = useRef({ to, cc, bcc, subject, value, uploads: att.uploads })
  useEffect(() => {
    latest.current = { to, cc, bcc, subject, value, uploads: att.uploads }
  })

  const saveDraft = useCallback(async () => {
    pending.current = false
    const d = latest.current
    try {
      if (d.value.empty && d.uploads.length === 0) {
        await api('DELETE', draftPath)
      } else {
        await api('PUT', draftPath, {
          to: d.to,
          cc: d.cc,
          bcc: d.bcc,
          subject: d.subject,
          html: d.value.html,
          attachment_ids: d.uploads.map((u) => u.id),
        })
      }
      setDraftStatus('saved')
    } catch {
      setDraftStatus('failed')
    }
  }, [draftPath])

  useEffect(() => {
    if (!dirty.current || sent.current) return
    pending.current = true
    const t = setTimeout(() => {
      void saveDraft()
    }, AUTOSAVE_MS)
    return () => {
      clearTimeout(t)
    }
  }, [to, cc, bcc, subject, value.html, att.uploads.length, saveDraft])

  useEffect(
    () => () => {
      if (pending.current && dirty.current && !sent.current) void saveDraft()
    },
    [saveDraft],
  )

  const unresolved = useMemo(() => Array.from(new Set(findVariables(value.html).map((v) => v.name))), [value.html])
  const canSend = to.length + cc.length + bcc.length > 0 && !value.empty && !att.busy && !att.failed && !sending

  async function send(status: StatusAfter) {
    if (!canSend) return
    setSending(true)
    setError(null)
    const snapshot: Snapshot & { uploads: Upload[] } = { to, cc, bcc, subject, html: value.html, uploads: att.uploads }
    try {
      const res = await api<SentMessage>('POST', `/conversations/${encodeURIComponent(conversationId)}/replies`, payload(status))
      sent.current = true
      idempotencyKey.current = crypto.randomUUID()
      qc.setQueryData(draftQuery(conversationId).queryKey, { draft: null })
      await Promise.all([
        qc.refetchQueries({ queryKey: replyDefaultsQuery(conversationId).queryKey }),
        qc.invalidateQueries({ queryKey: ['inbox'] }),
      ])
      resetComposer(conversationId)
      announceSent(res, snapshot)
    } catch (err) {
      setError(err)
      setSending(false)
    }
  }

  function announceSent(res: SentMessage, snapshot: Snapshot & { uploads: Upload[] }) {
    const window = undoWindowMs(res.undo_until)
    if (window === 0) {
      toast('Verstuurd')
      return
    }
    toast('Verstuurd', {
      durationMs: window,
      action: {
        label: 'Ongedaan maken',
        onClick: () => {
          void undoSend(res.message.id, snapshot)
        },
      },
    })
  }

  async function undoSend(messageId: string, snapshot: Snapshot & { uploads: Upload[] }) {
    try {
      const r = await api<{ attachments: Upload[] }>(
        'POST',
        `/conversations/${encodeURIComponent(conversationId)}/replies/${messageId}/cancel`,
      )
      const restored: Draft = { ...snapshot, attachments: r.attachments, updated_at: new Date().toISOString() }
      await api('PUT', draftPath, {
        to: restored.to,
        cc: restored.cc,
        bcc: restored.bcc,
        subject: restored.subject,
        html: restored.html,
        attachment_ids: restored.attachments.map((u) => u.id),
      })
      qc.setQueryData(draftQuery(conversationId).queryKey, { draft: restored })
      await qc.invalidateQueries({ queryKey: ['inbox'] })
      resetComposer(conversationId)
      toast('Verzenden ongedaan gemaakt. Je bericht staat weer in het invoerveld.')
    } catch (err) {
      toast(errorMessage(err), { tone: 'error' })
      if (err instanceof ApiError && err.status === 409) await qc.invalidateQueries({ queryKey: ['inbox'] })
    }
  }

  function onDrop(e: DragEvent) {
    e.preventDefault()
    setDragging(false)
    dirty.current = true
    att.add(Array.from(e.dataTransfer.files))
  }

  function insertSlash() {
    if (!editor) return
    const before = editor.state.doc.textBetween(Math.max(0, editor.state.selection.from - 1), editor.state.selection.from)
    editor
      .chain()
      .focus()
      .insertContent(before === '' || before === ' ' || before === '\n' ? '/' : ' /')
      .run()
  }

  const toError = fieldError(error, 'to') ?? fieldError(error, 'cc') ?? fieldError(error, 'bcc')

  return (
    <div
      onDragOver={(e) => {
        if (e.dataTransfer.types.includes('Files')) {
          e.preventDefault()
          setDragging(true)
        }
      }}
      onDragLeave={() => setDragging(false)}
      onDrop={onDrop}
      className={`relative ${dragging ? 'rounded-md outline-2 -outline-offset-2 outline-dashed outline-ink' : ''}`}
    >
      <div className="divide-y divide-line">
        <div className="pb-3">
          <div className="flex flex-wrap items-start gap-x-3">
            <div className="min-w-56 flex-1">
              <RecipientField
                label="Aan"
                value={to}
                onChange={(next) => {
                  dirty.current = true
                  setTo(next)
                }}
              />
            </div>
            <div className="t-label flex shrink-0 gap-3 pt-4 max-sm:pt-0 max-sm:pl-19">
              {!showCc && (
                <button type="button" onClick={() => setShowCc(true)} className="underline underline-offset-2 hover:text-ink">
                  Cc
                </button>
              )}
              {!showBcc && (
                <button type="button" onClick={() => setShowBcc(true)} className="underline underline-offset-2 hover:text-ink">
                  Bcc
                </button>
              )}
              {!showSubject && (
                <button type="button" onClick={() => setShowSubject(true)} className="underline underline-offset-2 hover:text-ink">
                  Onderwerp wijzigen
                </button>
              )}
            </div>
          </div>
          {(showCc || showBcc || showSubject) && (
            <>
              {showCc && (
                <RecipientField
                  label="Cc"
                  value={cc}
                  onChange={(next) => {
                    dirty.current = true
                    setCc(next)
                  }}
                />
              )}
              {showBcc && (
                <RecipientField
                  label="Bcc"
                  value={bcc}
                  onChange={(next) => {
                    dirty.current = true
                    setBcc(next)
                  }}
                />
              )}
              {showSubject && (
                <div className="flex items-center gap-3 py-2">
                  <label htmlFor="composer-subject" className="t-label w-16 shrink-0">
                    Onderwerp
                  </label>
                  <Input
                    id="composer-subject"
                    value={subject}
                    onChange={(e) => {
                      dirty.current = true
                      setSubject(e.target.value)
                    }}
                    className="flex-1"
                  />
                </div>
              )}
            </>
          )}
          {pendingSuggestions(defaults.suggested_cc, to, cc, bcc).map((a) => (
            <div key={a.address} className="t-label flex flex-wrap items-center gap-2 py-2 pl-19">
              <span>Nieuwe afzender in deze thread, niet automatisch toegevoegd: {a.name ? `${a.name} <${a.address}>` : a.address}</span>
              <button
                type="button"
                onClick={() => {
                  dirty.current = true
                  setCc((prev) => [...prev, a])
                  setShowCc(true)
                }}
                className="text-ink underline underline-offset-2"
              >
                Toevoegen aan Cc
              </button>
            </div>
          ))}
        </div>
      </div>

      <div className="border-t border-line pt-4">
        <RichEditor
          initialHtml={initial.html}
          surface="bare"
          ariaLabel="Antwoord"
          placeholder="Schrijf je antwoord. Typ / voor een standaardantwoord."
          extensions={extensions}
          onChange={(v) => {
            dirty.current = true
            reportTyping()
            setValue(v)
          }}
          onSubmit={() => void send(null)}
          onEditor={setEditor}
          toolbarExtra={
            <>
              <IconButton label="Bijlage toevoegen" size="sm" className="border-transparent" onClick={() => fileInput.current?.click()}>
                <Paperclip aria-hidden />
              </IconButton>
              <IconButton label="Standaardantwoord invoegen" size="sm" className="border-transparent" onClick={insertSlash}>
                <FileText aria-hidden />
              </IconButton>
              <IconButton label="Artikel invoegen" size="sm" className="border-transparent" onClick={() => setArticleOpen(true)}>
                <BookOpen aria-hidden />
              </IconButton>
              <p role="status" className="t-label ml-auto min-w-0 truncate">
                {draftStatus === 'saved' && <span className="max-[1599px]:sr-only">Concept opgeslagen</span>}
                {draftStatus === 'failed' && <span className="text-danger-text">Concept niet opgeslagen</span>}
              </p>
              <div className="ml-auto pl-3">
                <SendButton disabled={!canSend} busy={sending} onSend={(s) => void send(s)} />
              </div>
            </>
          }
        />
        <input
          ref={fileInput}
          type="file"
          multiple
          hidden
          aria-label="Bestanden kiezen"
          onChange={(e) => {
            dirty.current = true
            att.add(Array.from(e.target.files ?? []))
            e.target.value = ''
          }}
        />
        <AttachmentBar
          items={att.items}
          onRemove={(key) => {
            dirty.current = true
            att.remove(key)
          }}
        />
        {signature && (
          <div className="echoo-signature mt-4 border-t border-line pt-4">
            <div className="t-label mb-2 flex items-center justify-between">
              <span>Handtekening</span>
              <Link to="/instellingen/profiel" hash="handtekening" className="underline underline-offset-2 hover:text-ink">
                Handtekening wijzigen
              </Link>
            </div>
            <ReadOnlyHtml html={signature} ariaLabel="Handtekening" />
          </div>
        )}
        {unresolved.length > 0 && (
          <p role="status" className="mt-3 text-base text-danger-text">
            Nog niet ingevuld: {unresolved.map((n) => `{{${n}}}`).join(', ')}. Pas dit aan voordat je verstuurt.
          </p>
        )}
      </div>

      {error !== null && (
        <div className="mt-3">
          <ErrorNotice>{toError ?? fieldError(error, 'html') ?? fieldError(error, 'attachment_ids') ?? errorMessage(error)}</ErrorNotice>
        </div>
      )}

      <SlashPicker view={slash.view} />
      <ArticlePicker
        open={articleOpen}
        onOpenChange={setArticleOpen}
        onPick={(a) => {
          setArticleOpen(false)
          if (!editor || !a.public_url) return
          editor
            .chain()
            .focus()
            .insertContent([
              { type: 'text', text: a.title, marks: [{ type: 'link', attrs: { href: a.public_url } }] },
              { type: 'text', text: ' ' },
            ])
            .run()
        }}
      />
    </div>
  )
}

function SlashPicker({ view }: { view: ReturnType<typeof useSuggestion<Template, Template>>['view'] }) {
  if (!view) return null
  return <TemplatePicker view={view} />
}
