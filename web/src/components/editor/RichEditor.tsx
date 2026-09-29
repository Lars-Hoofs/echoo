import type { Editor, Extensions } from '@tiptap/core'
import Placeholder from '@tiptap/extension-placeholder'
import { EditorContent, useEditor, useEditorState } from '@tiptap/react'
import StarterKit from '@tiptap/starter-kit'
import { Bold, Italic, Link2, List, ListOrdered, Quote } from 'lucide-react'
import { type ReactNode, type SubmitEvent, useEffect, useRef, useState } from 'react'

import { Dialog, DialogFooter } from '../Dialog'
import { Button, Field, IconButton, Input } from '../ui'

export interface EditorValue {
  html: string
  empty: boolean
}

// TipTap injects one <style> element with the base ProseMirror rules. The strict CSP only allows
// it with the per-request nonce that the server puts in a meta tag.
export const cspNonce = (): string | undefined => document.querySelector<HTMLMetaElement>('meta[name="csp-nonce"]')?.content

const allowedLink = /^(https?:\/\/|mailto:)/i

// Turns what a person typed into a link target: bare domains get https://, addresses mailto:.
export function normalizeLink(input: string): string | null {
  const value = input.trim()
  if (!value || /\s/.test(value)) return null
  if (allowedLink.test(value)) return value
  if (/^[^\s@/:]+@[^\s@/:]+\.[^\s@/:]{2,}$/.test(value)) return `mailto:${value}`
  if (/^[^\s@/:]+\.[^\s@/:]{2,}(\/.*)?$/.test(value)) return `https://${value}`
  return null
}

function baseExtensions(placeholder: string): Extensions {
  return [
    StarterKit.configure({
      horizontalRule: false,
      heading: { levels: [1, 2, 3] },
      link: {
        openOnClick: false,
        autolink: true,
        HTMLAttributes: { rel: 'noopener noreferrer nofollow' },
        isAllowedUri: (url) => allowedLink.test(url),
      },
    }),
    Placeholder.configure({ placeholder }),
  ]
}

const surfaces = {
  plain: 'min-h-24 rounded-md border border-line-input bg-surface px-4 py-3 focus-within:border-ink',
  note: 'min-h-24 rounded-md bg-note px-4 py-3',
  bare: '',
}

interface Props {
  initialHtml: string
  ariaLabel: string
  placeholder: string
  extensions?: Extensions
  onChange: (value: EditorValue) => void
  // Called on Cmd/Ctrl+Enter.
  onSubmit?: (() => void) | undefined
  onEditor?: ((editor: Editor | null) => void) | undefined
  toolbarExtra?: ReactNode
  className?: string
  // Tone of the writing surface: notes use the note background, bare has no box of its own (the
  // caller supplies the card around it).
  surface?: 'plain' | 'note' | 'bare'
}

export function RichEditor({ initialHtml, ariaLabel, placeholder, extensions = [], onChange, onSubmit, onEditor, toolbarExtra, className = '', surface = 'plain' }: Props) {
  const onChangeRef = useRef(onChange)
  const onSubmitRef = useRef(onSubmit)
  useEffect(() => {
    onChangeRef.current = onChange
    onSubmitRef.current = onSubmit
  })
  const [extensionSet] = useState(() => [...baseExtensions(placeholder), ...extensions])

  const editor = useEditor({
    extensions: extensionSet,
    content: initialHtml,
    injectNonce: cspNonce(),
    editorProps: {
      attributes: { role: 'textbox', 'aria-multiline': 'true', 'aria-label': ariaLabel, class: 'echoo-prose focus:outline-none' },
      handleKeyDown: (_view, event) => {
        if (event.key === 'Enter' && (event.metaKey || event.ctrlKey) && onSubmitRef.current) {
          event.preventDefault()
          onSubmitRef.current()
          return true
        }
        return false
      },
    },
    onUpdate: ({ editor: e }) => {
      onChangeRef.current({ html: e.getHTML(), empty: e.isEmpty })
    },
  })

  useEffect(() => {
    onEditor?.(editor)
    return () => onEditor?.(null)
  }, [editor, onEditor])

  return (
    <div className={`flex min-h-0 flex-col ${className}`}>
      <div
        className={`echoo-editor cursor-text overflow-y-auto text-base text-ink ${surfaces[surface]}`}
        onClick={() => editor.commands.focus()}
      >
        <EditorContent editor={editor} />
      </div>
      <Toolbar editor={editor} extra={toolbarExtra} />
    </div>
  )
}

