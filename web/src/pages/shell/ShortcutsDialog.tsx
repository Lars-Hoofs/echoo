import { Dialog } from '../../components/Dialog'
import { Kbd } from '../../components/ui'
import { modKey, shortcutGroups } from '../../lib/shortcuts'

export function ShortcutsDialog({ open, onOpenChange }: { open: boolean; onOpenChange: (open: boolean) => void }) {
  return (
    <Dialog open={open} onOpenChange={onOpenChange} title="Sneltoetsen" description="Letters werken niet zolang je in een tekstveld typt." size="lg">
      <div className="flex flex-col gap-5">
        {shortcutGroups(modKey()).map((group) => (
          <section key={group.title} aria-label={group.title}>
            <h3 className="t-label mb-1">{group.title}</h3>
            <dl>
              {group.shortcuts.map((s) => (
                <div key={s.label} className="flex items-center justify-between gap-4 border-b border-line py-3 last:border-b-0">
                  <dt className="text-base text-ink">{s.label}</dt>
                  <dd className="flex shrink-0 items-center gap-1">
                    {s.keys.map((k, i) => (
                      <Kbd key={`${k}-${i}`}>{k}</Kbd>
                    ))}
                  </dd>
                </div>
              ))}
            </dl>
          </section>
        ))}
      </div>
    </Dialog>
  )
}
