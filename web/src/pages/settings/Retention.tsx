import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Database, KeyRound, Trash2 } from 'lucide-react'
import { useState } from 'react'

import { mailboxesQuery } from '../../components/automation/directory'
import { Num } from '../../components/Num'
import { Badge, Button, Card, ErrorNotice, Field, Input, Page, PageHeader, SettingRow, Skeleton, Switch } from '../../components/ui'
import { errorMessage } from '../../lib/errors'
import { api } from '../../lib/api'
import { formatBytes, formatDateTime } from '../../lib/format'
import {
  auditFromText,
  fromText,
  type Impact,
  keyStatusQuery,
  type PeriodText,
  type RetentionInput,
  type RetentionPreview,
  retentionQuery,
  type RetentionSettings,
  toText,
} from '../../lib/retention'
import { meQuery } from '../../lib/session'

interface Form {
  global: PeriodText
  audit: string
  overrides: Record<string, PeriodText>
}

function toForm(s: RetentionSettings): Form {
  return {
    global: toText(s.global),
    audit: s.audit_months === null ? '' : String(s.audit_months),
    overrides: Object.fromEntries(s.mailboxes.map((m) => [m.mailbox_id, toText(m)])),
  }
}

// Returns the request body, or undefined while a field is not a valid number.
function toInput(f: Form): RetentionInput | undefined {
  const global = fromText(f.global)
  const audit = auditFromText(f.audit)
  if (!global || audit === undefined) return undefined
  const mailboxes: RetentionInput['mailboxes'] = []
  for (const [mailbox_id, text] of Object.entries(f.overrides)) {
    const periods = fromText(text)
    if (!periods) return undefined
    mailboxes.push({ mailbox_id, ...periods })
  }
  return { global, audit_months: audit, mailboxes }
}

function PeriodFields({ value, onChange, disabled }: { value: PeriodText; onChange: (next: PeriodText) => void; disabled?: boolean }) {
  const bad = (t: string) => t.trim() !== '' && !/^[1-9]\d*$/.test(t.trim())
  return (
    <div className="grid gap-4 sm:grid-cols-2">
      <Field label="Gesloten gesprekken verwijderen na (maanden)" help="Leeg: altijd bewaren." error={bad(value.closed) ? 'Vul een heel getal vanaf 1 in.' : undefined}>
        {(p) => <Input {...p} inputMode="numeric" value={value.closed} disabled={disabled} onChange={(e) => onChange({ ...value, closed: e.target.value })} />}
      </Field>
      <Field label="Bijlagen verwijderen na (maanden)" help="De tekst van het bericht blijft staan." error={bad(value.attachments) ? 'Vul een heel getal vanaf 1 in.' : undefined}>
        {(p) => <Input {...p} inputMode="numeric" value={value.attachments} disabled={disabled} onChange={(e) => onChange({ ...value, attachments: e.target.value })} />}
      </Field>
      <Field label="Spam verwijderen na (dagen)" error={bad(value.spam) ? 'Vul een heel getal vanaf 1 in.' : undefined}>
        {(p) => <Input {...p} inputMode="numeric" value={value.spam} disabled={disabled} onChange={(e) => onChange({ ...value, spam: e.target.value })} />}
      </Field>
      <Field
        label="Prullenbak legen na (dagen)"
        help="Gerekend vanaf het verwijderen. Leeg: nooit vanzelf legen."
        error={bad(value.trash) ? 'Vul een heel getal vanaf 1 in.' : undefined}
      >
        {(p) => <Input {...p} inputMode="numeric" value={value.trash} disabled={disabled} onChange={(e) => onChange({ ...value, trash: e.target.value })} />}
      </Field>
    </div>
  )
}

function impactText(i: Impact): string {
  return `${i.conversations} gesprekken, ${i.messages} berichten, ${i.attachments} bijlagen (${formatBytes(i.attachment_bytes)})`
}

