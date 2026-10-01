import type { ReactNode } from 'react'
import { KeyboardAvoidingView, Platform, ScrollView, StyleSheet, View } from 'react-native'
import { SafeAreaView } from 'react-native-safe-area-context'

import { Card, T } from './components'
import { Logo } from './Logo'
import { space, useColors } from './theme'

// The frame of the screens before sign-in, as the web app's login: the logo above one card.
export function AuthScreen({ title, children }: { title: string; children: ReactNode }) {
  const c = useColors()
  return (
    <SafeAreaView style={{ flex: 1, backgroundColor: c.bg }}>
      <KeyboardAvoidingView style={{ flex: 1 }} behavior={Platform.OS === 'ios' ? 'padding' : undefined}>
        <ScrollView contentContainerStyle={styles.scroll} keyboardShouldPersistTaps="handled">
          <View style={styles.logo}>
            <Logo />
          </View>
          <Card style={styles.card}>
            <T variant="title">{title}</T>
            {children}
          </Card>
        </ScrollView>
      </KeyboardAvoidingView>
    </SafeAreaView>
  )
}

const styles = StyleSheet.create({
  scroll: { flexGrow: 1, justifyContent: 'center', padding: space[4], gap: space[5] },
  logo: { alignItems: 'center' },
  card: { gap: space[5], padding: space[5], width: '100%', maxWidth: 440, alignSelf: 'center' },
})
