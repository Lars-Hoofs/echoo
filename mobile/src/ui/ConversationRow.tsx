import { CornerUpLeft, Paperclip } from 'lucide-react-native'
import { memo } from 'react'
import { Pressable, StyleSheet, Text, View } from 'react-native'

import { formatRelative, priorityLabel, statusLabel } from '../lib/text'
import type { ConversationListItem } from '../lib/types'
import { Avatar, Tag } from './components'
import { font, size, space, useColors } from './theme'

// One conversation in a list, as in the web inbox: who, how urgent, how long ago, the latest
// line and where it sits. The accent dot means unread: the customer waits on us.
export const ConversationRow = memo(function ConversationRow({ item, onPress }: { item: ConversationListItem; onPress: (id: string) => void }) {
  const c = useColors()
  const name = item.contact?.name || item.contact?.email || 'Onbekende afzender'
  const slaAlert = item.sla && (item.sla.state === 'at_risk' || item.sla.state === 'breached') && item.status !== 'closed'
  const meta = [statusLabel[item.status], item.mailbox.name, item.assignee?.name].filter(Boolean).join(' · ')

  return (
    <Pressable
      accessibilityRole="button"
      accessibilityLabel={`${item.unread ? 'Ongelezen, ' : ''}${name}, ${item.subject}, ${formatRelative(item.last_message_at)}`}
      onPress={() => onPress(item.id)}
      style={({ pressed }) => [styles.row, { backgroundColor: pressed ? c.surface2 : c.surface, borderBottomColor: c.line }]}
    >
      <Avatar name={name} />
      <View style={styles.body}>
        <View style={styles.top}>
          {item.unread ? <View style={[styles.dot, { backgroundColor: c.accent }]} accessibilityElementsHidden /> : null}
          <Text style={[styles.name, { color: c.ink, fontFamily: item.unread ? font.medium : font.regular }]} numberOfLines={1}>
            {name}
          </Text>
          {item.priority === 'urgent' || item.priority === 'high' ? <Tag>{priorityLabel[item.priority]}</Tag> : null}
          <Text style={[styles.time, { color: c.ink2 }]}>{formatRelative(item.last_message_at)}</Text>
        </View>
        <Text style={[styles.subject, { color: c.ink }]} numberOfLines={1}>
          {item.subject || '(geen onderwerp)'}
        </Text>
        <View style={styles.previewRow}>
          {item.last_direction === 'out' ? <CornerUpLeft size={14} color={c.ink2} strokeWidth={1.5} /> : null}
          <Text style={[styles.preview, { color: c.ink2 }]} numberOfLines={1}>
            {item.preview}
          </Text>
          {item.has_attachments ? <Paperclip size={14} color={c.ink2} strokeWidth={1.5} /> : null}
        </View>
        <View style={styles.metaRow}>
          <Text style={[styles.meta, { color: c.ink2 }]} numberOfLines={1}>
            {meta}
          </Text>
          {slaAlert ? <Tag tone="alert">{item.sla?.state === 'breached' ? 'SLA verlopen' : 'SLA in gevaar'}</Tag> : null}
        </View>
      </View>
    </Pressable>
  )
})

const styles = StyleSheet.create({
  row: { flexDirection: 'row', gap: space[3], paddingHorizontal: space[4], paddingVertical: space[4], borderBottomWidth: StyleSheet.hairlineWidth },
  body: { flex: 1, gap: 2 },
  top: { flexDirection: 'row', alignItems: 'center', gap: space[2] },
  dot: { width: 8, height: 8, borderRadius: 4 },
  name: { fontSize: size.m, flexShrink: 1 },
  time: { fontFamily: font.regular, fontSize: size.xs, marginLeft: 'auto' },
  subject: { fontFamily: font.regular, fontSize: size.s },
  previewRow: { flexDirection: 'row', alignItems: 'center', gap: space[1] },
  preview: { fontFamily: font.regular, fontSize: size.s, flexShrink: 1 },
  metaRow: { flexDirection: 'row', alignItems: 'center', gap: space[2], marginTop: 2 },
  meta: { fontFamily: font.regular, fontSize: size.xs, flexShrink: 1 },
})
