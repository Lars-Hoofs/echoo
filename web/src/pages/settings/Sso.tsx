import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { LogIn } from 'lucide-react'
import { type SubmitEvent, useState } from 'react'

import { CopyButton } from '../../components/CopyButton'
import { useToast } from '../../components/Toast'
import { Button, Card, ErrorNotice, Field, Input, Page, PageHeader, Select, SettingRow, Skeleton, Switch, Textarea } from '../../components/ui'
import { api } from '../../lib/api'
import { errorMessage, fieldError } from '../../lib/errors'
import { rolesQuery } from '../../lib/permissions'
import { parseDomains, type SsoSettings } from '../../lib/sso'

const settingsQuery = { queryKey: ['settings', 'sso'], queryFn: () => api<SsoSettings>('GET', '/settings/sso') }

export function SsoPage() {
  const settings = useQuery(settingsQuery)
  return (
    <Page>
      <PageHeader breadcrumb="Werkruimte"
        title="Inloggen met SSO"
        description="Laat mensen inloggen met het account van je organisatie via OpenID Connect, bijvoorbeeld Microsoft Entra ID, Google Workspace, Okta, Keycloak of Authentik."
      />
      {settings.isPending ? (
        <Skeleton className="h-96" />
      ) : settings.isError ? (
        <ErrorNotice>{errorMessage(settings.error)}</ErrorNotice>
      ) : (
        <SsoForm initial={settings.data} />
      )}
    </Page>
  )
}

// A role choice is either a built-in role or a custom one; both travel as one select value.
const roleValue = (s: Pick<SsoSettings, 'default_role' | 'default_custom_role_id'>) =>
  s.default_role === 'custom' && s.default_custom_role_id ? `custom:${s.default_custom_role_id}` : s.default_role

