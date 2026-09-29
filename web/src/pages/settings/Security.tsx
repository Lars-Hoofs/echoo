import { useMutation, useQuery, useQueryClient, useSuspenseQuery } from '@tanstack/react-query'
import { Check, KeyRound, Laptop, LogOut, ShieldCheck, ShieldOff, Smartphone } from 'lucide-react'
import { type SubmitEvent, useState } from 'react'

import { Dialog, DialogFooter } from '../../components/Dialog'
import { ChangePasswordForm, RecoveryCodes, TotpEnrollment } from '../../components/security'
import { Badge, Button, Card, ErrorNotice, Field, Input, Page, PageHeader, SettingRow, Skeleton } from '../../components/ui'
import { api } from '../../lib/api'
import { errorMessage, fieldError } from '../../lib/errors'
import { describeUserAgent, formatDateTime } from '../../lib/format'
import { meQuery } from '../../lib/session'

interface SessionInfo {
  id: string
  current: boolean
  created_at: string
  last_seen_at: string
  ip: string
  user_agent: string
}

export function SecurityPage() {
  const { data: me } = useSuspenseQuery(meQuery)
  const [passwordChanged, setPasswordChanged] = useState(false)
  // Keeps the enrollment (and its recovery codes) on screen after 2FA turns on.
  const [showingCodes, setShowingCodes] = useState(false)

  return (
    <Page>
      <PageHeader breadcrumb="Persoonlijk" title="Beveiliging" description="Je wachtwoord, tweestapsverificatie en de apparaten waarop je bent ingelogd." />
      <Card title="Wachtwoord" icon={<KeyRound size={16} />}>
        {passwordChanged && (
          <p className="mb-4 flex items-center gap-1.5 text-base text-ink">
            <Check size={16} aria-hidden />
            Wachtwoord gewijzigd. Je andere sessies zijn afgemeld.
          </p>
        )}
        <ChangePasswordForm onDone={() => setPasswordChanged(true)} />
      </Card>

      {me.user.mfa_enabled && !showingCodes ? (
        <MfaEnabled codesLeft={me.recovery_codes_remaining} />
      ) : (
        <Card
          title="Tweestapsverificatie"
          icon={<ShieldCheck size={16} />}
          description="Met een authenticator-app kan niemand inloggen met alleen je wachtwoord."
          actions={
            <Badge icon={me.user.mfa_enabled ? <ShieldCheck size={12} /> : <ShieldOff size={12} />}>
              {me.user.mfa_enabled ? 'Aan' : 'Uit'}
            </Badge>
          }
        >
          <TotpEnrollment onEnabled={() => setShowingCodes(true)} onDone={() => setShowingCodes(false)} />
        </Card>
      )}

      <Sessions />
    </Page>
  )
}

function MfaEnabled({ codesLeft }: { codesLeft: number }) {
  const [dialog, setDialog] = useState<'disable' | 'codes' | null>(null)
  return (
    <>
      <Card
        title="Tweestapsverificatie"
        icon={<ShieldCheck size={16} />}
        actions={
          <Badge dot>
            Aan
          </Badge>
        }
        flush
      >
        <SettingRow title="Authenticator-app" help="Bij het inloggen vragen we een code uit je authenticator-app.">
          <Button onClick={() => setDialog('disable')}>Uitschakelen</Button>
        </SettingRow>
        <SettingRow
          title="Herstelcodes"
          help={
            <span className="tabular-nums">
              {codesLeft === 1 ? 'Nog 1 code beschikbaar.' : `Nog ${codesLeft} codes beschikbaar.`}
              {codesLeft <= 3 && ' Maak nieuwe codes aan voordat ze op zijn.'}
            </span>
          }
        >
          <Button onClick={() => setDialog('codes')}>Nieuwe codes</Button>
        </SettingRow>
      </Card>
      <PasswordConfirmDialog
        open={dialog === 'disable'}
        onClose={() => setDialog(null)}
        title="Tweestapsverificatie uitschakelen"
        description="Daarna kun je inloggen met alleen je wachtwoord. Je herstelcodes vervallen."
        action="Uitschakelen"
        danger
        path="/me/totp/disable"
      />
      <PasswordConfirmDialog
        open={dialog === 'codes'}
        onClose={() => setDialog(null)}
        title="Nieuwe herstelcodes"
        description="Je huidige herstelcodes vervallen."
        action="Nieuwe codes maken"
        path="/me/recovery-codes"
      />
    </>
  )
}

