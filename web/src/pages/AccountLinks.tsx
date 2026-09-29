import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Link, useNavigate, useParams } from '@tanstack/react-router'
import { type SubmitEvent, useState } from 'react'

import { AuthCard, authLinkClass } from '../components/AuthCard'
import { Button, ErrorNotice, Field, Input } from '../components/ui'
import { api, ApiError } from '../lib/api'
import { errorMessage, fieldError } from '../lib/errors'

interface LinkInfo {
  email: string
  name: string
}

const passwordHelp = 'Minimaal 12 tekens. Een zin van een paar woorden werkt goed.'

function BackToLogin() {
  return (
    <Link to="/inloggen" className={authLinkClass}>
      Terug naar inloggen
    </Link>
  )
}

export function ForgotPasswordPage() {
  const [email, setEmail] = useState('')
  const request = useMutation({ mutationFn: () => api('POST', '/auth/password-reset', { email }) })
  const submit = (e: SubmitEvent) => {
    e.preventDefault()
    request.mutate()
  }

  if (request.isSuccess) {
    return (
      <AuthCard title="Controleer je e-mail">
        <div className="flex flex-col gap-4">
          <p className="text-base text-muted">
            Hoort dit e-mailadres bij een account, dan is er een e-mail onderweg met een link om een nieuw wachtwoord te kiezen. De link is 30
            minuten geldig.
          </p>
          <BackToLogin />
        </div>
      </AuthCard>
    )
  }
  return (
    <AuthCard title="Wachtwoord vergeten" intro="Vul je e-mailadres in. We sturen een link om een nieuw wachtwoord te kiezen.">
      <form onSubmit={submit} className="flex flex-col gap-4" noValidate>
        {request.isError && <ErrorNotice>{errorMessage(request.error)}</ErrorNotice>}
        <Field label="E-mailadres">
          {(p) => <Input {...p} type="email" autoComplete="username" autoFocus required value={email} onChange={(e) => setEmail(e.target.value)} />}
        </Field>
        <Button type="submit" variant="primary" className="w-full" busy={request.isPending} disabled={!email}>
          Link versturen
        </Button>
        <BackToLogin />
      </form>
    </AuthCard>
  )
}

// Looks at the link before showing the form, so an expired link says so right away.
function useLinkInfo(path: string) {
  return useQuery({
    queryKey: ['account-link', path],
    queryFn: () => api<LinkInfo>('GET', path),
    retry: false,
    staleTime: Infinity,
    refetchOnWindowFocus: false,
  })
}

function InvalidLink({ retry }: { retry?: 'reset' }) {
  return (
    <AuthCard title="Deze link werkt niet meer">
      <div className="flex flex-col gap-4">
        <p className="text-base text-muted">De link is verlopen of al gebruikt.</p>
        {retry === 'reset' ? (
          <Link to="/wachtwoord-vergeten" className={authLinkClass}>
            Een nieuwe link aanvragen
          </Link>
        ) : (
          <p className="text-base text-muted">Vraag een beheerder om je opnieuw uit te nodigen.</p>
        )}
        <BackToLogin />
      </div>
    </AuthCard>
  )
}

function isInvalidLink(err: unknown): boolean {
  return err instanceof ApiError && err.code === 'link_invalid'
}

