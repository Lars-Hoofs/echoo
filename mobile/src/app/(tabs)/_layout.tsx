import { useQuery } from '@tanstack/react-query'
import { type BottomTabBarProps, Tabs } from 'expo-router/js-tabs'
import { Bell, Inbox, Search, UserRound } from 'lucide-react-native'
import type { ComponentType } from 'react'
import { Pressable, StyleSheet, Text, View } from 'react-native'
import { useSafeAreaInsets } from 'react-native-safe-area-context'

import { notificationsQuery, summaryQuery } from '../../lib/queries'
import { font, radius, size, space, useColors } from '../../ui/theme'

const tabs: Record<string, { label: string; icon: ComponentType<{ color: string; size: number; strokeWidth: number }> }> = {
  index: { label: 'Inbox', icon: Inbox },
  zoeken: { label: 'Zoeken', icon: Search },
  meldingen: { label: 'Meldingen', icon: Bell },
  ik: { label: 'Ik', icon: UserRound },
}

export default function TabsLayout() {
  return (
    <Tabs screenOptions={{ headerShown: false }} tabBar={(props) => <TabBar {...props} />}>
      <Tabs.Screen name="index" />
      <Tabs.Screen name="zoeken" />
      <Tabs.Screen name="meldingen" />
      <Tabs.Screen name="ik" />
    </Tabs>
  )
}

// The web app's phone tab bar: a floating pill, the current tab a solid ink pill. Only the
// unread count of the agent's own conversations is accent: the customer waits on us there.
function TabBar({ state, navigation }: BottomTabBarProps) {
  const c = useColors()
  const insets = useSafeAreaInsets()
  const summary = useQuery(summaryQuery)
  const notifications = useQuery(notificationsQuery)
  const badges: Record<string, { n: number; accent: boolean } | undefined> = {
    index: summary.data?.counts.unread_mine ? { n: summary.data.counts.unread_mine, accent: true } : undefined,
    meldingen: notifications.data?.unread_count ? { n: notifications.data.unread_count, accent: false } : undefined,
  }

  return (
    <View style={[styles.wrap, { paddingBottom: Math.max(insets.bottom, space[2]), backgroundColor: c.bg }]}>
      <View style={[styles.bar, { backgroundColor: c.surface, borderColor: c.line }]} accessibilityRole="tablist">
        {state.routes.map((route, i) => {
          const tab = tabs[route.name]
          if (!tab) return null
          const active = state.index === i
          const Icon = tab.icon
          const badge = badges[route.name]
          return (
            <Pressable
              key={route.key}
              accessibilityRole="tab"
              accessibilityState={{ selected: active }}
              accessibilityLabel={badge ? `${tab.label}, ${badge.n} ongelezen` : tab.label}
              onPress={() => {
                const event = navigation.emit({ type: 'tabPress', target: route.key, canPreventDefault: true })
                if (!active && !event.defaultPrevented) navigation.navigate(route.name)
              }}
              style={[styles.tab, active && { backgroundColor: c.ink }]}
            >
              <Icon color={active ? c.surface : c.ink2} size={22} strokeWidth={1.5} />
              <Text style={[styles.label, { color: active ? c.surface : c.ink2 }]}>{tab.label}</Text>
              {badge ? (
                // A neutral badge inverts on the ink pill of the current tab, so it stays visible.
                <View style={[styles.badge, { backgroundColor: badge.accent ? c.accent : active ? c.surface : c.ink }]}>
                  <Text style={[styles.badgeText, { color: badge.accent ? c.accentInk : active ? c.ink : c.surface }]}>{badge.n > 99 ? '99+' : badge.n}</Text>
                </View>
              ) : null}
            </Pressable>
          )
        })}
      </View>
    </View>
  )
}

const styles = StyleSheet.create({
  wrap: { paddingHorizontal: space[3], paddingTop: space[2] },
  bar: { flexDirection: 'row', borderRadius: radius.pill, borderWidth: 1, padding: space[1], gap: space[1] },
  tab: { flex: 1, height: 56, borderRadius: radius.pill, alignItems: 'center', justifyContent: 'center', gap: 2 },
  label: { fontFamily: font.regular, fontSize: size.xs },
  badge: { position: 'absolute', top: 4, right: '22%', minWidth: 18, height: 18, borderRadius: 9, paddingHorizontal: 4, alignItems: 'center', justifyContent: 'center' },
  badgeText: { fontFamily: font.medium, fontSize: 11 },
})