function SsoForm({ initial }: { initial: SsoSettings }) {
  const [enabled, setEnabled] = useState(initial.enabled)
  const [issuer, setIssuer] = useState(initial.issuer_url)
  const [clientId, setClientId] = useState(initial.client_id)
  const [secret, setSecret] = useState('')
  const [domains, setDomains] = useState(initial.allowed_domains.join('\n'))
  const [label, setLabel] = useState(initial.button_label)
  const [required, setRequired] = useState(initial.required)
  const [provision, setProvision] = useState(initial.auto_provision)
  const [role, setRole] = useState(roleValue(initial))
  const [trustMfa, setTrustMfa] = useState(initial.trust_idp_mfa)
  const [trustMissing, setTrustMissing] = useState(initial.trust_missing_email_verified)
  const [internal, setInternal] = useState(initial.allow_internal_issuer)
  const roles = useQuery(rolesQuery)
  const queryClient = useQueryClient()
  const toast = useToast()
  const save = useMutation({
    mutationFn: () => {
      const custom = role.startsWith('custom:')
      return api<SsoSettings>('PUT', '/settings/sso', {
        enabled,
        issuer_url: issuer,
        client_id: clientId,
        client_secret: secret,
        allowed_domains: parseDomains(domains),
        button_label: label,
        required,
        auto_provision: provision,
        ...(custom ? { default_custom_role_id: role.slice('custom:'.length) } : { default_role: role }),
        trust_idp_mfa: trustMfa,
        trust_missing_email_verified: trustMissing,
        allow_internal_issuer: internal,
      })
    },
    onSuccess: async (saved) => {
      queryClient.setQueryData(settingsQuery.queryKey, saved)
      setSecret('')
      await queryClient.invalidateQueries({ queryKey: ['sso-info'] })
      toast('Opgeslagen.')
    },
  })
  const submit = (e: SubmitEvent) => {
    e.preventDefault()
    save.mutate()
  }
  const unassignedFields = ['issuer_url', 'client_id', 'client_secret', 'allowed_domains', 'button_label', 'default_role', 'default_custom_role_id', 'required']
  const showGeneric = save.isError && !unassignedFields.some((f) => fieldError(save.error, f))

  return (
    <form onSubmit={submit} className="flex flex-col gap-6" noValidate>
      {showGeneric && <ErrorNotice>{errorMessage(save.error)}</ErrorNotice>}
      <Card title="Provider" icon={<LogIn size={16} />} flush>
        <SettingRow
          title="Inloggen met SSO"
          help="Op de inlogpagina verschijnt een knop. Bewaar de instellingen pas als de provider bereikbaar is; Echoo controleert dat."
        >
          <Switch checked={enabled} label="Inloggen met SSO" onChange={setEnabled} />
        </SettingRow>
        <div className="flex flex-col gap-4 px-4 py-4 sm:px-5">
          <Field
            label="Issuer-URL"
            help="Het adres van de provider, bijvoorbeeld https://login.microsoftonline.com/<tenant>/v2.0."
            error={fieldError(save.error, 'issuer_url')}
          >
            {(p) => <Input {...p} type="url" autoComplete="off" spellCheck={false} value={issuer} onChange={(e) => setIssuer(e.target.value)} />}
          </Field>
          <Field label="Client-ID" error={fieldError(save.error, 'client_id')}>
            {(p) => <Input {...p} autoComplete="off" spellCheck={false} value={clientId} onChange={(e) => setClientId(e.target.value)} />}
          </Field>
          <Field
            label="Client secret"
            help={
              initial.client_secret_set
                ? 'Er is een secret opgeslagen. Laat dit leeg om het te bewaren; het wordt nooit getoond.'
                : 'Wordt versleuteld opgeslagen en nooit getoond.'
            }
            error={fieldError(save.error, 'client_secret')}
          >
            {(p) => <Input {...p} type="password" autoComplete="new-password" value={secret} onChange={(e) => setSecret(e.target.value)} />}
          </Field>
          <Field label="Redirect-URI" help="Vul dit adres in bij de provider als toegestane redirect-URI.">
            {(p) => (
              <div className="flex flex-wrap items-center gap-2">
                <Input {...p} readOnly value={initial.redirect_uri} className="min-w-0 flex-1 font-mono" onFocus={(e) => e.currentTarget.select()} />
                <CopyButton text={initial.redirect_uri} />
              </div>
            )}
          </Field>
          <Field label="Tekst op de knop" help={`De knop toont: Inloggen met ${label || '…'}`} error={fieldError(save.error, 'button_label')}>
            {(p) => <Input {...p} maxLength={40} value={label} onChange={(e) => setLabel(e.target.value)} />}
          </Field>
        </div>
      </Card>

      <Card title="Wie mag inloggen" flush>
        <div className="flex flex-col gap-4 border-b border-line px-4 py-4 sm:px-5">
          <Field
            label="Toegestane e-maildomeinen"
            help="Eén domein per regel, bijvoorbeeld voorbeeld.nl. Leeg betekent: elk domein, voor gebruikers die al een account hebben."
            error={fieldError(save.error, 'allowed_domains')}
          >
            {(p) => <Textarea {...p} rows={3} spellCheck={false} value={domains} onChange={(e) => setDomains(e.target.value)} />}
          </Field>
        </div>
        <SettingRow
          title="Nieuwe gebruikers automatisch aanmaken"
          help="Bij de eerste keer inloggen krijgt iemand uit een toegestaan domein direct een account. Zonder dit moet een beheerder eerst een gebruiker toevoegen."
        >
          <Switch checked={provision} label="Nieuwe gebruikers automatisch aanmaken" onChange={setProvision} />
        </SettingRow>
        {provision && (
          <div className="border-b border-line px-4 py-4 sm:px-5">
            <Field label="Rol voor nieuwe gebruikers" error={fieldError(save.error, 'default_custom_role_id') ?? fieldError(save.error, 'default_role')}>
              {(p) => (
                <Select {...p} value={role} onChange={(e) => setRole(e.target.value)}>
                  <option value="agent">Agent</option>
                  <option value="readonly">Alleen lezen</option>
                  {roles.data?.roles.map((r) => (
                    <option key={r.id} value={`custom:${r.id}`}>
                      {r.name}
                    </option>
                  ))}
                </Select>
              )}
            </Field>
          </div>
        )}
        <SettingRow
          title="SSO verplicht"
          help="Inloggen met een wachtwoord staat dan uit voor iedereen behalve de eigenaar, die zo altijd binnen kan als de provider niet werkt."
        >
          <Switch checked={required} label="SSO verplicht" onChange={setRequired} />
        </SettingRow>
        {fieldError(save.error, 'required') && <p className="px-4 pb-3 text-sm text-danger-text sm:px-5">{fieldError(save.error, 'required')}</p>}
      </Card>

      <Card title="Beveiliging" flush>
        <SettingRow
          title="Tweestapsverificatie van de provider telt"
          help="Meldt de provider een tweede factor (amr: mfa, otp of hwk), dan vraagt Echoo niet nog een code. Anders gelden de 2FA-regels van Echoo gewoon."
        >
          <Switch checked={trustMfa} label="Tweestapsverificatie van de provider telt" onChange={setTrustMfa} />
        </SettingRow>
        <SettingRow
          title="E-mailadres vertrouwen zonder email_verified"
          help="Nodig voor Microsoft Entra ID, dat deze claim niet meestuurt. Een claim die zegt dat het adres niet klopt, wordt altijd geweigerd."
        >
          <Switch checked={trustMissing} label="E-mailadres vertrouwen zonder email_verified" onChange={setTrustMissing} />
        </SettingRow>
        <SettingRow
          title="Provider op een intern netwerk"
          help="Sta een issuer toe op een privé-adres of via http. Zet dit alleen aan voor een provider die je zelf beheert."
        >
          <Switch checked={internal} label="Provider op een intern netwerk" onChange={setInternal} />
        </SettingRow>
      </Card>

      <div className="flex justify-end">
        <Button type="submit" variant="primary" busy={save.isPending}>
          Opslaan
        </Button>
      </div>
    </form>
  )
}
