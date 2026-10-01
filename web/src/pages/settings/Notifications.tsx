import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { BellRing, Share, Smartphone, SquarePlus, Trash2 } from 'lucide-react'

import { useToast } from '../../components/Toast'
import { Button, Card, EmptyState, ErrorNotice, IconButton, Page, PageHeader, SettingRow, Skeleton, Switch } from '../../components/ui'
import { api } from '../../lib/api'
import { errorMessage } from '../../lib/errors'
import { formatRelative } from '../../lib/format'
import { disablePush, enablePush, PushDeniedError, type PushConfig, type PushDevice, type PushPrefs, pushSupport } from '../../lib/push'
import { NotificationSettingsCard } from './NotificationSettings'

const devicesKey = ['push', 'devices']
const settingsKey = ['me', 'notification-settings']

export function NotificationsPage() {
  return (
    <Page>
      <PageHeader breadcrumb="Persoonlijk" title="Meldingen" description="Waar en waarover Echoo je waarschuwt als je niet in de app zit." />
      <PushCard />
      <PushKindsCard />
      <NotificationSettingsCard />
    </Page>
  )
}

const kindLabel: Record<PushDevice['kind'], string> = { webpush: 'Browser of web-app', apns: 'iOS-app', fcm: 'Android-app' }

function PushCard() {
  const qc = useQueryClient()
  const toast = useToast()
  const support = pushSupport()
  const config = useQuery({ queryKey: ['push', 'config'], queryFn: () => api<PushConfig>('GET', '/push/config') })
  const devices = useQuery({ queryKey: devicesKey, queryFn: () => api<{ devices: PushDevice[] }>('GET', '/push/devices').then((r) => r.devices) })
  const current = devices.data?.find((d) => d.current)

  const enable = useMutation({
    mutationFn: () => {
      if (!config.data) throw new Error('config not loaded')
      return enablePush(config.data)
    },
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: devicesKey })
      toast('Pushmeldingen staan aan op dit apparaat')
    },
  })
  const disable = useMutation({
    mutationFn: (d: PushDevice | undefined) => disablePush(d),
    onSuccess: () => void qc.invalidateQueries({ queryKey: devicesKey }),
  })
  // Another device is only removed on the server; its browser or app stops on its next push.
  const remove = useMutation({
    mutationFn: (d: PushDevice) => api('DELETE', `/push/devices/${encodeURIComponent(d.id)}`),
    onSuccess: () => void qc.invalidateQueries({ queryKey: devicesKey }),
  })
  const test = useMutation({
    mutationFn: () => api<{ results: { id: string; ok: boolean; error?: string }[] }>('POST', '/push/test'),
    onSuccess: (r) => {
      void qc.invalidateQueries({ queryKey: devicesKey })
      const ok = r.results.filter((x) => x.ok).length
      if (r.results.length === 0) toast('Er is nog geen apparaat aangemeld', { tone: 'error' })
      else if (ok === r.results.length) toast(ok === 1 ? 'Testmelding verstuurd' : `Testmelding naar ${ok} apparaten verstuurd`)
      else toast(`${ok} van ${r.results.length} apparaten bereikt`, { tone: 'error' })
    },
  })

  const enableError = enable.error instanceof PushDeniedError
    ? 'Meldingen zijn geblokkeerd voor Echoo. Sta ze toe in de instellingen van je browser of telefoon en probeer het opnieuw.'
    : enable.error
      ? errorMessage(enable.error)
      : null

  return (
    <Card title="Pushmeldingen" icon={<BellRing size={16} />} description="Een melding op je telefoon of computer, ook als Echoo dicht is." flush>
      {config.isPending || devices.isPending ? (
        <div className="p-4">
          <Skeleton className="h-16" />
        </div>
      ) : config.isError || devices.isError ? (
        <div className="p-4">
          <ErrorNotice>{errorMessage(config.error ?? devices.error)}</ErrorNotice>
        </div>
      ) : (
        <>
          {support === 'install' ? (
            <div className="flex flex-col gap-3 px-4 py-4 sm:px-5">
              <p className="text-base text-ink">Zet Echoo op je beginscherm om meldingen te krijgen.</p>
              <ol className="flex flex-col gap-2 text-base text-muted">
                <li className="flex items-center gap-2">
                  <Share size={16} aria-hidden className="shrink-0" /> Tik in Safari op Deel.
                </li>
                <li className="flex items-center gap-2">
                  <SquarePlus size={16} aria-hidden className="shrink-0" /> Kies Zet op beginscherm.
                </li>
                <li className="flex items-center gap-2">
                  <Smartphone size={16} aria-hidden className="shrink-0" /> Open Echoo vanaf het beginscherm en kom hier terug.
                </li>
              </ol>
            </div>
          ) : support === 'unsupported' ? (
            <p className="px-4 py-4 text-base text-muted sm:px-5">Deze browser ondersteunt geen pushmeldingen. Gebruik Chrome, Edge, Firefox of Safari.</p>
          ) : (
            <SettingRow title="Op dit apparaat" help={current ? `Aangemeld als ${current.label}.` : 'Je krijgt meldingen zolang je op dit apparaat bent ingelogd.'}>
              <Switch
                checked={Boolean(current)}
                label="Pushmeldingen op dit apparaat"
                disabled={enable.isPending || disable.isPending}
                onChange={(on) => (on ? enable.mutate() : disable.mutate(current))}
              />
            </SettingRow>
          )}
          {remove.isError && (
            <div className="px-4 pb-4 sm:px-5">
              <ErrorNotice>{errorMessage(remove.error)}</ErrorNotice>
            </div>
          )}
          {enableError && (
            <div className="px-4 pb-4 sm:px-5">
              <ErrorNotice>{enableError}</ErrorNotice>
            </div>
          )}
          <div className="border-t border-line">
            {devices.data.length === 0 ? (
              <EmptyState icon={<Smartphone size={20} />} title="Nog geen apparaten" description="Zet meldingen aan op elk apparaat waarop je bereikbaar wilt zijn." />
            ) : (
              <ul aria-label="Aangemelde apparaten" className="list">
                {devices.data.map((d) => (
                  <li key={d.id} className="list-row flex items-center gap-3 px-4 py-3 sm:px-5">
                    <Smartphone size={16} aria-hidden className="shrink-0 text-muted" />
                    <div className="flex min-w-0 flex-1 flex-col">
                      <span className="truncate text-ink">
                        {d.label || kindLabel[d.kind]}
                        {d.current && <span className="text-muted"> · dit apparaat</span>}
                      </span>
                      <span className="text-sm text-muted">
                        {kindLabel[d.kind]} · {d.last_push_at ? `laatste melding ${formatRelative(d.last_push_at)}` : 'nog geen melding ontvangen'}
                      </span>
                    </div>
                    <IconButton label={`${d.label || kindLabel[d.kind]} afmelden`} size="sm" onClick={() => (d.current ? disable.mutate(d) : remove.mutate(d))}>
                      <Trash2 aria-hidden />
                    </IconButton>
                  </li>
                ))}
              </ul>
            )}
          </div>
          {devices.data.length > 0 && (
            <div className="flex justify-end border-t border-line px-4 py-3 sm:px-5">
              <Button size="sm" busy={test.isPending} onClick={() => test.mutate()}>
                Testmelding sturen
              </Button>
            </div>
          )}
        </>
      )}
    </Card>
  )
}