export function ResetPasswordPage() {
  const { token } = useParams({ from: '/wachtwoord-herstellen/$token' })
  const info = useLinkInfo(`/auth/password-reset/${token}`)
  const [password, setPassword] = useState('')
  const reset = useMutation({ mutationFn: () => api('POST', `/auth/password-reset/${token}`, { password }) })
  const submit = (e: SubmitEvent) => {
    e.preventDefault()
    reset.mutate()
  }

  if (info.isPending) return <AuthCard title="Nieuw wachtwoord kiezen">{null}</AuthCard>
  if (info.isError) {
    return isInvalidLink(info.error) ? (
      <InvalidLink retry="reset" />
    ) : (
      <AuthCard title="Nieuw wachtwoord kiezen">
        <ErrorNotice>{errorMessage(info.error)}</ErrorNotice>
      </AuthCard>
    )
  }
  if (reset.isSuccess) {
    return (
      <AuthCard title="Wachtwoord gewijzigd">
        <div className="flex flex-col gap-4">
          <p className="text-base text-muted">Je bent overal afgemeld. Log in met je nieuwe wachtwoord. Heb je tweestapsverificatie aan, dan is je code nog steeds nodig.</p>
          <BackToLogin />
        </div>
      </AuthCard>
    )
  }
  if (reset.isError && isInvalidLink(reset.error)) return <InvalidLink retry="reset" />
  return (
    <AuthCard title="Nieuw wachtwoord kiezen" intro={`Kies een nieuw wachtwoord voor ${info.data.email}.`}>
      <form onSubmit={submit} className="flex flex-col gap-4" noValidate>
        {reset.isError && !fieldError(reset.error, 'password') && <ErrorNotice>{errorMessage(reset.error)}</ErrorNotice>}
        <Field label="Nieuw wachtwoord" help={passwordHelp} error={fieldError(reset.error, 'password')}>
          {(p) => <Input {...p} type="password" autoComplete="new-password" autoFocus value={password} onChange={(e) => setPassword(e.target.value)} />}
        </Field>
        <Button type="submit" variant="primary" className="w-full" busy={reset.isPending} disabled={!password}>
          Wachtwoord opslaan
        </Button>
      </form>
    </AuthCard>
  )
}

export function InvitationPage() {
  const { token } = useParams({ from: '/uitnodiging/$token' })
  const info = useLinkInfo(`/invitations/${token}`)
  if (info.isPending) return <AuthCard title="Uitnodiging">{null}</AuthCard>
  if (info.isError) {
    return isInvalidLink(info.error) ? (
      <InvalidLink />
    ) : (
      <AuthCard title="Uitnodiging">
        <ErrorNotice>{errorMessage(info.error)}</ErrorNotice>
      </AuthCard>
    )
  }
  return <AcceptInvitation token={token} info={info.data} />
}

function AcceptInvitation({ token, info }: { token: string; info: LinkInfo }) {
  const [name, setName] = useState(info.name)
  const [password, setPassword] = useState('')
  const navigate = useNavigate()
  const queryClient = useQueryClient()
  const accept = useMutation({
    mutationFn: () => api('POST', `/invitations/${token}/accept`, { name, password }),
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: ['me'] })
      await navigate({ to: '/' })
    },
  })
  const submit = (e: SubmitEvent) => {
    e.preventDefault()
    accept.mutate()
  }

  if (accept.isError && isInvalidLink(accept.error)) return <InvalidLink />
  const generic = accept.isError && !fieldError(accept.error, 'name') && !fieldError(accept.error, 'password')
  return (
    <AuthCard title="Welkom bij Echoo" intro={`Je bent uitgenodigd met ${info.email}. Controleer je naam en kies een wachtwoord.`}>
      <form onSubmit={submit} className="flex flex-col gap-4" noValidate>
        {generic && <ErrorNotice>{errorMessage(accept.error)}</ErrorNotice>}
        <Field label="Naam" error={fieldError(accept.error, 'name')}>
          {(p) => <Input {...p} autoComplete="name" value={name} onChange={(e) => setName(e.target.value)} />}
        </Field>
        <Field label="Wachtwoord" help={passwordHelp} error={fieldError(accept.error, 'password')}>
          {(p) => <Input {...p} type="password" autoComplete="new-password" autoFocus value={password} onChange={(e) => setPassword(e.target.value)} />}
        </Field>
        <Button type="submit" variant="primary" className="w-full" busy={accept.isPending} disabled={!name.trim() || !password}>
          Account activeren
        </Button>
      </form>
    </AuthCard>
  )
}
