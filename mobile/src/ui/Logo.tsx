import { StyleSheet, Text, View } from 'react-native'

import { font, radius, useColors } from './theme'

// ron's logo: the accent tile with the initial and the wordmark beside it, as in the web app.
export function Logo({ small }: { small?: boolean }) {
  const c = useColors()
  const tile = small ? 32 : 40
  return (
    <View style={styles.row} accessibilityRole="header" accessibilityLabel="Echoo">
      <View style={[styles.tile, { width: tile, height: tile, backgroundColor: c.accent }]}>
        <Text style={{ fontFamily: font.medium, fontSize: small ? 16 : 20, color: c.accentInk }}>e</Text>
      </View>
      <Text style={{ fontFamily: font.regular, fontSize: small ? 20 : 24, letterSpacing: -0.5, color: c.ink }}>echoo</Text>
    </View>
  )
}

const styles = StyleSheet.create({
  row: { flexDirection: 'row', alignItems: 'center', gap: 12 },
  tile: { borderRadius: radius.s, alignItems: 'center', justifyContent: 'center' },
})
