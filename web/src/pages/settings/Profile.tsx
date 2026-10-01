import { useMutation, useQueryClient, useSuspenseQuery } from '@tanstack/react-query'
import { Palette, UserRound } from 'lucide-react'
import { type SubmitEvent, useState } from 'react'

import { Avatar } from '../../components/Avatar'
import { Badge, Button, Card, ErrorNotice, Field, Input, Page, PageHeader, Segmented, SettingRow } from '../../components/ui'
import { api } from '../../lib/api'
import { errorMessage, fieldError } from '../../lib/errors'
import { applyTheme, type Me, meQuery, roleName, type Theme, type User } from '../../lib/session'
import { SignatureCard } from './Signatures'

const themes: { value: Theme; label: string }[] = [
  { value: 'system', label: 'Systeem' },
  { value: 'light', label: 'Licht' },
  { value: 'dark', label: 'Donker' },
]

export function ProfilePage() {
  const { data: me } = useSuspenseQuery(meQuery)
  const queryClient = useQueryClient()
  const [name, setName] = useState(me.user.name)

  const update = useMutation({
    mutationFn: (body: { name?: string; theme?: Theme }) => api<{ user: User }>('PATCH', '/me', body),
    onMutate: (body) => {
      if (body.theme) applyTheme(body.theme)
    },
    onSuccess: (r) => {
      queryClient.setQueryData<Me>(['me'], (old) => (old ? { ...old, user: r.user } : old))
    },
    onError: () => {
      applyTheme(me.user.theme)
    },
  })

  const submitName = (e: SubmitEvent) => {
    e.preventDefault()
    update.mutate({ name })
  }

  return (
    <Page>
      <PageHeader breadcrumb="Persoonlijk" title="Profiel" description="Je gegevens en hoe Echoo eruitziet voor jou." />
      <form onSubmit={submitName} noValidate>
        <Card
          title="Account"
          icon={<UserRound size={16} />}
          footer={
            <Button type="submit" variant="primary" busy={update.isPending} disabled={name.trim() === me.user.name}>
              Opslaan
            </Button>
          }
        >
          <div className="flex flex-col gap-5">
            <div className="flex items-center gap-3">
              <Avatar name={me.user.name} size={40} />
              <div className="min-w-0">
                <div className="truncate text-base text-ink">{me.user.name}</div>
                <div className="flex flex-wrap items-center gap-x-2 gap-y-1 text-sm text-muted">
                  <span className="truncate">{me.user.email}</span>
                  <Badge>{roleName(me.user.role, me.custom_role_name)}</Badge>
                </div>
              </div>
            </div>
            {update.isError && !fieldError(update.error, 'name') && <ErrorNotice>{errorMessage(update.error)}</ErrorNotice>}
            <div className="max-w-sm">
              <Field label="Naam" help="Zo zien collega's je in toewijzingen en notities." error={fieldError(update.error, 'name')}>
                {(p) => <Input {...p} autoComplete="name" value={name} onChange={(e) => setName(e.target.value)} />}
              </Field>
            </div>
            <p className="text-sm text-faint">Het e-mailadres kan alleen door een beheerder worden gewijzigd.</p>
          </div>
        </Card>
      </form>
      <SignatureCard />
      <Card title="Weergave" icon={<Palette size={16} />} flush>
        <SettingRow title="Thema" help="Systeem volgt de instelling van je besturingssysteem.">
          <Segmented label="Thema" value={me.user.theme} options={themes} onChange={(theme) => update.mutate({ theme })} />
        </SettingRow>
      </Card>
    </Page>
  )
}
