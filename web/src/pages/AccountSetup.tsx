import { useNavigate } from '@tanstack/react-router'

import { AuthCard } from '../components/AuthCard'
import { ChangePasswordForm, TotpEnrollment } from '../components/security'

export function SetupPasswordPage() {
  const navigate = useNavigate()
  return (
    <AuthCard title="Kies een eigen wachtwoord" intro="Je bent ingelogd met een tijdelijk wachtwoord. Kies een nieuw wachtwoord om verder te gaan.">
      <ChangePasswordForm currentLabel="Tijdelijk wachtwoord" onDone={() => void navigate({ to: '/' })} />
    </AuthCard>
  )
}

export function SetupMfaPage() {
  const navigate = useNavigate()
  return (
    <AuthCard
      title="Tweestapsverificatie instellen"
      intro="Je werkruimte vereist tweestapsverificatie. Stel het nu in om verder te gaan."
    >
      <TotpEnrollment onDone={() => void navigate({ to: '/' })} />
    </AuthCard>
  )
}
