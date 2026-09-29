import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { ChartColumn } from 'lucide-react'

import { Card, ErrorNotice, SettingRow, Select, Skeleton } from '../../components/ui'
import { api } from '../../lib/api'
import { errorMessage } from '../../lib/errors'
import { type ReportSettings, reportSettingsQuery } from '../../lib/reports'

const zones = [
  'Europe/Amsterdam',
  'Europe/Brussels',
  'Europe/Berlin',
  'Europe/Paris',
  'Europe/London',
  'Europe/Madrid',
  'Europe/Rome',
  'Europe/Lisbon',
  'Europe/Zurich',
  'Europe/Stockholm',
  'Europe/Athens',
  'UTC',
  'America/New_York',
  'America/Chicago',
  'America/Denver',
  'America/Los_Angeles',
  'America/Sao_Paulo',
  'Asia/Dubai',
  'Asia/Kolkata',
  'Asia/Singapore',
  'Asia/Tokyo',
  'Australia/Sydney',
]

export function ReportSettingsCard() {
  const queryClient = useQueryClient()
  const settings = useQuery(reportSettingsQuery)
  const save = useMutation({
    mutationFn: (next: ReportSettings) => api<ReportSettings>('PUT', '/settings/reports', next),
    onSuccess: (saved) => {
      queryClient.setQueryData(reportSettingsQuery.queryKey, saved)
      void queryClient.invalidateQueries({ queryKey: ['reports'] })
    },
  })
  const current = settings.data?.timezone
  const options = current && !zones.includes(current) ? [current, ...zones] : zones

  return (
    <Card title="Rapportage" icon={<ChartColumn size={16} />} flush>
      {settings.isPending ? (
        <div className="p-4">
          <Skeleton className="h-10" />
        </div>
      ) : settings.isError ? (
        <div className="p-4">
          <ErrorNotice>{errorMessage(settings.error)}</ErrorNotice>
        </div>
      ) : (
        <SettingRow title="Tijdzone" help="Bepaalt waar een dag begint en eindigt in de rapporten. Bestaande rapporten worden opnieuw berekend.">
          <Select aria-label="Tijdzone" value={settings.data.timezone} disabled={save.isPending} onChange={(e) => save.mutate({ timezone: e.target.value })}>
            {options.map((z) => (
              <option key={z} value={z}>
                {z}
              </option>
            ))}
          </Select>
        </SettingRow>
      )}
      {save.isError && (
        <div className="border-t border-line p-4">
          <ErrorNotice>{errorMessage(save.error)}</ErrorNotice>
        </div>
      )}
    </Card>
  )
}
