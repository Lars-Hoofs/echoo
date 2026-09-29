import { useMutation, useQuery, useQueryClient, useSuspenseQuery } from '@tanstack/react-query'
import { getRouteApi } from '@tanstack/react-router'
import { Check, ChevronLeft, CircleAlert, CircleDashed, CirclePause, CircleX, Inbox, Link2, Mail, Pencil, Plus, Power, Send, TriangleAlert, Users } from 'lucide-react'
import { type SubmitEvent, useState } from 'react'

import {
  Button,
  Card,
  EmptyState,
  ErrorNotice,
  Field,
  Input,
  Page,
  PageHeader,
  Segmented,
  Select,
  SettingRow,
  Skeleton,
  Switch,
  Table,
  TBody,
  Td,
  Th,
  THead,
  Tr,
} from '../../components/ui'
import { CopyButton } from '../../components/CopyButton'
import { Dialog, DialogFooter } from '../../components/Dialog'
import { CsatSettingsCard } from './CsatSettings'
import { api } from '../../lib/api'
import { errorMessage, fieldError, probeText } from '../../lib/errors'
import { formatDateTime } from '../../lib/format'
import { type AccessLevel, authTypeLabel, type Mailbox, type OAuthProvider, oauthErrorText, type ProbeResult, syncStatus, type SyncGlyph, type TlsMode } from '../../lib/mailbox'
import { meQuery } from '../../lib/session'
import { MailboxSignatureCard } from './Signatures'

interface Team {
  id: string
  name: string
}

interface AccessEntry {
  team_id: string
  level: AccessLevel
}

type TeamAccess = Record<string, AccessLevel | 'none'>

const glyphs: Record<SyncGlyph, typeof Check> = {
  ok: Check,
  waiting: CircleDashed,
  problem: TriangleAlert,
  error: CircleX,
  off: CirclePause,
}

function StatusLine({ mailbox }: { mailbox: Mailbox }) {
  const { glyph, text } = syncStatus(mailbox)
  const Glyph = glyphs[glyph]
  const tone = glyph === 'error' || glyph === 'problem' ? 'text-danger-text' : glyph === 'ok' ? 'text-muted' : 'text-faint'
  return (
    <div className={`flex items-start gap-1.5 text-sm ${tone}`}>
      <Glyph size={14} aria-hidden className="mt-0.5 shrink-0" />
      <span>{text}</span>
    </div>
  )
}

const route = getRouteApi('/auth/ready/settings/admin/instellingen/mailboxen')

function OAuthResult() {
  const { oauth, oauth_error: oauthError } = route.useSearch()
  if (oauthError) return <ErrorNotice>{oauthErrorText(oauthError)}</ErrorNotice>
  if (oauth !== 'connected') return null
  return (
    <p role="status" className="card card-line card-s flex items-center gap-2 text-base text-ink">
      <Check size={14} aria-hidden />
      De mailbox is verbonden. Stel via Bewerken in welke teams de mailbox zien.
    </p>
  )
}