function PreviewResult({ p }: { p: RetentionPreview }) {
  return (
    <div className="rounded-md border border-line bg-subtle px-4 py-3 text-base text-ink">
      <p>Als je dit nu opslaat, verwijdert de eerstvolgende opruiming:</p>
      <ul className="mt-2 list-disc pl-5 text-muted">
        <li>Gesloten gesprekken: {impactText(p.closed_conversations)}</li>
        <li>Spam: {impactText(p.spam_conversations)}</li>
        <li>Prullenbak: {impactText(p.trash_conversations)}</li>
        <li>
          Losse bijlagen: {p.attachments.attachments} bijlagen ({formatBytes(p.attachments.attachment_bytes)})
        </li>
        <li>Auditlog: {p.audit_entries} regels</li>
      </ul>
    </div>
  )
}

export function RetentionPage() {
  const settings = useQuery(retentionQuery)
  return (
    <Page>
      <PageHeader breadcrumb="Werkruimte"
        title="Privacy en retentie"
        description="Hoe lang Echoo gesprekken, bijlagen en logboeken bewaart. Verwijderen is definitief en gebeurt elke nacht in porties."
      />
      {settings.isPending ? (
        <Skeleton />
      ) : settings.isError ? (
        <ErrorNotice>{errorMessage(settings.error)}</ErrorNotice>
      ) : (
        <RetentionForm settings={settings.data} />
      )}
    </Page>
  )
}

function RetentionForm({ settings }: { settings: RetentionSettings }) {
  const queryClient = useQueryClient()
  const mailboxes = useQuery(mailboxesQuery)
  const me = useQuery(meQuery)
  const [form, setForm] = useState(() => toForm(settings))

  const input = toInput(form)
  const preview = useMutation({ mutationFn: (body: RetentionInput) => api<RetentionPreview>('POST', '/settings/retention/preview', body) })
  const save = useMutation({
    mutationFn: (body: RetentionInput) => api<RetentionSettings>('PUT', '/settings/retention', body),
    onSuccess: (saved) => {
      queryClient.setQueryData(retentionQuery.queryKey, saved)
      setForm(toForm(saved))
      preview.reset()
    },
  })

  const change = (next: Form) => {
    setForm(next)
    preview.reset()
    save.reset()
  }

  return (
    <>
          <Card title="Voor de hele werkruimte" icon={<Trash2 size={16} />}>
            <div className="flex flex-col gap-5">
              <PeriodFields value={form.global} onChange={(global) => change({ ...form, global })} />
              <div className="max-w-xs">
                <Field
                  label="Auditlog bewaren (maanden)"
                  help="Minimaal 3. Leeg: altijd bewaren. Oude regels verdwijnen alleen via de opruiming, die dit zelf vastlegt."
                  error={input === undefined && auditFromText(form.audit) === undefined ? 'Vul een heel getal vanaf 3 in.' : undefined}
                >
                  {(p) => <Input {...p} inputMode="numeric" value={form.audit} onChange={(e) => change({ ...form, audit: e.target.value })} />}
                </Field>
              </div>
              <p className="text-sm text-muted">
                Vaste termijnen, niet instelbaar: webhooklog 14 dagen, uitvoeringen van regels 30 dagen, ongebruikte uploads uit het antwoordvenster 24 uur.
              </p>
            </div>
          </Card>

          <Card title="Per mailbox afwijken" description="Een mailbox met eigen termijnen negeert de termijnen hierboven, ook als je daar iets leeg laat." flush>
            {mailboxes.isPending ? (
              <div className="p-4">
                <Skeleton className="h-10" />
              </div>
            ) : mailboxes.isError ? (
              <div className="p-4">
                <ErrorNotice>{errorMessage(mailboxes.error)}</ErrorNotice>
              </div>
            ) : mailboxes.data.mailboxes.length === 0 ? (
              <p className="p-4 text-base text-muted">Er zijn nog geen mailboxen.</p>
            ) : (
              mailboxes.data.mailboxes.map((mb) => {
                const own = form.overrides[mb.id]
                return (
                  <div key={mb.id} className="border-b border-line last:border-b-0">
                    <SettingRow title={mb.name} help={own ? 'Eigen termijnen' : 'Volgt de hele werkruimte'}>
                      <Switch
                        checked={own !== undefined}
                        label={`Eigen termijnen voor ${mb.name}`}
                        onChange={(on) => {
                          const others = Object.entries(form.overrides).filter(([id]) => id !== mb.id)
                          change({ ...form, overrides: Object.fromEntries(on ? [...others, [mb.id, { ...form.global }]] : others) })
                        }}
                      />
                    </SettingRow>
                    {own && (
                      <div className="px-4 pb-4 sm:px-5">
                        <PeriodFields value={own} onChange={(next) => change({ ...form, overrides: { ...form.overrides, [mb.id]: next } })} />
                      </div>
                    )}
                  </div>
                )
              })
            )}
          </Card>

          <Card
            title="Controleren en opslaan"
            icon={<Database size={16} />}
            footer={
              <>
                <Button disabled={!input} busy={preview.isPending} onClick={() => input && preview.mutate(input)}>
                  Voorbeeld bekijken
                </Button>
                <Button variant="primary" disabled={!input} busy={save.isPending} onClick={() => input && save.mutate(input)}>
                  Opslaan
                </Button>
              </>
            }
          >
            <div className="flex flex-col gap-3">
              <p className="text-base text-muted">
                Bekijk eerst wat er nu zou verdwijnen. Er wordt niets verwijderd zolang je niet opslaat. Reservekopieën van vóór de opruiming bevatten de gegevens nog; die verlopen volgens jouw bewaarschema.
              </p>
              {preview.isError && <ErrorNotice>{errorMessage(preview.error)}</ErrorNotice>}
              {preview.data && <PreviewResult p={preview.data} />}
              {save.isError && <ErrorNotice>{errorMessage(save.error)}</ErrorNotice>}
              {save.isSuccess && <p role="status" className="text-base text-ink">Opgeslagen.</p>}
              {settings.last_run && (
                <p className="text-sm text-muted">
                  Laatste opruiming: {formatDateTime(settings.last_run.at)}. Verwijderd: {settings.last_run.closed_conversations} gesloten gesprekken,{' '}
                  {settings.last_run.spam_conversations} spamgesprekken, {settings.last_run.trash_conversations} gesprekken uit de prullenbak,{' '}
                  {settings.last_run.attachments} bijlagen, {settings.last_run.audit_entries} auditregels.
                </p>
              )}
            </div>
          </Card>

          {me.data?.user.role === 'owner' && <KeysCard />}
    </>
  )
}

