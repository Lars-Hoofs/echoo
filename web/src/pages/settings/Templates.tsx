import type { Editor } from '@tiptap/core'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { FileText, Mail as MailIcon, MessageSquareText, Pencil, Plus, Trash2 } from 'lucide-react'
import { type SubmitEvent, useState } from 'react'

import { ActionMenu } from '../../components/ActionMenu'
import { Dialog, DialogFooter } from '../../components/Dialog'
import { VariableHighlight } from '../../components/editor/extensions'
import { type EditorValue, RichEditor } from '../../components/editor/RichEditor'
import { Button, Card, EmptyState, ErrorNotice, Field, Input, Page, PageHeader, Select, Skeleton, Table, TBody, Td, Textarea, Th, THead, Tr } from '../../components/ui'
import { api } from '../../lib/api'
import { scopeLabel, type Template, type TemplateScope, templatesQuery, templateVariables } from '../../lib/composer'
import { errorMessage, fieldError } from '../../lib/errors'
import { hasPermission, meQuery } from '../../lib/session'

interface Named {
  id: string
  name: string
}

type Editing = { kind: 'new'; scope: TemplateScope } | { kind: 'edit'; template: Template }

const editorExtensions = [VariableHighlight]

export function TemplatesPage() {
  const me = useQuery(meQuery)
  const admin = hasPermission(me.data, 'templates.manage_shared')
  const visible = useQuery(templatesQuery())
  const managed = useQuery({ ...templatesQuery(true), enabled: admin })
  const teams = useQuery({ queryKey: ['teams'], queryFn: () => api<{ teams: Named[] }>('GET', '/teams'), enabled: admin })
  const mailboxes = useQuery({ queryKey: ['mailboxes'], queryFn: () => api<{ mailboxes: Named[] }>('GET', '/mailboxes'), enabled: admin })
  const [editing, setEditing] = useState<Editing | null>(null)
  const [deleting, setDeleting] = useState<Template | null>(null)

  const personal = (visible.data?.templates ?? []).filter((t) => t.scope === 'personal')
  const shared = (admin ? managed.data : visible.data)?.templates.filter((t) => t.scope !== 'personal') ?? []
  const target = (t: Template): string => {
    if (t.scope === 'team') return teams.data?.teams.find((x) => x.id === t.team_id)?.name ?? scopeLabel.team
    if (t.scope === 'mailbox') return mailboxes.data?.mailboxes.find((x) => x.id === t.mailbox_id)?.name ?? scopeLabel.mailbox
    return scopeLabel[t.scope]
  }

  const pending = visible.isPending || (admin && managed.isPending)
  const failed = visible.error ?? managed.error

  return (
    <Page>
      <PageHeader breadcrumb="Persoonlijk"
        title="Standaardantwoorden"
        description="Bouwstenen voor je antwoorden. Typ / in het antwoordveld om er een in te voegen."
        actions={
          <Button variant="primary" onClick={() => setEditing({ kind: 'new', scope: 'personal' })}>
            <Plus size={16} aria-hidden />
            Standaardantwoord toevoegen
          </Button>
        }
      />
      {pending ? (
        <Skeleton className="h-40" />
      ) : failed ? (
        <ErrorNotice>{errorMessage(failed)}</ErrorNotice>
      ) : (
        <>
          <Card title="Persoonlijk" icon={<MessageSquareText size={16} />} description="Alleen voor jou." flush>
            <TemplateTable templates={personal} target={target} onEdit={(t) => setEditing({ kind: 'edit', template: t })} onDelete={setDeleting} empty="Je hebt nog geen persoonlijke standaardantwoorden." />
          </Card>
          <Card
            title="Werkruimte"
            icon={<MailIcon size={16} />}
            description={admin ? 'Voor teams, mailboxen of iedereen. Alleen beheerders wijzigen deze.' : 'Gedeeld door je beheerders. Je kunt ze gebruiken, niet wijzigen.'}
            actions={
              admin ? (
                <Button size="sm" onClick={() => setEditing({ kind: 'new', scope: 'global' })}>
                  <Plus size={14} aria-hidden />
                  Toevoegen
                </Button>
              ) : undefined
            }
            flush
          >
            <TemplateTable templates={shared} target={target} onEdit={(t) => setEditing({ kind: 'edit', template: t })} onDelete={setDeleting} empty="Er zijn nog geen gedeelde standaardantwoorden." />
          </Card>
          {admin && <FooterCard />}
        </>
      )}
      {editing && (
        <TemplateDialog
          editing={editing}
          admin={admin}
          teams={teams.data?.teams ?? []}
          mailboxes={mailboxes.data?.mailboxes ?? []}
          onClose={() => setEditing(null)}
        />
      )}
      {deleting && <DeleteDialog template={deleting} onClose={() => setDeleting(null)} />}
    </Page>
  )
}

