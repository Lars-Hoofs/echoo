import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Clock3, Mail, UsersRound } from 'lucide-react'
import { useState } from 'react'

import { Avatar } from '../../components/Avatar'
import { useToast } from '../../components/Toast'
import { Badge, Button, Card, ErrorNotice, Field, Input, Page, PageHeader, Select, Skeleton, Switch, Table, TBody, Td, Th, THead, Tr } from '../../components/ui'
import { api } from '../../lib/api'
import {
  type Agent,
  type AssignMode,
  assignModeLabel,
  assignmentQuery,
  automationSettingsQuery,
  availabilityLabel,
  businessHoursQuery,
  type MailboxAutomation,
  slaPoliciesQuery,
} from '../../lib/automation'
import { errorMessage, fieldError } from '../../lib/errors'

export function AssignmentPage() {
  const data = useQuery(assignmentQuery)
  const policies = useQuery(slaPoliciesQuery)
  const hours = useQuery(businessHoursQuery)

  return (
    <Page>
      <PageHeader
        breadcrumb="Werkruimte"
        title="Toewijzing"
        description="Nieuwe gesprekken kunnen automatisch worden toegewezen aan online agenten van de teams met schrijfrechten op de mailbox. Bezette en offline agenten worden overgeslagen."
      />
      {data.isPending ? (
        <Skeleton className="h-48" />
      ) : data.isError ? (
        <ErrorNotice>{errorMessage(data.error)}</ErrorNotice>
      ) : (
        <>
          <Card title="Mailboxen" icon={<Mail size={16} />} description="Wijzigingen gelden direct." flush>
            <Table>
              <THead>
                <Th>Mailbox</Th>
                <Th>Automatisch toewijzen</Th>
                <Th className="hidden lg:table-cell">SLA-beleid voor nieuwe gesprekken</Th>
                <Th className="hidden lg:table-cell">Werktijden</Th>
              </THead>
              <TBody>
                {data.data.mailboxes.map((m) => (
                  <MailboxRow key={m.id} mailbox={m} policies={policies.data ?? []} hours={hours.data ?? []} />
                ))}
              </TBody>
            </Table>
          </Card>
          <Card title="Agenten" icon={<UsersRound size={16} />} description="Agenten zetten hun beschikbaarheid zelf in het gebruikersmenu. Een lege capaciteit betekent geen limiet." flush>
            <Table>
              <THead>
                <Th>Agent</Th>
                <Th>Beschikbaarheid</Th>
                <Th numeric>Open</Th>
                <Th className="w-44">Maximaal open</Th>
              </THead>
              <TBody>
                {data.data.agents.map((a) => (
                  <AgentRow key={a.id} agent={a} />
                ))}
              </TBody>
            </Table>
          </Card>
        </>
      )}
      <AutoResolveCard />
    </Page>
  )
}

function MailboxRow({ mailbox: m, policies, hours }: { mailbox: MailboxAutomation; policies: { id: string; name: string }[]; hours: { id: string; name: string; is_default: boolean }[] }) {
  const queryClient = useQueryClient()
  const toast = useToast()
  const save = useMutation({
    mutationFn: (patch: Partial<Pick<MailboxAutomation, 'auto_assign_mode' | 'default_sla_policy_id' | 'business_hours_id'>>) =>
      api('PUT', `/mailboxes/${m.id}/automation`, {
        auto_assign_mode: m.auto_assign_mode,
        default_sla_policy_id: m.default_sla_policy_id,
        business_hours_id: m.business_hours_id,
        ...patch,
      }),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ['automation', 'assignment'] }),
    onError: (err) => toast(errorMessage(err), { tone: 'error' }),
  })
  return (
    <Tr>
      <Td>
        <div className="text-ink">{m.name}</div>
        <div className="text-sm text-faint">{m.email_address}</div>
      </Td>
      <Td>
        <Select
          aria-label={`Automatisch toewijzen voor ${m.name}`}
          className="w-44"
          value={m.auto_assign_mode}
          disabled={save.isPending}
          onChange={(e) => save.mutate({ auto_assign_mode: e.target.value as AssignMode })}
        >
          {(Object.keys(assignModeLabel) as AssignMode[]).map((mode) => (
            <option key={mode} value={mode}>
              {assignModeLabel[mode]}
            </option>
          ))}
        </Select>
      </Td>
      <Td className="hidden lg:table-cell">
        <Select
          aria-label={`SLA-beleid voor ${m.name}`}
          className="w-44"
          value={m.default_sla_policy_id ?? ''}
          disabled={save.isPending}
          onChange={(e) => save.mutate({ default_sla_policy_id: e.target.value || null })}
        >
          <option value="">Geen</option>
          {policies.map((p) => (
            <option key={p.id} value={p.id}>
              {p.name}
            </option>
          ))}
        </Select>
      </Td>
      <Td className="hidden lg:table-cell">
        <Select
          aria-label={`Werktijden voor ${m.name}`}
          className="w-44"
          value={m.business_hours_id ?? ''}
          disabled={save.isPending}
          onChange={(e) => save.mutate({ business_hours_id: e.target.value || null })}
        >
          <option value="">Standaardschema</option>
          {hours.map((h) => (
            <option key={h.id} value={h.id}>
              {h.name}
            </option>
          ))}
        </Select>
      </Td>
    </Tr>
  )
}