function KeysCard() {
  const status = useQuery(keyStatusQuery)
  return (
    <Card
      title="Versleutelingssleutels"
      icon={<KeyRound size={16} />}
      description="Hoeveel opgeslagen wachtwoorden en geheimen elke sleutel nog beschermt. Een oude sleutel mag pas uit de configuratie als hij op 0 staat; zie docs/operations.md."
      flush
    >
      {status.isPending ? (
        <div className="p-4">
          <Skeleton className="h-10" />
        </div>
      ) : status.isError ? (
        <div className="p-4">
          <ErrorNotice>{errorMessage(status.error)}</ErrorNotice>
        </div>
      ) : (
        <>
          {status.data.keys.map((k) => (
            <SettingRow key={k.id} title={`Sleutel ${k.id}`} help={k.active ? 'Versleutelt nieuwe waarden.' : k.values === 0 ? 'Wordt niet meer gebruikt.' : 'Wordt nog gebruikt; draai de sleutelrotatie.'}>
              {k.active || k.values === 0 ? <Num value={k.values} size="s" unit="waarden" /> : <Badge tone="danger">{k.values} waarden</Badge>}
            </SettingRow>
          ))}
          {status.data.unknown.map((k) => (
            <SettingRow key={k.id} title={`Onbekende sleutel ${k.id}`} help="Deze waarden zijn onleesbaar: zet de sleutel terug in ECHOO_ENCRYPTION_KEYS.">
              <Badge tone="danger">{k.values} waarden</Badge>
            </SettingRow>
          ))}
        </>
      )}
    </Card>
  )
}

