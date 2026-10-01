import { useState } from 'react'

import { ApiError } from '../lib/api'
import { useSession } from '../lib/session'
import { normalizeServerUrl } from '../lib/text'
import { AuthScreen } from '../ui/AuthScreen'
import { Button, Field, T } from '../ui/components'

export default function ConnectScreen() {
  const { connect } = useSession()
  const [address, setAddress] = useState('')
  const [error, setError] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)

  const submit = async () => {
    const server = normalizeServerUrl(address)
    if (!server) {
      setError('Vul het adres in waarop je Echoo opent, bijvoorbeeld support.bedrijf.nl.')
      return
    }
    setBusy(true)
    setError(null)
    try {
      await connect(server)
    } catch (err) {
      setError(
        err instanceof ApiError && err.code === 'network'
          ? 'Dit adres is niet bereikbaar. Controleer het adres en je internetverbinding.'
          : 'Op dit adres draait geen Echoo. Gebruik het adres waarop je Echoo in de browser opent.',
      )
    } finally {
      setBusy(false)
    }
  }

  return (
    <AuthScreen title="Verbinden">
      <T muted>Het adres waarop je Echoo in de browser opent.</T>
      <Field
        label="Adres van Echoo"
        value={address}
        onChangeText={setAddress}
        placeholder="support.bedrijf.nl"
        autoCapitalize="none"
        autoCorrect={false}
        keyboardType="url"
        textContentType="URL"
        returnKeyType="go"
        onSubmitEditing={() => void submit()}
        error={error}
      />
      <Button variant="primary" large busy={busy} onPress={() => void submit()}>
        Verbinden
      </Button>
    </AuthScreen>
  )
}