export function MailboxesPage() {
  const mailboxes = useQuery({ queryKey: ['mailboxes'], queryFn: () => api<{ mailboxes: Mailbox[] }>('GET', '/mailboxes') })
  const [editing, setEditing] = useState<Mailbox | 'new' | null>(null)

  if (editing) {
    return <MailboxForm mailbox={editing === 'new' ? null : editing} onDone={() => setEditing(null)} />
  }
  return (
    <Page>
      <OAuthResult />
      <PageHeader
        breadcrumb="Werkruimte"
        title="Mailboxen"
        description="De mailboxen waaruit Echoo e-mail ophaalt en waarmee antwoorden worden verstuurd."
        actions={
          <Button variant="primary" onClick={() => setEditing('new')}>
            <Plus size={16} aria-hidden />
            Mailbox toevoegen
          </Button>
        }
      />
      {mailboxes.isPending ? (
        <Skeleton className="h-40" />
      ) : mailboxes.isError ? (
        <ErrorNotice>{errorMessage(mailboxes.error)}</ErrorNotice>
      ) : mailboxes.data.mailboxes.length === 0 ? (
        <Card>
          <EmptyState
            icon={<Inbox size={20} />}
            title="Nog geen mailboxen"
            description="Koppel een mailbox om e-mail in Echoo te ontvangen."
            action={
              <Button variant="primary" onClick={() => setEditing('new')}>
                Koppel de eerste mailbox
              </Button>
            }
          />
        </Card>
      ) : (
        <Card flush>
          <Table>
            <THead>
              <Th>Mailbox</Th>
              <Th className="hidden md:table-cell">Status</Th>
              <Th className="w-px">
                <span className="sr-only">Acties</span>
              </Th>
            </THead>
            <TBody>
              {mailboxes.data.mailboxes.map((mb) => (
                <MailboxRow key={mb.id} mailbox={mb} onEdit={() => setEditing(mb)} />
              ))}
            </TBody>
          </Table>
        </Card>
      )}
    </Page>
  )
}

function MailboxRow({ mailbox, onEdit }: { mailbox: Mailbox; onEdit: () => void }) {
  const queryClient = useQueryClient()
  const disabled = mailbox.disabled_at !== null
  const [confirming, setConfirming] = useState(false)
  const toggle = useMutation({
    mutationFn: () => api('POST', `/mailboxes/${mailbox.id}/${disabled ? 'enable' : 'disable'}`),
    onSuccess: async () => {
      setConfirming(false)
      await queryClient.invalidateQueries({ queryKey: ['mailboxes'] })
    },
  })
  return (
    <Tr>
      <Td>
        <div className="flex items-start gap-3">
          <span aria-hidden className="flex size-8 shrink-0 items-center justify-center rounded-full bg-subtle text-muted">
            <Mail size={16} />
          </span>
          <div className="max-w-32 min-w-0 sm:max-w-xs md:max-w-none">
            <div className={`truncate ${disabled ? 'text-faint' : 'text-ink'}`}>{mailbox.name}</div>
            <div className="truncate text-sm text-faint">
              {mailbox.email_address}
              {mailbox.auth_type !== 'password' && ` · ${authTypeLabel[mailbox.auth_type]}`}
            </div>
            <div className="mt-1 md:hidden">
              <StatusLine mailbox={mailbox} />
            </div>
          </div>
        </div>
        {toggle.isError && (
          <div className="mt-2">
            <ErrorNotice>{errorMessage(toggle.error)}</ErrorNotice>
          </div>
        )}
      </Td>
      <Td className="hidden md:table-cell">
        <StatusLine mailbox={mailbox} />
      </Td>
      <Td>
        <div className="flex items-center justify-end gap-1">
          <Button size="sm" onClick={onEdit} aria-label={`${mailbox.name} bewerken`}>
            <Pencil size={14} aria-hidden />
            <span className="max-md:sr-only">Bewerken</span>
          </Button>
          <Button
            variant="ghost"
            size="sm"
            busy={toggle.isPending && disabled}
            onClick={() => (disabled ? toggle.mutate() : setConfirming(true))}
            aria-label={`${mailbox.name} ${disabled ? 'inschakelen' : 'uitschakelen'}`}
          >
            <Power size={14} aria-hidden />
            <span className="max-md:sr-only">{disabled ? 'Inschakelen' : 'Uitschakelen'}</span>
          </Button>
        </div>
        {confirming && (
          <Dialog open onOpenChange={(o) => !o && setConfirming(false)} title="Mailbox uitschakelen">
            <div className="flex flex-col gap-4">
              <p className="text-base text-muted">
                Echoo haalt dan geen mail meer op voor {mailbox.name} en verstuurt er niets meer mee. Gesprekken en berichten blijven bewaard. Je kunt de mailbox later weer inschakelen.
              </p>
              {toggle.isError && <ErrorNotice>{errorMessage(toggle.error)}</ErrorNotice>}
              <DialogFooter>
                <Button onClick={() => setConfirming(false)}>Annuleren</Button>
                <Button variant="danger" busy={toggle.isPending} onClick={() => toggle.mutate()}>
                  Uitschakelen
                </Button>
              </DialogFooter>
            </div>
          </Dialog>
        )}
      </Td>
    </Tr>
  )
}

