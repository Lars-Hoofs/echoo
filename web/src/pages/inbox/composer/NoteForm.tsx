import type { Editor } from '@tiptap/core'
import Mention from '@tiptap/extension-mention'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { Lock } from 'lucide-react'
import { useState } from 'react'

import { type EditorValue, RichEditor } from '../../../components/editor/RichEditor'
import { useSuggestion } from '../../../components/editor/suggestion'
import { useToast } from '../../../components/Toast'
import { Button, ErrorNotice } from '../../../components/ui'
import { api } from '../../../lib/api'
import { collectMentions, type Mentionable, mentionableQuery } from '../../../lib/composer'
import { errorMessage } from '../../../lib/errors'
import { MentionList } from './pickers'

function filterMentionable(users: Mentionable[], query: string): Mentionable[] {
  const q = query.trim().toLowerCase()
  return users.filter((u) => u.name.toLowerCase().includes(q) || u.email.toLowerCase().includes(q)).slice(0, 8)
}

interface Attrs {
  id: string
  label: string
}

export function NoteForm({ conversationId }: { conversationId: string }) {
  const qc = useQueryClient()
  const toast = useToast()
  const users = useQuery(mentionableQuery(conversationId))
  const mention = useSuggestion<Mentionable, Attrs>({
    source: users.data?.users ?? [],
    filter: filterMentionable,
    toSelected: (u) => ({ id: u.id, label: u.name }),
  })
  const [extensions] = useState(() => [
    Mention.configure({
      HTMLAttributes: { class: 'echoo-mention' },
      renderText: ({ node }) => `@${String(node.attrs.label ?? node.attrs.id)}`,
      suggestion: { char: '@', items: mention.options.items, render: mention.options.render },
    }),
  ])

  const [value, setValue] = useState<EditorValue>({ html: '', empty: true })
  const [editor, setEditor] = useState<Editor | null>(null)
  const [sending, setSending] = useState(false)
  const [error, setError] = useState<unknown>(null)
  const [nonce, setNonce] = useState(0)

  async function submit() {
    if (!editor || value.empty || sending) return
    setSending(true)
    setError(null)
    try {
      await api('POST', `/conversations/${encodeURIComponent(conversationId)}/notes`, {
        html: value.html,
        mentions: collectMentions(editor.getJSON()),
      })
      await qc.invalidateQueries({ queryKey: ['inbox'] })
      setValue({ html: '', empty: true })
      setNonce((n) => n + 1)
      toast('Notitie toegevoegd')
    } catch (err) {
      setError(err)
    } finally {
      setSending(false)
    }
  }

  return (
    <div>
      <p className="t-label mb-3 flex items-center gap-2">
        <Lock size={16} aria-hidden /> Alleen zichtbaar voor je team
      </p>
      <RichEditor
        key={nonce}
        initialHtml=""
        ariaLabel="Notitie"
        placeholder="Schrijf een notitie. Typ @ om een collega te noemen."
        surface="note"
        extensions={extensions}
        onChange={setValue}
        onSubmit={() => void submit()}
        onEditor={setEditor}
      />
      {error !== null && (
        <div className="mt-3">
          <ErrorNotice>{errorMessage(error)}</ErrorNotice>
        </div>
      )}
      <div className="mt-3 flex justify-end">
        <Button variant="primary" disabled={value.empty} busy={sending} onClick={() => void submit()}>
          Notitie toevoegen
        </Button>
      </div>
      {mention.view && <MentionList view={mention.view} />}
    </div>
  )
}
