import type { Extensions } from '@tiptap/core'
import { EditorContent, useEditor } from '@tiptap/react'
import StarterKit from '@tiptap/starter-kit'
import { useEffect, useState } from 'react'

import { cspNonce } from './RichEditor'

// Shows HTML that is already sanitized (a signature, a template) without ever putting it into the
// page as markup: TipTap parses it into its own document and renders that.
export function ReadOnlyHtml({ html, ariaLabel, extensions = [] }: { html: string; ariaLabel: string; extensions?: Extensions }) {
  const [extensionSet] = useState(() => [StarterKit.configure({ link: { openOnClick: false } }), ...extensions])
  const editor = useEditor({
    extensions: extensionSet,
    content: html,
    editable: false,
    injectNonce: cspNonce(),
    editorProps: { attributes: { 'aria-label': ariaLabel, class: 'echoo-prose' } },
  })
  useEffect(() => {
    if (editor.getHTML() !== html) editor.commands.setContent(html)
  }, [editor, html])
  return <EditorContent editor={editor} />
}