interface FormState {
  name: string
  email_address: string
  display_name: string
  imap_host: string
  imap_port: string
  imap_tls: TlsMode
  imap_username: string
  imap_password: string
  smtp_host: string
  smtp_port: string
  smtp_tls: TlsMode
  smtp_username: string
  smtp_password: string
  sent_folder: string
  send_delay_seconds: string
  allow_internal_host: boolean
}

function initialState(mb: Mailbox | null): FormState {
  return {
    name: mb?.name ?? '',
    email_address: mb?.email_address ?? '',
    display_name: mb?.display_name ?? '',
    imap_host: mb?.imap_host ?? '',
    imap_port: String(mb?.imap_port ?? 993),
    imap_tls: mb?.imap_tls ?? 'implicit',
    imap_username: mb?.imap_username ?? '',
    imap_password: '',
    smtp_host: mb?.smtp_host ?? '',
    smtp_port: String(mb?.smtp_port ?? 465),
    smtp_tls: mb?.smtp_tls ?? 'implicit',
    smtp_username: mb?.smtp_username ?? '',
    smtp_password: '',
    sent_folder: mb?.sent_folder ?? '',
    send_delay_seconds: String(mb?.send_delay_seconds ?? 5),
    allow_internal_host: mb?.allow_internal_host ?? false,
  }
}

// Anything that is not a whole number becomes -1, which the API rejects, instead of silently
// falling back to a default.
function toInt(s: string): number {
  const n = Number(s.trim())
  return s.trim() !== '' && Number.isInteger(n) ? n : -1
}

const tlsOptions: { value: TlsMode; label: string }[] = [
  { value: 'implicit', label: 'SSL/TLS' },
  { value: 'starttls', label: 'STARTTLS' },
]

const keepPasswordHelp =
  'Laat leeg om het huidige wachtwoord te behouden. Wijzig je de server, poort, versleuteling of gebruikersnaam, vul het wachtwoord dan opnieuw in.'

