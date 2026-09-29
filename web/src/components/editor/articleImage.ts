import { mergeAttributes, Node } from '@tiptap/core'

// A block image whose source is one of the knowledge base's own uploads (/hulp/i/<id>). The
// server sanitizer only lets those through, so nothing else is offered here.
export const ArticleImage = Node.create({
  name: 'articleImage',
  group: 'block',
  atom: true,
  draggable: true,
  addAttributes() {
    return { src: { default: null }, alt: { default: '' } }
  },
  parseHTML() {
    return [{ tag: 'img[src^="/hulp/i/"]' }]
  },
  renderHTML({ HTMLAttributes }) {
    return ['img', mergeAttributes(HTMLAttributes)]
  },
})
