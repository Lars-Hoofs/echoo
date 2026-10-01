import { useQuery, useQueryClient } from '@tanstack/react-query'
import { router, useLocalSearchParams } from 'expo-router'
import { ChevronLeft, Clock, Ellipsis } from 'lucide-react-native'
import { useEffect, useMemo, useRef, useState } from 'react'
import { ActivityIndicator, FlatList, KeyboardAvoidingView, Linking, Platform, StyleSheet, View } from 'react-native'
import { SafeAreaView } from 'react-native-safe-area-context'

import { api, errorMessage, server } from '../../lib/api'
import { conversationQuery, eventsQuery, patchConversation } from '../../lib/queries'
import { hasPermission, useMe } from '../../lib/session'
import { priorityLabel, snoozePresets, statusLabel } from '../../lib/text'
import { mergeThread } from '../../lib/thread'
import type { ConversationStatus, Priority } from '../../lib/types'
import { Button, IconButton, Notice, Sheet, type SheetOption, T, Tag } from '../../ui/components'
import { Composer } from '../../ui/Composer'
import { space, useColors } from '../../ui/theme'
import { ThreadEntryView } from '../../ui/Thread'
import { useToast } from '../../ui/toast'

type SheetKind = 'more' | 'status' | 'priority' | 'snooze'

