import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { router } from 'expo-router'
import { Bell } from 'lucide-react-native'
import { ActivityIndicator, FlatList, Pressable, RefreshControl, StyleSheet, View } from 'react-native'
import { SafeAreaView } from 'react-native-safe-area-context'

import { api, errorMessage } from '../../lib/api'
import { notificationsQuery } from '../../lib/queries'
import { formatRelative, notificationText } from '../../lib/text'
import type { AppNotification } from '../../lib/types'
import { Button, EmptyState, Notice, T } from '../../ui/components'
import { radius, space, useColors } from '../../ui/theme'

export default function NotificationsScreen() {
  const c = useColors()
  const qc = useQueryClient()
  const list = useQuery(notificationsQuery)
  const markRead = useMutation({
    mutationFn: (body: { ids: string[] } | { all: true }) => api('POST', '/notifications/read', body),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['notifications'] }),
  })

  const open = (n: AppNotification) => {
    if (!n.read_at) markRead.mutate({ ids: [n.id] })
    router.push(`/gesprek/${n.conversation_id}`)
  }

  return (
    <SafeAreaView edges={['top']} style={{ flex: 1, backgroundColor: c.bg }}>
      <View style={[styles.sheet, { backgroundColor: c.surface, borderColor: c.line }]}>
        <FlatList
          data={list.data?.notifications ?? []}
          keyExtractor={(n) => n.id}
          refreshControl={<RefreshControl refreshing={list.isRefetching} onRefresh={() => void list.refetch()} tintColor={c.ink2} />}
          ListHeaderComponent={
            <View style={styles.header}>
              <T variant="display">Meldingen</T>
              {list.data?.unread_count ? (
                <Button onPress={() => markRead.mutate({ all: true })} busy={markRead.isPending}>
                  Alles gelezen
                </Button>
              ) : null}
            </View>
          }
          renderItem={({ item: n }) => (
            <Pressable
              accessibilityRole="button"
              onPress={() => open(n)}
              style={({ pressed }) => [styles.row, { borderBottomColor: c.line, backgroundColor: pressed ? c.surface2 : c.surface }]}
            >
              <View style={[styles.dot, { backgroundColor: n.read_at ? 'transparent' : c.ink }]} />
              <View style={{ flex: 1, gap: 2 }}>
                <T medium={!n.read_at}>{notificationText(n)}</T>
                <T variant="label" muted numberOfLines={1}>
                  #{n.conversation_number} · {n.conversation_subject || '(geen onderwerp)'}
                </T>
              </View>
              <T variant="small" muted>
                {formatRelative(n.created_at)}
              </T>
            </Pressable>
          )}
          ListEmptyComponent={
            list.isPending ? (
              <ActivityIndicator style={{ marginTop: space[6] }} color={c.ink2} />
            ) : list.isError ? (
              <View style={{ padding: space[4] }}>
                <Notice tone="alert">{errorMessage(list.error)}</Notice>
              </View>
            ) : (
              <EmptyState
                icon={<Bell size={24} color={c.ink2} strokeWidth={1.5} />}
                title="Geen meldingen"
                text="Vermeldingen, toewijzingen, antwoorden van klanten en SLA-waarschuwingen komen hier."
              />
            )
          }
        />
      </View>
    </SafeAreaView>
  )
}

const styles = StyleSheet.create({
  sheet: { flex: 1, marginHorizontal: space[2], borderTopLeftRadius: radius.m, borderTopRightRadius: radius.m, borderWidth: 1, borderBottomWidth: 0, overflow: 'hidden' },
  header: { flexDirection: 'row', alignItems: 'center', justifyContent: 'space-between', padding: space[4], paddingTop: space[5] },
  row: { flexDirection: 'row', alignItems: 'center', gap: space[3], paddingHorizontal: space[4], paddingVertical: space[4], borderBottomWidth: StyleSheet.hairlineWidth },
  dot: { width: 8, height: 8, borderRadius: 4 },
})
