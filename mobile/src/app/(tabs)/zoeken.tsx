import { useQuery } from '@tanstack/react-query'
import { router } from 'expo-router'
import { Search } from 'lucide-react-native'
import { useEffect, useState } from 'react'
import { ActivityIndicator, FlatList, Pressable, StyleSheet, Text, TextInput, View } from 'react-native'
import { SafeAreaView } from 'react-native-safe-area-context'

import { errorMessage } from '../../lib/api'
import { searchQuery } from '../../lib/queries'
import { formatRelative, snippetParts, statusLabel } from '../../lib/text'
import { EmptyState, Notice, T } from '../../ui/components'
import { font, height, radius, size, space, useColors } from '../../ui/theme'

export default function SearchScreen() {
  const c = useColors()
  const [input, setInput] = useState('')
  const [q, setQ] = useState('')
  // Search as the agent types, without a request per keystroke.
  useEffect(() => {
    const t = setTimeout(() => setQ(input.trim()), 300)
    return () => clearTimeout(t)
  }, [input])
  const results = useQuery(searchQuery(q))

  return (
    <SafeAreaView edges={['top']} style={{ flex: 1, backgroundColor: c.bg }}>
      <View style={[styles.sheet, { backgroundColor: c.surface, borderColor: c.line }]}>
        <View style={styles.header}>
          <T variant="display">Zoeken</T>
          <View style={[styles.field, { borderColor: c.lineStrong }]}>
            <Search size={18} color={c.ink2} strokeWidth={1.5} />
            <TextInput
              value={input}
              onChangeText={setInput}
              placeholder="Onderwerp, klant, tekst of #nummer"
              placeholderTextColor={c.ink3}
              autoCapitalize="none"
              autoCorrect={false}
              returnKeyType="search"
              clearButtonMode="while-editing"
              accessibilityLabel="Zoeken in gesprekken"
              style={[styles.input, { color: c.ink }]}
            />
          </View>
        </View>
        <FlatList
          data={results.data ?? []}
          keyExtractor={(r) => r.conversation.id}
          keyboardShouldPersistTaps="handled"
          renderItem={({ item }) => {
            const conv = item.conversation
            return (
              <Pressable
                accessibilityRole="button"
                onPress={() => router.push(`/gesprek/${conv.id}`)}
                style={({ pressed }) => [styles.row, { borderBottomColor: c.line, backgroundColor: pressed ? c.surface2 : c.surface }]}
              >
                <View style={styles.rowTop}>
                  <T numberOfLines={1} style={{ flex: 1 }}>
                    {conv.subject || '(geen onderwerp)'}
                  </T>
                  <T variant="small" muted>
                    {formatRelative(conv.last_message_at)}
                  </T>
                </View>
                <T variant="label" muted numberOfLines={1}>
                  {conv.contact?.name || conv.contact?.email || 'Onbekende afzender'} · #{conv.number} · {statusLabel[conv.status]}
                </T>
                {item.snippet ? (
                  <Text style={[styles.snippet, { color: c.ink2 }]} numberOfLines={2}>
                    {snippetParts(item.snippet).map((p, i) => (
                      <Text key={i} style={p.match ? { color: c.ink, fontFamily: font.medium } : undefined}>
                        {p.text}
                      </Text>
                    ))}
                  </Text>
                ) : null}
              </Pressable>
            )
          }}
          ListEmptyComponent={
            q.length < 2 ? (
              <EmptyState icon={<Search size={24} color={c.ink2} strokeWidth={1.5} />} title="Zoek in alle gesprekken" text="Op onderwerp, klant, e-mailadres, tekst of gespreksnummer." />
            ) : results.isPending ? (
              <ActivityIndicator style={{ marginTop: space[6] }} color={c.ink2} />
            ) : results.isError ? (
              <View style={{ padding: space[4] }}>
                <Notice tone="alert">{errorMessage(results.error)}</Notice>
              </View>
            ) : (
              <EmptyState icon={<Search size={24} color={c.ink2} strokeWidth={1.5} />} title="Niets gevonden" text={`Geen gesprekken met “${q}”.`} />
            )
          }
        />
      </View>
    </SafeAreaView>
  )
}

const styles = StyleSheet.create({
  sheet: { flex: 1, marginHorizontal: space[2], borderTopLeftRadius: radius.m, borderTopRightRadius: radius.m, borderWidth: 1, borderBottomWidth: 0, overflow: 'hidden' },
  header: { padding: space[4], paddingTop: space[5], gap: space[4] },
  field: { flexDirection: 'row', alignItems: 'center', gap: space[2], height: height.m, borderWidth: 1, borderRadius: radius.pill, paddingHorizontal: space[4] },
  input: { flex: 1, fontFamily: font.regular, fontSize: size.m, height: '100%' },
  row: { paddingHorizontal: space[4], paddingVertical: space[3], gap: 2, borderBottomWidth: StyleSheet.hairlineWidth },
  rowTop: { flexDirection: 'row', alignItems: 'center', gap: space[2] },
  snippet: { fontFamily: font.regular, fontSize: size.s, marginTop: space[1] },
})
