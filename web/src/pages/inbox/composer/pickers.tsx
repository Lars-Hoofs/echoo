import { Command } from 'cmdk'

import { FloatingPanel, type SuggestionView } from '../../../components/editor/suggestion'
import { VariableHighlight } from '../../../components/editor/extensions'
import { ReadOnlyHtml } from '../../../components/editor/ReadOnlyHtml'
import { type Mentionable, scopeLabel, type Template } from '../../../lib/composer'

export function MentionList({ view }: { view: SuggestionView<Mentionable> }) {
  return (
    <FloatingPanel rect={view.rect} label="Collega's">
      {view.items.length === 0 ? (
        <p className="px-4 py-3 text-base text-muted">Niemand gevonden met toegang tot deze mailbox.</p>
      ) : (
        <ul role="listbox" aria-label="Collega's" className="max-h-56 overflow-y-auto p-2">
          {view.items.map((u, i) => (
            <li
              key={u.id}
              role="option"
              aria-selected={i === view.index}
              onClick={() => view.pick(u)}
              className={`flex cursor-default flex-col rounded-md px-4 py-2 ${i === view.index ? 'bg-selected' : ''}`}
            >
              <span className="text-base text-ink">{u.name}</span>
              <span className="t-label">{u.email}</span>
            </li>
          ))}
        </ul>
      )}
    </FloatingPanel>
  )
}

const previewExtensions = [VariableHighlight]

// The canned response list opened with "/". The editor keeps the keyboard focus, so the highlighted
// item is controlled from outside; cmdk only renders the list.
export function TemplatePicker({ view }: { view: SuggestionView<Template> }) {
  const selected = view.items[view.index]
  return (
    <FloatingPanel rect={view.rect} label="Standaardantwoorden" width={560}>
      <Command shouldFilter={false} value={selected?.id ?? ''} label="Standaardantwoorden" className="flex max-h-72">
        <Command.List className="w-full shrink-0 overflow-y-auto p-2 sm:w-56">
          <Command.Empty className="px-4 py-3 text-base text-muted">Geen standaardantwoorden gevonden.</Command.Empty>
          {view.items.map((t) => (
            <Command.Item
              key={t.id}
              value={t.id}
              onSelect={() => view.pick(t)}
              className="flex cursor-default flex-col rounded-md px-4 py-2 data-[selected=true]:bg-selected"
            >
              <span className="truncate text-base text-ink">{t.name}</span>
              <span className="t-label truncate">
                {t.shortcode ? `/${t.shortcode} · ` : ''}
                {scopeLabel[t.scope]}
              </span>
            </Command.Item>
          ))}
        </Command.List>
        {selected && (
          <div className="hidden min-w-0 flex-1 overflow-y-auto border-l border-line p-4 sm:block">
            {selected.subject && <p className="t-label mb-2">Onderwerp: {selected.subject}</p>}
            <ReadOnlyHtml key={selected.id} html={selected.body_html} ariaLabel="Voorbeeld" extensions={previewExtensions} />
          </div>
        )}
      </Command>
    </FloatingPanel>
  )
}