function PushKindsCard() {
  const qc = useQueryClient()
  const settings = useQuery({ queryKey: settingsKey, queryFn: () => api<{ push: PushPrefs }>('GET', '/me/notification-settings') })
  const save = useMutation({
    mutationFn: (push: PushPrefs) => api<{ push: PushPrefs }>('PUT', '/me/notification-settings', { push }),
    onSuccess: (r) => qc.setQueryData<{ push: PushPrefs }>(settingsKey, (old) => (old ? { ...old, push: r.push } : old)),
  })
  if (!settings.data) return null
  const p = settings.data.push
  const rows: { key: keyof PushPrefs; title: string; help?: string }[] = [
    { key: 'replies', title: 'Als een klant antwoordt', help: 'In een gesprek dat aan jou is toegewezen.' },
    { key: 'assignments', title: 'Als een gesprek aan mij wordt toegewezen' },
    { key: 'mentions', title: 'Als iemand mij noemt', help: 'Bij een @vermelding in een notitie.' },
    { key: 'sla', title: 'Als een SLA-termijn in gevaar komt', help: 'Voor gesprekken die aan jou zijn toegewezen.' },
  ]
  return (
    <Card title="Waarover" description="Geldt voor al je apparaten." flush>
      {save.isError && (
        <div className="p-4">
          <ErrorNotice>{errorMessage(save.error)}</ErrorNotice>
        </div>
      )}
      {rows.map((r, i) => (
        <div key={r.key} className={i > 0 ? 'border-t border-line' : ''}>
          <SettingRow title={r.title} help={r.help}>
            <Switch checked={p[r.key]} label={`Push: ${r.title.toLowerCase()}`} disabled={save.isPending} onChange={(on) => save.mutate({ ...p, [r.key]: on })} />
          </SettingRow>
        </div>
      ))}
    </Card>
  )
}