function TemplateTable({
  templates,
  target,
  onEdit,
  onDelete,
  empty,
}: {
  templates: Template[]
  target: (t: Template) => string
  onEdit: (t: Template) => void
  onDelete: (t: Template) => void
  empty: string
}) {
  if (templates.length === 0) return <EmptyState icon={<FileText size={20} />} title={empty} />
  return (
    <Table>
      <THead>
        <Th>Naam</Th>
        <Th className="hidden sm:table-cell">Snelcode</Th>
        <Th className="hidden sm:table-cell">Voor</Th>
        <Th className="w-12">
          <span className="sr-only">Acties</span>
        </Th>
      </THead>
      <TBody>
        {templates.map((t) => (
          <Tr key={t.id}>
            <Td className="text-ink">{t.name}</Td>
            <Td className="hidden font-mono text-sm text-muted sm:table-cell">{t.shortcode ? `/${t.shortcode}` : ''}</Td>
            <Td className="hidden text-muted sm:table-cell">{target(t)}</Td>
            <Td>
              {t.editable && (
                <ActionMenu
                  label={`Acties voor ${t.name}`}
                  items={[
                    { label: 'Wijzigen', icon: <Pencil size={16} />, onSelect: () => onEdit(t) },
                    { label: 'Verwijderen', icon: <Trash2 size={16} />, danger: true, separated: true, onSelect: () => onDelete(t) },
                  ]}
                />
              )}
            </Td>
          </Tr>
        ))}
      </TBody>
    </Table>
  )
}

function TemplateDialog({
  editing,
  admin,
  teams,
  mailboxes,
  onClose,
}: {
  editing: Editing
  admin: boolean
  teams: Named[]
  mailboxes: Named[]
  onClose: () => void
}) {
  const qc = useQueryClient()
  const existing = editing.kind === 'edit' ? editing.template : null
  const [name, setName] = useState(existing?.name ?? '')
  const [shortcode, setShortcode] = useState(existing?.shortcode ?? '')
  const [scope, setScope] = useState<TemplateScope>(existing?.scope ?? (editing.kind === 'new' ? editing.scope : 'personal'))
  const [teamId, setTeamId] = useState(existing?.team_id ?? teams[0]?.id ?? '')
  const [mailboxId, setMailboxId] = useState(existing?.mailbox_id ?? mailboxes[0]?.id ?? '')
  const [subject, setSubject] = useState(existing?.subject ?? '')
  const [body, setBody] = useState<EditorValue>({ html: existing?.body_html ?? '', empty: !existing })
  const [editor, setEditor] = useState<Editor | null>(null)

  const save = useMutation({
    mutationFn: () => {
      const content = { name, shortcode, subject, body_html: body.html }
      if (existing) return api('PATCH', `/templates/${existing.id}`, content)
      return api('POST', '/templates', {
        ...content,
        scope,
        ...(scope === 'team' ? { team_id: teamId } : {}),
        ...(scope === 'mailbox' ? { mailbox_id: mailboxId } : {}),
      })
    },
    onSuccess: async () => {
      await qc.invalidateQueries({ queryKey: ['templates'] })
      onClose()
    },
  })
  const submit = (e: SubmitEvent) => {
    e.preventDefault()
    save.mutate()
  }
  const err = (field: string) => fieldError(save.error, field)
  const general = save.isError && !['name', 'shortcode', 'template_scope', 'team_id', 'mailbox_id', 'subject', 'body_html'].some((f) => err(f))

  return (
    <Dialog open onOpenChange={(open) => {
        if (!open) onClose()
      }} title={existing ? 'Standaardantwoord wijzigen' : 'Standaardantwoord toevoegen'} size="lg">
      <form onSubmit={submit} className="flex flex-col gap-4" noValidate>
        {general && <ErrorNotice>{errorMessage(save.error)}</ErrorNotice>}
        <Field label="Naam" error={err('name')}>
          {(p) => <Input {...p} maxLength={100} value={name} onChange={(e) => setName(e.target.value)} autoFocus />}
        </Field>
        <div className="grid gap-4 sm:grid-cols-2">
          <Field label="Snelcode" help="Optioneel. Typ / en dan deze code." error={err('shortcode')}>
            {(p) => <Input {...p} maxLength={32} value={shortcode} onChange={(e) => setShortcode(e.target.value)} />}
          </Field>
          <Field label="Voor wie" error={err('template_scope')}>
            {(p) => (
              <Select {...p} value={scope} disabled={existing !== null || !admin} onChange={(e) => setScope(e.target.value as TemplateScope)}>
                <option value="personal">Alleen ik</option>
                {admin && <option value="team">Een team</option>}
                {admin && <option value="mailbox">Een mailbox</option>}
                {admin && <option value="global">Iedereen</option>}
              </Select>
            )}
          </Field>
        </div>
        {scope === 'team' && (
          <Field label="Team" error={err('team_id')}>
            {(p) => (
              <Select {...p} value={teamId} disabled={existing !== null} onChange={(e) => setTeamId(e.target.value)}>
                {teams.map((t) => (
                  <option key={t.id} value={t.id}>
                    {t.name}
                  </option>
                ))}
              </Select>
            )}
          </Field>
        )}
        {scope === 'mailbox' && (
          <Field label="Mailbox" error={err('mailbox_id')}>
            {(p) => (
              <Select {...p} value={mailboxId} disabled={existing !== null} onChange={(e) => setMailboxId(e.target.value)}>
                {mailboxes.map((m) => (
                  <option key={m.id} value={m.id}>
                    {m.name}
                  </option>
                ))}
              </Select>
            )}
          </Field>
        )}
        <Field label="Onderwerp" help="Optioneel. Vervangt het onderwerp van het antwoord." error={err('subject')}>
          {(p) => <Input {...p} value={subject} onChange={(e) => setSubject(e.target.value)} />}
        </Field>
        <div>
          <span className="text-sm text-ink">Tekst</span>
          {err('body_html') && <p className="mt-1 text-sm text-danger-text">{err('body_html')}</p>}
          <div className="mt-1.5">
            <RichEditor
              initialHtml={body.html}
              ariaLabel="Tekst van het standaardantwoord"
              placeholder="Schrijf de tekst"
              extensions={editorExtensions}
              onChange={setBody}
              onEditor={setEditor}
            />
          </div>
          <div className="mt-2 flex flex-wrap gap-1.5" role="group" aria-label="Variabelen invoegen">
            <span className="w-full text-sm text-faint">Klik in de tekst en voeg een variabele in:</span>
            <VariableButtons
              onInsert={(variable) => {
                editor?.chain().focus().insertContent(`{{${variable}}}`).run()
              }}
            />
          </div>
        </div>
        <DialogFooter>
          <Button onClick={onClose}>Annuleren</Button>
          <Button type="submit" variant="primary" busy={save.isPending} disabled={body.empty || name.trim() === ''}>
            Opslaan
          </Button>
        </DialogFooter>
      </form>
    </Dialog>
  )
}

