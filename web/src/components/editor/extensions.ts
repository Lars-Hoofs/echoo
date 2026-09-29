import { Extension } from '@tiptap/core'
import { PluginKey, Plugin } from '@tiptap/pm/state'
import { Decoration, DecorationSet } from '@tiptap/pm/view'
import Suggestion from '@tiptap/suggestion'

import { findVariables, type Template } from '../../lib/composer'
import type { SuggestionHandlers } from './suggestion'

// Marks {{variables}} in the text: known ones as chips, unknown ones as a warning, because the
// server leaves those visible in the message.
export const VariableHighlight = Extension.create({
  name: 'variableHighlight',
  addProseMirrorPlugins() {
    return [
      new Plugin({
        key: new PluginKey('variableHighlight'),
        props: {
          decorations(state) {
            const decorations: Decoration[] = []
            state.doc.descendants((node, pos) => {
              if (!node.isText || !node.text) return
              for (const m of findVariables(node.text)) {
                decorations.push(Decoration.inline(pos + m.from, pos + m.to, { class: m.known ? 'echoo-var' : 'echoo-var echoo-var-unknown' }))
              }
            })
            return DecorationSet.create(state.doc, decorations)
          },
        },
      }),
    ]
  },
})

export interface SlashOptions {
  suggestion: SuggestionHandlers<Template, Template>
}

// Typing "/" at the start of a line or after a space opens the canned response picker.
export const SlashCommand = Extension.create<SlashOptions>({
  name: 'slashCommand',
  addProseMirrorPlugins() {
    return [
      Suggestion<Template, Template>({
        editor: this.editor,
        pluginKey: new PluginKey('slashCommand'),
        char: '/',
        ...this.options.suggestion,
      }),
    ]
  },
})