function MailboxForm({ mailbox, onDone }: { mailbox: Mailbox | null; onDone: () => void }) {
  const queryClient = useQueryClient()
  const { data: me } = useSuspenseQuery(meQuery)
  const isOwner = me.user.role === 'owner'
  const [saved, setSaved] = useState(mailbox)
  const [form, setForm] = useState(() => initialState(mailbox))
  const [access, setAccess] = useState<TeamAccess | null>(null)
  const [choice, setChoice] = useState<'password' | OAuthProvider['id']>('password')
  const oauthProviders = useQuery({
    queryKey: ['mailboxes', 'oauth-providers'],
    queryFn: () => api<{ providers: OAuthProvider[] }>('GET', '/mailboxes/oauth/providers'),
  })
  const providers = oauthProviders.data?.providers ?? []
  const viaOAuth = mailbox ? mailbox.auth_type !== 'password' : choice !== 'password'
  const set = <K extends keyof FormState>(key: K, value: FormState[K]) => setForm((f) => ({ ...f, [key]: value }))

  const teams = useQuery({ queryKey: ['teams'], queryFn: () => api<{ teams: Team[] }>('GET', '/teams') })
  const accessList = useQuery({
    queryKey: ['mailboxes', mailbox?.id, 'access'],
    queryFn: () => api<{ access: AccessEntry[] }>('GET', `/mailboxes/${mailbox?.id}/access`),
    enabled: mailbox !== null,
  })
  const storedAccess: TeamAccess = Object.fromEntries((accessList.data?.access ?? []).map((a) => [a.team_id, a.level]))
  const currentAccess = access ?? storedAccess

  const payload = () => (viaOAuth ? oauthPayload() : passwordPayload())
  const oauthPayload = () => ({
    name: form.name,
    display_name: form.display_name,
    sent_folder: form.sent_folder,
    send_delay_seconds: toInt(form.send_delay_seconds),
  })
  const passwordPayload = () => ({
    name: form.name,
    email_address: form.email_address,
    display_name: form.display_name,
    imap_host: form.imap_host,
    imap_port: toInt(form.imap_port),
    imap_tls: form.imap_tls,
    imap_username: form.imap_username,
    ...(form.imap_password ? { imap_password: form.imap_password } : {}),
    smtp_host: form.smtp_host,
    smtp_port: toInt(form.smtp_port),
    smtp_tls: form.smtp_tls,
    smtp_username: form.smtp_username,
    ...(form.smtp_password ? { smtp_password: form.smtp_password } : {}),
    sent_folder: form.sent_folder,
    send_delay_seconds: toInt(form.send_delay_seconds),
    ...(isOwner ? { allow_internal_host: form.allow_internal_host } : {}),
  })

  const save = useMutation({
    mutationFn: async () => {
      let current = saved
      if (current) {
        await api('PATCH', `/mailboxes/${current.id}`, payload())
      } else {
        // Remember the new mailbox right away, so a failing access update never leads to a second one.
        current = (await api<{ mailbox: Mailbox }>('POST', '/mailboxes', payload())).mailbox
        setSaved(current)
      }
      if (access) {
        const entries = Object.entries(access).flatMap(([team_id, level]) => (level === 'none' ? [] : [{ team_id, level }]))
        await api('PUT', `/mailboxes/${current.id}/access`, { access: entries })
      }
    },
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: ['mailboxes'] })
      onDone()
    },
  })

  const probe = useMutation({
    mutationFn: () => api<{ imap: ProbeResult; smtp: ProbeResult }>('POST', '/mailboxes/test', { ...(saved ? { id: saved.id } : {}), ...payload() }),
  })

  const submit = (e: SubmitEvent) => {
    e.preventDefault()
    probe.reset()
    save.mutate()
  }
  const runProbe = () => {
    save.reset()
    probe.mutate()
  }
  const connect = useMutation({
    mutationFn: (provider: OAuthProvider['id']) => {
      const query = saved ? `?mailbox_id=${saved.id}` : form.name ? `?name=${encodeURIComponent(form.name)}` : ''
      return api<{ url: string }>('GET', `/mailboxes/oauth/${provider}/start${query}`)
    },
    onSuccess: (r) => window.location.assign(r.url),
  })
  const failure: unknown = save.error ?? probe.error ?? connect.error
  const err = (field: string) => fieldError(failure, field)

  const tlsField = (label: string, value: TlsMode, onChange: (v: TlsMode) => void) => (
    <div className="flex flex-col items-start gap-1.5">
      <span aria-hidden className="text-sm text-ink">
        Beveiliging
      </span>
      <Segmented label={label} value={value} options={tlsOptions} onChange={onChange} />
    </div>
  )

  const header = (
    <PageHeader
      title={mailbox ? mailbox.name : 'Mailbox toevoegen'}
      breadcrumb={
        <button type="button" onClick={onDone} className="-ml-1 inline-flex items-center gap-1 rounded-full px-1 py-0.5 text-ink hover:underline">
          <ChevronLeft size={14} aria-hidden />
          Mailboxen
        </button>
      }
    />
  )
  const choiceCard = providers.length > 0 && (
    <Card title="Verbinding" icon={<Link2 size={16} />} description="Kies hoe Echoo bij de mailbox inlogt.">
      <Segmented
        label="Type verbinding"
        value={choice}
        options={[{ value: 'password', label: 'IMAP/SMTP handmatig' }, ...providers.map((p) => ({ value: p.id, label: p.name }))]}
        onChange={setChoice}
      />
    </Card>
  )
  const provider = providers.find((p) => p.id === choice)

  if (!mailbox && provider) {
    return (
      <Page>
        {header}
        {failure !== null && <ErrorNotice>{errorMessage(failure)}</ErrorNotice>}
        {choiceCard}
        <Card title={`Verbinden met ${provider.name}`} icon={<Mail size={16} />}>
          <div className="flex flex-col gap-4">
            <ProviderHelp provider={provider} />
            <div className="max-w-sm">
              <Field label="Naam" help="Laat leeg om het e-mailadres van het account te gebruiken." error={err('name')}>
                {(p) => <Input {...p} maxLength={100} value={form.name} onChange={(e) => set('name', e.target.value)} />}
              </Field>
            </div>
            <div className="flex flex-wrap gap-2">
              <Button variant="primary" busy={connect.isPending} onClick={() => connect.mutate(provider.id)}>
                Verbinden met {provider.name}
              </Button>
              <Button onClick={onDone}>Annuleren</Button>
            </div>
          </div>
        </Card>
      </Page>
    )
  }

  return (
    <form onSubmit={submit} noValidate>
      <Page>
        {header}
        {failure !== null && <ErrorNotice>{errorMessage(failure)}</ErrorNotice>}
        {!mailbox && choiceCard}
        <Card title="Algemeen" icon={<Mail size={16} />} flush>
          <div className="grid gap-4 px-4 py-4 sm:grid-cols-2 sm:px-6 sm:py-6">
            <Field label="Naam" error={err('name')}>
              {(p) => <Input {...p} maxLength={100} value={form.name} onChange={(e) => set('name', e.target.value)} />}
            </Field>
            <Field label="E-mailadres" error={err('email_address')}>
              {(p) => <Input {...p} type="email" readOnly={viaOAuth} value={form.email_address} onChange={(e) => set('email_address', e.target.value)} />}
            </Field>
            <Field label="Weergavenaam" help="De naam die ontvangers zien bij je antwoorden." error={err('display_name')}>
              {(p) => <Input {...p} value={form.display_name} onChange={(e) => set('display_name', e.target.value)} />}
            </Field>
            <Field label="Verzendvertraging (seconden)" help="Zolang kun je een verstuurd antwoord nog terughalen." error={err('send_delay_seconds')}>
              {(p) => (
                <Input {...p} inputMode="numeric" className="max-w-24" value={form.send_delay_seconds} onChange={(e) => set('send_delay_seconds', e.target.value)} />
              )}
            </Field>
            <div className="sm:col-span-2">
              <Field
                label="Map met verzonden berichten"
                help="Echoo controleert hier of een antwoord al is verstuurd. Leeg laten slaat die controle over."
                error={err('sent_folder')}
              >
                {(p) => <Input {...p} className="sm:max-w-sm" value={form.sent_folder} onChange={(e) => set('sent_folder', e.target.value)} />}
              </Field>
            </div>
          </div>
          {isOwner && !viaOAuth && (
            <div className="border-t border-line">
              <SettingRow
                title="Interne mailserver toestaan"
                help="Nodig als de server op een intern of lokaal adres draait. Deze keuze wordt vastgelegd in het auditlog."
              >
                <Switch
                  checked={form.allow_internal_host}
                  label="Interne mailserver toestaan"
                  onChange={(v) => set('allow_internal_host', v)}
                />
              </SettingRow>
            </div>
          )}
        </Card>

        {viaOAuth && saved && <OAuthConnectionCard mailbox={saved} busy={connect.isPending} onReconnect={(id) => connect.mutate(id)} />}

        {!viaOAuth && (
          <>
        <Card title="Inkomend (IMAP)" icon={<Inbox size={16} />}>
          <div className="flex flex-col gap-4">
            <div className="grid gap-4 sm:grid-cols-[1fr_7rem]">
              <Field label="IMAP-server" error={err('imap_host')}>
                {(p) => <Input {...p} placeholder="imap.voorbeeld.nl" value={form.imap_host} onChange={(e) => set('imap_host', e.target.value)} />}
              </Field>
              <Field label="IMAP-poort" error={err('imap_port')}>
                {(p) => <Input {...p} inputMode="numeric" value={form.imap_port} onChange={(e) => set('imap_port', e.target.value)} />}
              </Field>
            </div>
            {tlsField('IMAP-beveiliging', form.imap_tls, (v) => set('imap_tls', v))}
            <div className="grid gap-4 sm:grid-cols-2">
              <Field label="IMAP-gebruikersnaam" error={err('imap_username')}>
                {(p) => <Input {...p} autoComplete="off" value={form.imap_username} onChange={(e) => set('imap_username', e.target.value)} />}
              </Field>
              <Field label="IMAP-wachtwoord" help={saved?.imap_password_set ? keepPasswordHelp : undefined} error={err('imap_password')}>
                {(p) => (
                  <Input
                    {...p}
                    type="password"
                    autoComplete="new-password"
                    placeholder={saved?.imap_password_set ? 'Ingesteld' : ''}
                    value={form.imap_password}
                    onChange={(e) => set('imap_password', e.target.value)}
                  />
                )}
              </Field>
            </div>
          </div>
        </Card>

        <Card title="Uitgaand (SMTP)" icon={<Send size={16} />}>
          <div className="flex flex-col gap-4">
            <div className="grid gap-4 sm:grid-cols-[1fr_7rem]">
              <Field label="SMTP-server" error={err('smtp_host')}>
                {(p) => <Input {...p} placeholder="smtp.voorbeeld.nl" value={form.smtp_host} onChange={(e) => set('smtp_host', e.target.value)} />}
              </Field>
              <Field label="SMTP-poort" error={err('smtp_port')}>
                {(p) => <Input {...p} inputMode="numeric" value={form.smtp_port} onChange={(e) => set('smtp_port', e.target.value)} />}
              </Field>
            </div>
            {tlsField('SMTP-beveiliging', form.smtp_tls, (v) => set('smtp_tls', v))}
            <div className="grid gap-4 sm:grid-cols-2">
              <Field label="SMTP-gebruikersnaam" help="Laat leeg als de server geen inlog vraagt." error={err('smtp_username')}>
                {(p) => <Input {...p} autoComplete="off" value={form.smtp_username} onChange={(e) => set('smtp_username', e.target.value)} />}
              </Field>
              <Field label="SMTP-wachtwoord" help={saved?.smtp_password_set ? keepPasswordHelp : undefined} error={err('smtp_password')}>
                {(p) => (
                  <Input
                    {...p}
                    type="password"
                    autoComplete="new-password"
                    placeholder={saved?.smtp_password_set ? 'Ingesteld' : ''}
                    value={form.smtp_password}
                    onChange={(e) => set('smtp_password', e.target.value)}
                  />
                )}
              </Field>
            </div>
          </div>
        </Card>

          </>
        )}

        <Card title="Toegang" icon={<Users size={16} />} description="Welke teams deze mailbox zien." flush>
          {teams.isPending || accessList.isPending ? (
            <div className="p-4">
              <Skeleton className="h-16" />
            </div>
          ) : teams.isError || accessList.isError ? (
            <div className="p-4">
              <ErrorNotice>{errorMessage(teams.error ?? accessList.error)}</ErrorNotice>
            </div>
          ) : teams.data.teams.length === 0 ? (
            <p className="px-4 py-4 text-base text-muted sm:px-6">Er zijn nog geen teams. Beheerders zien alle mailboxen.</p>
          ) : (
            <ul className="divide-y divide-line">
              {teams.data.teams.map((t) => (
                <li key={t.id} className="flex items-center justify-between gap-4 px-4 py-3 sm:px-6">
                  <label htmlFor={`access-${t.id}`} className="min-w-0 truncate text-base text-ink">
                    {t.name}
                  </label>
                  <Select
                    id={`access-${t.id}`}
                    className="w-44 shrink-0"
                    value={currentAccess[t.id] ?? 'none'}
                    onChange={(e) => setAccess({ ...currentAccess, [t.id]: e.target.value as AccessLevel | 'none' })}
                  >
                    <option value="none">Geen toegang</option>
                    <option value="read">Lezen</option>
                    <option value="write">Schrijven</option>
                  </Select>
                </li>
              ))}
            </ul>
          )}
          {err('access') && <p className="border-t border-line px-4 py-3 text-sm text-danger-text sm:px-6">{err('access')}</p>}
        </Card>

        {saved && <MailboxSignatureCard mailboxId={saved.id} />}
        {saved && <CsatSettingsCard mailboxId={saved.id} />}

        {probe.data && (
          <ul role="status" className="card card-line card-s flex flex-col gap-2">
            {(['imap', 'smtp'] as const).map((p) => (
              <li key={p} className={`flex items-center gap-2 text-base ${probe.data[p].ok ? 'text-ink' : 'text-danger-text'}`}>
                {probe.data[p].ok ? <Check size={14} aria-hidden /> : <CircleAlert size={14} aria-hidden />}
                {probeText(p === 'imap' ? 'IMAP' : 'SMTP', probe.data[p])}
              </li>
            ))}
          </ul>
        )}
        <div className="flex flex-wrap justify-between gap-2">
          <Button busy={probe.isPending} onClick={runProbe}>
            Verbinding testen
          </Button>
          <div className="flex gap-2">
            <Button onClick={onDone}>Annuleren</Button>
            <Button type="submit" variant="primary" busy={save.isPending}>
              Opslaan
            </Button>
          </div>
        </div>
      </Page>
    </form>
  )
}