function VariableButtons({ onInsert }: { onInsert: (name: string) => void }) {
  return (
    <>
      {templateVariables.map((v) => (
        <button
          key={v.name}
          type="button"
          title={`{{${v.name}}}`}
          onMouseDown={(e) => {
            // Keep the caret in the editor, so the variable lands where the person was typing.
            e.preventDefault()
          }}
          onClick={() => onInsert(v.name)}
          className="tag border border-line bg-transparent transition-colors hover:border-line-strong hover:text-ink"
        >
          {v.label}
        </button>
      ))}
    </>
  )
}

function DeleteDialog({ template, onClose }: { template: Template; onClose: () => void }) {
  const qc = useQueryClient()
  const remove = useMutation({
    mutationFn: () => api('DELETE', `/templates/${template.id}`),
    onSuccess: async () => {
      await qc.invalidateQueries({ queryKey: ['templates'] })
      onClose()
    },
  })
  return (
    <Dialog open onOpenChange={(open) => {
        if (!open) onClose()
      }} title="Standaardantwoord verwijderen" size="sm" description={`“${template.name}” wordt verwijderd voor iedereen die het gebruikt.`}>
      <div className="flex flex-col gap-4">
        {remove.isError && <ErrorNotice>{errorMessage(remove.error)}</ErrorNotice>}
        <DialogFooter>
          <Button onClick={onClose}>Annuleren</Button>
          <Button variant="danger" busy={remove.isPending} onClick={() => remove.mutate()}>
            Verwijderen
          </Button>
        </DialogFooter>
      </div>
    </Dialog>
  )
}

function FooterCard() {
  const qc = useQueryClient()
  const settings = useQuery({ queryKey: ['settings', 'email'], queryFn: () => api<{ footer_text: string }>('GET', '/settings/email') })
  const [text, setText] = useState<string | null>(null)
  const save = useMutation({
    mutationFn: (footer_text: string) => api<{ footer_text: string }>('PUT', '/settings/email', { footer_text }),
    onSuccess: async () => {
      await qc.invalidateQueries({ queryKey: ['settings', 'email'] })
      setText(null)
    },
  })
  const value = text ?? settings.data?.footer_text ?? ''
  return (
    <Card
      title="Voettekst van uitgaande e-mail"
      icon={<MailIcon size={16} />}
      description="Staat onderaan elk bericht dat je verstuurt, onder de handtekening. Bovenaan staat de naam van de mailbox."
      footer={
        <Button variant="primary" busy={save.isPending} disabled={text === null || text === settings.data?.footer_text} onClick={() => save.mutate(value)}>
          Opslaan
        </Button>
      }
    >
      {settings.isPending ? (
        <Skeleton className="h-16" />
      ) : settings.isError ? (
        <ErrorNotice>{errorMessage(settings.error)}</ErrorNotice>
      ) : (
        <div className="max-w-xl">
          <Field label="Voettekst" help="Bijvoorbeeld je bedrijfsnaam en adres. Maximaal 300 tekens." error={fieldError(save.error, 'footer_text')}>
            {(p) => <Textarea {...p} rows={2} maxLength={300} value={value} onChange={(e) => setText(e.target.value)} />}
          </Field>
          {save.isError && !fieldError(save.error, 'footer_text') && <ErrorNotice>{errorMessage(save.error)}</ErrorNotice>}
        </div>
      )}
    </Card>
  )
}
