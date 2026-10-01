import { useQuery } from '@tanstack/react-query'
import { useState } from 'react'
import { Pressable, View } from 'react-native'

import { api, errorMessage } from '../lib/api'
import { useSession } from '../lib/session'
import { AuthScreen } from '../ui/AuthScreen'
import { Button, Field, Notice, T } from '../ui/components'
import { space } from '../ui/theme'

export default function LoginScreen() {
  const { state, login, verifyMfa, forgetServer } = useSession()
  const sso = useQuery({ queryKey: ['auth', 'sso'], queryFn: () => api<{ enabled: boolean; required: boolean; label: string }>('GET', '/auth/sso') })
  const [step, setStep] = useState<'password' | 'mfa'>('password')
  const [email, setEmail] = useState('')
  const [password, setPassword] = useState('')
  const [code, setCode] = useState('')
  const [error, setError] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)
  const host = 'server' in state ? new URL(state.server).host : ''

  const run = async (fn: () => Promise<void>) => {
    setBusy(true)
    setError(null)
    try {
      await fn()
    } catch (err) {
      setError(errorMessage(err))
    } finally {
      setBusy(false)
    }
  }

  if (step === 'mfa') {
    return (
      <AuthScreen title="Tweestapsverificatie">
        <T muted>Vul de code uit je authenticator-app in, of een van je herstelcodes.</T>
        <Field
          label="Verificatiecode"
          value={code}
          onChangeText={setCode}
          autoCapitalize="none"
          autoCorrect={false}
          keyboardType="default"
          textContentType="oneTimeCode"
          autoComplete="one-time-code"
          returnKeyType="go"
          onSubmitEditing={() => void run(() => verifyMfa(code.trim()))}
          error={error}
        />
        <Button variant="primary" large busy={busy} onPress={() => void run(() => verifyMfa(code.trim()))}>
          Doorgaan
        </Button>
      </AuthScreen>
    )
  }

  return (
    <AuthScreen title="Inloggen">
      {sso.data?.required ? (
        <Notice>Je organisatie logt in met single sign-on ({sso.data.label}). Dat kan nog niet in de app; gebruik Echoo in de browser.</Notice>
      ) : (
        <>
          <Field
            label="E-mailadres"
            value={email}
            onChangeText={setEmail}
            autoCapitalize="none"
            autoCorrect={false}
            keyboardType="email-address"
            textContentType="username"
            autoComplete="email"
            returnKeyType="next"
          />
          <Field
            label="Wachtwoord"
            value={password}
            onChangeText={setPassword}
            secureTextEntry
            textContentType="password"
            autoComplete="current-password"
            returnKeyType="go"
            onSubmitEditing={() =>
              void run(async () => {
                if ((await login(email.trim(), password)).mfaRequired) setStep('mfa')
              })
            }
            error={error}
          />
          <Button
            variant="primary"
            large
            busy={busy}
            disabled={!email || !password}
            onPress={() =>
              void run(async () => {
                if ((await login(email.trim(), password)).mfaRequired) setStep('mfa')
              })
            }
          >
            Inloggen
          </Button>
        </>
      )}
      <View style={{ flexDirection: 'row', justifyContent: 'space-between', alignItems: 'center', gap: space[3] }}>
        <T variant="label" muted numberOfLines={1} style={{ flexShrink: 1 }}>
          {host}
        </T>
        <Pressable accessibilityRole="button" onPress={() => void forgetServer()} hitSlop={8}>
          <T variant="label" style={{ textDecorationLine: 'underline' }}>
            Ander adres
          </T>
        </Pressable>
      </View>
    </AuthScreen>
  )
}