export default function ConversationScreen() {
  const { id } = useLocalSearchParams<{ id: string }>()
  const c = useColors()
  const me = useMe()
  const qc = useQueryClient()
  const toast = useToast()
  const detail = useQuery(conversationQuery(id))
  const events = useQuery(eventsQuery(id))
  const [sheet, setSheet] = useState<SheetKind | null>(null)
  const [busy, setBusy] = useState(false)
  const list = useRef<FlatList>(null)

  const entries = useMemo(() => mergeThread(detail.data?.messages ?? [], events.data ?? []), [detail.data, events.data])

  // Opening a conversation marks it read for this agent, as in the web app.
  useEffect(() => {
    if (!detail.data) return
    const t = setTimeout(() => {
      api('POST', `/conversations/${id}/read`)
        .then(() => {
          void qc.invalidateQueries({ queryKey: ['inbox', 'conversations'] })
          void qc.invalidateQueries({ queryKey: ['inbox', 'summary'] })
        })
        .catch(() => undefined)
    }, 1000)
    return () => clearTimeout(t)
  }, [detail.data, id, qc])

  const back = () => (router.canGoBack() ? router.back() : router.replace('/'))

  if (detail.isPending) {
    return (
      <SafeAreaView style={[styles.center, { backgroundColor: c.surface }]}>
        <ActivityIndicator color={c.ink2} />
      </SafeAreaView>
    )
  }
  if (detail.isError) {
    return (
      <SafeAreaView style={{ flex: 1, backgroundColor: c.surface, padding: space[4], gap: space[4] }}>
        <IconButton label="Terug" onPress={back}>
          <ChevronLeft size={20} color={c.ink} strokeWidth={1.5} />
        </IconButton>
        <Notice tone="alert">{errorMessage(detail.error)}</Notice>
        <Button onPress={() => void detail.refetch()}>Opnieuw proberen</Button>
      </SafeAreaView>
    )
  }

  const { conversation: conv, contact } = detail.data
  const name = contact?.name || contact?.email || 'Onbekende afzender'
  const canWrite = conv.can_write && hasPermission(me, 'conversations.write')
  const canAssign = canWrite && hasPermission(me, 'conversations.assign')
  const done = conv.status === 'closed' || conv.status === 'spam'
  const mine = conv.assignee?.id === me.user.id
  const slaAlert = conv.sla && (conv.sla.state === 'at_risk' || conv.sla.state === 'breached') && !done

  const change = async (body: Record<string, unknown>, doneText: string) => {
    setBusy(true)
    try {
      await patchConversation(id, conv.version, body)
      toast({ text: doneText })
    } catch (err) {
      toast({ text: errorMessage(err), tone: 'alert' })
    } finally {
      setBusy(false)
      void qc.invalidateQueries({ queryKey: ['inbox'] })
    }
  }

  const setStatus = (s: ConversationStatus) => {
    const text = s === 'closed' ? 'Gesprek gesloten' : s === 'open' ? 'Gesprek heropend' : s === 'waiting' ? 'Op wachtend gezet' : 'Als spam gemarkeerd'
    void change({ status: s }, text)
  }

  let options: SheetOption[] = []
  if (sheet === 'more') {
    options = [
      ...(canWrite ? [{ label: 'Status wijzigen', onPress: () => setSheet('status') }] : []),
      ...(canWrite ? [{ label: 'Prioriteit', onPress: () => setSheet('priority') }] : []),
      ...(canAssign && conv.assignee ? [{ label: 'Toewijzing weghalen', onPress: () => void change({ assignee_user_id: null }, 'Toewijzing weggehaald') }] : []),
      ...(canWrite && conv.snoozed_until ? [{ label: 'Uitstel opheffen', onPress: () => void change({ snoozed_until: null }, 'Uitstel opgeheven') }] : []),
      {
        label: 'Markeren als ongelezen',
        onPress: () =>
          void api('POST', `/conversations/${id}/unread`).then(() => {
            void qc.invalidateQueries({ queryKey: ['inbox'] })
            back()
          }),
      },
      { label: 'Openen in de browser', onPress: () => void Linking.openURL(`${server()}/inbox/alle/${id}`) },
    ]
  } else if (sheet === 'status') {
    const all: ConversationStatus[] = hasPermission(me, 'conversations.delete') ? ['open', 'waiting', 'closed', 'spam'] : ['open', 'waiting', 'closed']
    options = all.map((s) => ({ label: statusLabel[s], selected: s === conv.status, destructive: s === 'spam', onPress: () => setStatus(s) }))
  } else if (sheet === 'priority') {
    const all: Priority[] = ['urgent', 'high', 'normal', 'low', 'none']
    options = all.map((p) => ({ label: priorityLabel[p], selected: p === conv.priority, onPress: () => void change({ priority: p }, `Prioriteit ${priorityLabel[p].toLowerCase()}`) }))
  } else if (sheet === 'snooze') {
    options = snoozePresets(new Date()).map((p) => ({
      label: p.label,
      onPress: () => void change({ snoozed_until: p.at.toISOString() }, `Uitgesteld: ${p.label.toLowerCase()}`),
    }))
  }

  return (
    <SafeAreaView edges={['top']} style={{ flex: 1, backgroundColor: c.surface }}>
      <View style={[styles.header, { borderBottomColor: c.line }]}>
        <View style={styles.headRow}>
          <IconButton label="Terug" onPress={back}>
            <ChevronLeft size={20} color={c.ink} strokeWidth={1.5} />
          </IconButton>
          <View style={{ flex: 1 }}>
            <T variant="label" muted numberOfLines={1}>
              {name} · #{conv.number} · {conv.mailbox.name}
            </T>
            <T variant="heading" numberOfLines={2}>
              {conv.subject || '(geen onderwerp)'}
            </T>
          </View>
          <IconButton label="Meer acties" onPress={() => setSheet('more')}>
            <Ellipsis size={20} color={c.ink} strokeWidth={1.5} />
          </IconButton>
        </View>
        <View style={styles.tags}>
          <Tag tone={conv.status === 'open' ? 'accent' : 'neutral'}>{statusLabel[conv.status]}</Tag>
          {conv.priority !== 'none' ? <Tag>{priorityLabel[conv.priority]}</Tag> : null}
          {slaAlert ? <Tag tone="alert">{conv.sla?.state === 'breached' ? 'SLA verlopen' : 'SLA in gevaar'}</Tag> : null}
          <T variant="small" muted numberOfLines={1} style={{ flexShrink: 1 }}>
            {conv.assignee ? `Bij ${mine ? 'jou' : conv.assignee.name}` : 'Niet toegewezen'}
          </T>
        </View>
        {canWrite ? (
          <View style={styles.actions}>
            {canAssign && !mine ? (
              <Button variant="secondary" busy={busy} onPress={() => void change({ assignee_user_id: me.user.id }, 'Aan jou toegewezen')}>
                Aan mij
              </Button>
            ) : null}
            <Button variant="primary" busy={busy} onPress={() => setStatus(done ? 'open' : 'closed')} style={{ flex: 1 }}>
              {done ? 'Heropenen' : 'Sluiten'}
            </Button>
            {!done ? (
              <IconButton label="Uitstellen" onPress={() => setSheet('snooze')}>
                <Clock size={20} color={c.ink} strokeWidth={1.5} />
              </IconButton>
            ) : null}
          </View>
        ) : null}
      </View>

      <KeyboardAvoidingView style={{ flex: 1 }} behavior={Platform.OS === 'ios' ? 'padding' : undefined}>
        <FlatList
          ref={list}
          data={entries}
          keyExtractor={(e) => (e.kind === 'message' ? e.message.id : e.event.id)}
          renderItem={({ item }) => <ThreadEntryView entry={item} />}
          contentContainerStyle={styles.thread}
          style={{ backgroundColor: c.surface }}
          // The newest message stays in view when it arrives and when the composer opens or closes.
          onContentSizeChange={() => list.current?.scrollToEnd({ animated: false })}
          onLayout={() => list.current?.scrollToEnd({ animated: false })}
        />
        {canWrite ? (
          <Composer conversationId={id} />
        ) : (
          <View style={{ padding: space[4] }}>
            <Notice>Je hebt alleen leesrechten voor dit gesprek.</Notice>
          </View>
        )}
      </KeyboardAvoidingView>

      {sheet ? (
        <Sheet
          title={sheet === 'status' ? 'Status' : sheet === 'priority' ? 'Prioriteit' : sheet === 'snooze' ? 'Uitstellen tot' : 'Gesprek'}
          options={options}
          onClose={() => setSheet((s) => (s === sheet ? null : s))}
        />
      ) : null}
    </SafeAreaView>
  )
}

const styles = StyleSheet.create({
  center: { flex: 1, alignItems: 'center', justifyContent: 'center' },
  header: { paddingHorizontal: space[4], paddingTop: space[2], paddingBottom: space[3], gap: space[3], borderBottomWidth: StyleSheet.hairlineWidth },
  headRow: { flexDirection: 'row', alignItems: 'flex-start', gap: space[3] },
  tags: { flexDirection: 'row', alignItems: 'center', gap: space[2], flexWrap: 'wrap' },
  actions: { flexDirection: 'row', alignItems: 'center', gap: space[2] },
  thread: { padding: space[4], gap: space[5] },
})
