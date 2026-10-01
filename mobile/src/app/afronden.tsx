import { Linking } from 'react-native'

import { useSession } from '../lib/session'
import { AuthScreen } from '../ui/AuthScreen'
import { Button, T } from '../ui/components'

// An account that must first choose a password or set up two-factor authentication finishes
// that in the web app, where those flows live.
export default function FinishAccountScreen() {
  const { state, refresh, logout } = useSession()
  if (state.status !== 'unfinished') return null
  const what = state.me.must_change_password ? 'een eigen wachtwoord kiezen' : 'tweestapsverificatie instellen'
  return (
    <AuthScreen title="Account afronden">
      <T muted>Voordat je Echoo kunt gebruiken, moet je {what}. Doe dat in de browser en kom daarna terug.</T>
      <Button variant="primary" large onPress={() => void Linking.openURL(`${state.server}/inbox/alle`)}>
        Open Echoo in de browser
      </Button>
      <Button onPress={() => void refresh()}>Ik ben klaar</Button>
      <Button variant="ghost" onPress={() => void logout()}>
        Uitloggen
      </Button>
    </AuthScreen>
  )
}
