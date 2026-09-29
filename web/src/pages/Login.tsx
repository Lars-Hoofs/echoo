import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Link, useNavigate, useSearch } from '@tanstack/react-router'
import { LogIn } from 'lucide-react'
import { type SubmitEvent, useState } from 'react'

import { AuthCard, authLinkClass } from '../components/AuthCard'
import { Button, buttonClass, ErrorNotice, Field, Input } from '../components/ui'
import { api, ApiError } from '../lib/api'
import { errorMessage } from '../lib/errors'
import { type SsoInfo, ssoErrorMessage, ssoInfoQuery } from '../lib/sso'

interface LoginResponse {
  mfa_required: boolean
}

export function LoginPage() {
  const { terug, sso_error: ssoError, mfa } = useSearch({ from: '/inloggen' })
  const [step, setStep] = useState<'password' | 'code'>(mfa ? 'code' : 'password')
  const sso = useQuery(ssoInfoQuery)
  const navigate = useNavigate()
  const queryClient = useQueryClient()

  const finish = async () => {
    await queryClient.invalidateQueries({ queryKey: ['me'] })
    await navigate({ to: terug ?? '/' })
  }

  return (
    <AuthCard title={step === 'password' ? 'Inloggen' : 'Tweestapsverificatie'}>
      {step === 'password' ? (
        <PasswordStep
          sso={sso.data}
          ssoError={ssoError}
          onDone={(mfaRequired) => {
            if (mfaRequired) setStep('code')
            else void finish()
          }}
        />
      ) : (
        <CodeStep onDone={() => void finish()} onRestart={() => setStep('password')} />
      )}
    </AuthCard>
  )
}

function PasswordStep({ sso, ssoError, onDone }: { sso: SsoInfo | undefined; ssoError: string | undefined; onDone: (mfaRequired: boolean) => void }) {
  // With SSO required the password form is only for the owner, who keeps a way in when the provider is down.
  const [ownerLogin, setOwnerLogin] = useState(false)
  const showForm = !sso?.required || ownerLogin

  return (
    <div className="flex flex-col gap-4">
      {ssoError && <ErrorNotice>{ssoErrorMessage(ssoError)}</ErrorNotice>}
      {sso?.enabled && (
        <a href="/auth/sso/start" className={`${buttonClass()} w-full`}>
          <LogIn size={16} aria-hidden />
          Inloggen met {sso.label}
        </a>
      )}
      {sso?.enabled && showForm && (
        <div className="flex items-center gap-3 text-sm text-faint" aria-hidden>
          <span className="h-px flex-1 bg-line" />
          of
          <span className="h-px flex-1 bg-line" />
        </div>
      )}
      {showForm ? (
        <PasswordForm onDone={onDone} />
      ) : (
        <button type="button" className={authLinkClass} onClick={() => setOwnerLogin(true)}>
          Eigenaar? Inloggen met wachtwoord
        </button>
      )}
    </div>
  )
}

function PasswordForm({ onDone }: { onDone: (mfaRequired: boolean) => void }) {
  const [email, setEmail] = useState('')
  const [password, setPassword] = useState('')
  const login = useMutation({
    mutationFn: () => api<LoginResponse>('POST', '/auth/login', { email, password }),
    onSuccess: (r) => {
      onDone(r.mfa_required)
    },
  })

  const submit = (e: SubmitEvent) => {
    e.preventDefault()
    login.mutate()
  }

  return (
    <form onSubmit={submit} className="flex flex-col gap-4" noValidate>
      {login.isError && <ErrorNotice>{errorMessage(login.error)}</ErrorNotice>}
      <Field label="E-mailadres">
        {(p) => <Input {...p} type="email" autoComplete="username" autoFocus required value={email} onChange={(e) => setEmail(e.target.value)} />}
      </Field>
      <Field label="Wachtwoord">
        {(p) => <Input {...p} type="password" autoComplete="current-password" required value={password} onChange={(e) => setPassword(e.target.value)} />}
      </Field>
      <Button type="submit" variant="primary" className="w-full" busy={login.isPending} disabled={!email || !password}>
        Inloggen
      </Button>
      <Link to="/wachtwoord-vergeten" className={authLinkClass}>
        Wachtwoord vergeten?
      </Link>
    </form>
  )
}

function CodeStep({ onDone, onRestart }: { onDone: () => void; onRestart: () => void }) {
  const [useRecovery, setUseRecovery] = useState(false)
  const [code, setCode] = useState('')
  const verify = useMutation({
    mutationFn: () => api('POST', '/auth/mfa', { code }),
    onSuccess: onDone,
  })

  const submit = (e: SubmitEvent) => {
    e.preventDefault()
    verify.mutate()
  }

  return (
    <form onSubmit={submit} className="flex flex-col gap-4" noValidate>
      <p className="text-base text-muted">
        {useRecovery ? 'Vul een van je herstelcodes in. Elke code werkt één keer.' : 'Vul de zescijferige code uit je authenticator-app in.'}
      </p>
      {verify.isError && (
        <ErrorNotice>
          {errorMessage(verify.error)}{' '}
          {verify.error instanceof ApiError && verify.error.code === 'unauthenticated' && (
            <button type="button" className="underline underline-offset-4" onClick={onRestart}>
              Opnieuw inloggen
            </button>
          )}
        </ErrorNotice>
      )}
      <Field label={useRecovery ? 'Herstelcode' : 'Verificatiecode'}>
        {(p) =>
          useRecovery ? (
            <Input
              {...p}
              key="recovery"
              autoComplete="off"
              spellCheck={false}
              autoFocus
              className="font-mono"
              value={code}
              onChange={(e) => setCode(e.target.value)}
            />
          ) : (
            <Input
              {...p}
              key="totp"
              inputMode="numeric"
              autoComplete="one-time-code"
              pattern="[0-9]*"
              maxLength={6}
              autoFocus
              className="font-mono tracking-[0.2em] tabular-nums"
              value={code}
              onChange={(e) => setCode(e.target.value.replace(/\D/g, ''))}
            />
          )
        }
      </Field>
      <Button type="submit" variant="primary" className="w-full" busy={verify.isPending} disabled={useRecovery ? code.length < 16 : code.length !== 6}>
        Bevestigen
      </Button>
      <button
        type="button"
        className={authLinkClass}
        onClick={() => {
          setUseRecovery(!useRecovery)
          setCode('')
          verify.reset()
        }}
      >
        {useRecovery ? 'Code uit authenticator-app gebruiken' : 'Herstelcode gebruiken'}
      </button>
    </form>
  )
}
