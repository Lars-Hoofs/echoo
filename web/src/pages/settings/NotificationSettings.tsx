import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Bell } from 'lucide-react'

import { Card, ErrorNotice, SettingRow, Skeleton, Switch } from '../../components/ui'
import { api } from '../../lib/api'
import { errorMessage } from '../../lib/errors'

interface EmailPrefs {
  mentions: boolean
  assignments: boolean
  replies: boolean
}

interface NotificationSettingsResponse {
  email: EmailPrefs
  system_mail_available: boolean
}

const queryKey = ['me', 'notification-settings']

export function NotificationSettingsCard() {
  const queryClient = useQueryClient()
  const settings = useQuery({ queryKey, queryFn: () => api<NotificationSettingsResponse>('GET', '/me/notification-settings') })
  const save = useMutation({
    mutationFn: (email: EmailPrefs) => api<{ email: EmailPrefs }>('PUT', '/me/notification-settings', email),
    onSuccess: (r) => {
      queryClient.setQueryData<NotificationSettingsResponse>(queryKey, (old) => (old ? { ...old, email: r.email } : old))
    },
  })

  return (
    <Card
      title="Meldingen per e-mail"
      icon={<Bell size={16} />}
      description="Een korte e-mail met een link naar het gesprek, zonder de inhoud van het bericht."
      flush
    >
      {settings.isPending ? (
        <div className="p-4">
          <Skeleton className="h-16" />
        </div>
      ) : settings.isError ? (
        <div className="p-4">
          <ErrorNotice>{errorMessage(settings.error)}</ErrorNotice>
        </div>
      ) : (
        <>
          {save.isError && (
            <div className="p-4">
              <ErrorNotice>{errorMessage(save.error)}</ErrorNotice>
            </div>
          )}
          <SettingRow title="Als iemand mij noemt" help="Bij een @vermelding in een notitie.">
            <Switch
              checked={settings.data.email.mentions}
              label="E-mail bij een vermelding"
              disabled={save.isPending}
              onChange={(mentions) => save.mutate({ ...settings.data.email, mentions })}
            />
          </SettingRow>
          <div className="border-t border-line">
            <SettingRow title="Als een gesprek aan mij wordt toegewezen">
              <Switch
                checked={settings.data.email.assignments}
                label="E-mail bij een toewijzing"
                disabled={save.isPending}
                onChange={(assignments) => save.mutate({ ...settings.data.email, assignments })}
              />
            </SettingRow>
          </div>
          <div className="border-t border-line">
            <SettingRow title="Als een klant antwoordt" help="Bij een nieuw bericht in een gesprek dat aan mij is toegewezen.">
              <Switch
                checked={settings.data.email.replies}
                label="E-mail bij een antwoord van de klant"
                disabled={save.isPending}
                onChange={(replies) => save.mutate({ ...settings.data.email, replies })}
              />
            </SettingRow>
          </div>
          {!settings.data.system_mail_available && (
            <p className="border-t border-line px-4 py-3 text-sm text-muted sm:px-5">
              Echoo kan nog geen e-mail versturen. Vraag je beheerder om een systeemmailbox of SMTP-relay in te stellen; je keuze blijft bewaard.
            </p>
          )}
        </>
      )}
    </Card>
  )
}
