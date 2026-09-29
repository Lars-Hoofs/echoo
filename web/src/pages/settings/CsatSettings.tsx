import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { SmilePlus } from 'lucide-react'

import { Card, ErrorNotice, Select, SettingRow, Skeleton, Switch } from '../../components/ui'
import { api } from '../../lib/api'
import { errorMessage } from '../../lib/errors'
import { type CsatSettings, csatSettingsQuery } from '../../lib/reports'

const delays = Array.from({ length: 25 }, (_, h) => h)

// Saves on every change, like the signature card: it has its own endpoint and needs no Opslaan.
export function CsatSettingsCard({ mailboxId }: { mailboxId: string }) {
  const queryClient = useQueryClient()
  const query = csatSettingsQuery(mailboxId)
  const settings = useQuery(query)
  const save = useMutation({
    mutationFn: (next: CsatSettings) => api<CsatSettings>('PUT', `/mailboxes/${mailboxId}/csat`, next),
    onSuccess: (saved) => {
      queryClient.setQueryData(query.queryKey, saved)
    },
  })

  return (
    <Card
      title="Tevredenheidsonderzoek"
      icon={<SmilePlus size={16} />}
      description="Vraagt de klant na het oplossen van een gesprek om een beoordeling van 1 tot 5."
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
        <div className="divide-y divide-line">
          <SettingRow
            title="Onderzoek versturen"
            help="Wordt eenmalig per gesprek verstuurd nadat het is opgelost. Niet bij spam, automatische berichten of gesprekken waarvan het laatste klantbericht een automatisch antwoord was. Alleen gesprekken die worden opgelost nadat je dit aanzet, krijgen een onderzoek."
          >
            <Switch
              checked={settings.data.enabled}
              label="Tevredenheidsonderzoek versturen"
              onChange={(enabled) => save.mutate({ ...settings.data, enabled })}
            />
          </SettingRow>
          <SettingRow title="Wachttijd na oplossen" help="Zo lang wachten we met versturen. Wordt het gesprek ondertussen heropend, dan gaat er niets uit.">
            <Select
              aria-label="Wachttijd in uren"
              value={String(settings.data.delay_hours)}
              disabled={save.isPending}
              onChange={(e) => save.mutate({ ...settings.data, delay_hours: Number(e.target.value) })}
            >
              {delays.map((h) => (
                <option key={h} value={h}>
                  {h === 0 ? 'Direct' : `${h} uur`}
                </option>
              ))}
            </Select>
          </SettingRow>
        </div>
      )}
      {save.isError && (
        <div className="border-t border-line p-4">
          <ErrorNotice>{errorMessage(save.error)}</ErrorNotice>
        </div>
      )}
    </Card>
  )
}
