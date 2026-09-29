import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { ShieldCheck } from 'lucide-react'

import { Card, ErrorNotice, Page, PageHeader, SettingRow, Skeleton, Switch } from '../../components/ui'
import { api } from '../../lib/api'
import { errorMessage } from '../../lib/errors'
import { ReportSettingsCard } from './ReportSettings'

interface SecuritySettings {
  require_mfa: boolean
}

export function WorkspacePage() {
  const queryClient = useQueryClient()
  const settings = useQuery({ queryKey: ['settings', 'security'], queryFn: () => api<SecuritySettings>('GET', '/settings/security') })
  const save = useMutation({
    mutationFn: (next: SecuritySettings) => api<SecuritySettings>('PUT', '/settings/security', next),
    onMutate: (next) => {
      queryClient.setQueryData(['settings', 'security'], next)
    },
    onError: () => queryClient.invalidateQueries({ queryKey: ['settings', 'security'] }),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ['me'] }),
  })

  return (
    <Page>
      <PageHeader breadcrumb="Werkruimte" title="Beveiliging werkruimte" description="Instellingen die voor iedereen in deze werkruimte gelden." />
      <Card title="Inloggen" icon={<ShieldCheck size={16} />} flush>
        {settings.isPending ? (
          <div className="p-4">
            <Skeleton className="h-10" />
          </div>
        ) : settings.isError ? (
          <div className="p-4">
            <ErrorNotice>{errorMessage(settings.error)}</ErrorNotice>
          </div>
        ) : (
          <SettingRow
            title="Tweestapsverificatie verplichten"
            help="Gebruikers zonder tweestapsverificatie moeten het instellen voordat ze verder kunnen. Dit geldt ook voor jou."
          >
            <Switch
              checked={settings.data.require_mfa}
              label="Tweestapsverificatie verplichten"
              onChange={(require_mfa) => save.mutate({ require_mfa })}
            />
          </SettingRow>
        )}
        {save.isError && (
          <div className="border-t border-line p-4">
            <ErrorNotice>{errorMessage(save.error)}</ErrorNotice>
          </div>
        )}
      </Card>
      <ReportSettingsCard />
    </Page>
  )
}