function PasswordConfirmDialog({
  open,
  onClose,
  title,
  description,
  action,
  danger = false,
  path,
}: {
  open: boolean
  onClose: () => void
  title: string
  description: string
  action: string
  danger?: boolean
  path: string
}) {
  const [password, setPassword] = useState('')
  const queryClient = useQueryClient()
  const confirm = useMutation({
    mutationFn: () => api<{ recovery_codes?: string[] } | undefined>('POST', path, { password }),
    onSuccess: async () => {
      setPassword('')
      await queryClient.invalidateQueries({ queryKey: ['me'] })
    },
  })
  const close = () => {
    setPassword('')
    confirm.reset()
    onClose()
  }
  const codes = confirm.data?.recovery_codes

  const submit = (e: SubmitEvent) => {
    e.preventDefault()
    confirm.mutate()
  }

  return (
    <Dialog open={open} onOpenChange={(o) => !o && close()} title={title} description={codes ? undefined : description}>
      {codes ? (
        <div className="flex flex-col gap-4">
          <RecoveryCodes codes={codes} />
          <DialogFooter>
            <Button variant="primary" onClick={close}>
              Klaar
            </Button>
          </DialogFooter>
        </div>
      ) : confirm.isSuccess ? (
        <div className="flex flex-col gap-4">
          <p className="text-base">Tweestapsverificatie is uitgeschakeld.</p>
          <DialogFooter>
            <Button onClick={close}>Sluiten</Button>
          </DialogFooter>
        </div>
      ) : (
        <form onSubmit={submit} className="flex flex-col gap-4" noValidate>
          {confirm.isError && !fieldError(confirm.error, 'password') && <ErrorNotice>{errorMessage(confirm.error)}</ErrorNotice>}
          <Field label="Wachtwoord" error={fieldError(confirm.error, 'password')}>
            {(p) => <Input {...p} type="password" autoComplete="current-password" autoFocus value={password} onChange={(e) => setPassword(e.target.value)} />}
          </Field>
          <DialogFooter>
            <Button onClick={close}>Annuleren</Button>
            <Button type="submit" variant={danger ? 'danger' : 'primary'} busy={confirm.isPending} disabled={!password}>
              {action}
            </Button>
          </DialogFooter>
        </form>
      )}
    </Dialog>
  )
}

function Sessions() {
  const queryClient = useQueryClient()
  const sessions = useQuery({ queryKey: ['sessions'], queryFn: () => api<{ sessions: SessionInfo[] }>('GET', '/me/sessions') })
  const revoke = useMutation({
    mutationFn: (id: string) => api('DELETE', `/me/sessions/${id}`),
    onSettled: () => queryClient.invalidateQueries({ queryKey: ['sessions'] }),
  })
  const revokeOthers = useMutation({
    mutationFn: () => api('POST', '/me/sessions/revoke-others'),
    onSettled: () => queryClient.invalidateQueries({ queryKey: ['sessions'] }),
  })

  const others = sessions.data?.sessions.filter((s) => !s.current).length ?? 0
  const failure = revoke.error ?? revokeOthers.error

  return (
    <Card
      title="Sessies"
      icon={<Laptop size={16} />}
      description="Apparaten en browsers waarop je bent ingelogd."
      flush
      footer={
        <Button disabled={others === 0} busy={revokeOthers.isPending} onClick={() => revokeOthers.mutate()}>
          <LogOut size={14} aria-hidden />
          Alle andere sessies afmelden
        </Button>
      }
    >
      {sessions.isPending ? (
        <div className="p-4">
          <Skeleton />
        </div>
      ) : sessions.isError ? (
        <div className="p-4">
          <ErrorNotice>{errorMessage(sessions.error)}</ErrorNotice>
        </div>
      ) : (
        <ul className="divide-y divide-line">
          {sessions.data.sessions.map((s) => {
            const Device = /iPhone|iPad|Android/.test(s.user_agent) ? Smartphone : Laptop
            return (
              <li key={s.id} className="flex items-center gap-3 px-4 py-3 sm:px-5">
                <span aria-hidden className="flex size-8 shrink-0 items-center justify-center rounded-full bg-subtle text-muted">
                  <Device size={16} />
                </span>
                <div className="min-w-0 flex-1">
                  <div className="flex flex-wrap items-center gap-x-2 gap-y-1 text-base">
                    {describeUserAgent(s.user_agent)}
                    {s.current && <Badge dot>Deze sessie</Badge>}
                  </div>
                  <div className="text-sm text-faint tabular-nums">
                    {s.ip || 'Onbekend IP-adres'} · laatst actief {formatDateTime(s.last_seen_at)}
                  </div>
                </div>
                {!s.current && (
                  <Button variant="ghost" size="sm" busy={revoke.isPending && revoke.variables === s.id} onClick={() => revoke.mutate(s.id)}>
                    Afmelden
                  </Button>
                )}
              </li>
            )
          })}
        </ul>
      )}
      {failure && (
        <div className="border-t border-line p-4">
          <ErrorNotice>{errorMessage(failure)}</ErrorNotice>
        </div>
      )}
    </Card>
  )
}