function Toolbar({ editor, extra }: { editor: Editor; extra: ReactNode }) {
  const active = useEditorState({
    editor,
    selector: (ctx) => ({
      bold: ctx.editor.isActive('bold'),
      italic: ctx.editor.isActive('italic'),
      link: ctx.editor.isActive('link'),
      bullet: ctx.editor.isActive('bulletList'),
      ordered: ctx.editor.isActive('orderedList'),
      quote: ctx.editor.isActive('blockquote'),
    }),
  })
  const [linkOpen, setLinkOpen] = useState(false)
  const tool = (on: boolean) => (on ? 'border-transparent bg-ink text-on-ink hover:bg-ink' : 'border-transparent')

  return (
    <div role="toolbar" aria-label="Opmaak" className="mt-3 flex flex-wrap items-center">
      <IconButton label="Vet" size="sm" aria-pressed={active.bold} className={tool(active.bold)} onClick={() => editor.chain().focus().toggleBold().run()}>
        <Bold aria-hidden />
      </IconButton>
      <IconButton label="Cursief" size="sm" aria-pressed={active.italic} className={tool(active.italic)} onClick={() => editor.chain().focus().toggleItalic().run()}>
        <Italic aria-hidden />
      </IconButton>
      <IconButton
        label={active.link ? 'Link wijzigen of verwijderen' : 'Link toevoegen'}
        size="sm"
        aria-pressed={active.link}
        className={tool(active.link)}
        onClick={() => setLinkOpen(true)}
      >
        <Link2 aria-hidden />
      </IconButton>
      <IconButton label="Lijst" size="sm" aria-pressed={active.bullet} className={tool(active.bullet)} onClick={() => editor.chain().focus().toggleBulletList().run()}>
        <List aria-hidden />
      </IconButton>
      <IconButton
        label="Genummerde lijst"
        size="sm"
        aria-pressed={active.ordered}
        className={tool(active.ordered)}
        onClick={() => editor.chain().focus().toggleOrderedList().run()}
      >
        <ListOrdered aria-hidden />
      </IconButton>
      <IconButton label="Citaat" size="sm" aria-pressed={active.quote} className={tool(active.quote)} onClick={() => editor.chain().focus().toggleBlockquote().run()}>
        <Quote aria-hidden />
      </IconButton>
      {extra}
      <LinkDialog editor={editor} open={linkOpen} onOpenChange={setLinkOpen} hasLink={active.link} />
    </div>
  )
}

function LinkDialog({ editor, open, onOpenChange, hasLink }: { editor: Editor; open: boolean; onOpenChange: (open: boolean) => void; hasLink: boolean }) {
  return (
    <Dialog open={open} onOpenChange={onOpenChange} title="Link" size="sm">
      <LinkForm editor={editor} onClose={() => onOpenChange(false)} hasLink={hasLink} />
    </Dialog>
  )
}

// Mounted only while the dialog is open, so it starts from the link under the caret each time.
function LinkForm({ editor, onClose, hasLink }: { editor: Editor; onClose: () => void; hasLink: boolean }) {
  const current: unknown = editor.getAttributes('link').href
  const [value, setValue] = useState(typeof current === 'string' ? current : '')
  const [invalid, setInvalid] = useState(false)

  function submit(e: SubmitEvent) {
    e.preventDefault()
    const href = normalizeLink(value)
    if (!href) {
      setInvalid(true)
      return
    }
    editor.chain().focus().extendMarkRange('link').setLink({ href }).run()
    onClose()
  }

  return (
    <form onSubmit={submit} className="flex flex-col gap-4" noValidate>
      <Field label="Webadres of e-mailadres" error={invalid ? 'Vul een webadres (https://…) of een e-mailadres in.' : undefined}>
        {(props) => <Input {...props} value={value} onChange={(e) => setValue(e.target.value)} autoFocus placeholder="https://voorbeeld.nl" />}
      </Field>
      <DialogFooter>
        {hasLink && (
          <Button
            onClick={() => {
              editor.chain().focus().extendMarkRange('link').unsetLink().run()
              onClose()
            }}
          >
            Link verwijderen
          </Button>
        )}
        <Button variant="primary" type="submit">
          Toepassen
        </Button>
      </DialogFooter>
    </form>
  )
}
