import { useInfiniteQuery, useQuery } from '@tanstack/react-query'
import { router } from 'expo-router'
import { ChevronDown, Inbox } from 'lucide-react-native'
import { useCallback, useState } from 'react'
import { ActivityIndicator, FlatList, Pressable, RefreshControl, StyleSheet, View } from 'react-native'
import { SafeAreaView } from 'react-native-safe-area-context'

import { errorMessage } from '../../lib/api'
import { conversationsQuery, summaryQuery } from '../../lib/queries'
import { listStatusLabel } from '../../lib/text'
import type { InboxView, ListStatus } from '../../lib/types'
import { EmptyState, Notice, Segmented, Sheet, T } from '../../ui/components'
import { ConversationRow } from '../../ui/ConversationRow'
import { radius, space, useColors } from '../../ui/theme'

const statuses: ListStatus[] = ['open', 'waiting', 'snoozed', 'closed', 'spam']

const emptyText: Record<InboxView, string> = {
  mine: 'Er staat niets voor je open. Nieuwe gesprekken die aan jou worden toegewezen, komen hier.',
  unassigned: 'Alle gesprekken hebben iemand die ze oppakt.',
  all: 'Geen gesprekken met deze status.',
}

export default function InboxScreen() {
  const c = useColors()
  const [view, setView] = useState<InboxView>('mine')
  const [status, setStatus] = useState<ListStatus>('open')
  const [picking, setPicking] = useState(false)
  const summary = useQuery(summaryQuery)
  const list = useInfiniteQuery(conversationsQuery(view, status))
  const rows = list.data?.pages.flatMap((p) => p.conversations) ?? []
  const counts = summary.data?.counts
  const open = useCallback((id: string) => router.push(`/gesprek/${id}`), [])

  return (
    <SafeAreaView edges={['top']} style={{ flex: 1, backgroundColor: c.bg }}>
      <View style={[styles.sheet, { backgroundColor: c.surface, borderColor: c.line }]}>
        <FlatList
          data={rows}
          keyExtractor={(item) => item.id}
          renderItem={({ item }) => <ConversationRow item={item} onPress={open} />}
          onEndReached={() => {
            if (list.hasNextPage && !list.isFetchingNextPage) void list.fetchNextPage()
          }}
          onEndReachedThreshold={0.4}
          refreshControl={
            <RefreshControl
              refreshing={list.isRefetching && !list.isFetchingNextPage}
              onRefresh={() => {
                void list.refetch()
                void summary.refetch()
              }}
              tintColor={c.ink2}
            />
          }
          ListHeaderComponent={
            <View style={styles.header}>
              <View style={styles.titleRow}>
                <T variant="display">Gesprekken</T>
                <Pressable
                  accessibilityRole="button"
                  accessibilityLabel={`Status: ${listStatusLabel[status]}`}
                  onPress={() => setPicking(true)}
                  style={[styles.statusButton, { borderColor: c.lineStrong }]}
                >
                  <T variant="label">{listStatusLabel[status]}</T>
                  <ChevronDown size={16} color={c.ink} strokeWidth={1.5} />
                </Pressable>
              </View>
              <Segmented
                label="Weergave"
                value={view}
                onChange={setView}
                options={[
                  { value: 'mine', label: 'Mijn', count: counts?.mine },
                  { value: 'unassigned', label: 'Zonder', count: counts?.unassigned },
                  { value: 'all', label: 'Alle', count: counts?.all },
                ]}
              />
              {list.isError ? <Notice tone="alert">{errorMessage(list.error)}</Notice> : null}
            </View>
          }
          ListEmptyComponent={
            list.isPending ? (
              <ActivityIndicator style={{ marginTop: space[7] }} color={c.ink2} />
            ) : list.isError ? null : (
              <EmptyState icon={<Inbox size={24} color={c.ink2} strokeWidth={1.5} />} title="Niets te doen" text={emptyText[view]} />
            )
          }
          ListFooterComponent={list.isFetchingNextPage ? <ActivityIndicator style={{ margin: space[5] }} color={c.ink2} /> : null}
        />
      </View>
      {picking ? (
        <Sheet
          title="Status"
          onClose={() => setPicking(false)}
          options={statuses.map((s) => ({ label: listStatusLabel[s], selected: s === status, onPress: () => setStatus(s) }))}
        />
      ) : null}
    </SafeAreaView>
  )
}

const styles = StyleSheet.create({
  sheet: { flex: 1, marginHorizontal: space[2], borderTopLeftRadius: radius.m, borderTopRightRadius: radius.m, borderWidth: 1, borderBottomWidth: 0, overflow: 'hidden' },
  header: { paddingHorizontal: space[4], paddingTop: space[5], paddingBottom: space[4], gap: space[4] },
  titleRow: { flexDirection: 'row', alignItems: 'center', justifyContent: 'space-between', gap: space[3] },
  statusButton: { flexDirection: 'row', alignItems: 'center', gap: space[1], height: 36, paddingHorizontal: space[4], borderRadius: radius.pill, borderWidth: 1 },
})
