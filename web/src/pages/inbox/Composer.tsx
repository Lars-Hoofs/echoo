import { useQuery } from '@tanstack/react-query'
import { ChevronDown, MessageSquareLock } from 'lucide-react'
import { useEffect, useRef, useState } from 'react'

import { ErrorNotice, IconButton, Skeleton } from '../../components/ui'
import { type ComposerMode, draftQuery, onComposerRequest, replyDefaultsQuery, useComposerVersion } from '../../lib/composer'
import { errorMessage } from '../../lib/errors'
import { hasPermission, meQuery } from '../../lib/session'
import { NoteForm } from './composer/NoteForm'
import { ReplyForm } from './composer/ReplyForm'

const tabs: { id: ComposerMode; label: string }[] = [
  { id: 'reply', label: 'Antwoorden' },
  { id: 'note', label: 'Notitie' },
]

export function Composer({ conversationId, mailboxId, canWrite }: { conversationId: string; mailboxId: string; canWrite: boolean }) {
  const me = useQuery(meQuery)
  const [tab, setTab] = useState<ComposerMode>('reply')
  // On a phone the editor would fill the screen and leave no room to read, so it opens on tap.
  const [open, setOpen] = useState(() => window.matchMedia('(min-width: 768px)').matches)
  const defaults = useQuery({ ...replyDefaultsQuery(conversationId), enabled: canWrite && hasPermission(me.data, 'conversations.write') })
  const draft = useQuery({ ...draftQuery(conversationId), enabled: defaults.isSuccess })
  const version = useComposerVersion(conversationId)
  const pendingFocus = useRef<ComposerMode | null>(null)
  const [focusRequests, setFocusRequests] = useState(0)
  const ready = defaults.isSuccess && draft.isSuccess

  useEffect(
    () =>
      onComposerRequest((mode) => {
        pendingFocus.current = mode
        setTab(mode)
        setOpen(true)
        setFocusRequests((n) => n + 1)
      }),
    [],
  )

  // The editor of the reply tab only exists once its defaults and draft are loaded, so a request
  // that arrives earlier waits for `ready`.
  useEffect(() => {
    const mode = pendingFocus.current
    if (mode === null || (mode === 'reply' && !ready)) return
    const editor = document.querySelector<HTMLElement>(`#composer-panel-${mode} [contenteditable="true"]`)
    if (!editor) return
    pendingFocus.current = null
    editor.focus()
  }, [focusRequests, ready, tab, open])

  if (!canWrite || (me.data && !hasPermission(me.data, 'conversations.write'))) return <ReadOnlyNotice />

  return (
    <div className="shrink-0 px-4 pb-[max(1rem,env(safe-area-inset-bottom))] min-[900px]:px-6">
      <div className="card card-line card-s">
        <div className="flex items-center justify-between gap-3">
          <div role="tablist" aria-label="Soort bericht" className="tabs">
            {tabs.map((t) => (
              <button
                key={t.id}
                type="button"
                role="tab"
                id={`composer-tab-${t.id}`}
                aria-selected={tab === t.id}
                aria-controls={`composer-panel-${t.id}`}
                onClick={() => {
                  setTab(t.id)
                  setOpen(true)
                }}
                className={`tab max-md:h-11 ${tab === t.id ? 'is-active' : ''}`}
              >
                {t.label}
              </button>
            ))}
          </div>
          {open && (
            <IconButton label="Editor inklappen" size="sm" onClick={() => setOpen(false)} className="md:hidden">
              <ChevronDown aria-hidden />
            </IconButton>
          )}
        </div>
        <div className="max-h-[55vh] overflow-y-auto pt-4" hidden={!open}>
          <div role="tabpanel" id="composer-panel-reply" aria-labelledby="composer-tab-reply" hidden={tab !== 'reply'}>
            {defaults.isError ? (
              <ErrorNotice>{errorMessage(defaults.error)}</ErrorNotice>
            ) : draft.isError ? (
              <ErrorNotice>{errorMessage(draft.error)}</ErrorNotice>
            ) : defaults.data && draft.data ? (
              <ReplyForm key={version} conversationId={conversationId} mailboxId={mailboxId} defaults={defaults.data} draft={draft.data.draft} />
            ) : (
              <Skeleton className="h-32 w-full" />
            )}
          </div>
          <div role="tabpanel" id="composer-panel-note" aria-labelledby="composer-tab-note" hidden={tab !== 'note'}>
            <NoteForm conversationId={conversationId} />
          </div>
        </div>
      </div>
    </div>
  )
}

function ReadOnlyNotice() {
  return (
    <div className="shrink-0 px-4 pb-[max(1rem,env(safe-area-inset-bottom))] min-[900px]:px-6">
      <p className="t-body flex items-center gap-3 rounded-lg bg-subtle px-4 py-3">
        <MessageSquareLock aria-hidden className="shrink-0" />
        Je hebt alleen leesrechten voor dit gesprek.
      </p>
    </div>
  )
}