function ProviderHelp({ provider }: { provider: OAuthProvider }) {
  const registrar = provider.id === 'google' ? 'de Google Cloud Console (OAuth-client van het type Webtoepassing)' : 'Microsoft Entra (app-registratie met het platform Web)'
  return (
    <div className="flex flex-col gap-2 text-base text-muted">
      <p>
        Registreer Echoo in {registrar} en voeg deze omleidings-URI toe. Zet het client-ID en het geheim daarna in de omgeving van Echoo.
      </p>
      <div className="flex flex-wrap items-center gap-2">
        <code className="rounded-md border border-line bg-subtle px-2 py-1 font-mono text-sm break-all text-ink select-all">{provider.redirect_uri}</code>
        <CopyButton text={provider.redirect_uri} />
      </div>
      <p>Je logt hierna in bij {provider.name} met het account van de mailbox. Echoo bewaart alleen een versleuteld toegangstoken, nooit het wachtwoord.</p>
    </div>
  )
}

function OAuthConnectionCard({ mailbox, busy, onReconnect }: { mailbox: Mailbox; busy: boolean; onReconnect: (provider: OAuthProvider['id']) => void }) {
  const provider = mailbox.auth_type === 'oauth_google' ? 'google' : 'microsoft'
  return (
    <Card
      title="Verbinding"
      icon={<Link2 size={16} />}
      footer={
        <Button busy={busy} onClick={() => onReconnect(provider)}>
          Opnieuw verbinden
        </Button>
      }
    >
      <div className="flex flex-col gap-2 text-base">
        <p className="text-ink">
          Verbonden als {mailbox.imap_username} via {authTypeLabel[mailbox.auth_type]}.
        </p>
        {mailbox.oauth_connected_at && <p className="text-sm text-faint">Laatst verbonden op {formatDateTime(mailbox.oauth_connected_at)}.</p>}
        <p className="text-sm text-muted">
          Opnieuw verbinden is nodig als het wachtwoord van het account is gewijzigd of de toegang is ingetrokken. Log dan in met hetzelfde account.
        </p>
      </div>
    </Card>
  )
}