function AgentRow({ agent: a }: { agent: Agent }) {
  const [value, setValue] = useState(a.max_open === null ? '' : String(a.max_open))
  const queryClient = useQueryClient()
  const toast = useToast()
  const save = useMutation({
    mutationFn: () => api('PUT', `/users/${a.id}/capacity`, { max_open: value.trim() === '' ? null : Number(value) }),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ['automation', 'assignment'] }),
    onError: (err) => {
      setValue(a.max_open === null ? '' : String(a.max_open))
      toast(fieldError(err, 'max_open') ?? errorMessage(err), { tone: 'error' })
    },
  })
  const changed = value.trim() !== (a.max_open === null ? '' : String(a.max_open))
  return (
    <Tr>
      <Td>
        <div className="flex items-center gap-3">
          <Avatar name={a.name} />
          <div className="min-w-0">
            <div className="truncate text-ink">{a.name}</div>
            <div className="truncate text-sm text-faint">{a.email}</div>
          </div>
        </div>
      </Td>
      <Td>
        <Badge dot>{availabilityLabel[a.availability]}</Badge>
      </Td>
      <Td numeric>{a.open_count}</Td>
      <Td>
        <Input
          aria-label={`Maximaal aantal open gesprekken van ${a.name}`}
          type="number"
          min={1}
          max={10000}
          placeholder="Geen limiet"
          className="w-36"
          value={value}
          onChange={(e) => setValue(e.target.value)}
          onBlur={() => changed && save.mutate()}
          onKeyDown={(e) => {
            if (e.key === 'Enter' && changed) save.mutate()
          }}
        />
      </Td>
    </Tr>
  )
}

function AutoResolveCard() {
  const settings = useQuery(automationSettingsQuery)
  const queryClient = useQueryClient()
  const [days, setDays] = useState<string | null>(null)
  const stored = settings.data?.auto_resolve_days ?? 0
  const shown = days ?? String(stored || 7)
  const [on, setOn] = useState<boolean | null>(null)
  const active = on ?? stored > 0
  const save = useMutation({
    mutationFn: () => api('PUT', '/settings/automation', { auto_resolve_days: active ? Number(shown) : 0 }),
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: ['automation', 'settings'] })
      setDays(null)
      setOn(null)
    },
  })
  const dirty = active !== stored > 0 || (active && Number(shown) !== stored)
  return (
    <Card
      title="Automatisch sluiten"
      icon={<Clock3 size={16} />}
      description="Gesprekken in status Wachtend sluiten vanzelf als de klant niet meer reageert. Het gesprek gaat open zodra de klant alsnog antwoordt."
      footer={
        <Button variant="primary" busy={save.isPending} disabled={!dirty || settings.isPending} onClick={() => save.mutate()}>
          Opslaan
        </Button>
      }
    >
      {settings.isError ? (
        <ErrorNotice>{errorMessage(settings.error)}</ErrorNotice>
      ) : (
        <div className="flex flex-col gap-3">
          {save.isError && <ErrorNotice>{errorMessage(save.error)}</ErrorNotice>}
          <div className="flex items-center gap-3">
            <Switch checked={active} onChange={setOn} label="Gesprekken automatisch sluiten" />
            <span className="text-base text-ink">Gesprekken automatisch sluiten</span>
          </div>
          {active && (
            <Field label="Sluiten na (dagen zonder reactie van de klant in status Wachtend)" error={fieldError(save.error, 'auto_resolve_days')}>
              {(p) => <Input {...p} type="number" min={1} max={365} className="w-28" value={shown} onChange={(e) => setDays(e.target.value)} />}
            </Field>
          )}
        </div>
      )}
    </Card>
  )
}
