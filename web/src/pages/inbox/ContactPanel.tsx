import { useQuery } from '@tanstack/react-query'
import { Link } from '@tanstack/react-router'
import { Check, Copy, Plus } from 'lucide-react'
import { type ReactNode, useEffect, useState } from 'react'

import { Avatar } from '../../components/Avatar'
import { LabelChip } from '../../components/LabelChip'
import { StatusBadge } from '../../components/StatusBadge'
import { Num } from '../../components/Num'
import { Reveal } from '../../components/Reveal'
import { IconButton } from '../../components/ui'
import { ContactPanelExtras, ConversationAttributes } from '../contacts/PanelExtras'
import { type ConversationDetail, priorityLabel } from '../../lib/inbox'
import { hasPermission, meQuery } from '../../lib/session'
import { type PickerKind, usePickers } from './ActionPickers'
import { useContactHistory } from './useContactHistory'
import { useConversationActions } from './useConversationActions'

function CopyButton({ value, label }: { value: string; label: string }) {
  const [state, setState] = useState<'idle' | 'copied' | 'failed'>('idle')
  useEffect(() => {
    if (state === 'idle') return
    const t = setTimeout(() => {
      setState('idle')
    }, 2000)
    return () => {
      clearTimeout(t)
    }
  }, [state])
  return (
    <IconButton
      label={label}
      onClick={() => {
        navigator.clipboard.writeText(value).then(
          () => {
            setState('copied')
          },
          () => {
            setState('failed')
          },
        )
      }}
      size="sm"
      className="shrink-0 border-transparent"
    >
      {state === 'copied' ? <Check size={16} aria-hidden /> : <Copy size={16} aria-hidden />}
      <span role="status" className="sr-only">
        {state === 'copied' ? 'Gekopieerd' : state === 'failed' ? 'Kopiëren mislukt' : ''}
      </span>
    </IconButton>
  )
}

function Row({ label, children }: { label: string; children: ReactNode }) {
  return (
    <div className="flex min-h-11 items-center justify-between gap-3 py-2">
      <dt className="t-label shrink-0">{label}</dt>
      <dd className="flex min-w-0 items-center gap-2 text-right text-base text-ink">{children}</dd>
    </div>
  )
}

export function ContactPanel({ data }: { data: ConversationDetail }) {
  const { conversation: c, contact } = data
  const me = useQuery(meQuery)
  const apply = useConversationActions()
  const { openPicker } = usePickers()
  const editable = c.can_write && hasPermission(me.data, 'conversations.write')
  const canAssign = c.can_write && hasPermission(me.data, 'conversations.assign')
  const edit = (kind: PickerKind, children: ReactNode) =>
    (kind === 'assign' ? canAssign : editable) ? (
      <button
        type="button"
        onClick={() => openPicker(kind, [c])}
        className="relative -mr-3 inline-flex min-h-8 max-w-full items-center gap-2 rounded-full px-3 whitespace-nowrap hover:bg-subtle max-md:min-h-11"
      >
        {children}
        <span className="sr-only">, wijzigen</span>
      </button>
    ) : (
      children
    )
  const name = contact?.name || contact?.email || 'Onbekende afzender'
  const history = useContactHistory(contact?.id)
  return (
    <div className="flex h-full flex-col overflow-y-auto">
      <div className="flex flex-col items-center gap-3 px-6 pt-8 pb-6 text-center">
        <Avatar name={name} size={64} />
        <h2 className="t-title max-w-full wrap-anywhere">
          {contact ? (
            <Link to="/contacten/$id" params={{ id: contact.id }} className="hover:underline">
              {name}
            </Link>
          ) : (
            name
          )}
        </h2>
        {contact?.email && (
          <div className="-mt-2 flex max-w-full items-center gap-1">
            <span className="t-body min-w-0 truncate">{contact.email}</span>
            <CopyButton value={contact.email} label="E-mailadres kopiëren" />
          </div>
        )}
      </div>
      {contact && (
        <Reveal className="px-6 pb-6">
          <div className="rounded-lg bg-subtle p-4">
            <p className="t-label mb-3">Dit contact</p>
            <div className="grid grid-cols-3 gap-3">
              <div>
                <Num value={contact.conversation_count} size="s" />
                <p className="t-label mt-2">Gesprekken</p>
              </div>
              <div>
                <Num value={history.data?.open ?? '–'} {...(history.data?.partial ? { unit: '+' } : {})} size="s" />
                <p className="t-label mt-2">Open</p>
              </div>
              <div>
                <Num value={history.data?.waitingOnUs ?? '–'} {...(history.data?.partial ? { unit: '+' } : {})} size="s" />
                <p className="t-label mt-2 flex items-center gap-2">
                  {history.data && history.data.waitingOnUs > 0 && <span aria-hidden className="size-2 shrink-0 rounded-full bg-accent" />}
                  Wacht op ons
                </p>
              </div>
            </div>
          </div>
        </Reveal>
      )}
      <div className="px-6 pb-6">
        {contact && (
          <>
            <dl className="divide-y divide-line">
              <Row label="Organisatie">{contact.organization?.name ?? '—'}</Row>
            </dl>
            <ContactPanelExtras contactId={contact.id} />
          </>
        )}
      </div>
      <section className="border-t border-line px-6 py-6" aria-labelledby="conversation-heading">
        <h3 id="conversation-heading" className="t-label mb-2">
          Gesprek
        </h3>
        <dl className="divide-y divide-line">
          <Row label="Mailbox">{c.mailbox.name}</Row>
          <Row label="Status">
            <StatusBadge status={c.status} />
          </Row>
          <Row label="Toegewezen aan">
            {edit(
              'assign',
              c.assignee ? (
                <>
                  <Avatar name={c.assignee.name} size={24} />
                  <span className="truncate">{c.assignee.name}</span>
                </>
              ) : (
                <span className="text-muted">Niemand</span>
              ),
            )}
          </Row>
          <Row label="Team">{edit('assign', c.team ? c.team.name : <span className="text-muted">Geen team</span>)}</Row>
          <Row label="Prioriteit">{edit('priority', priorityLabel[c.priority])}</Row>
          <div className="flex flex-col gap-2 py-3">
            <dt className="t-label">Labels</dt>
            <dd className="flex flex-wrap items-center gap-2">
              {c.labels.map((l) => (
                <LabelChip
                  key={l.id}
                  label={l}
                  {...(editable ? { onRemove: () => void apply([c], { removeLabelIds: [l.id] }) } : {})}
                />
              ))}
              {editable && (
                <button
                  type="button"
                  onClick={() => openPicker('label', [c])}
                  className="inline-flex h-8 items-center gap-1 rounded-full border border-dashed border-line-strong px-3 text-base text-muted hover:bg-subtle hover:text-ink max-md:h-11"
                >
                  <Plus size={16} aria-hidden />
                  Label toevoegen
                </button>
              )}
              {!editable && c.labels.length === 0 && <span className="text-base text-muted">Geen</span>}
            </dd>
          </div>
        </dl>
        <ConversationAttributes conversationId={c.id} editable={editable} />
      </section>
    </div>
  )
}
